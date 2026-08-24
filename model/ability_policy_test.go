package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
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

func ptrInt64(value int64) *int64 {
	return &value
}
