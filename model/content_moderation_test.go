package model

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestResetContentModerationUserViolationsKeepsAuditLogs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ContentModerationLog{}, &ContentModerationUserState{}))

	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	const userID = 987660
	now := time.Now()
	require.NoError(t, db.Create(&ContentModerationLog{
		UserID: userID, Flagged: true, Action: "observe", CreatedAt: now.Add(-time.Minute).Unix(),
	}).Error)
	require.NoError(t, db.Create(&ContentModerationLog{
		UserID: userID, Flagged: true, Action: ContentModerationActionCyberPolicy, CreatedAt: now.Add(-time.Minute).Unix(),
	}).Error)

	count, err := CountFlaggedContentModerationByUserSince(userID, now.Add(-time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, count)

	require.NoError(t, ResetContentModerationUserViolations(userID))
	count, err = CountFlaggedContentModerationByUserSince(userID, now.Add(-time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 0, count)

	var logs int64
	require.NoError(t, db.Model(&ContentModerationLog{}).Where("user_id = ?", userID).Count(&logs).Error)
	require.EqualValues(t, 2, logs)

	require.NoError(t, db.Create(&ContentModerationLog{
		UserID: userID, Flagged: true, Action: "observe", CreatedAt: now.Unix(),
	}).Error)
	require.NoError(t, db.Create(&ContentModerationLog{
		UserID: userID, Flagged: true, Action: ContentModerationActionCyberPolicy, CreatedAt: now.Unix(),
	}).Error)
	count, err = CountFlaggedContentModerationByUserSince(userID, now.Add(-time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, count)

	require.NoError(t, ResetContentModerationUserViolations(userID))
	count, err = CountFlaggedContentModerationByUserSince(userID, now.Add(-time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 0, count)
}
