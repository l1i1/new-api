package helper

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newProbeContext builds a context whose writer is a real recorder, so Size()
// reflects what actually reached the client.
func newProbeContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
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
	_, err := recorder.Body.WriteString(payload)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &got), "probes must not break JSON parsing")

	var want map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &want))
	assert.Equal(t, want, got)
	assert.Equal(t, "  "+payload, recorder.Body.String(), "probes are a prefix, never interleaved")
}

// A probe must be booked as keep-alive output, otherwise a retry after probes
// would look like it had already delivered a payload and a legitimate channel
// swap would be refused (or worse, two bodies concatenated).
func TestWhitespaceDataCountsAsKeepAliveOnly(t *testing.T) {
	c, _ := newProbeContext(t)

	require.NoError(t, WhitespaceData(c))
	assert.Equal(t, ResponseKeepAliveOnly, ResponseCommitStateOf(c))

	require.NoError(t, StringData(c, `{"ok":true}`))
	assert.Equal(t, ResponsePayloadWritten, ResponseCommitStateOf(c))
}

// PingData's SSE frame is unaffected by the refactor that introduced the shared
// probe writer.
func TestPingDataStillWritesSSEComment(t *testing.T) {
	c, recorder := newProbeContext(t)

	require.NoError(t, PingData(c))
	assert.Equal(t, ": PING\n\n", recorder.Body.String())
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
	assert.Empty(t, recorder.Body.String())
}
