package helper

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The rejection matrix is calibrated against the live open.bigmodel.cn
// api/paas/v4 endpoint (2026-08-28). Only these are rejected: disabled
// thinking, reasoning_effort outside low/high/max, temperature/top_p outside
// [0,1], empty messages and unknown model ids.
func TestGlm53OfficialFieldsReject(t *testing.T) {
	base := dto.GeneralOpenAIRequest{Model: "glm-5.3", Messages: []dto.Message{{Role: "user", Content: "1+1=?"}}}
	rejected := []struct {
		name    string
		request *dto.GeneralOpenAIRequest
		code    string
		message string
	}{
		{
			"thinking disabled",
			&dto.GeneralOpenAIRequest{Model: "glm-5.3", Messages: base.Messages,
				THINKING: []byte(`{"type":"disabled"}`)},
			"1210", glm53ThinkingMessage,
		},
		{
			"effort ultra",
			&dto.GeneralOpenAIRequest{Model: "glm-5.3", Messages: base.Messages, ReasoningEffort: "ultra"},
			"1210", glm53ThinkingMessage,
		},
		{
			"effort medium",
			&dto.GeneralOpenAIRequest{Model: "glm-5.3", Messages: base.Messages, ReasoningEffort: "medium"},
			"1210", glm53ThinkingMessage,
		},
		{
			"temperature above one",
			&dto.GeneralOpenAIRequest{Model: "glm-5.3", Messages: base.Messages, Temperature: floatPtr(2.5)},
			"1210", glm53TemperatureMessage,
		},
		{
			"temperature negative",
			&dto.GeneralOpenAIRequest{Model: "glm-5.3", Messages: base.Messages, Temperature: floatPtr(-0.1)},
			"1210", glm53TemperatureMessage,
		},
		{
			"top_p above one",
			&dto.GeneralOpenAIRequest{Model: "glm-5.3", Messages: base.Messages, TopP: floatPtr(1.5)},
			"1210", glm53TopPMessage,
		},
		{
			"empty messages",
			&dto.GeneralOpenAIRequest{Model: "glm-5.3"},
			"1214", glm53EmptyMessagesMessage,
		},
	}
	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGlm53OfficialFields(tt.request)
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			oaiErr := apiErr.ToOpenAIError()
			assert.Equal(t, tt.code, oaiErr.Code)
			assert.Equal(t, tt.message, oaiErr.Message)
		})
	}
}

// The official endpoint tolerates n, penalties, logprobs, top_logprobs,
// arbitrary tool names, specified tool_choice, json_object without the "json"
// keyword, explicit default sampling values and low-reasoning effort; none of
// these may be rejected locally when fit mode is on.
func TestGlm53OfficialFieldsAccept(t *testing.T) {
	base := dto.GeneralOpenAIRequest{Model: "glm-5.3", Messages: []dto.Message{{Role: "user", Content: "1+1=?"}}}
	accepted := []*dto.GeneralOpenAIRequest{
		&base,
		{Model: "glm-5.3", Messages: base.Messages, THINKING: []byte(`{"type":"enabled"}`)},
		{Model: "glm-5.3", Messages: base.Messages, ReasoningEffort: "low"},
		{Model: "glm-5.3", Messages: base.Messages, ReasoningEffort: "high"},
		{Model: "glm-5.3", Messages: base.Messages, ReasoningEffort: "max"},
		{Model: "glm-5.3", Messages: base.Messages, Temperature: floatPtr(0)},
		{Model: "glm-5.3", Messages: base.Messages, Temperature: floatPtr(1.0)},
		{Model: "glm-5.3", Messages: base.Messages, TopP: floatPtr(0.1)},
		{Model: "glm-5.3", Messages: base.Messages, TopP: floatPtr(0.95)},
		{Model: "glm-5.3", Messages: base.Messages, N: intPtr(2)},
		{Model: "glm-5.3", Messages: base.Messages, PresencePenalty: floatPtr(1.5)},
		{Model: "glm-5.3", Messages: base.Messages, FrequencyPenalty: floatPtr(1.5)},
		{Model: "glm-5.3", Messages: base.Messages, LogProbs: boolPtr(true)},
		{Model: "glm-5.3", Messages: base.Messages, TopLogProbs: intPtr(5)},
		{Model: "glm-5.3", Messages: base.Messages, LogProbs: boolPtr(true), TopLogProbs: intPtr(21)},
		{Model: "glm-5.3", Messages: base.Messages,
			ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}},
		{Model: "glm-5.3", Messages: base.Messages,
			Tools: []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "get weather"}}}},
		{Model: "glm-5.3", Messages: base.Messages,
			ResponseFormat: &dto.ResponseFormat{Type: "json_object"}},
		// non-5.3 models are never inspected
		{Model: "glm-5.2", THINKING: []byte(`{"type":"disabled"}`)},
		{Model: "deepseek-v4-flash", Temperature: floatPtr(2.5)},
	}
	for i, req := range accepted {
		assert.NoError(t, validateGlm53OfficialFields(req), "case %d", i)
	}
}

func TestGlm53ValidationMessageRecognized(t *testing.T) {
	for _, msg := range []string{
		glm53ThinkingMessage,
		glm53TemperatureMessage,
		glm53TopPMessage,
		glm53ModelNotFoundMessage,
		glm53EmptyMessagesMessage,
	} {
		assert.True(t, IsStrictGlmValidationMessage(msg), "%q", msg)
	}
	// DS/K3 texts are not GLM texts and vice versa.
	assert.False(t, IsStrictGlmValidationMessage("Invalid temperature value, the valid range of temperature is [0, 2]"))
	assert.False(t, IsStrictFitValidationMessage(glm53ThinkingMessage))
}

func TestGlm53OfficialModelNames(t *testing.T) {
	assert.True(t, IsGlm53OfficialModelName("glm-5.3"))
	assert.True(t, IsGlm53OfficialModelName("glm-5.3-flash"))
	assert.False(t, IsGlm53OfficialModelName("glm-5.3x"))
	assert.False(t, IsGlm53OfficialModelName("glm-5.2"))
}
