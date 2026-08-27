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

func TestKimiK3OfficialFieldsReject(t *testing.T) {
	rejected := []struct {
		name    string
		request *dto.GeneralOpenAIRequest
		message string
	}{
		{
			"thinking parameter is illegal on kimi-k3",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", THINKING: []byte(`{"type":"disabled"}`)},
			kimiK3ThinkingMessage,
		},
		{
			"reasoning_effort invalid enum",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", ReasoningEffort: "ultra"},
			kimiK3ReasoningEffortMessage,
		},
		{
			"temperature non-fixed",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Temperature: floatPtr(0.5)},
			kimiK3TemperatureMessage,
		},
		{
			"temperature zero",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Temperature: floatPtr(0)},
			kimiK3TemperatureMessage,
		},
		{
			"top_p non-fixed",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", TopP: floatPtr(1.5)},
			kimiK3TopPMessage,
		},
		{
			"n above one",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", N: intPtr(2)},
			kimiK3NMessage,
		},
		{
			"presence_penalty non-fixed",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", PresencePenalty: floatPtr(1.5)},
			kimiK3PresencePenaltyMessage,
		},
		{
			"frequency_penalty non-fixed",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", FrequencyPenalty: floatPtr(1.5)},
			kimiK3FrequencyPenaltyMessage,
		},
		{
			"top_logprobs above 20",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", TopLogProbs: intPtr(21)},
			kimiK3TopLogprobsRangeMessage,
		},
		{
			"tool_choice specified function object",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3",
				ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}},
			kimiK3ToolChoiceSpecifiedMessage,
		},
	}
	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			err := validateKimiK3OfficialFields(tt.request)
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			assert.Equal(t, tt.message, apiErr.ToOpenAIError().Message)
		})
	}
}

func TestKimiK3OfficialFieldsAccept(t *testing.T) {
	accepted := []*dto.GeneralOpenAIRequest{
		{Model: "kimi-k3", Messages: []dto.Message{{Role: "user", Content: "1+1=?"}}},
		{Model: "kimi-k3", ReasoningEffort: "low"},
		{Model: "kimi-k3", ReasoningEffort: "max"},
		{Model: "kimi-k3", Temperature: floatPtr(1.0)},
		{Model: "kimi-k3", TopP: floatPtr(0.95)},
		{Model: "kimi-k3", N: intPtr(1)},
		{Model: "kimi-k3", PresencePenalty: floatPtr(0)},
		{Model: "kimi-k3", FrequencyPenalty: floatPtr(0)},
		{Model: "kimi-k3", TopLogProbs: intPtr(20)},
		{Model: "kimi-k3", ToolChoice: "required"},
		{Model: "kimi-k3", ToolChoice: "none"},
		// non-kimi models are never inspected
		{Model: "deepseek-v4-flash", ReasoningEffort: "extreme", Temperature: floatPtr(0)},
		{Model: "kimi-k2.6", THINKING: []byte(`{"type":"disabled"}`)},
	}
	for i, req := range accepted {
		assert.NoError(t, validateKimiK3OfficialFields(req), "case %d", i)
	}
}

func TestKimiK3ValidationMessageRecognized(t *testing.T) {
	for _, msg := range []string{
		kimiK3ThinkingMessage,
		kimiK3ReasoningEffortMessage,
		kimiK3TemperatureMessage,
		kimiK3TopPMessage,
		kimiK3NMessage,
		kimiK3PresencePenaltyMessage,
		kimiK3FrequencyPenaltyMessage,
		kimiK3TopLogprobsRangeMessage,
		kimiK3ToolChoiceSpecifiedMessage,
	} {
		assert.True(t, IsStrictFitValidationMessage(msg), "%q", msg)
	}
	// DeepSeek texts are still recognized by the combined predicate.
	assert.True(t, IsStrictFitValidationMessage("Invalid top_p value, the valid range of top_p is (0, 1]."))
	assert.False(t, IsStrictFitValidationMessage("some internal platform error"))
}

func intPtr(v int) *int { return &v }
