package helper

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// TestClaudeChunkDataSanitizesImplausibleUsage: the Claude-native SSE writer
// must not forward an implausible usage verbatim. Found by the .52 end-to-end
// matrix: message_start / message_delta events carried the incident's 1.39e15
// counts straight through ClaudeChunkData because only the typed patch path
// was sanitized.
func TestClaudeChunkDataSanitizesImplausibleUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	garbageEvent := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":562949953421056,"input_tokens":1388347719831046}}`
	ClaudeChunkData(c, dto.ClaudeResponse{Type: "message_delta"}, garbageEvent)

	body := recorder.Body.String()
	assert.Contains(t, body, "event: message_delta")
	assert.NotContains(t, body, "562949953421056", "the implausible output_tokens must not reach the client")
	assert.NotContains(t, body, "1388347719831046", "the implausible input_tokens must not reach the client")
	assert.Contains(t, body, `"input_tokens":0`, "the usage object is zeroed, not deleted")
}

// TestClaudeChunkDataNormalEventUnchanged: a plausible usage passes through
// byte-identical (no re-encode on the happy path).
func TestClaudeChunkDataNormalEventUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	normalEvent := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`
	ClaudeChunkData(c, dto.ClaudeResponse{Type: "message_delta"}, normalEvent)

	body := recorder.Body.String()
	assert.Contains(t, body, `"output_tokens":42`)
	assert.Contains(t, body, normalEvent, "plausible events are forwarded verbatim")
}
