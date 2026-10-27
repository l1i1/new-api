package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatContinuationOptionsApplyAndValidate(t *testing.T) {
	oldOnAbnormal := ChatContinuationOnAbnormal
	oldOnMissing := ChatContinuationOnMissing
	oldChannels := ChatContinuationChannelIDs
	oldRaw := ChatContinuationChannelsRaw
	oldMaxSteps := ChatContinuationMaxSteps
	oldAbnormalSteps := ChatContinuationAbnormalMaxSteps
	oldAbnormalBytes := ChatContinuationAbnormalMaxReasoningBytes
	oldAbnormalChars := ChatContinuationAbnormalMinReasoningChars
	oldMissingSteps := ChatContinuationMissingMaxSteps
	oldMissingBytes := ChatContinuationMissingMaxReasoningBytes
	oldMissingChars := ChatContinuationMissingMinReasoningChars
	t.Cleanup(func() {
		ChatContinuationOnAbnormal = oldOnAbnormal
		ChatContinuationOnMissing = oldOnMissing
		ChatContinuationChannelIDs = oldChannels
		ChatContinuationChannelsRaw = oldRaw
		ChatContinuationMaxSteps = oldMaxSteps
		ChatContinuationAbnormalMaxSteps = oldAbnormalSteps
		ChatContinuationAbnormalMaxReasoningBytes = oldAbnormalBytes
		ChatContinuationAbnormalMinReasoningChars = oldAbnormalChars
		ChatContinuationMissingMaxSteps = oldMissingSteps
		ChatContinuationMissingMaxReasoningBytes = oldMissingBytes
		ChatContinuationMissingMinReasoningChars = oldMissingChars
	})

	require.NoError(t, SetChatContinuationOption(ChatContinuationOptionOnAbnormal, "true"))
	require.True(t, ChatContinuationOnAbnormal)
	require.NoError(t, SetChatContinuationOption(ChatContinuationOptionOnMissing, "true"))
	require.True(t, ChatContinuationOnMissing)
	require.NoError(t, SetChatContinuationOption(ChatContinuationOptionChannels, "37, 91"))
	require.Equal(t, "37, 91", ChatContinuationChannelsRaw)
	require.True(t, ChatContinuationChannelAllowed(37))
	require.True(t, ChatContinuationChannelAllowed(91))
	require.False(t, ChatContinuationChannelAllowed(1))
	require.NoError(t, SetChatContinuationOption(ChatContinuationOptionMaxSteps, "2"))
	require.Equal(t, 2, ChatContinuationMaxSteps)
	require.NoError(t, SetChatContinuationOption(ChatContinuationOptionAbnormalMaxSteps, "1"))
	require.Equal(t, 1, ChatContinuationAbnormalMaxSteps)
	require.NoError(t, SetChatContinuationOption(ChatContinuationOptionAbnormalMaxReasoningBytes, "65536"))
	require.Equal(t, 65536, ChatContinuationAbnormalMaxReasoningBytes)
	require.NoError(t, SetChatContinuationOption(ChatContinuationOptionAbnormalMinReasoningChars, "128"))
	require.Equal(t, 128, ChatContinuationAbnormalMinReasoningChars)
	require.NoError(t, SetChatContinuationOption(ChatContinuationOptionMissingMaxSteps, "1"))
	require.Equal(t, 1, ChatContinuationMissingMaxSteps)
	require.NoError(t, SetChatContinuationOption(ChatContinuationOptionMissingMaxReasoningBytes, "32768"))
	require.Equal(t, 32768, ChatContinuationMissingMaxReasoningBytes)
	require.NoError(t, SetChatContinuationOption(ChatContinuationOptionMissingMinReasoningChars, "64"))
	require.Equal(t, 64, ChatContinuationMissingMinReasoningChars)

	require.Error(t, SetChatContinuationOption(ChatContinuationOptionOnAbnormal, "yes"))
	require.Error(t, SetChatContinuationOption(ChatContinuationOptionChannels, "37,abc"))
	require.Error(t, SetChatContinuationOption(ChatContinuationOptionMaxSteps, "-1"))
	require.Error(t, SetChatContinuationOption(ChatContinuationOptionAbnormalMaxReasoningBytes, "0"))
	require.Error(t, SetChatContinuationOption("unknown", "1"))
}
