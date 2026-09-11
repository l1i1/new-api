package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Aggregators that resell the DeepSeek V4 line expose the DeepSeek dialect but
// often ignore OpenAI's reasoning_effort; a disabled-thinking request then keeps
// thinking upstream and, because the platform strips reasoning_content for that
// request, the caller receives a 200 with no visible content while the upstream
// generated and billed tokens (DEF_aiping, 2026-09-11). The fix mirrors the
// intent onto the native `thinking` field for generic openai channels.

func deepSeekV4DialectInfo(apiType int, upstreamModel, effort string) *relaycommon.RelayInfo {
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiType:           apiType,
			UpstreamModelName: upstreamModel,
		},
		OriginModelName: "deepseek-v4-flash",
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     types.RelayFormatOpenAI,
	}
	info.SetReasoningEffort(effort)
	return info
}

func thinkingType(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	require.NotEmpty(t, raw, "thinking field must be populated")
	var parsed struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(raw, &parsed))
	return parsed.Type
}

func TestDisabledThinkingDialectInjectedForOpenAIChannel(t *testing.T) {
	info := deepSeekV4DialectInfo(constant.APITypeOpenAI, "deepseek-v4-flash", "none")
	request := &dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash", ReasoningEffort: "none"}

	applyDeepSeekV4DisabledThinkingDialect(info, request)

	assert.Equal(t, "disabled", thinkingType(t, request.THINKING),
		"an openai-compatible aggregator must also receive the native thinking field")
	assert.Equal(t, "none", request.ReasoningEffort, "the OpenAI field is preserved")
}

func TestDisabledThinkingDialectSkipsOfficialDeepSeekChannel(t *testing.T) {
	// The official channel has its own adaptor that already maps disabled
	// thinking; the generic openai adaptor must not touch it.
	info := deepSeekV4DialectInfo(constant.APITypeDeepSeek, "deepseek-v4-flash", "none")
	request := &dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash", ReasoningEffort: "none"}

	applyDeepSeekV4DisabledThinkingDialect(info, request)

	assert.Empty(t, request.THINKING)
}

func TestDisabledThinkingDialectSkipsOpenRouterStyleUpstream(t *testing.T) {
	// A namespaced upstream id marks an OpenRouter-style router whose dialect
	// is the `reasoning` object, not DeepSeek's `thinking`.
	info := deepSeekV4DialectInfo(constant.APITypeOpenRouter, "deepseek/deepseek-v4-flash-0731", "none")
	request := &dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash", ReasoningEffort: "none"}

	applyDeepSeekV4DisabledThinkingDialect(info, request)

	assert.Empty(t, request.THINKING)
}

func TestDisabledThinkingDialectSkipsWhenThinkingRequested(t *testing.T) {
	info := deepSeekV4DialectInfo(constant.APITypeOpenAI, "deepseek-v4-flash", "high")
	request := &dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash", ReasoningEffort: "high"}

	applyDeepSeekV4DisabledThinkingDialect(info, request)

	assert.Empty(t, request.THINKING)
}

func TestDisabledThinkingDialectPreservesClientThinking(t *testing.T) {
	info := deepSeekV4DialectInfo(constant.APITypeOpenAI, "deepseek-v4-flash", "none")
	request := &dto.GeneralOpenAIRequest{
		Model:           "deepseek-v4-flash",
		ReasoningEffort: "none",
		THINKING:        json.RawMessage(`{"type":"enabled"}`),
	}

	applyDeepSeekV4DisabledThinkingDialect(info, request)

	assert.Equal(t, "enabled", thinkingType(t, request.THINKING),
		"an explicit client thinking field must never be rewritten")
}

func TestDisabledThinkingDialectSkipsNonDeepSeekModel(t *testing.T) {
	info := deepSeekV4DialectInfo(constant.APITypeOpenAI, "glm-5.3-flash", "none")
	info.OriginModelName = "glm-5.3-flash"
	request := &dto.GeneralOpenAIRequest{Model: "glm-5.3-flash", ReasoningEffort: "none"}

	applyDeepSeekV4DisabledThinkingDialect(info, request)

	assert.Empty(t, request.THINKING)
}

func TestDisabledThinkingDialectSurvivesMarshal(t *testing.T) {
	// The injected field must actually reach the outbound body: `thinking` is a
	// known GeneralOpenAIRequest field, so MarshalJSON keeps it.
	info := deepSeekV4DialectInfo(constant.APITypeOpenAI, "deepseek-v4-flash", "none")
	request := &dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash", ReasoningEffort: "none"}

	applyDeepSeekV4DisabledThinkingDialect(info, request)

	raw, err := json.Marshal(request)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"thinking":{"type":"disabled"}`,
		"the native dialect must be on the wire, not only in memory")
}

// The widening from ChannelType==1 to ApiType==APITypeOpenAI exists for
// channels whose type is unmapped and therefore falls back to the generic
// OpenAI adaptor — channel 27 (DEF_streamlake, type 8 custom) serves
// deepseek-v4-flash that way. This drives the real ConvertOpenAIRequest to pin
// that path, not just the helper.
func TestDisabledThinkingDialectReachesBodyForUnmappedChannelType(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	// A custom (type 8) channel resolves to API type OpenAI via the
	// ChannelType2APIType fallback, so this is the adaptor the gateway uses.
	apiType, mapped := common.ChannelType2APIType(constant.ChannelTypeCustom)
	require.False(t, mapped, "an unmapped channel type must take the fallback branch")
	require.Equal(t, constant.APITypeOpenAI, apiType)

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeCustom,
			ApiType:           apiType,
			UpstreamModelName: "DeepSeek-V4-Flash-0731",
		},
		OriginModelName: "deepseek-v4-flash",
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     types.RelayFormatOpenAI,
	}
	info.SetReasoningEffort("none")
	request := &dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash", ReasoningEffort: "none"}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(c, info, request)
	require.NoError(t, err)

	raw, err := json.Marshal(converted)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"thinking":{"type":"disabled"}`,
		"a custom-type reseller that falls back to the OpenAI adaptor must also get the dialect")
}
