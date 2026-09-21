package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// videoChannel builds a channel with the video capability declared or omitted.
func videoChannel(id int, supportsVideo bool) *Channel {
	channel := &Channel{Id: id, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled}
	if supportsVideo {
		channel.SetOtherSettings(kitdto.ChannelOtherSettings{SupportsVideo: true})
	}
	return channel
}

// TestFilterChannelIDsByVideoCapability pins the routing rule that keeps a
// media-blind upstream from receiving a video request: it would answer from the
// text alone instead of rejecting the part, so the request must never be routed
// there when any declared channel exists. An undeclared channel is excluded
// rather than assumed capable, and a missing id map entry counts as undeclared.
func TestFilterChannelIDsByVideoCapability(t *testing.T) {
	capable := videoChannel(800001, true)
	blind := videoChannel(800002, false)

	channelSyncLock.Lock()
	previousVideo := channel2supportsVideo
	previousChannels := channelsIDM
	channel2supportsVideo = map[int]struct{}{800001: {}}
	channelsIDM = map[int]*Channel{800001: capable, 800002: blind}
	t.Cleanup(func() {
		channel2supportsVideo = previousVideo
		channelsIDM = previousChannels
		channelSyncLock.Unlock()
	})

	// Not a video request: the narrowing is inert, so unrelated traffic keeps
	// its full candidate set.
	assert.Equal(t, []int{800001, 800002}, filterChannelIDsByVideoCapability([]int{800001, 800002}, false))
	// A video request keeps only the declaring channel.
	assert.Equal(t, []int{800001}, filterChannelIDsByVideoCapability([]int{800001, 800002}, true))
	// No declared channel means no candidate at all, which callers surface as
	// "no available channel" rather than silently using a blind upstream.
	assert.Empty(t, filterChannelIDsByVideoCapability([]int{800002}, true))
	// An id with no cache entry cannot be treated as capable.
	assert.Empty(t, filterChannelIDsByVideoCapability([]int{800099}, true))
}

// TestSupportsVideoForCacheReadsTheDeclaration pins the parsing rule: only an
// explicit true counts, and malformed settings are not capable.
func TestSupportsVideoForCacheReadsTheDeclaration(t *testing.T) {
	declared := `{"supports_video":true}`
	notDeclared := `{"supports_video":false}`
	otherOnly := `{"video_usage_mode":"estimate"}`
	malformed := `{"supports_video":`

	require.True(t, supportsVideoForCache(&Channel{OtherSettings: declared}))
	assert.False(t, supportsVideoForCache(&Channel{OtherSettings: notDeclared}))
	// A usage mode without the capability must not route video here: the mode
	// only shapes billing, it never claims the upstream can read the media.
	assert.False(t, supportsVideoForCache(&Channel{OtherSettings: otherOnly}))
	assert.False(t, supportsVideoForCache(&Channel{OtherSettings: malformed}))
	assert.False(t, supportsVideoForCache(&Channel{OtherSettings: ""}))
	assert.False(t, supportsVideoForCache(nil))
}

// TestChannelSatisfiesVideoFilter covers the filter that guards the pinned and
// affinity paths, where a channel is picked outside the selection functions.
func TestChannelSatisfiesVideoFilter(t *testing.T) {
	capable := videoChannel(800010, true)
	blind := videoChannel(800011, false)
	filter := []dto.ChannelFilter{{Kind: dto.FilterVideoRequest}}

	ok, kind := ChannelSatisfiesFilters(capable, "kimi-k3", filter)
	require.True(t, ok)
	assert.Equal(t, dto.ChannelFilterKind(""), kind)

	ok, kind = ChannelSatisfiesFilters(blind, "kimi-k3", filter)
	assert.False(t, ok)
	assert.Equal(t, dto.FilterVideoRequest, kind)
}
