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

func TestResetChannelUsedQuotaReportsMissingChannel(t *testing.T) {
	setupChannelUsedQuotaTestDB(t)

	_, err := ResetChannelUsedQuota(999999)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
