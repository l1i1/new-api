package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayDoesNotAppendJSONErrorAfterCommittedStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{"))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	_, err := c.Writer.Write([]byte("data: partial\n\n"))
	require.NoError(t, err)

	Relay(c, types.RelayFormatOpenAI)

	assert.Equal(t, "data: partial\n\n", recorder.Body.String())
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
}

func TestRelayUsesOfficialContentTypeForDeepSeekV4Validation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"1+1=?"}],"top_logprobs":5}`))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "deepseek-v4-flash")

	Relay(c, types.RelayFormatOpenAI)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Equal(t, "application/octet-stream", recorder.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"error":{"message":"Invalid top_logprobs and logprobs value, logprobs must be set to true if top_logprobs is used.","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "request id")
}
