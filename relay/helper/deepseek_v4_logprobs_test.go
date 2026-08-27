package helper

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestDeepSeekV4LogprobsValidationMatchesOfficialErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newContext := func(body string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		// The strict validators run only for users with the official-fit
		// Validate dimension; inject it here.
		common.SetContextKey(c, coreconstant.ContextKeyUserSetting, dto.UserSetting{
			OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
				"deepseek-v4-": {Validate: true},
			}},
		})
		return c
	}

	tests := []struct {
		name    string
		body    string
		message string
	}{
		{
			name:    "top_logprobs requires logprobs true",
			body:    `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"1+1=?"}],"top_logprobs":5}`,
			message: "Invalid top_logprobs and logprobs value, logprobs must be set to true if top_logprobs is used.",
		},
		{
			name:    "top_logprobs upper bound",
			body:    `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"1+1=?"}],"logprobs":true,"top_logprobs":21}`,
			message: "Invalid top_logprobs value, the valid range of top_logprobs is [0, 20].",
		},
		{
			name:    "extreme reasoning effort is rejected",
			body:    `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"1+1=?"}],"reasoning_effort":"extreme"}`,
			message: "Invalid reasoning_effort value, the valid values are [low, high, max].",
		},
		{
			name:    "top_p above one is rejected",
			body:    `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"1+1=?"}],"top_p":1.5}`,
			message: "Invalid top_p value, the valid range of top_p is (0, 1].",
		},
		{
			name:    "temperature above two is rejected",
			body:    `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"你好"}],"temperature":2.5}`,
			message: "Invalid temperature value, the valid range of temperature is [0, 2]",
		},
		{
			name:    "negative temperature is rejected",
			body:    `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"你好"}],"temperature":-0.5}`,
			message: "Invalid temperature value, the valid range of temperature is [0, 2]",
		},
		{
			name:    "zero top_p is rejected",
			body:    `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"1+1=?"}],"top_p":0}`,
			message: "Invalid top_p value, the valid range of top_p is (0, 1].",
		},
		{
			name:    "json_object requires the word json in the prompt",
			body:    `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"世界上最长的河是哪条？尼罗河。"}],"response_format":{"type":"json_object"}}`,
			message: "Prompt must contain the word 'json' in some form to use 'response_format' of type 'json_object'.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GetAndValidateTextRequest(newContext(tt.body), constant.RelayModeChatCompletions)
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			oaiErr := apiErr.ToOpenAIError()
			assert.Equal(t, tt.message, oaiErr.Message)
			assert.Equal(t, "invalid_request_error", oaiErr.Type)
			assert.Equal(t, "invalid_request_error", oaiErr.Code)
			assert.Nil(t, oaiErr.Param)
		})
	}
}

func TestDeepSeekV4ValidationGatedByOfficialFitProfile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// Without an official-fit profile the strict validators are skipped and
	// the platform-compatible behavior applies: the request passes validation.
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"1+1=?"}],"top_p":1.5,"top_logprobs":5,"reasoning_effort":"extreme"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	req, err := GetAndValidateTextRequest(c, constant.RelayModeChatCompletions)
	require.NoError(t, err)
	require.NotNil(t, req.TopP)
	assert.Equal(t, 1.5, *req.TopP)
	require.NotNil(t, req.TopLogProbs)
	assert.Equal(t, 5, *req.TopLogProbs)
	assert.Equal(t, "extreme", req.ReasoningEffort)
}

func TestKimiK3ValidationGatedByOfficialFitProfile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// kimi-k3 validation is likewise gated: with the profile enabled the
	// fixed-sampling rules reject, without it the request passes.
	body := `{"model":"kimi-k3","messages":[{"role":"user","content":"1+1=?"}],"temperature":0}`
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	_, err := GetAndValidateTextRequest(c, constant.RelayModeChatCompletions)
	require.NoError(t, err)

	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	c2.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	c2.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c2, coreconstant.ContextKeyUserSetting, dto.UserSetting{
		OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"kimi-k3": {Validate: true},
		}},
	})
	_, err = GetAndValidateTextRequest(c2, constant.RelayModeChatCompletions)
	require.Error(t, err)
	var apiErr *types.NewAPIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, kimiK3TemperatureMessage, apiErr.ToOpenAIError().Message)
}

func TestDeepSeekV4RequiredToolChoiceIsAccepted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"天气？"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}],"tool_choice":"required"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	request, err := GetAndValidateTextRequest(c, constant.RelayModeChatCompletions)
	require.NoError(t, err)
	assert.Equal(t, "required", request.ToolChoice)
}

func TestDeepSeekV4JsonObjectValidationBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name string
		body string
	}{
		{"json word in user message", `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"请用json表示最长的河。"}],"response_format":{"type":"json_object"}}`},
		{"json word uppercase is accepted", `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"请用JSON表示最长的河。"}],"response_format":{"type":"json_object"}}`},
		{"json word in system message", `{"model":"deepseek-v4-flash","messages":[{"role":"system","content":"Respond in json."},{"role":"user","content":"最长的河？"}],"response_format":{"type":"json_object"}}`},
		{"other response_format types are untouched", `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"最长的河？"}],"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":{"type":"object"}},"strict":true}}`},
		{"non-V4 model json_object is untouched", `{"model":"gpt-4o","messages":[{"role":"user","content":"最长的河？"}],"response_format":{"type":"json_object"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")
			_, err := GetAndValidateTextRequest(c, constant.RelayModeChatCompletions)
			require.NoError(t, err)
		})
	}
}

func TestDeepSeekV4LogprobsValidationLeavesOtherModelsUntouched(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-4o","messages":[{"role":"user","content":"1+1=?"}],"top_logprobs":21}`))
	c.Request.Header.Set("Content-Type", "application/json")
	request, err := GetAndValidateTextRequest(c, constant.RelayModeChatCompletions)
	require.NoError(t, err)
	require.NotNil(t, request.TopLogProbs)
	assert.Equal(t, 21, *request.TopLogProbs)
}

func TestDeepSeekV4SamplingBoundsAreAccepted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// temperature=0, temperature=2 (upper boundary) and top_p=0.1 are all
	// inside the official ranges and must pass validation untouched.
	tests := []struct {
		name  string
		body  string
		field string
		want  float64
	}{
		{"temperature zero", `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"你好"}],"temperature":0}`, "temperature", 0},
		{"temperature upper boundary", `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"你好"}],"temperature":2}`, "temperature", 2},
		{"top_p lower sample", `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"你好"}],"top_p":0.1}`, "top_p", 0.1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")
			request, err := GetAndValidateTextRequest(c, constant.RelayModeChatCompletions)
			require.NoError(t, err)
			switch tt.field {
			case "temperature":
				require.NotNil(t, request.Temperature)
				assert.Equal(t, tt.want, *request.Temperature)
			case "top_p":
				require.NotNil(t, request.TopP)
				assert.Equal(t, tt.want, *request.TopP)
			}
		})
	}
}
