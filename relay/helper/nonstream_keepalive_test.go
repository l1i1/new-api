package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncRecorder is an http.ResponseWriter that tolerates the test reading it while
// the keep-alive goroutine writes (the production writer is only ever touched by
// one goroutine at a time — that is what the write guard guarantees). Reading a
// plain httptest.ResponseRecorder concurrently trips the race detector.
type syncRecorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncRecorder) Header() http.Header { return http.Header{} }

func (s *syncRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncRecorder) WriteHeader(int) {}

func (s *syncRecorder) Flush() {}

func (s *syncRecorder) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Len()
}

func (s *syncRecorder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// newProbeContext builds a context whose writer records what actually reached the
// client, so Size() and the keep-alive byte count can be compared.
func newProbeContext(t *testing.T) (*gin.Context, *syncRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := &syncRecorder{}
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c, recorder
}

// The whole point of the non-stream probe is that it cannot change what the
// client parses: whitespace is insignificant JSON, so a body prefixed with the
// probes must decode to exactly the same value as the body alone.
func TestWhitespaceDataKeepsBodyParseable(t *testing.T) {
	c, recorder := newProbeContext(t)
	payload := `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0}]}`

	require.NoError(t, WhitespaceData(c))
	require.NoError(t, WhitespaceData(c))
	_, err := recorder.Write([]byte(payload))
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(recorder.String()), &got), "probes must not break JSON parsing")

	var want map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &want))
	assert.Equal(t, want, got)
	assert.Equal(t, "\n\n"+payload, recorder.String(), "probes are a prefix, never interleaved")
}

// A probe must be booked as keep-alive output, otherwise a retry after probes
// would look like it had already delivered a payload and a legitimate channel
// swap would be refused (or worse, two bodies concatenated).
func TestWhitespaceDataCountsAsKeepAliveOnly(t *testing.T) {
	c, _ := newProbeContext(t)

	require.NoError(t, WhitespaceData(c))
	assert.Equal(t, ResponseKeepAliveOnly, ResponseCommitStateOf(c))
	assert.True(t, KeepAliveOnlyCommitted(c))

	require.NoError(t, StringData(c, `{"ok":true}`))
	assert.Equal(t, ResponsePayloadWritten, ResponseCommitStateOf(c))
	assert.False(t, KeepAliveOnlyCommitted(c))
}

// PingData's SSE frame is unaffected by the refactor that introduced the shared
// probe writer.
func TestPingDataStillWritesSSEComment(t *testing.T) {
	c, recorder := newProbeContext(t)

	require.NoError(t, PingData(c))
	assert.Equal(t, ": PING\n\n", recorder.String())
	assert.Equal(t, ResponseKeepAliveOnly, ResponseCommitStateOf(c))
}

// Probes must stop as soon as the request is over, so nothing is appended to the
// response after the handler finished writing it.
func TestWhitespaceDataRefusesAfterContextDone(t *testing.T) {
	c, recorder := newProbeContext(t)
	requestContext, cancel := context.WithCancel(context.Background())
	c.Request = c.Request.WithContext(requestContext)
	cancel()

	require.Error(t, WhitespaceData(c))
	assert.Empty(t, recorder.String())
}

// The write guard is what makes the probes outlive doRequest: they must keep
// flowing while the handler waits for the upstream body, and stop the instant the
// handler writes the real body — probes interleaved into a body would corrupt it.
func TestWriteGuardStopsProbesAtFirstRealWrite(t *testing.T) {
	c, recorder := newProbeContext(t)

	StartNonStreamKeepAlive(c, 20*time.Millisecond)
	require.Eventually(t, func() bool { return recorder.Len() > 0 }, time.Second, 5*time.Millisecond,
		"probes must start while the body is still pending")

	// The handler finally has the body and writes it through the guarded writer.
	if _, err := c.Writer.Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatalf("write body: %v", err)
	}
	settled := recorder.Len()

	time.Sleep(80 * time.Millisecond)
	assert.Equal(t, settled, recorder.Len(), "no probe may be written after the body")

	body := recorder.String()
	assert.True(t, strings.HasSuffix(body, `{"ok":true}`), "the body must not be interleaved")
	require.NoError(t, json.Unmarshal([]byte(body), &map[string]any{}))
	// Probes are booked as keep-alive output, so once real content is out the
	// commit state must no longer claim "keep-alive only".
	assert.False(t, KeepAliveOnlyCommitted(c))
}

// The probe is a newline precisely so that a response which turns out to be SSE
// is still parsed correctly: " data: {...}" would have the field name " data" and
// spec-compliant parsers would drop the first event.
func TestProbeDoesNotBreakAnSSEBody(t *testing.T) {
	c, recorder := newProbeContext(t)

	StartNonStreamKeepAlive(c, 15*time.Millisecond)
	require.Eventually(t, func() bool { return recorder.Len() > 0 }, time.Second, 5*time.Millisecond)

	// The upstream answered text/event-stream and the handler streams it.
	if _, err := c.Writer.Write([]byte("data: {\"delta\":\"hi\"}\n\n")); err != nil {
		t.Fatalf("write sse: %v", err)
	}

	stream := recorder.String()
	for _, line := range strings.Split(stream, "\n") {
		require.False(t, strings.HasPrefix(line, " "), "no SSE line may gain a leading space: %q", line)
	}
	require.True(t, strings.HasSuffix(stream, "data: {\"delta\":\"hi\"}\n\n"))
}
