package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
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
	// The strict-fit path in this test requires the official-fit profile
	// (Validate rejects the request, Errors keeps the message verbatim).
	common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{
		OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"deepseek-v4-": {Validate: true, Errors: true},
		}},
	})

	Relay(c, types.RelayFormatOpenAI)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Equal(t, "application/octet-stream", recorder.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"error":{"message":"Invalid top_logprobs and logprobs value, logprobs must be set to true if top_logprobs is used.","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "request id")
}

// TestRelayRendersMoonshotTwoFieldEnvelopeForKimiK3 mirrors the DeepSeek test
// above for the other wire shape. Official K3 answers a business rejection with
// exactly {error:{message,type}} — no param, no code (live-probed 2026-09-22:
// 14/14 sampled 400s). The shared OpenAIError always renders both extra keys
// because their tags carry no omitempty, so this test is what keeps the
// explicit two-field mapping from silently regressing back to four fields.
func TestRelayRendersMoonshotTwoFieldEnvelopeForKimiK3(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"kimi-k3","messages":[{"role":"user","content":"1+1=?"}],"top_p":0.1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "kimi-k3")
	common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{
		OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"kimi-k3": {Validate: true, Errors: true},
		}},
	})

	Relay(c, types.RelayFormatOpenAI)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"error":{"message":"invalid top_p: only 0.95 is allowed for this model","type":"invalid_request_error"}}`, recorder.Body.String())
	for _, forbidden := range []string{`"param"`, `"code"`, "request id"} {
		assert.NotContains(t, recorder.Body.String(), forbidden)
	}
}
