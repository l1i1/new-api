package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func setupPartnerInviteCodeDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Log{}))
	previousDB, previousLogDB := DB, LOG_DB
	DB, LOG_DB = db, db
	t.Cleanup(func() { DB, LOG_DB = previousDB, previousLogDB })
}

// TestResolveInviterByAffCodePrefersPartnerCodes covers the registration entry
// point: a console-issued code attributes to its synthetic inviter id, a real
// aff code keeps attributing to its account, and an unknown code attributes to
// nobody instead of failing the registration.
func TestResolveInviterByAffCodePrefersPartnerCodes(t *testing.T) {
	setupPartnerInviteCodeDB(t)
	require.NoError(t, DB.Create(&User{Username: "code-owner", AffCode: "O30E"}).Error)

	synthetic := operation_setting.PartnerSyntheticInviterIDFloor + 5
	_, err := operation_setting.UpsertPartnerInviteCodes("test-resolve", []operation_setting.PartnerInviteCode{
		{Code: "TEST-RESOLVE-1", InviterID: synthetic},
	}, nil)
	require.NoError(t, err)

	assert.Equal(t, synthetic, ResolveInviterByAffCode("TEST-RESOLVE-1"))
	assert.Equal(t, 1, ResolveInviterByAffCode("O30E"))
	assert.Equal(t, 0, ResolveInviterByAffCode("  "))
	assert.Equal(t, 0, ResolveInviterByAffCode("NOSUCHCODE"))

	taken, err := AffCodeTakenByUser("O30E")
	require.NoError(t, err)
	assert.True(t, taken)
	taken, err = AffCodeTakenByUser("TEST-RESOLVE-1")
	require.NoError(t, err)
	assert.False(t, taken)
}

// TestRegistrationAttributesToPartnerCodeWithoutAccountRow is the whole point of
// console-issued codes: a registration through one lands on the synthetic
// inviter id with no account behind it, still gets the partner's white label,
// and leaves the site's own invite counters alone.
func TestRegistrationAttributesToPartnerCodeWithoutAccountRow(t *testing.T) {
	setupPartnerInviteCodeDB(t)
	synthetic := operation_setting.PartnerSyntheticInviterIDFloor + 7
	_, err := operation_setting.UpsertPartnerInviteCodes("test-registration", []operation_setting.PartnerInviteCode{
		{Code: "TEST-REG-1", InviterID: synthetic},
	}, nil)
	require.NoError(t, err)

	inviterID := ResolveInviterByAffCode("TEST-REG-1")
	require.Equal(t, synthetic, inviterID)

	user := &User{Username: "code-invitee", Password: "password-1"}
	require.NoError(t, user.Insert(inviterID))

	stored, err := GetUserById(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, synthetic, stored.InviterId)
	setting := stored.GetSetting()
	require.NotNil(t, setting.WhiteLabel)
	assert.Equal(t, "test-registration", setting.WhiteLabel.PartnerID)

	var accounts int64
	require.NoError(t, DB.Model(&User{}).Where("id = ?", synthetic).Count(&accounts).Error)
	assert.Zero(t, accounts)
}
