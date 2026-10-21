package model

import (
	"testing"

	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/stretchr/testify/require"
)

// installChannel2SupportsVideo replaces the video-capability cache for one test
// and restores it afterwards.
func installChannel2SupportsVideo(t *testing.T, channelIDs ...int) {
	t.Helper()
	previous := channel2supportsVideo
	channel2supportsVideo = make(map[int]struct{}, len(channelIDs))
	for _, id := range channelIDs {
		channel2supportsVideo[id] = struct{}{}
	}
	t.Cleanup(func() { channel2supportsVideo = previous })
}

// TestVideoCapabilityIsAppliedBeforeTheFitNarrowing is the regression gate for
// the CN video outage of 2026-10-21: the fit narrowing ran first, kept only the
// channels that proved the requested behaviour (none of which declared video),
// and the video filter then emptied the set. Because the request was pinned by
// the policy, the selector reported "cannot prove candidate exhaustion" and the
// client received a 500 instead of being served by the official channel — even
// though that channel is the only video-capable one.
//
// Here channel 1 is official and video-capable but fails the marks, channel 3 is
// non-official and satisfies them. A video request must narrow inside the
// video-capable set and pick channel 1; it must not lose that channel to the
// narrowing.
func TestVideoCapabilityIsAppliedBeforeTheFitNarrowing(t *testing.T) {
	installFitSelectionCache(t)
	installChannel2SupportsVideo(t, 1)

	fit := &FitChannelFilter{
		Requirement:   fitpolicy.Requirement{Marks: []string{"usage.thinking_counting"}, Model: "deepseek-v4-flash"},
		MarkSatisfied: func(channelID int) bool { return channelID == 3 },
	}
	for range 20 {
		selected, err := GetRandomSatisfiedChannelPinnedWithFit("default", "deepseek-v4-flash", 0, "", nil, false, true, fit)
		require.NoError(t, err)
		require.NotNil(t, selected, "the video request must still find the capable official channel")
		require.Equal(t, 1, selected.Id,
			"video capability is a hard requirement applied before the fit narrowing")
	}
}

// A video request whose group publishes no capable channel is a definite gap,
// not an unprovable exhaustion: the selector reports ErrNoVideoCapableChannel so
// the caller can answer with the video_request rejection instead of 500.
func TestVideoRequestWithoutCapableChannelReportsTheGap(t *testing.T) {
	installFitSelectionCache(t)
	installChannel2SupportsVideo(t)

	fit := &FitChannelFilter{
		Requirement:   fitpolicy.Requirement{Marks: []string{"usage.thinking_counting"}, Model: "deepseek-v4-flash"},
		MarkSatisfied: func(int) bool { return true },
	}
	_, err := GetRandomSatisfiedChannelPinnedWithFit("default", "deepseek-v4-flash", 0, "", nil, true, true, fit)
	require.ErrorIs(t, err, ErrNoVideoCapableChannel)

	// The same request without the video flag keeps the ordinary answer.
	selected, err := GetRandomSatisfiedChannelPinnedWithFit("default", "deepseek-v4-flash", 0, "", nil, true, false, fit)
	require.NoError(t, err)
	require.NotNil(t, selected)

	// A model the group does not publish must stay "no candidate" (nil, nil), so
	// the caller keeps its model_not_configured answer instead of blaming video.
	_, err = GetRandomSatisfiedChannelPinnedWithFit("default", "deepseek-v4-pro", 0, "", nil, true, true, fit)
	require.NoError(t, err)
}
