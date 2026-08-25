package helper

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relay/constant"
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

func TestDeepSeekV4RequiredToolChoiceIsAccepted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"天气？"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}],"tool_choice":"required"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	request, err := GetAndValidateTextRequest(c, constant.RelayModeChatCompletions)
	require.NoError(t, err)
	assert.Equal(t, "required", request.ToolChoice)
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
