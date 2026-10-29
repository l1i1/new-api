package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSetChatContinuationRotationOptions pins the rotation-era options: the
// admin API must accept them, reject malformed values, and remember that
// max_rounds was configured explicitly (0 then means "off" instead of falling
// back to the legacy max_steps round count).
func TestSetChatContinuationRotationOptions(t *testing.T) {
	oldRounds, oldSet := ChatContinuationMaxRounds, ChatContinuationMaxRoundsSet
	oldElapsed := ChatContinuationRoundMaxElapsedSeconds
	oldRotate := ChatContinuationRotateChannels
	t.Cleanup(func() {
		ChatContinuationMaxRounds, ChatContinuationMaxRoundsSet = oldRounds, oldSet
		ChatContinuationRoundMaxElapsedSeconds = oldElapsed
		ChatContinuationRotateChannels = oldRotate
	})

	require.Error(t, SetChatContinuationOption(ChatContinuationOptionMaxRounds, "-1"))
	require.Error(t, SetChatContinuationOption(ChatContinuationOptionMaxRounds, "many"))
	require.Error(t, SetChatContinuationOption(ChatContinuationOptionRoundMaxElapsedSeconds, "-5"))
	require.Error(t, SetChatContinuationOption(ChatContinuationOptionRotateChannels, "sometimes"))

	require.NoError(t, SetChatContinuationOption(ChatContinuationOptionMaxRounds, "3"))
	require.Equal(t, 3, ChatContinuationMaxRounds)
	require.True(t, ChatContinuationMaxRoundsSet)

	require.NoError(t, SetChatContinuationOption(ChatContinuationOptionMaxRounds, "0"))
	require.Equal(t, 0, ChatContinuationMaxRounds)
	require.True(t, ChatContinuationMaxRoundsSet, "an explicit 0 disables continuation and must not look unset")

	require.NoError(t, SetChatContinuationOption(ChatContinuationOptionRoundMaxElapsedSeconds, "1500"))
	require.Equal(t, 1500, ChatContinuationRoundMaxElapsedSeconds)

	require.NoError(t, SetChatContinuationOption(ChatContinuationOptionRotateChannels, "false"))
	require.False(t, ChatContinuationRotateChannels)
	require.NoError(t, SetChatContinuationOption(ChatContinuationOptionRotateChannels, "true"))
	require.True(t, ChatContinuationRotateChannels)
}

// TestChatContinuationOptionsParseRotationKeys pins the raw option keys, because
// they are the operator-facing contract written in the docs.
func TestChatContinuationOptionsParseRotationKeys(t *testing.T) {
	for key, want := range map[string]string{
		"relay_chat_continuation_max_rounds":                ChatContinuationOptionMaxRounds,
		"relay_chat_continuation_round_max_elapsed_seconds": ChatContinuationOptionRoundMaxElapsedSeconds,
		"relay_chat_continuation_rotate_channels":           ChatContinuationOptionRotateChannels,
	} {
		require.Equal(t, want, key)
	}
}
