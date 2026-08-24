package deepseek

import (
	"errors"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertOpenAIRequestNormalizesDeepSeekV4CompatibilityFields(t *testing.T) {
	tests := []struct {
		name       string
		effort     string
		topP       *float64
		wantEffort string
		wantTopP   *float64
	}{
		{name: "extreme effort", effort: "extreme", wantEffort: "max"},
		{name: "upper top p", topP: float64Pointer(1.5), wantTopP: float64Pointer(1)},
		{name: "zero top p", topP: float64Pointer(0), wantTopP: float64Pointer(deepSeekV4MinimumTopP)},
		{name: "valid top p", topP: float64Pointer(0.7), wantTopP: float64Pointer(0.7)},
		{name: "omitted top p", wantTopP: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := &dto.GeneralOpenAIRequest{
				Model:           "deepseek-v4-flash",
				ReasoningEffort: test.effort,
				TopP:            test.topP,
			}
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
			}
			converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)

			require.NoError(t, err)
			got := converted.(*dto.GeneralOpenAIRequest)
			assert.Equal(t, test.wantEffort, got.ReasoningEffort)
			assert.Equal(t, test.wantEffort, info.GetReasoningEffort())
			if test.wantTopP == nil {
				assert.Nil(t, got.TopP)
				return
			}
			require.NotNil(t, got.TopP)
			assert.InDelta(t, *test.wantTopP, *got.TopP, 0.0000001)
		})
	}
}

func TestConvertOpenAIRequestPreservesCustomerControls(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Model:       "deepseek-v4-flash",
		THINKING:    []byte(`{"type":"disabled"}`),
		Stop:        "香蕉",
		LogProbs:    boolPointer(true),
		TopLogProbs: intPointer(5),
	}
	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
	}, request)

	require.NoError(t, err)
	got := converted.(*dto.GeneralOpenAIRequest)
	assert.Equal(t, `{"type":"disabled"}`, string(got.THINKING))
	assert.Equal(t, "香蕉", got.Stop)
	assert.True(t, *got.LogProbs)
	assert.Equal(t, 5, *got.TopLogProbs)
}

func TestConvertOpenAIRequestLeavesLegacyDeepSeekControlsUnchanged(t *testing.T) {
	topP := 1.5
	request := &dto.GeneralOpenAIRequest{
		Model:           "deepseek-chat",
		ReasoningEffort: "extreme",
		TopP:            &topP,
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-chat"},
	}, request)

	require.NoError(t, err)
	got := converted.(*dto.GeneralOpenAIRequest)
	assert.Equal(t, "extreme", got.ReasoningEffort)
	require.NotNil(t, got.TopP)
	assert.Equal(t, 1.5, *got.TopP)
}

func TestConvertOpenAIResponsesRequestNormalizesDeepSeekV4CompatibilityFields(t *testing.T) {
	topP := 1.5
	request := dto.OpenAIResponsesRequest{
		Model:     "deepseek-v4-flash",
		TopP:      &topP,
		Reasoning: &dto.Reasoning{Effort: " extreme "},
	}
	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
	}, request)

	require.NoError(t, err)
	got := converted.(dto.OpenAIResponsesRequest)
	require.NotNil(t, got.TopP)
	assert.Equal(t, float64(1), *got.TopP)
	require.NotNil(t, got.Reasoning)
	assert.Equal(t, "max", got.Reasoning.Effort)
}

func TestClassifyDeepSeekV4DFlashLogprobErrorAsChannelCapability(t *testing.T) {
	err := types.NewOpenAIError(
		errors.New("DFLASH speculative decoding does not support return_logprob yet."),
		types.ErrorCodeBadResponseStatusCode,
		400,
	)

	classified := classifyDeepSeekV4CapabilityError(err)

	require.NotNil(t, classified)
	assert.Equal(t, types.ErrorCodeChannelUnsupportedFeature, classified.GetErrorCode())
	assert.False(t, types.IsSkipRetryError(classified))
	assert.Contains(t, classified.Error(), "return_logprob")
}

func float64Pointer(value float64) *float64 { return &value }

func boolPointer(value bool) *bool { return &value }

func intPointer(value int) *int { return &value }
