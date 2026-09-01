package helper

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	coreconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDeepSeekV4FitContext builds a chat-completions request context whose user
// carries the official-fit Validate dimension, mirroring the buyer's tester.
func newDeepSeekV4FitContext(body string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, coreconstant.ContextKeyUserSetting, dto.UserSetting{
		OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"deepseek-v4-": {Validate: true},
		}},
	})
	return c
}

func TestDeepSeekV4StopArrayValidationMatchesOfficial(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stopArray := func(n int) string {
		items := make([]string, n)
		for i := range items {
			items[i] = `"word"`
		}
		return "[" + strings.Join(items, ",") + "]"
	}

	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "17 stop items exceed the official cap",
			body:    `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"stop":` + stopArray(17) + `}`,
			wantErr: "Stop string array too long: 17",
		},
		{
			name: "16 stop items are accepted",
			body: `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"stop":` + stopArray(16) + `}`,
		},
		{
			name: "single string stop is accepted",
			body: `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"stop":"香蕉"}`,
		},
		{
			name: "non-V4 model stop array is untouched",
			body: `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stop":` + stopArray(20) + `}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GetAndValidateTextRequest(newDeepSeekV4FitContext(tt.body), constant.RelayModeChatCompletions)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			oaiErr := apiErr.ToOpenAIError()
			assert.Equal(t, tt.wantErr, oaiErr.Message)
			assert.Equal(t, "invalid_request_error", oaiErr.Type)
			assert.Equal(t, "invalid_request_error", oaiErr.Code)
			assert.Nil(t, oaiErr.Param)
		})
	}
}

func TestDeepSeekV4MaxTokensValidationMatchesOfficial(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "max_tokens above the 384K model limit is rejected",
			body:    `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"max_tokens":393217}`,
			wantErr: "Invalid max_tokens value, the valid range of max_tokens is [1, 393216]",
		},
		{
			name: "max_tokens at the 384K model limit is accepted",
			body: `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"max_tokens":393216}`,
		},
		{
			name: "non-V4 model max_tokens is untouched",
			body: `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":999999}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GetAndValidateTextRequest(newDeepSeekV4FitContext(tt.body), constant.RelayModeChatCompletions)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			oaiErr := apiErr.ToOpenAIError()
			assert.Equal(t, tt.wantErr, oaiErr.Message)
			assert.Equal(t, "invalid_request_error", oaiErr.Type)
			assert.Equal(t, "invalid_request_error", oaiErr.Code)
			assert.Nil(t, oaiErr.Param)
		})
	}
}

func TestDeepSeekV4ReasoningEffortSilentMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name string
		body string
		want string
	}{
		{"medium maps to high", `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`, "high"},
		{"xhigh maps to high", `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`, "high"},
		{"low is untouched", `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`, "low"},
		{"max is untouched", `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"max"}`, "max"},
		{"none is untouched", `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`, "none"},
		{"minimal is untouched", `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"minimal"}`, "minimal"},
		{"mapping is case-sensitive like the official serde", `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"Medium"}`, "Medium"},
		{"non-V4 model is untouched", `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`, "medium"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := GetAndValidateTextRequest(newDeepSeekV4FitContext(tt.body), constant.RelayModeChatCompletions)
			require.NoError(t, err)
			assert.Equal(t, tt.want, request.ReasoningEffort)
		})
	}

	t.Run("mapping applies without an official-fit profile", func(t *testing.T) {
		// The mapping is official model-family behavior, not a fit-only rule:
		// aggregator upstreams with a narrower enum must never see medium/xhigh.
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			bytes.NewBufferString(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`))
		c.Request.Header.Set("Content-Type", "application/json")
		request, err := GetAndValidateTextRequest(c, constant.RelayModeChatCompletions)
		require.NoError(t, err)
		assert.Equal(t, "high", request.ReasoningEffort)
	})
}

func TestDeepSeekV4OfficialPinThinkingAndLogprobs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name    string
		request *dto.GeneralOpenAIRequest
		pinned  bool
	}{
		{"explicit thinking object pins",
			&dto.GeneralOpenAIRequest{Model: "deepseek-v4-pro", THINKING: []byte(`{"type":"disabled"}`)}, true},
		{"thinking enabled pins too",
			&dto.GeneralOpenAIRequest{Model: "deepseek-v4-pro", THINKING: []byte(`{"type":"enabled"}`)}, true},
		{"logprobs true pins",
			&dto.GeneralOpenAIRequest{Model: "deepseek-v4-pro", LogProbs: boolPtr(true)}, true},
		{"logprobs false does not pin",
			&dto.GeneralOpenAIRequest{Model: "deepseek-v4-pro", LogProbs: boolPtr(false)}, false},
		{"plain request does not pin",
			&dto.GeneralOpenAIRequest{Model: "deepseek-v4-pro"}, false},
		{"thinking on a non-V4 model does not pin",
			&dto.GeneralOpenAIRequest{Model: "gpt-4o", THINKING: []byte(`{"type":"disabled"}`)}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			common.SetContextKey(c, coreconstant.ContextKeyV4OfficialPin, false)

			markDeepSeekV4OfficialPin(c, test.request)

			assert.Equal(t, test.pinned, common.GetContextKeyBool(c, coreconstant.ContextKeyV4OfficialPin))
		})
	}
}
