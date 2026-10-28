package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// videoChannelWithExclusions declares video capability and lists the models its
// upstream is known to be media-blind for.
func videoChannelWithExclusions(id int, excluded ...string) *Channel {
	channel := &Channel{Id: id, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled}
	channel.SetOtherSettings(kitdto.ChannelOtherSettings{
		SupportsVideo:          true,
		VideoUnsupportedModels: excluded,
	})
	return channel
}

// TestFilterChannelIDsByVideoCapabilityHonoursModelExclusion pins the model-level
// half of the capability rule: a channel that reads video for kimi-k3 but not for
// kimi-k3.1-pro must stay a candidate for the former and be dropped for the
// latter. Measured on 2026-10-28: an aggregator reported 7168 prompt tokens for a
// kimi-k3 clip and 122 for the same clip on kimi-k3.1-pro, answering from text
// alone — the channel-level declaration said "video capable" for both.
func TestFilterChannelIDsByVideoCapabilityHonoursModelExclusion(t *testing.T) {
	excluded := videoChannelWithExclusions(810101, "kimi-k3.1-pro")

	channelSyncLock.Lock()
	previousVideo := channel2supportsVideo
	previousExcluded := channel2videoUnsupportedModels
	previousChannels := channelsIDM
	channel2supportsVideo = map[int]struct{}{810101: {}}
	channel2videoUnsupportedModels = map[int]map[string]struct{}{}
	setVideoUnsupportedModelsIn(channel2videoUnsupportedModels, excluded)
	channelsIDM = map[int]*Channel{810101: excluded}
	channelSyncLock.Unlock()
	t.Cleanup(func() {
		channelSyncLock.Lock()
		channel2supportsVideo = previousVideo
		channel2videoUnsupportedModels = previousExcluded
		channelsIDM = previousChannels
		channelSyncLock.Unlock()
	})

	// The model the upstream does read video for stays routable.
	assert.Equal(t, []int{810101}, filterChannelIDsByVideoCapability([]int{810101}, true, "kimi-k3"))
	// The excluded model is dropped, and the match is case-insensitive because
	// clients spell model names both ways.
	assert.Empty(t, filterChannelIDsByVideoCapability([]int{810101}, true, "kimi-k3.1-pro"))
	assert.Empty(t, filterChannelIDsByVideoCapability([]int{810101}, true, "Kimi-K3.1-Pro"))
	// Text-only requests are never affected by the exclusion.
	assert.Equal(t, []int{810101}, filterChannelIDsByVideoCapability([]int{810101}, false, "kimi-k3.1-pro"))
}

// TestFilterAbilitiesByVideoCapabilityHonoursModelExclusion covers the database
// selection path, which loads channel settings by id and filters the ability
// rows of that channel model by model.
func TestFilterAbilitiesByVideoCapabilityHonoursModelExclusion(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	DB = db
	t.Cleanup(func() { DB = previousDB })

	excluded := videoChannelWithExclusions(810201, "kimi-k3.1-pro")
	require.NoError(t, db.Create(excluded).Error)
	plain := videoChannelWithExclusions(810202)
	require.NoError(t, db.Create(plain).Error)
	blind := videoChannel(810203, false)
	require.NoError(t, db.Create(blind).Error)

	abilities := []Ability{
		{Group: "default", Model: "kimi-k3", ChannelId: 810201, Enabled: true},
		{Group: "default", Model: "kimi-k3.1-pro", ChannelId: 810201, Enabled: true},
		{Group: "default", Model: "kimi-k3.1-pro", ChannelId: 810202, Enabled: true},
		{Group: "default", Model: "kimi-k3.1-pro", ChannelId: 810203, Enabled: true},
	}

	filtered, err := filterAbilitiesByVideoCapability(abilities, true)
	require.NoError(t, err)
	require.Len(t, filtered, 2)
	assert.Equal(t, 810201, filtered[0].ChannelId)
	assert.Equal(t, "kimi-k3", filtered[0].Model)
	// The second channel declares video without exceptions, so its
	// kimi-k3.1-pro row survives; the blind channel is dropped entirely.
	assert.Equal(t, 810202, filtered[1].ChannelId)
	assert.Equal(t, "kimi-k3.1-pro", filtered[1].Model)

	// Text requests keep every candidate.
	unfiltered, err := filterAbilitiesByVideoCapability(abilities, false)
	require.NoError(t, err)
	assert.Len(t, unfiltered, 4)
}

// TestChannelSatisfiesFiltersHonoursModelExclusion covers the pinned and
// affinity paths: they pick a channel without the video-aware selectors and
// then ask ChannelSatisfiesFilters whether it is acceptable, so the model-level
// exclusion has to be applied there as well or a client that chatted on the
// blind channel first would have the next video request pinned to it.
func TestChannelSatisfiesFiltersHonoursModelExclusion(t *testing.T) {
	filters := []dto.ChannelFilter{{Kind: dto.FilterVideoRequest}}

	excluded := videoChannelWithExclusions(810301, "kimi-k3.1-pro")
	plain := videoChannelWithExclusions(810302)
	blind := videoChannel(810303, false)

	ok, _ := ChannelSatisfiesFilters(excluded, "kimi-k3", filters)
	assert.True(t, ok, "the model the upstream does read video for keeps the channel")
	ok, kind := ChannelSatisfiesFilters(excluded, "kimi-k3.1-pro", filters)
	assert.False(t, ok, "the marked model must not satisfy the video filter")
	assert.Equal(t, dto.FilterVideoRequest, kind)
	ok, _ = ChannelSatisfiesFilters(excluded, "Kimi-K3.1-Pro", filters)
	assert.False(t, ok, "the exclusion match is case-insensitive")

	ok, _ = ChannelSatisfiesFilters(plain, "kimi-k3.1-pro", filters)
	assert.True(t, ok, "a channel without exceptions stays capable")
	ok, _ = ChannelSatisfiesFilters(blind, "kimi-k3.1-pro", filters)
	assert.False(t, ok, "an undeclared channel can never serve video")
}

// setupVideoExclusionDB gives one test its own SQLite database with the channel
// table the capability marker writes to.
func setupVideoExclusionDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousMemoryCache := common.MemoryCacheEnabled
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}))
	DB = db
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		DB = previousDB
		common.MemoryCacheEnabled = previousMemoryCache
	})
	return db
}

// TestSetChannelVideoUnsupportedModel pins the marker's write behaviour: it
// appends the model to the channel settings, is idempotent (case-insensitive),
// clears on request, and refuses to record an exception on a channel that never
// declares video (dead configuration).
func TestSetChannelVideoUnsupportedModel(t *testing.T) {
	db := setupVideoExclusionDB(t)

	aggregator := videoChannelWithExclusions(820001)
	aggregator.Models = "kimi-k3,kimi-k3.1-pro"
	require.NoError(t, db.Create(aggregator).Error)

	changed, err := SetChannelVideoUnsupportedModel(820001, "kimi-k3.1-pro", true)
	require.NoError(t, err)
	assert.True(t, changed)

	reloaded, err := GetChannelById(820001, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"kimi-k3.1-pro"}, reloaded.GetOtherSettings().VideoUnsupportedModels)
	assert.True(t, reloaded.GetOtherSettings().SupportsVideo)

	// Marking again is a no-op, even with a different letter case.
	changed, err = SetChannelVideoUnsupportedModel(820001, "Kimi-K3.1-Pro", true)
	require.NoError(t, err)
	assert.False(t, changed)

	// A second blind model accumulates next to the first.
	changed, err = SetChannelVideoUnsupportedModel(820001, "glm-5.3-flash", true)
	require.NoError(t, err)
	assert.True(t, changed)
	reloaded, err = GetChannelById(820001, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"kimi-k3.1-pro", "glm-5.3-flash"}, reloaded.GetOtherSettings().VideoUnsupportedModels)

	// A capable verdict clears exactly one entry and keeps the other.
	changed, err = SetChannelVideoUnsupportedModel(820001, "kimi-k3.1-pro", false)
	require.NoError(t, err)
	assert.True(t, changed)
	reloaded, err = GetChannelById(820001, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"glm-5.3-flash"}, reloaded.GetOtherSettings().VideoUnsupportedModels)

	// Clearing an absent entry is a no-op.
	changed, err = SetChannelVideoUnsupportedModel(820001, "kimi-k3.1-pro", false)
	require.NoError(t, err)
	assert.False(t, changed)

	// A channel without the video declaration gets no exception: video requests
	// never reach it, so the record would be dead configuration.
	textOnly := &Channel{Id: 820002, Status: common.ChannelStatusEnabled, Models: "kimi-k3.1-pro"}
	require.NoError(t, db.Create(textOnly).Error)
	changed, err = SetChannelVideoUnsupportedModel(820002, "kimi-k3.1-pro", true)
	require.NoError(t, err)
	assert.False(t, changed)
	reloaded, err = GetChannelById(820002, false)
	require.NoError(t, err)
	assert.Empty(t, reloaded.GetOtherSettings().VideoUnsupportedModels)

	// Unknown channels are reported, not silently ignored.
	_, err = SetChannelVideoUnsupportedModel(820099, "kimi-k3.1-pro", true)
	require.Error(t, err)
}
