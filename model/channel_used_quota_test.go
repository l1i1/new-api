package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelUsedQuotaTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	previousDB := DB
	previousBatchUpdateEnabled := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&Channel{}))

	t.Cleanup(func() {
		common.BatchUpdateEnabled = previousBatchUpdateEnabled
		DB = previousDB
		sqlDB, closeErr := db.DB()
		if closeErr == nil {
			_ = sqlDB.Close()
		}
	})

	return db
}

func TestChannelUsedQuotaBypassesProcessBatchAndResetsTransactionally(t *testing.T) {
	db := setupChannelUsedQuotaTestDB(t)
	channel := &Channel{Name: "quota channel", UsedQuota: 1000}
	require.NoError(t, db.Create(channel).Error)

	UpdateChannelUsedQuota(channel.Id, 75)

	var beforeReset Channel
	require.NoError(t, db.First(&beforeReset, channel.Id).Error)
	assert.Equal(t, int64(1075), beforeReset.UsedQuota)

	resetChannel, err := ResetChannelUsedQuota(channel.Id)
	require.NoError(t, err)
	assert.Equal(t, int64(1075), resetChannel.UsedQuota)

	var refreshed Channel
	require.NoError(t, db.First(&refreshed, channel.Id).Error)
	assert.Zero(t, refreshed.UsedQuota)
}

func TestResetChannelsUsedQuotaResetsAllExistingChannels(t *testing.T) {
	db := setupChannelUsedQuotaTestDB(t)
	first := &Channel{Name: "first quota channel", UsedQuota: 100}
	second := &Channel{Name: "second quota channel", UsedQuota: 200}
	require.NoError(t, db.Create(first).Error)
	require.NoError(t, db.Create(second).Error)

	UpdateChannelUsedQuota(first.Id, 25)
	UpdateChannelUsedQuota(second.Id, 50)

	resetChannels, err := ResetChannelsUsedQuota([]int{second.Id, first.Id, first.Id, 999999})
	require.NoError(t, err)
	require.Len(t, resetChannels, 2)
	assert.Equal(t, first.Id, resetChannels[0].Id)
	assert.Equal(t, int64(125), resetChannels[0].UsedQuota)
	assert.Equal(t, second.Id, resetChannels[1].Id)
	assert.Equal(t, int64(250), resetChannels[1].UsedQuota)

	var refreshed []Channel
	require.NoError(t, db.Order("id ASC").Find(&refreshed).Error)
	require.Len(t, refreshed, 2)
	assert.Zero(t, refreshed[0].UsedQuota)
	assert.Zero(t, refreshed[1].UsedQuota)
}

func TestResetChannelUsedQuotaReportsMissingChannel(t *testing.T) {
	setupChannelUsedQuotaTestDB(t)

	_, err := ResetChannelUsedQuota(999999)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
