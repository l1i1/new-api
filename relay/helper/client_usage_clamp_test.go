package helper

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newClientClampContext(t *testing.T, budget int) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	common.SetContextKey(c, constant.ContextKeyRelayInfoPtr, &relaycommon.RelayInfo{ClientMaxTokens: budget})
	return c, recorder
}

// TestStringDataClampsAccumulatedClientUsage pins the Phase B P0-1 hook: a
// verbatim upstream chunk carrying an accumulated completion count above the
// client budget is clamped by the shared SSE writer.
func TestStringDataClampsAccumulatedClientUsage(t *testing.T) {
	c, recorder := newClientClampContext(t, 100)
	require.NoError(t, StringData(c, `{"id":"c1","choices":[],"usage":{"prompt_tokens":50,"completion_tokens":457,"total_tokens":507}}`))
	assert.Contains(t, recorder.Body.String(), `"completion_tokens":100`)
	assert.Contains(t, recorder.Body.String(), `"total_tokens":150`)
	assert.NotContains(t, recorder.Body.String(), `"completion_tokens":457`)
}

func TestResponseChunkDataClampsAccumulatedClientUsage(t *testing.T) {
	c, recorder := newClientClampContext(t, 100)
	require.NoError(t, ResponseChunkData(c, dto.ResponsesStreamResponse{Type: "response.completed"},
		`{"type":"response.completed","response":{"usage":{"input_tokens":50,"output_tokens":457,"total_tokens":507}}}`))
	assert.Contains(t, recorder.Body.String(), `"output_tokens":100`)
	assert.Contains(t, recorder.Body.String(), `"total_tokens":150`)
}

func TestClaudeChunkDataClampsAccumulatedClientUsage(t *testing.T) {
	c, recorder := newClientClampContext(t, 100)
	ClaudeChunkData(c, dto.ClaudeResponse{Type: "message_delta"}, `{"type":"message_delta","usage":{"input_tokens":50,"output_tokens":457}}`)
	assert.Contains(t, recorder.Body.String(), `"output_tokens":100`)
	assert.NotContains(t, recorder.Body.String(), `"output_tokens":457`)
}
