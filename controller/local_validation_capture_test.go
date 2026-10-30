package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestLocalValidationCaptureKeepsTheRejectedBody pins the diagnostic gap the
// hook closes (2026-10-30: ~81k `unsupported image url: [image/jpeg]` local
// rejections carried no request body anywhere — the response middleware only
// observes requests that reached an upstream, and a local rejection answers
// before the distributor runs).
func TestLocalValidationCaptureKeepsTheRejectedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := `{"model":"kimi-k3.1-pro","stream":true,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":["image/jpeg"]}}]}]}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Authorization", "Bearer sk-live-secret")
	c.Set(common.RequestIdKey, "req-capture-local-1")
	c.Set("token_id", 246)
	c.Set("id", 3)
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "kimi-k3.1-pro")

	setting := &operation_setting.RequestCaptureSetting{
		Enabled: true,
		Rules: []operation_setting.RequestCaptureRule{{
			Name:             "kimi-400-probe",
			Enabled:          true,
			ModelRegex:       []string{"kimi-k3"},
			ResponseStatusIn: "400-499",
			Include:          operation_setting.RequestCaptureInclude{RequestHeaders: true, RequestBody: true},
			RedactHeaders:    []string{"Authorization"},
		}},
	}
	rule := &setting.Rules[0]
	rejection := types.NewOpenAIError(errors.New("Invalid request: unsupported image url: [image/jpeg]"),
		types.ErrorCodeInvalidRequest, http.StatusBadRequest)

	payload := buildLocalValidationCapturePayload(c, rejection, rule, setting, "kimi-k3.1-pro", "/v1/chat/completions", 246, 3)
	require.NotNil(t, payload)
	require.Equal(t, "kimi-400-probe", payload.RuleName)
	require.Equal(t, "req-capture-local-1", payload.RequestID)
	require.Equal(t, "kimi-k3.1-pro", payload.Model)
	require.Equal(t, 246, payload.TokenID)
	require.Equal(t, 0, payload.ChannelID, "a locally rejected request belongs to no channel")
	require.Equal(t, http.StatusBadRequest, payload.ResponseStatus)
	require.Contains(t, payload.RequestBody, `"image_url"`, "the rejected body is the whole point of the capture")
	require.Contains(t, payload.RequestBody, `["image/jpeg"]`, "the malformed image value must survive verbatim")
	require.Equal(t, "[redacted]", payload.RequestHeaders["Authorization"], "credentials never reach disk")
}
