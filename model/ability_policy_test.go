package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetChannelWithBlockedChannelsExcludesBlockedDatabaseCandidates(t *testing.T) {
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

	blocked := Channel{Name: "blocked", Status: common.ChannelStatusEnabled}
	allowed := Channel{Name: "allowed", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&blocked).Error)
	require.NoError(t, DB.Create(&allowed).Error)
	require.NoError(t, DB.Create(&[]Ability{
		{Group: "default", Model: "policy-model", ChannelId: blocked.Id, Enabled: true, Priority: ptrInt64(10)},
		{Group: "default", Model: "policy-model", ChannelId: allowed.Id, Enabled: true, Priority: ptrInt64(10)},
	}).Error)

	selected, err := GetChannelWithBlockedChannels("default", "policy-model", 0, "", map[int]struct{}{blocked.Id: {}})
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, allowed.Id, selected.Id)
}

func TestGetGroupModelAvailabilityDistinguishesConfiguredChannelState(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	DB = db
	t.Cleanup(func() { DB = previousDB })

	priority := int64(0)
	require.NoError(t, DB.Create(&Channel{Id: 701, Status: common.ChannelStatusEnabled, Models: "availability-model", Group: "default"}).Error)
	require.NoError(t, DB.Create(&Channel{Id: 702, Status: common.ChannelStatusManuallyDisabled, Models: "disabled-model", Group: "default"}).Error)
	require.NoError(t, DB.Create(&[]Ability{
		{Group: "default", Model: "availability-model", ChannelId: 701, Enabled: true, Priority: &priority},
		{Group: "default", Model: "availability-model-openai-compact", ChannelId: 701, Enabled: true, Priority: &priority},
		{Group: "default", Model: "disabled-model", ChannelId: 702, Enabled: true, Priority: &priority},
	}).Error)

	configured, enabled, permittedEnabled, err := GetGroupModelAvailability("default", "availability-model", nil)
	require.NoError(t, err)
	require.True(t, configured)
	require.True(t, enabled)
	require.True(t, permittedEnabled)

	configured, enabled, permittedEnabled, err = GetGroupModelAvailability("default", "availability-model-openai-compact", nil)
	require.NoError(t, err)
	require.True(t, configured)
	require.True(t, enabled)
	require.True(t, permittedEnabled)

	configured, enabled, permittedEnabled, err = GetGroupModelAvailability("default", "disabled-model", nil)
	require.NoError(t, err)
	require.True(t, configured)
	require.False(t, enabled)
	require.False(t, permittedEnabled)

	configured, enabled, permittedEnabled, err = GetGroupModelAvailability("default", "missing-model", nil)
	require.NoError(t, err)
	require.False(t, configured)
	require.False(t, enabled)
	require.False(t, permittedEnabled)
}

func TestGetGroupModelAvailabilityForPathExcludesUnsupportedAdvancedCustomRoute(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	DB = db
	t.Cleanup(func() { DB = previousDB })

	priority := int64(0)
	channel := &Channel{Id: 703, Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusEnabled}
	channel.SetOtherSettings(dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{
		IncomingPath: "/v1/responses",
		Models:       []string{"path-model"},
	}}}})
	require.NoError(t, DB.Create(channel).Error)
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "path-model", ChannelId: channel.Id, Enabled: true, Priority: &priority}).Error)

	configured, enabled, permittedEnabled, err := GetGroupModelAvailabilityForPath("default", "path-model", "/v1/chat/completions", nil)
	require.NoError(t, err)
	require.False(t, configured)
	require.False(t, enabled)
	require.False(t, permittedEnabled)

	configured, enabled, permittedEnabled, err = GetGroupModelAvailabilityForPath("default", "path-model", "/v1/responses", nil)
	require.NoError(t, err)
	require.True(t, configured)
	require.True(t, enabled)
	require.True(t, permittedEnabled)
}

func ptrInt64(value int64) *int64 {
	return &value
}

func TestGetChannelWithBlockedChannelsPinsDeepSeekV4ToOfficialDatabaseCandidates(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	DB = db
	t.Cleanup(func() { DB = previousDB })

	aggregator := Channel{Name: "aggregator", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled}
	official := Channel{Name: "official", Type: constant.ChannelTypeDeepSeek, Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&aggregator).Error)
	require.NoError(t, DB.Create(&official).Error)
	require.NoError(t, DB.Create(&[]Ability{
		{Group: "default", Model: "deepseek-v4-flash", ChannelId: aggregator.Id, Enabled: true, Priority: ptrInt64(9), Weight: 100},
		{Group: "default", Model: "deepseek-v4-flash", ChannelId: official.Id, Enabled: true, Priority: ptrInt64(9), Weight: 0},
	}).Error)

	for i := 0; i < 20; i++ {
		selected, err := GetChannelWithBlockedChannels("default", "deepseek-v4-flash", 0, "", nil)
		require.NoError(t, err)
		require.NotNil(t, selected)
		require.Equal(t, official.Id, selected.Id, "same-priority V4 candidates must narrow to the official channel despite the aggregator's higher weight")
	}
}

func TestGetChannelWithBlockedChannelsKeepsSoleDeepSeekV4Candidate(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	DB = db
	t.Cleanup(func() { DB = previousDB })

	aggregator := Channel{Name: "sole-aggregator", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&aggregator).Error)
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "deepseek-v4-flash", ChannelId: aggregator.Id, Enabled: true, Priority: ptrInt64(9)}).Error)

	selected, err := GetChannelWithBlockedChannels("default", "deepseek-v4-flash", 0, "", nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, aggregator.Id, selected.Id, "a sole candidate is returned unchanged with no official channel to prefer")
}
