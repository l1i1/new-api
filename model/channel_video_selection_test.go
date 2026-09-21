package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestSelectChannelPrefersVideoCapableOnlyWhenRequested is the routing contract
// for video: with a media-blind channel holding the highest priority, a video
// request must land on the lower-priority declared channel, while a text
// request keeps the pre-existing priority order (so declaring the capability
// does not demote an otherwise preferred channel).
func TestSelectChannelPrefersVideoCapableOnlyWhenRequested(t *testing.T) {
	previousDB := DB
	previousMemoryCacheEnabled := common.MemoryCacheEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	DB = db
	// Exercise the database path directly: it is the fallback used when the
	// memory cache is off, and it must obey the same contract.
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCacheEnabled
		DB = previousDB
	})

	const modelName = "kimi-k3"
	// The media-blind channel deliberately outranks the capable one: without
	// the narrowing this is exactly the configuration that silently drops video.
	blindSettings := `{}`
	capableSettings := `{"supports_video":true}`
	blind := Channel{Name: "blind", Status: common.ChannelStatusEnabled, OtherSettings: blindSettings}
	capable := Channel{Name: "capable", Status: common.ChannelStatusEnabled, OtherSettings: capableSettings}
	require.NoError(t, DB.Create(&blind).Error)
	require.NoError(t, DB.Create(&capable).Error)
	require.NoError(t, DB.Create(&[]Ability{
		{Group: "default", Model: modelName, ChannelId: blind.Id, Enabled: true, Priority: ptrInt64(50)},
		{Group: "default", Model: modelName, ChannelId: capable.Id, Enabled: true, Priority: ptrInt64(10)},
	}).Error)

	// A text request keeps priority order: the blind channel still wins.
	textSelected, err := GetChannelWithBlockedChannelsPinned("default", modelName, 0, "", nil, false, false)
	require.NoError(t, err)
	require.NotNil(t, textSelected)
	assert.Equal(t, blind.Id, textSelected.Id, "a text request must keep the highest-priority channel")

	// A video request must skip the blind channel despite its higher priority.
	videoSelected, err := GetChannelWithBlockedChannelsPinned("default", modelName, 0, "", nil, false, true)
	require.NoError(t, err)
	require.NotNil(t, videoSelected)
	assert.Equal(t, capable.Id, videoSelected.Id, "a video request must reach a channel that can read it")
}

// TestSelectChannelVideoRequestFailsWhenNothingDeclaresVideo pins the honest
// failure mode: with no declared channel the video request finds no candidate
// rather than being served by an upstream that ignores the media.
func TestSelectChannelVideoRequestFailsWhenNothingDeclaresVideo(t *testing.T) {
	previousDB := DB
	previousMemoryCacheEnabled := common.MemoryCacheEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	DB = db
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCacheEnabled
		DB = previousDB
	})

	const modelName = "kimi-k3"
	blind := Channel{Name: "blind", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&blind).Error)
	require.NoError(t, DB.Create(&Ability{
		Group: "default", Model: modelName, ChannelId: blind.Id, Enabled: true, Priority: ptrInt64(50),
	}).Error)

	selected, err := GetChannelWithBlockedChannelsPinned("default", modelName, 0, "", nil, false, true)
	require.NoError(t, err)
	assert.Nil(t, selected, "no declared channel must mean no candidate, not a blind upstream")
}

// TestValidateVideoUsageModeRequiresCapability pins the save-time rule that
// keeps the two settings consistent: a usage mode without the capability would
// be dead configuration, because video requests never reach such a channel.
func TestValidateVideoUsageModeRequiresCapability(t *testing.T) {
	modeOnly := &dto.ChannelOtherSettings{VideoUsageMode: dto.VideoUsageModeEstimate}
	require.Error(t, modeOnly.ValidateVideoUsageMode())

	both := &dto.ChannelOtherSettings{SupportsVideo: true, VideoUsageMode: dto.VideoUsageModeEstimate}
	require.NoError(t, both.ValidateVideoUsageMode())

	capabilityOnly := &dto.ChannelOtherSettings{SupportsVideo: true}
	require.NoError(t, capabilityOnly.ValidateVideoUsageMode())

	stillRejected := &dto.ChannelOtherSettings{SupportsVideo: true, VideoUsageMode: "estiamte"}
	require.Error(t, stillRejected.ValidateVideoUsageMode())
}
