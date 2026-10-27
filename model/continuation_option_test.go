package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUpdateOptionAppliesChatContinuationSettings(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	t.Cleanup(func() { DB = previousDB })

	oldMap := common.OptionMap
	common.OptionMap = map[string]string{}
	t.Cleanup(func() { common.OptionMap = oldMap })

	oldOnAbnormal := common.ChatContinuationOnAbnormal
	oldChannels := common.ChatContinuationChannelIDs
	oldRaw := common.ChatContinuationChannelsRaw
	oldSteps := common.ChatContinuationMaxSteps
	t.Cleanup(func() {
		common.ChatContinuationOnAbnormal = oldOnAbnormal
		common.ChatContinuationChannelIDs = oldChannels
		common.ChatContinuationChannelsRaw = oldRaw
		common.ChatContinuationMaxSteps = oldSteps
	})

	require.NoError(t, UpdateOption(common.ChatContinuationOptionOnAbnormal, "true"))
	require.NoError(t, UpdateOption(common.ChatContinuationOptionChannels, "37,91"))
	require.NoError(t, UpdateOption(common.ChatContinuationOptionMaxSteps, "2"))
	require.True(t, common.ChatContinuationOnAbnormal)
	require.True(t, common.ChatContinuationChannelAllowed(37))
	require.True(t, common.ChatContinuationChannelAllowed(91))
	require.Equal(t, 2, common.ChatContinuationMaxSteps)

	var stored Option
	require.NoError(t, DB.First(&stored, "key = ?", common.ChatContinuationOptionChannels).Error)
	require.Equal(t, "37,91", stored.Value)

	require.Error(t, UpdateOption(common.ChatContinuationOptionChannels, "37,oops"))
	require.NoError(t, DB.First(&stored, "key = ?", common.ChatContinuationOptionChannels).Error)
	require.Equal(t, "37,91", stored.Value, "invalid update must not persist")
}
