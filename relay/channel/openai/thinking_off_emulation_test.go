package openai

import (
	"testing"

	rootcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func emulationInfo(t *testing.T, offset, perMessage, toolsOffset int, thinking string, messages int, withTools bool) *relaycommon.RelayInfo {
	t.Helper()
	req := &dto.GeneralOpenAIRequest{}
	for i := 0; i < messages; i++ {
		req.Messages = append(req.Messages, dto.Message{Role: "user"})
	}
	if withTools {
		req.Tools = []dto.ToolCallRequest{{}}
	}
	if thinking != "" {
		req.THINKING = rootcommon.StringToByteSlice(thinking)
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3.1-pro"},
		Request:     req,
	}
	info.ChannelSetting.ThinkingOffEmulation = true
	info.ChannelSetting.ThinkingOffPromptOffset = offset
	info.ChannelSetting.ThinkingOffPromptOffsetPerMessage = perMessage
	info.ChannelSetting.ThinkingOffPromptOffsetTools = toolsOffset
	return info
}

// TestThinkingOffEmulationRewritesUsage pins the 2026-10-29 operator decision
// 「确认按官方口径计费」: on a channel that cannot disable thinking, the caller
// sees and pays the official thinking-off usage — fixed preamble removed from
// the prompt, reasoning tokens removed from the completion, no completion
// token details.
func TestThinkingOffEmulationRewritesUsage(t *testing.T) {
	info := emulationInfo(t, 68, 3, 37, `{"type":"disabled"}`, 3, true)
	usage := &dto.Usage{
		PromptTokens:     500,
		CompletionTokens: 100,
		TotalTokens:      600,
		CompletionTokenDetails: dto.OutputTokenDetails{
			TextTokens:      60,
			ReasoningTokens: 40,
		},
	}
	require.True(t, applyThinkingOffEmulation(info, usage))
	require.Equal(t, 500-(68+3*2+37), usage.PromptTokens)
	require.Equal(t, 60, usage.CompletionTokens)
	require.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	require.Zero(t, usage.CompletionTokenDetails.ReasoningTokens)
}

func TestThinkingOffEmulationLeavesThinkingOnRequestsAlone(t *testing.T) {
	info := emulationInfo(t, 68, 3, 0, `{"type":"enabled"}`, 1, false)
	usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	require.False(t, applyThinkingOffEmulation(info, usage))
	require.Equal(t, 100, usage.PromptTokens)
	require.Equal(t, 50, usage.CompletionTokens)
}

func TestThinkingOffEmulationExemptsKimiK3OnlyWithoutSwitch(t *testing.T) {
	off := emulationInfo(t, 68, 0, 0, `{"type":"disabled"}`, 1, false)
	off.ChannelSetting.ThinkingOffEmulation = false
	require.False(t, shouldSuppressReasoningContent(off), "official-fit channels keep their reasoning: the upstream honoured the request")
	on := emulationInfo(t, 68, 0, 0, `{"type":"disabled"}`, 1, false)
	require.True(t, shouldSuppressReasoningContent(on), "a channel that cannot disable thinking must not leak its reasoning")
}
