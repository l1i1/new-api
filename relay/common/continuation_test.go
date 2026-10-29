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

// A non-streaming client on a channel that answers it from a buffered upstream
// stream has the same observable stream as a streaming one, so the repair modes
// must stay available: the 2026-10-29 900s-wall failure burned three channels
// (45 minutes) for a request whose answer was never committed.
func TestContinuationStepsForBufferedNonStreamRequest(t *testing.T) {
	oldSteps := rootcommon.ChatContinuationMaxSteps
	oldChannels := rootcommon.ChatContinuationChannelIDs
	t.Cleanup(func() {
		rootcommon.ChatContinuationMaxSteps = oldSteps
		rootcommon.ChatContinuationChannelIDs = oldChannels
	})
	rootcommon.ChatContinuationMaxSteps = 2
	rootcommon.ChatContinuationChannelIDs = map[int]struct{}{106: {}}

	info := &RelayInfo{
		ChannelMeta: &ChannelMeta{ChannelId: 106},
		IsStream:    false,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
	}
	require.Zero(t, ChatContinuationStepsForRequest(info), "a plain non-stream request keeps the ordinary path")

	info.ChannelSetting.InternalStreamForNonStream = true
	require.Equal(t, 2, ChatContinuationStepsForRequest(info), "buffered upstream stream can be resumed")
}

func TestTriedChannelsTracksEachChannelOnce(t *testing.T) {
	tried := &TriedChannels{}
	require.True(t, tried.Add(7), "first add records the channel")
	require.False(t, tried.Add(7), "re-adding is a no-op")
	require.True(t, tried.Add(9))
	require.True(t, tried.Contains(7))
	require.False(t, tried.Contains(11))
	require.Equal(t, []int{7, 9}, tried.IDs(), "order is the order the rounds ran")

	ids := tried.IDs()
	ids[0] = 999
	require.Equal(t, []int{7, 9}, tried.IDs(), "IDs returns a copy")

	var nilTried *TriedChannels
	require.False(t, nilTried.Add(3))
	require.False(t, nilTried.Contains(3))
	require.Nil(t, nilTried.IDs())
}

// TestMaxRoundsAloneEnablesContinuation pins the upgrade contract: configuring
// only the rotation-era budget must switch the repair on, and an explicit 0 must
// switch it off, without depending on the legacy step keys.
func TestMaxRoundsAloneEnablesContinuation(t *testing.T) {
	oldRounds, oldSet := rootcommon.ChatContinuationMaxRounds, rootcommon.ChatContinuationMaxRoundsSet
	oldSteps := rootcommon.ChatContinuationMaxSteps
	oldAbnormal, oldOnAbnormal := rootcommon.ChatContinuationAbnormalMaxSteps, rootcommon.ChatContinuationOnAbnormal
	oldMissing, oldOnMissing := rootcommon.ChatContinuationMissingMaxSteps, rootcommon.ChatContinuationOnMissing
	oldChannels := rootcommon.ChatContinuationChannelIDs
	t.Cleanup(func() {
		rootcommon.ChatContinuationMaxRounds, rootcommon.ChatContinuationMaxRoundsSet = oldRounds, oldSet
		rootcommon.ChatContinuationMaxSteps = oldSteps
		rootcommon.ChatContinuationAbnormalMaxSteps, rootcommon.ChatContinuationOnAbnormal = oldAbnormal, oldOnAbnormal
		rootcommon.ChatContinuationMissingMaxSteps, rootcommon.ChatContinuationOnMissing = oldMissing, oldOnMissing
		rootcommon.ChatContinuationChannelIDs = oldChannels
	})

	rootcommon.ChatContinuationChannelIDs = map[int]struct{}{7: {}}
	rootcommon.ChatContinuationOnAbnormal = true
	rootcommon.ChatContinuationOnMissing = true
	rootcommon.ChatContinuationMaxSteps = 0
	rootcommon.ChatContinuationAbnormalMaxSteps = 1
	rootcommon.ChatContinuationMissingMaxSteps = 1

	info := &RelayInfo{
		ChannelMeta: &ChannelMeta{},
		IsStream:    true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
	}
	info.ChannelId = 7

	// Only the new key configured: every mode runs on the new budget.
	rootcommon.ChatContinuationMaxRounds, rootcommon.ChatContinuationMaxRoundsSet = 4, true
	require.Equal(t, 4, ChatContinuationStepsForRequest(info))
	require.Equal(t, 4, ChatAbnormalContinuationStepsForRequest(info))
	require.Equal(t, 4, ChatMissingContinuationStepsForRequest(info))

	// Explicit 0 disables every mode, even with legacy steps set.
	rootcommon.ChatContinuationMaxRounds = 0
	require.Zero(t, ChatContinuationStepsForRequest(info))
	require.Zero(t, ChatAbnormalContinuationStepsForRequest(info))
	require.Zero(t, ChatMissingContinuationStepsForRequest(info))

	// Unset: the legacy step counts keep their old meaning.
	rootcommon.ChatContinuationMaxRoundsSet = false
	require.Equal(t, 0, ChatContinuationStepsForRequest(info), "legacy max_steps=0 keeps the feature off")
	require.Equal(t, 1, ChatAbnormalContinuationStepsForRequest(info))
	require.Equal(t, 1, ChatMissingContinuationStepsForRequest(info))
}
