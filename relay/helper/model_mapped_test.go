package helper

import (
	"testing"

	rootcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelMappedHelperAppliesResolvedCompactAlias(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	rootcommon.SetContextKey(c, constant.ContextKeyResolvedModel, "gpt-5.6-sol")
	c.Set("model_mapping", `{"gpt-5.6-sol":"upstream-gpt-5.6-sol"}`)
	request := &dto.GeneralOpenAIRequest{Model: "gpt-5.6-sol-openai-compact"}
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.6-sol-openai-compact",
		RelayMode:       relayconstant.RelayModeResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-5.6-sol-openai-compact",
		},
	}

	require.NoError(t, ModelMappedHelper(c, info, request))
	assert.Equal(t, "gpt-5.6-sol-openai-compact", info.OriginModelName)
	assert.Equal(t, "upstream-gpt-5.6-sol", info.UpstreamModelName)
	assert.Equal(t, "upstream-gpt-5.6-sol", request.Model)
	assert.True(t, info.IsModelMapped)
}

func TestModelMappedHelperPreservesExactCompactModelOutsideCompactEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	request := &dto.GeneralOpenAIRequest{Model: "gpt-5.5-openai-compact"}
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.5-openai-compact",
		RelayMode:       relayconstant.RelayModeResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-5.5-openai-compact",
		},
	}

	require.NoError(t, ModelMappedHelper(c, info, request))
	assert.Equal(t, "gpt-5.5-openai-compact", request.Model)
	assert.False(t, info.IsModelMapped)
}

func TestModelMappedHelperKeepsResponsesCompactBehavior(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	request := &dto.OpenAIResponsesCompactionRequest{Model: "gpt-5.5-openai-compact"}
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.5-openai-compact",
		RelayMode:       relayconstant.RelayModeResponsesCompact,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-5.5-openai-compact",
		},
	}

	require.NoError(t, ModelMappedHelper(c, info, request))
	assert.Equal(t, "gpt-5.5", request.Model)
	assert.Equal(t, "gpt-5.5-openai-compact", info.OriginModelName)
	assert.Equal(t, "gpt-5.5", info.UpstreamModelName)
}
