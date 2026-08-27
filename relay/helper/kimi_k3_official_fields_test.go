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

// The rejection matrix is calibrated against the live api.moonshot.cn API
// (2026-08-27): fixed sampling values, logprobs disabled, top_logprobs pair
// requirement and no specified tool_choice.
func TestKimiK3OfficialFieldsReject(t *testing.T) {
	rejected := []struct {
		name    string
		request *dto.GeneralOpenAIRequest
		message string
	}{
		{
			"temperature zero",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Temperature: floatPtr(0)},
			kimiK3TemperatureMessage,
		},
		{
			"temperature non-fixed",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Temperature: floatPtr(0.5)},
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
			"logprobs true is rejected by design",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", LogProbs: boolPtr(true)},
			kimiK3LogprobsFalseMessage,
		},
		{
			"top_logprobs without logprobs rejects the pair",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", TopLogProbs: intPtr(5)},
			kimiK3TopLogprobsPairMessage,
		},
		{
			"top_logprobs with logprobs false rejects the pair",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", LogProbs: boolPtr(false), TopLogProbs: intPtr(5)},
			kimiK3TopLogprobsPairMessage,
		},
		{
			"tool_choice specified function object",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3",
				ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}},
			kimiK3ToolChoiceSpecifiedMessage,
		},
		{
			"illegal tool name with space",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3",
				Tools: []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "get weather"}}}},
			kimiK3ToolNameMessage,
		},
		{
			"tool name starting with a digit",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3",
				Tools: []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "1weather"}}}},
			kimiK3ToolNameMessage,
		},
		{
			"tool name with a dot",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3",
				Tools: []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "get.weather"}}}},
			kimiK3ToolNameMessage,
		},
		{
			"empty messages",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3"},
			kimiK3MessagesEmptyMessage,
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

// The official API is laxer than the docs: thinking and arbitrary
// reasoning_effort strings are accepted, and none of these may be rejected
// locally when fit mode is on.
func TestKimiK3OfficialFieldsAccept(t *testing.T) {
	base := dto.GeneralOpenAIRequest{Model: "kimi-k3", Messages: []dto.Message{{Role: "user", Content: "1+1=?"}}}
	accepted := []*dto.GeneralOpenAIRequest{
		&base,
		{Model: "kimi-k3", Messages: base.Messages, THINKING: []byte(`{"type":"disabled"}`)},
		{Model: "kimi-k3", Messages: base.Messages, ReasoningEffort: "ultra"},
		{Model: "kimi-k3", Messages: base.Messages, ReasoningEffort: "low"},
		{Model: "kimi-k3", Messages: base.Messages, ReasoningEffort: "max"},
		{Model: "kimi-k3", Messages: base.Messages, Temperature: floatPtr(1.0)},
		{Model: "kimi-k3", Messages: base.Messages, TopP: floatPtr(0.95)},
		{Model: "kimi-k3", Messages: base.Messages, N: intPtr(1)},
		{Model: "kimi-k3", Messages: base.Messages, PresencePenalty: floatPtr(0)},
		{Model: "kimi-k3", Messages: base.Messages, FrequencyPenalty: floatPtr(0)},
		{Model: "kimi-k3", Messages: base.Messages, LogProbs: boolPtr(false)},
		{Model: "kimi-k3", Messages: base.Messages, ToolChoice: "required"},
		{Model: "kimi-k3", Messages: base.Messages, ToolChoice: "none"},
		{Model: "kimi-k3", Messages: base.Messages, ToolChoice: "auto"},
		// FIM-style prefix/suffix requests may omit messages (same exemption
		// as the generic path).
		{Model: "kimi-k3", Prefix: "<fill>", Suffix: "</fill>"},
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
		kimiK3TemperatureMessage,
		kimiK3TopPMessage,
		kimiK3NMessage,
		kimiK3PresencePenaltyMessage,
		kimiK3FrequencyPenaltyMessage,
		kimiK3LogprobsFalseMessage,
		kimiK3TopLogprobsPairMessage,
		kimiK3ToolChoiceSpecifiedMessage,
		kimiK3MessagesEmptyMessage,
	} {
		assert.True(t, IsStrictFitValidationMessage(msg), "%q", msg)
	}
	// DeepSeek texts are still recognized by the combined predicate.
	assert.True(t, IsStrictFitValidationMessage("Invalid top_p value, the valid range of top_p is (0, 1.0]"))
	assert.False(t, IsStrictFitValidationMessage("some internal platform error"))
}

func boolPtr(value bool) *bool { return &value }

func intPtr(v int) *int { return &v }
