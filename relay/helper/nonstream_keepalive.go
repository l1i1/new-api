package helper

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/logger"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// Non-stream keep-alive: a non-stream relay writes nothing downstream until the
// upstream generation finishes, and an intermediary that times out on the GAP
// BETWEEN BYTES cuts it (Tencent EdgeOne answers 524 after ~600s of silence; that
// is what a customer hit on non-stream requests that legitimately run for many
// minutes). While the upstream works we write insignificant JSON whitespace, then
// stop before the first byte of the real body.
//
// Three properties this file exists to guarantee, each of which was a review
// finding against the first version:
//
//  1. The probes must outlive the upstream *headers*. doRequest returns as soon as
//     `client.Do` returns, while the body (the part that actually takes minutes)
//     is read afterwards by the handler. Stopping at headers would leave the
//     client silent for the whole wait — the feature would do nothing. So the
//     keep-alive is stopped by a WRITE GUARD on the response writer, i.e. at the
//     moment the first real byte is about to go out, not when doRequest returns.
//  2. The probe must not corrupt a body that turns out not to be JSON. Some
//     upstreams answer `text/event-stream` even to a non-stream request and the
//     handler then streams (relay/compatible_handler.go). A space would turn the
//     first SSE line "data: {...}" into " data: {...}", whose field name is
//     " data" — spec-compliant parsers drop that event. A newline is
//     insignificant in JSON too, and in SSE an empty line with no accumulated
//     data dispatches nothing.
//  3. Probes must be attributable. They are booked as keep-alive bytes so the
//     retry decision still sees ResponseKeepAliveOnly, and a context flag marks
//     the write so downstream-first-byte metrics do not count a probe as content.
const (
	ginKeyNonStreamKeepAlive = "helper_nonstream_keepalive"
	ginKeyUnguardedWriter    = "helper_unguarded_response_writer"
	ginKeyKeepAliveWrite     = "helper_keepalive_write_in_progress"
)

// NonStreamKeepAliveMode selects the mechanism used to keep an intermediary from
// timing out on a silent non-stream response. The two have different failure
// modes and the choice is an operator decision, so it is a setting.
type NonStreamKeepAliveMode string

const (
	// KeepAliveModeInterim sends an HTTP 103 interim response. It is the only
	// mechanism that does NOT commit the final status — the response is still
	// wide open afterwards, so a request that fails late answers with its real
	// status code instead of a fake 200. It must reach the intermediary as
	// response bytes on the wire: verified that our own nginx (1.22.1,
	// proxy_buffering off) forwards an upstream 103 to its client.
	KeepAliveModeInterim NonStreamKeepAliveMode = "interim"

	// KeepAliveModeWhitespace writes JSON whitespace into the body. It is the
	// conservative choice — plain bytes traverse every hop — but the very first
	// byte commits the status, so afterwards a failure can only be reported as a
	// body at the already-committed 200.
	KeepAliveModeWhitespace NonStreamKeepAliveMode = "whitespace"
)

// ParseNonStreamKeepAliveMode maps the configured value onto a known mechanism.
//
// The default is whitespace, the mechanism whose concurrency is harmless under
// every header-mutation pattern. Interim is opt-in because sending a 1xx makes
// net/http read the response header map, so it is only safe as long as no caller
// mutates headers without going through the guarded writer — the guards below
// close every path we can observe, and the mode exists so an operator can trade
// that for a preserved status code once it has been validated end to end.
func ParseNonStreamKeepAliveMode(value string) NonStreamKeepAliveMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(KeepAliveModeInterim):
		return KeepAliveModeInterim
	default:
		return KeepAliveModeWhitespace
	}
}

// rawResponseWriterKey carries the net/http ResponseWriter captured before gin
// wrapped it. Interim responses must be written around gin: gin's WriteHeaderNow
// marks its writer as written, which would put the final status out of reach —
// exactly the property this mode exists to preserve.
type rawResponseWriterKey struct{}

// CaptureRawResponseWriter stores the underlying net/http ResponseWriter in the
// request context so keep-alive probes can send interim responses around gin.
func CaptureRawResponseWriter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), rawResponseWriterKey{}, w)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func rawResponseWriterOf(c *gin.Context) http.ResponseWriter {
	if c == nil || c.Request == nil {
		return nil
	}
	raw, _ := c.Request.Context().Value(rawResponseWriterKey{}).(http.ResponseWriter)
	return raw
}

// InterimKeepAlive sends one 103 interim response. No header map is set up for the
// final response and no body byte is written, so gin still considers the response
// unwritten and the eventual status is entirely free.
func InterimKeepAlive(c *gin.Context) error {
	if c == nil || c.Request == nil {
		return errors.New("context is nil")
	}
	if requestContextDone(c) {
		return fmt.Errorf("request context done: %w", c.Request.Context().Err())
	}
	raw := rawResponseWriterOf(c)
	if raw == nil {
		return errors.New("raw response writer not captured")
	}
	raw.WriteHeader(http.StatusEarlyHints)
	if flusher, ok := raw.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

// whitespaceProbe is what the keep-alive writes into the body: a single newline.
// See property 2 above for why it is not a space.
const whitespaceProbe = "\n"

type nonStreamKeepAlive struct {
	stop     context.CancelFunc
	done     <-chan struct{}
	stopOnce *sync.Once
	// headerAccessAt records the last time the handler touched the response header
	// map. Sending a 1xx interim response reads that map (net/http writes the
	// current headers), so a probe must not run while the handler may be mutating
	// it — concurrent map read/write in Go is a fatal error, not just a race.
	headerAccessAt atomic.Int64
}

// StartNonStreamKeepAlive begins probing and installs the write guard that ends
// the probing immediately before the response body is written. Calling it again
// for the same request (a channel retry) first stops the previous keep-alive, so
// at most one probe goroutine ever writes.
func StartNonStreamKeepAlive(c *gin.Context, interval time.Duration, mode NonStreamKeepAliveMode) {
	if c == nil || c.Writer == nil {
		return
	}
	StopNonStreamKeepAlive(c)

	// The guard wraps c.Writer, so the probes must write around it: capture the
	// writer that was current before installing it.
	unguarded, ok := c.Writer.(gin.ResponseWriter)
	if !ok {
		return
	}
	state := &nonStreamKeepAlive{stopOnce: &sync.Once{}}
	probe := func(ctx *gin.Context) error {
		if mode == KeepAliveModeWhitespace {
			return WhitespaceData(ctx)
		}
		// Interim: never send the probe while the handler may be mutating headers.
		if elapsed := time.Since(time.Unix(0, state.headerAccessAt.Load())); elapsed < time.Second {
			return nil
		}
		return InterimKeepAlive(ctx)
	}
	probeLabel := "non-stream keep-alive (" + string(mode) + ")"
	if rawResponseWriterOf(c) == nil && mode != KeepAliveModeWhitespace {
		// The raw writer was never captured, so interim cannot work at all.
		// Degrade to whitespace rather than silently doing nothing.
		probe = WhitespaceData
		probeLabel = "non-stream keep-alive (whitespace fallback)"
	}
	c.Set(ginKeyUnguardedWriter, unguarded)
	c.Writer = &keepAliveGuardedWriter{ResponseWriter: unguarded, ctx: c, state: state}

	keepAliveCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	state.stop, state.done = stop, done
	c.Set(ginKeyNonStreamKeepAlive, state)

	gopool.Go(func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				logger.LogDebug(c, "non-stream keep-alive goroutine panic recovered: %v", r)
			}
			logger.LogDebug(c, "non-stream keep-alive goroutine stopped")
		}()

		if interval <= 0 {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// Hard stop so a request that never writes cannot leak a goroutine; the
		// request context also ends the loop when the handler returns.
		timeout := time.NewTimer(120 * time.Minute)
		defer timeout.Stop()

		for {
			select {
			case <-ticker.C:
				if err := probe(c); err != nil {
					logger.LogDebug(c, "%s error, stopping: %s", probeLabel, err.Error())
					return
				}
			case <-keepAliveCtx.Done():
				return
			case <-c.Request.Context().Done():
				return
			case <-timeout.C:
				logger.LogDebug(c, "non-stream keep-alive goroutine timeout, stopping")
				return
			}
		}
	})
}

// StopNonStreamKeepAlive stops the probe goroutine and waits for it to exit, so
// no probe can be written after this returns. It is safe to call repeatedly.
func StopNonStreamKeepAlive(c *gin.Context) {
	if c == nil {
		return
	}
	value, exists := c.Get(ginKeyNonStreamKeepAlive)
	if !exists {
		return
	}
	state, ok := value.(*nonStreamKeepAlive)
	if !ok || state == nil || state.stopOnce == nil {
		return
	}
	state.stopOnce.Do(func() {
		state.stop()
		<-state.done
	})
}

// NonStreamKeepAliveActive reports whether this request currently has a
// non-stream keep-alive installed.
func NonStreamKeepAliveActive(c *gin.Context) bool {
	if c == nil {
		return false
	}
	_, exists := c.Get(ginKeyNonStreamKeepAlive)
	return exists
}

// keepAliveGuardedWriter ends the keep-alive the moment real content is written.
// Embedding gin.ResponseWriter promotes every other method unchanged.
type keepAliveGuardedWriter struct {
	gin.ResponseWriter
	ctx   *gin.Context
	state *nonStreamKeepAlive
}

// Header records the access so interim probes stay out of a header mutation, and
// delegates unchanged.
func (w *keepAliveGuardedWriter) Header() http.Header {
	if w.state != nil {
		w.state.headerAccessAt.Store(time.Now().UnixNano())
	}
	return w.ResponseWriter.Header()
}

func (w *keepAliveGuardedWriter) Write(p []byte) (int, error) {
	StopNonStreamKeepAlive(w.ctx)
	return w.ResponseWriter.Write(p)
}

func (w *keepAliveGuardedWriter) WriteString(s string) (int, error) {
	StopNonStreamKeepAlive(w.ctx)
	return w.ResponseWriter.WriteString(s)
}

// WriteHeader stops the keep-alive before the response is rendered. This is the
// path gin takes before it touches the content type (Context.Render calls
// c.Status first), so it closes the window in which a probe could read the header
// map while the renderer writes it.
func (w *keepAliveGuardedWriter) WriteHeader(code int) {
	StopNonStreamKeepAlive(w.ctx)
	w.ResponseWriter.WriteHeader(code)
}

// WhitespaceData writes one keep-alive probe.
//
// It goes through the unguarded writer (writing through the guard would stop the
// keep-alive it is part of) and sets the response headers lazily, so a request
// that finishes before the first interval produces a byte-identical response —
// headers included.
func WhitespaceData(c *gin.Context) error {
	setNonStreamKeepAliveHeaders(c)

	w := c.Writer
	if unguarded, exists := c.Get(ginKeyUnguardedWriter); exists {
		if writer, ok := unguarded.(gin.ResponseWriter); ok {
			w = writer
		}
	}
	c.Set(ginKeyKeepAliveWrite, true)
	defer c.Set(ginKeyKeepAliveWrite, false)
	return writeKeepAliveProbeTo(c, w, whitespaceProbe, "non-stream keep-alive")
}

// KeepAliveWriteInProgress reports whether the current write is keep-alive
// output, so observers can avoid treating a probe as response content.
func KeepAliveWriteInProgress(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, exists := c.Get(ginKeyKeepAliveWrite)
	if !exists {
		return false
	}
	inProgress, _ := value.(bool)
	return inProgress
}

// KeepAliveOnlyCommitted reports that nothing but keep-alive probes has reached
// the client yet. Callers that would otherwise refuse to write an error body
// because the status is committed use this to still report the failure.
func KeepAliveOnlyCommitted(c *gin.Context) bool {
	return ResponseCommitStateOf(c) == ResponseKeepAliveOnly
}

func setNonStreamKeepAliveHeaders(c *gin.Context) {
	header := c.Writer.Header()
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json")
	}
	header.Set("Cache-Control", "no-store")
	header.Set("X-Accel-Buffering", "no")
}

// writeKeepAliveProbeTo is writeKeepAliveProbe with an explicit writer.
func writeKeepAliveProbeTo(c *gin.Context, w gin.ResponseWriter, probe, label string) error {
	if c == nil || w == nil {
		return errors.New("context or writer is nil")
	}
	if requestContextDone(c) {
		return fmt.Errorf("request context done: %w", c.Request.Context().Err())
	}
	if _, err := w.Write([]byte(probe)); err != nil {
		return fmt.Errorf("write %s probe failed: %w", label, err)
	}
	c.Set(ginKeyKeepAliveBytes, c.GetInt(ginKeyKeepAliveBytes)+len(probe))
	return FlushWriter(c)
}

var _ http.Flusher = (*keepAliveGuardedWriter)(nil)
