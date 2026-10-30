package openai

import (
	"io"
	"strings"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// streamHeadCaptureLimit bounds how much of an upstream body is kept for the
// failure row. It only has to be enough to recognise what the upstream sent
// instead of an answer: an error object delivered with HTTP 200, an HTML page
// from an edge, a bare "[DONE]", or nothing at all.
const streamHeadCaptureLimit = 512

// streamHeadTee keeps the first bytes of an upstream response body while the
// stream handler consumes it normally.
//
// The cut counters (visible content, reasoning, tool calls, choices) cannot tell
// an empty body from a body this handler could not parse: both read as zero.
// On 2026-10-30 a row that said only "upstream stream ended without a terminal
// finish_reason" reached six different upstreams (official included) with zero
// events each, and nothing in /api/log/ could say whether the endpoint refused
// the request, answered with a JSON error object, or simply closed the socket.
type streamHeadTee struct {
	limit int
	buf   []byte
	total int64
}

func newStreamHeadTee(limit int) *streamHeadTee {
	if limit <= 0 {
		limit = streamHeadCaptureLimit
	}
	return &streamHeadTee{limit: limit}
}

func (t *streamHeadTee) Write(p []byte) (int, error) {
	if t == nil {
		return len(p), nil
	}
	t.total += int64(len(p))
	if room := t.limit - len(t.buf); room > 0 {
		if len(p) <= room {
			t.buf = append(t.buf, p...)
		} else {
			t.buf = append(t.buf, p[:room]...)
		}
	}
	return len(p), nil
}

// wrap returns a ReadCloser that forwards every read to the capture and keeps
// the original Close, so response cleanup is unchanged.
func (t *streamHeadTee) wrap(rc io.ReadCloser) io.ReadCloser {
	if t == nil || rc == nil {
		return rc
	}
	return &teeReadCloser{ReadCloser: rc, tee: t}
}

type teeReadCloser struct {
	io.ReadCloser
	tee io.Writer
}

func (r *teeReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		_, _ = r.tee.Write(p[:n])
	}
	return n, err
}

// bytesRead is the total number of bytes the upstream wrote, not just the
// captured prefix: 0 means the upstream closed without sending anything.
func (t *streamHeadTee) bytesRead() int64 {
	if t == nil {
		return 0
	}
	return t.total
}

// head renders the captured prefix for the failure row. Control bytes become
// spaces so the JSON stays readable on one line, invalid UTF-8 left over from
// the truncation is replaced, and a trailing marker records that the body went
// on past the capture.
func (t *streamHeadTee) head() string {
	if t == nil || len(t.buf) == 0 {
		return ""
	}
	s := strings.ToValidUTF8(string(t.buf), "\uFFFD")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r == '\r' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if t.total > int64(len(t.buf)) {
		s += " …"
	}
	return s
}

// attachStreamHead records the upstream's own bytes on a cut that delivered no
// answer payload at all. A row that did deliver something is left untouched: the
// captured prefix would be answer text the caller already has.
func attachStreamHead(diag *relaycommon.StreamCutDiagnostics, tee *streamHeadTee) {
	if diag == nil || tee == nil {
		return
	}
	if diag.VisibleContentChars != 0 || diag.ReasoningChars != 0 || diag.ToolCalls != 0 {
		return
	}
	diag.UpstreamBytes = tee.bytesRead()
	diag.UpstreamHead = tee.head()
}
