package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIOCopyBytesGracefullyClampsClientUsage pins the non-stream Phase B P0-1
// hook: a verbatim upstream body with an accumulated completion count is
// clamped (and its Content-Length recomputed) before it reaches the client.
func TestIOCopyBytesGracefullyClampsClientUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	common.SetContextKey(c, constant.ContextKeyRelayInfoPtr, &relaycommon.RelayInfo{ClientMaxTokens: 100})

	body := []byte(`{"id":"r1","usage":{"input_tokens":50,"output_tokens":457,"total_tokens":507}}`)
	require.NoError(t, IOCopyBytesGracefully(c, nil, body))

	out := recorder.Body.String()
	assert.Contains(t, out, `"output_tokens":100`)
	assert.Contains(t, out, `"total_tokens":150`)
	assert.NotContains(t, out, `"output_tokens":457`)
	assert.Equal(t, "78", recorder.Header().Get("Content-Length"))
}
