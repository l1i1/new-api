package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupPartnerStatsDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE users (id integer primary key, inviter_id integer, created_at integer, status integer, deleted_at datetime)").Error)
	require.NoError(t, db.Exec("CREATE TABLE logs (user_id integer, quota integer, `group` varchar(64), type integer, created_at integer)").Error)
	require.NoError(t, db.Exec("CREATE TABLE top_ups (user_id integer, credited_quota integer, status varchar(32))").Error)
	previousDB, previousLogDB := DB, LOG_DB
	DB, LOG_DB = db, db
	t.Cleanup(func() { DB, LOG_DB = previousDB, previousLogDB })
}

func TestGetPartnerInviteesPagesByInviter(t *testing.T) {
	setupPartnerStatsDB(t)
	require.NoError(t, DB.Exec("INSERT INTO users (id, inviter_id, created_at, status) VALUES (1, 7, 100, 1), (2, 7, 200, 1), (3, 8, 300, 1)").Error)

	invitees, total, err := GetPartnerInvitees([]int{7}, 1, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	require.Len(t, invitees, 2)
	assert.Equal(t, 1, invitees[0].ID)
	assert.Equal(t, 7, invitees[0].InviterID)
	assert.Equal(t, int64(100), invitees[0].RegisteredAt)
	assert.Greater(t, invitees[0].CommissionUntil, invitees[0].RegisteredAt)
}

func TestGetPartnerConsumptionSplitsOfficialAndRefund(t *testing.T) {
	setupPartnerStatsDB(t)
	// type=2 consume (one official), type=6 refund, type=1 topup-log ignored.
	require.NoError(t, DB.Exec(`INSERT INTO logs (user_id, quota, "group", type, created_at) VALUES
		(1, 1000, 'default', 2, 100),
		(1, 300, 'vip-Official', 2, 150),
		(1, 50, 'default', 6, 160),
		(1, 9999, 'default', 1, 170)`).Error)
	require.NoError(t, DB.Exec("INSERT INTO top_ups (user_id, credited_quota, status) VALUES (1, 2000, 'success'), (1, 500, 'pending')").Error)

	rows, err := GetPartnerConsumption([]int{1}, 0, 1000)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(1000), rows[0].ConsumeQuota)
	assert.Equal(t, int64(300), rows[0].OfficialExcluded)
	assert.Equal(t, int64(50), rows[0].RefundQuota)
	assert.Equal(t, int64(2000), rows[0].TopupCreditedQuota)
}
