package model

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupMultiKeyStatusDB brings up the isolated SQLite schema the channel status
// tests write to. It mirrors controller/channel_multi_key_test.go so both halves
// of the multi-key behaviour are exercised against the same storage.
func setupMultiKeyStatusDB(t *testing.T) {
	t.Helper()
	previousDB, previousLogDB := DB, LOG_DB
	previousType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	previousMaster, previousCache, previousRedis, previousSQLite := common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled, common.SQLitePath
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousType, previousLogType)
		common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled, common.SQLitePath = previousMaster, previousCache, previousRedis, previousSQLite
	})
	t.Setenv("SQL_DSN", os.Getenv("TEST_CHANNEL_SQL_DSN"))
	t.Setenv("LOG_SQL_DSN", "")
	common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled = false, false, false
	common.SQLitePath = filepath.Join(t.TempDir(), "channel-status.db")
	require.NoError(t, InitDB())
	sqlDB, err := DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	LOG_DB = DB
	common.SetLogDatabaseType(common.MainDatabaseType())
	require.NoError(t, DB.AutoMigrate(&Channel{}, &ChannelCredential{}, &ChannelCredentialRevision{}, &Ability{}, &User{}))
}

// newMultiKeyStatusChannel inserts an enabled four-key channel whose credential
// rows are migrated from the legacy key column, the same way a channel created
// through the API starts out.
func newMultiKeyStatusChannel(t *testing.T, name string) *Channel {
	t.Helper()
	channel := &Channel{
		Name:   name,
		Type:   1,
		Key:    "key-one\nkey-two\nkey-three\nkey-four",
		Status: common.ChannelStatusEnabled,
		Models: "test-model",
		Group:  "default",
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 4,
			MultiKeyMode: "affinity",
		},
	}
	require.NoError(t, channel.Insert())
	t.Cleanup(func() {
		require.NoError(t, DB.Unscoped().Where("channel_id = ?", channel.Id).Delete(&ChannelCredential{}).Error)
		require.NoError(t, channel.Delete())
	})
	return channel
}

func assertChannelAbilityEnabled(t *testing.T, channelID int, want bool) {
	t.Helper()
	var ability Ability
	require.NoError(t, DB.Where("channel_id = ?", channelID).First(&ability).Error)
	assert.Equal(t, want, ability.Enabled)
}

func credentialAt(t *testing.T, channel *Channel, position int) *ChannelCredential {
	t.Helper()
	for index := range channel.Credentials {
		if channel.Credentials[index].Position == position {
			return &channel.Credentials[index]
		}
	}
	t.Fatalf("credential at position %d not found", position)
	return nil
}

// One dead credential must not take the channel down: the failing key is
// disabled, every other key keeps serving, and the ability stays enabled so
// routing still considers the channel (2026-10-28 operator report).
func TestMultiKeyStatusChangeDisablesOnlyTheFailingKey(t *testing.T) {
	setupMultiKeyStatusDB(t)
	channel := newMultiKeyStatusChannel(t, "multi-key-one-key-fails")
	keys := channel.GetKeys()
	require.Len(t, keys, 4)

	changed := UpdateChannelStatus(channel.Id, keys[1], common.ChannelStatusAutoDisabled,
		"status_code=429, Your account is suspended due to insufficient balance")
	require.True(t, changed, "disabling the failing key is a real change")

	loaded, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, loaded.Status, "one dead key must not disable the channel")
	assert.Equal(t, common.ChannelStatusAutoDisabled, loaded.ChannelInfo.MultiKeyStatusList[1])
	for _, position := range []int{0, 2, 3} {
		assert.NotContains(t, loaded.ChannelInfo.MultiKeyStatusList, position, "the healthy keys must stay enabled")
	}
	// The durable credential mirror is what runtime key selection reads.
	assert.Equal(t, common.ChannelStatusAutoDisabled, credentialAt(t, loaded, 1).Status)
	assert.Contains(t, credentialAt(t, loaded, 1).DisabledReason, "insufficient balance")
	assert.Equal(t, common.ChannelStatusEnabled, credentialAt(t, loaded, 0).Status)
	assert.Equal(t, common.ChannelStatusEnabled, credentialAt(t, loaded, 3).Status)
	assertChannelAbilityEnabled(t, channel.Id, true)

	// A key that is no longer part of the channel must change nothing. The
	// caller uses this false to avoid the channel-level "disabled" notice.
	require.False(t, UpdateChannelStatus(channel.Id, "not-a-key-of-this-channel", common.ChannelStatusAutoDisabled, "unattributed"))
	again, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, again.Status)
	assert.Len(t, again.ChannelInfo.MultiKeyStatusList, 1, "no extra key may be disabled")
	assertChannelAbilityEnabled(t, channel.Id, true)
}

// When the last usable key fails the channel really is out of capacity, so the
// channel status flips to auto-disabled and the ability is withdrawn.
func TestMultiKeyStatusChangeDisablesChannelWhenLastKeyFails(t *testing.T) {
	setupMultiKeyStatusDB(t)
	channel := newMultiKeyStatusChannel(t, "multi-key-all-keys-fail")
	keys := channel.GetKeys()
	require.Len(t, keys, 4)

	for _, position := range []int{0, 1, 2} {
		require.True(t, UpdateChannelStatus(channel.Id, keys[position], common.ChannelStatusAutoDisabled, "key failed"))
		loaded, err := GetChannelById(channel.Id, true)
		require.NoError(t, err)
		assert.Equal(t, common.ChannelStatusEnabled, loaded.Status, "the channel keeps serving while a key remains")
	}

	require.True(t, UpdateChannelStatus(channel.Id, keys[3], common.ChannelStatusAutoDisabled, "last key failed"))
	loaded, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusAutoDisabled, loaded.Status)
	assert.Equal(t, ChannelStatusReasonAllKeysDisabled, loaded.GetOtherInfo()["status_reason"])
	assertChannelAbilityEnabled(t, channel.Id, false)
}

// Re-enabling a single key of an enabled channel is a key-level change: the
// channel status stays enabled and the caller can tell the difference through
// the returned value instead of sending a misleading channel notice.
func TestMultiKeyStatusChangeReenablesOneKeyOnly(t *testing.T) {
	setupMultiKeyStatusDB(t)
	channel := newMultiKeyStatusChannel(t, "multi-key-reenable-key")
	keys := channel.GetKeys()
	require.True(t, UpdateChannelStatus(channel.Id, keys[2], common.ChannelStatusAutoDisabled, "key failed"))

	previous, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	require.Equal(t, common.ChannelStatusEnabled, previous.Status)

	require.True(t, UpdateChannelStatus(channel.Id, keys[2], common.ChannelStatusEnabled, ""))
	loaded, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, loaded.Status)
	assert.NotContains(t, loaded.ChannelInfo.MultiKeyStatusList, 2)
	assert.Equal(t, common.ChannelStatusEnabled, credentialAt(t, loaded, 2).Status)
	assert.Empty(t, credentialAt(t, loaded, 2).DisabledReason)

	// Repeating the same key-level change is a no-op, not a notification.
	require.False(t, UpdateChannelStatus(channel.Id, keys[2], common.ChannelStatusEnabled, ""))
}
