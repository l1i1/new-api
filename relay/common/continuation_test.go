package common

import (
	"testing"

	rootcommon "github.com/QuantumNous/new-api/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

func TestChatAbnormalContinuationStepsForRequest(t *testing.T) {
	oldOn := rootcommon.ChatContinuationOnAbnormal
	oldSteps := rootcommon.ChatContinuationAbnormalMaxSteps
	oldChannels := rootcommon.ChatContinuationChannelIDs
	t.Cleanup(func() {
		rootcommon.ChatContinuationOnAbnormal = oldOn
		rootcommon.ChatContinuationAbnormalMaxSteps = oldSteps
		rootcommon.ChatContinuationChannelIDs = oldChannels
	})

	info := &RelayInfo{
		ChannelMeta: &ChannelMeta{ChannelId: 37},
		IsStream:    true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
	}

	rootcommon.ChatContinuationOnAbnormal = false
	rootcommon.ChatContinuationAbnormalMaxSteps = 1
	rootcommon.ChatContinuationChannelIDs = map[int]struct{}{37: {}}
	require.Zero(t, ChatAbnormalContinuationStepsForRequest(info), "disabled by default")

	rootcommon.ChatContinuationOnAbnormal = true
	require.Equal(t, 1, ChatAbnormalContinuationStepsForRequest(info))

	rootcommon.ChatContinuationChannelIDs = map[int]struct{}{91: {}}
	require.Zero(t, ChatAbnormalContinuationStepsForRequest(info), "channel must be whitelisted")

	rootcommon.ChatContinuationChannelIDs = map[int]struct{}{37: {}}
	rootcommon.ChatContinuationAbnormalMaxSteps = 0
	require.Zero(t, ChatAbnormalContinuationStepsForRequest(info), "zero steps disables")

	rootcommon.ChatContinuationAbnormalMaxSteps = 1
	notStream := *info
	notStream.IsStream = false
	require.Zero(t, ChatAbnormalContinuationStepsForRequest(&notStream), "streaming only")
}

func TestChatMissingContinuationStepsForRequest(t *testing.T) {
	oldOn := rootcommon.ChatContinuationOnMissing
	oldSteps := rootcommon.ChatContinuationMissingMaxSteps
	oldChannels := rootcommon.ChatContinuationChannelIDs
	t.Cleanup(func() {
		rootcommon.ChatContinuationOnMissing = oldOn
		rootcommon.ChatContinuationMissingMaxSteps = oldSteps
		rootcommon.ChatContinuationChannelIDs = oldChannels
	})

	info := &RelayInfo{
		ChannelMeta: &ChannelMeta{ChannelId: 37},
		IsStream:    true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
	}

	rootcommon.ChatContinuationOnMissing = false
	rootcommon.ChatContinuationMissingMaxSteps = 1
	rootcommon.ChatContinuationChannelIDs = map[int]struct{}{37: {}}
	require.Zero(t, ChatMissingContinuationStepsForRequest(info), "disabled by default")

	rootcommon.ChatContinuationOnMissing = true
	require.Equal(t, 1, ChatMissingContinuationStepsForRequest(info))

	rootcommon.ChatContinuationChannelIDs = map[int]struct{}{91: {}}
	require.Zero(t, ChatMissingContinuationStepsForRequest(info), "channel must be whitelisted")

	rootcommon.ChatContinuationChannelIDs = map[int]struct{}{37: {}}
	notStream := *info
	notStream.IsStream = false
	require.Zero(t, ChatMissingContinuationStepsForRequest(&notStream), "streaming only")
}
