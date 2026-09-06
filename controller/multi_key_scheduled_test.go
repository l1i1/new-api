package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupMultiKeyScheduledTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ChannelCredential{}, &model.ChannelCredentialRevision{}))
	t.Cleanup(func() {
		model.DB = previousDB
		sqlDB, closeErr := db.DB()
		if closeErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestMultiKeyScheduledTestStatusChanges(t *testing.T) {
	db := setupMultiKeyScheduledTestDB(t)
	channel := &model.Channel{Key: "key-a\nkey-b\nkey-c\nkey-d", Name: "scheduled", ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 4}}
	require.NoError(t, db.Create(channel).Error)
	for position, secret := range []string{"key-a", "key-b", "key-c", "key-d"} {
		credential, err := model.NewChannelCredential(channel.Id, position, secret)
		require.NoError(t, err)
		if position == 1 {
			credential.Status = common.ChannelStatusAutoDisabled
			credential.DisabledReason = "upstream 401"
		}
		if position == 2 {
			credential.Status = common.ChannelStatusManuallyDisabled
			credential.DisabledReason = "manual"
		}
		require.NoError(t, db.Create(credential).Error)
	}
	credentials, err := model.ListChannelCredentials(db, channel.Id)
	require.NoError(t, err)
	idByPosition := map[int]int{}
	for _, credential := range credentials {
		idByPosition[credential.Position] = credential.Id
	}

	probes := []multiKeyTestResult{
		// Key 0 is enabled and fails: auto-disable.
		{CredentialID: idByPosition[0], Status: "failed", HTTPStatus: 401, ErrorMessage: "invalid api key"},
		// Key 1 is auto-disabled and passes: re-enable.
		{CredentialID: idByPosition[1], Status: "success"},
		// Key 2 is manually disabled and passes: keep manual decision.
		{CredentialID: idByPosition[2], Status: "success"},
		// Key 3 is enabled and passes: no change.
		{CredentialID: idByPosition[3], Status: "success"},
	}

	disabled, reenabled := multiKeyScheduledTestStatusChanges(channel.Id, probes, false)
	assert.Equal(t, []int{idByPosition[0]}, disabled)
	assert.Equal(t, []int{idByPosition[1]}, reenabled)

	// Opt-in re-enables the manually disabled key as well. Classification is
	// read-only, so both variants run against the same stored state.
	optInDisabled, optInReenabled := multiKeyScheduledTestStatusChanges(channel.Id, probes, true)
	assert.Equal(t, []int{idByPosition[0]}, optInDisabled)
	assert.ElementsMatch(t, []int{idByPosition[1], idByPosition[2]}, optInReenabled)

	require.NoError(t, applyMultiKeyScheduledStatusChanges(channel.Id, disabled, reenabled))

	stored, err := model.ListChannelCredentials(db, channel.Id)
	require.NoError(t, err)
	statusByPosition := map[int]int{}
	for _, credential := range stored {
		statusByPosition[credential.Position] = credential.Status
	}
	assert.Equal(t, common.ChannelStatusAutoDisabled, statusByPosition[0])
	assert.Equal(t, common.ChannelStatusEnabled, statusByPosition[1])
	assert.Equal(t, common.ChannelStatusManuallyDisabled, statusByPosition[2])
	assert.Equal(t, common.ChannelStatusEnabled, statusByPosition[3])
}
