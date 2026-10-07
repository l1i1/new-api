package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The capture wraps the response writer on the path that streams a customer's answer. Whatever else
// it does, it must not change what the client receives - a debugging tool that alters the thing it is
// observing is worse than no tool.
func TestCaptureResponseWriterForwardsEverythingAndStoresOnlyTheCap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	limit := 10
	w := &captureResponseWriter{ResponseWriter: c.Writer, limit: limit}
	c.Writer = w

	chunks := []string{"hello ", "world, ", "this is ", "the rest of a long streaming answer"}
	total := 0
	for _, chunk := range chunks {
		n, err := w.Write([]byte(chunk))
		require.NoError(t, err)
		require.Equal(t, len(chunk), n, "the client must still receive every byte")
		total += len(chunk)
	}

	require.Equal(t, strings.Join(chunks, ""), rec.Body.String(), "the forwarded stream must be byte-for-byte what was written")
	require.Len(t, w.body, limit, "the stored copy stops at the cap")
	require.True(t, w.truncated, "a capped capture must say so, so nobody reads it as complete")
	require.Greater(t, total, limit)
}

func TestCaptureResponseWriterRecordsTheStatusEvenWhenNothingIsWritten(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w := &captureResponseWriter{ResponseWriter: c.Writer, limit: 1024}
	c.Writer = w

	w.WriteHeader(http.StatusTooManyRequests)
	require.Equal(t, http.StatusTooManyRequests, w.status, "the capture must record the status the handler set")

	// Gin defers the real WriteHeader until the body is written or the handler chain ends, so the
	// recorder only sees it once that happens. Asserting it here also covers this wrapper's
	// WriteHeaderNow, which is the path gin actually takes at the end of a request.
	w.WriteHeaderNow()
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
}

func TestCaptureRequestHeadersRedactCredentialsByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("Authorization", "Bearer sk-live-secret")
	c.Request.Header.Set("X-Api-Key", "sk-another-secret")
	c.Request.Header.Set("X-Session-Id", "sess-1")
	c.Request.Header.Set("User-Agent", "curl/8")

	rule := &operation_setting.RequestCaptureRule{}
	got := captureRequestHeaders(c, rule)

	require.Equal(t, "[redacted]", got["Authorization"], "a capture that writes a live key to disk is worse than the bug it was taken for")
	require.Equal(t, "[redacted]", got["X-Api-Key"])
	require.Equal(t, "sess-1", got["X-Session-Id"])
	require.Equal(t, "curl/8", got["User-Agent"])

	rule.RedactHeaders = []string{"x-session-id"}
	require.Equal(t, "[redacted]", captureRequestHeaders(c, rule)["X-Session-Id"],
		"an operator can extend the redaction list without losing the built-in ones")
	require.Equal(t, "[redacted]", captureRequestHeaders(c, rule)["Authorization"])
}

func TestCaptureTruncateMarksOversizedBodies(t *testing.T) {
	body := []byte(strings.Repeat("x", 100))
	value, truncated := captureTruncate(body, 10)
	require.True(t, truncated)
	require.Len(t, value, 10)

	value, truncated = captureTruncate([]byte("short"), 10)
	require.False(t, truncated)
	require.Equal(t, "short", value)
}
