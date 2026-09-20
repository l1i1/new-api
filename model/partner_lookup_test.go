package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupPartnerLookupDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE users (id integer primary key, username varchar(20), aff_code varchar(32), status integer, deleted_at datetime)").Error)
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous })
}

// The console maps a channel by username; the id and aff code must come from
// the account row, because a hand-typed pair is unverifiable and silently
// attributes the wrong invitees or mislabels the bucket.
func TestGetPartnerUserRefByUsernameReadsIdAndAffCode(t *testing.T) {
	setupPartnerLookupDB(t)
	require.NoError(t, DB.Exec("INSERT INTO users (id, username, aff_code, status) VALUES (15272, 'tommysen0127', 'O30E', 1)").Error)

	ref, err := GetPartnerUserRefByUsername("tommysen0127")
	require.NoError(t, err)
	assert.Equal(t, 15272, ref.ID)
	assert.Equal(t, "tommysen0127", ref.Username)
	assert.Equal(t, "O30E", ref.AffCode)
	assert.Equal(t, 1, ref.Status)
}

func TestGetPartnerUserRefByUsernameReportsMissingAccounts(t *testing.T) {
	setupPartnerLookupDB(t)

	_, err := GetPartnerUserRefByUsername("nobody")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPartnerUserNotFound)
}

// A disabled account still resolves: the mapping is real, and the operator —
// not this lookup — decides what to do about its state.
func TestGetPartnerUserRefByUsernameKeepsDisabledAccounts(t *testing.T) {
	setupPartnerLookupDB(t)
	require.NoError(t, DB.Exec("INSERT INTO users (id, username, aff_code, status) VALUES (7, 'paused', 'AB12', 2)").Error)

	ref, err := GetPartnerUserRefByUsername("paused")
	require.NoError(t, err)
	assert.Equal(t, 2, ref.Status)
	assert.Equal(t, "AB12", ref.AffCode)
}
