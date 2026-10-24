package moonshot

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

func TestConvertOpenAIRequestKimiK26UsesOnlyAllowedTemperature(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Model:       "kimi-k2.6",
		Temperature: common.GetPointer[float64](0.7),
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "kimi-k2.6",
		},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)

	require.NoError(t, err)
	convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.NotNil(t, convertedRequest.Temperature)
	require.Equal(t, 1.0, *convertedRequest.Temperature)
}

func TestConvertOpenAIRequestKimiK26KeepsOmittedTemperatureOmitted(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Model: "kimi-k2.6",
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "kimi-k2.6",
		},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)

	require.NoError(t, err)
	convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Nil(t, convertedRequest.Temperature)
}

func TestResponsesURLAndConversion(t *testing.T) {
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://api.moonshot.cn"},
		RelayFormat: types.RelayFormatOpenAIResponses,
	}
	url, err := (&Adaptor{}).GetRequestURL(info)
	require.NoError(t, err)
	require.Equal(t, "https://api.moonshot.cn/v1/responses", url)

	request := dto.OpenAIResponsesRequest{Model: "kimi-k3.1-pro"}
	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)
	require.NoError(t, err)
	convertedRequest, ok := converted.(dto.OpenAIResponsesRequest)
	require.True(t, ok)
	require.Equal(t, "kimi-k3.1-pro", convertedRequest.Model)
}

func TestResponsesURLUsesCodingPlanBase(t *testing.T) {
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "kimi-coding-plan"},
		RelayFormat: types.RelayFormatOpenAIResponses,
	}
	url, err := (&Adaptor{}).GetRequestURL(info)
	require.NoError(t, err)
	require.Equal(t, "https://api.kimi.com/coding/v1/responses", url)
}

func TestConvertOpenAIRequestOtherMoonshotModelKeepsTemperature(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Model:       "kimi-k2.5",
		Temperature: common.GetPointer[float64](0.7),
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "kimi-k2.5",
		},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)

	require.NoError(t, err)
	convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.NotNil(t, convertedRequest.Temperature)
	require.Equal(t, 0.7, *convertedRequest.Temperature)
}
