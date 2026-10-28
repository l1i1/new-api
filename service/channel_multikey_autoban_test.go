package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupMultiKeyAutoBanDB brings up the isolated SQLite schema these tests write
// to, plus the root user the notification path addresses.
func setupMultiKeyAutoBanDB(t *testing.T) {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	previousMaster, previousCache, previousRedis, previousSQLite := common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled, common.SQLitePath
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousType, previousLogType)
		common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled, common.SQLitePath = previousMaster, previousCache, previousRedis, previousSQLite
	})
	t.Setenv("SQL_DSN", os.Getenv("TEST_CHANNEL_SQL_DSN"))
	t.Setenv("LOG_SQL_DSN", "")
	common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled = false, false, false
	common.SQLitePath = filepath.Join(t.TempDir(), "channel-autoban.db")
	require.NoError(t, model.InitDB())
	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	model.LOG_DB = model.DB
	common.SetLogDatabaseType(common.MainDatabaseType())
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.ChannelCredential{}, &model.ChannelCredentialRevision{}, &model.Ability{}, &model.User{}, &model.Log{}))
	require.NoError(t, i18n.Init())
	root := &model.User{Username: "multi-key-autoban-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(root).Error)
	t.Cleanup(func() { require.NoError(t, model.DB.Unscoped().Delete(root).Error) })
}

func newAutoBanTestChannel(t *testing.T, keys int) *model.Channel {
	t.Helper()
	keyList := ""
	for i := 0; i < keys; i++ {
		if i > 0 {
			keyList += "\n"
		}
		keyList += "auto-ban-key-" + string(rune('a'+i))
	}
	channel := &model.Channel{
		Name:        t.Name(),
		Type:        1,
		Key:         keyList,
		Status:      common.ChannelStatusEnabled,
		Models:      "test-model",
		Group:       "default",
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: keys, MultiKeyMode: "affinity"},
	}
	require.NoError(t, channel.Insert())
	t.Cleanup(func() {
		require.NoError(t, model.DB.Unscoped().Where("channel_id = ?", channel.Id).Delete(&model.ChannelCredential{}).Error)
		require.NoError(t, channel.Delete())
	})
	return channel
}

func autoBanErrorFor(channel *model.Channel, usingKey string) types.ChannelError {
	return *types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, usingKey, true)
}

func loadAutoBanTestChannel(t *testing.T, channelID int) *model.Channel {
	t.Helper()
	loaded, err := model.GetChannelById(channelID, true)
	require.NoError(t, err)
	return loaded
}

// A relay error attributed to one key must only disable that key: the channel
// keeps serving on its other keys, which is the whole point of a key pool
// (2026-10-28 operator report: one suspended Moonshot org must not remove the
// three healthy orgs' capacity).
func TestRelayAutoBanDisablesOnlyTheFailingKey(t *testing.T) {
	setupMultiKeyAutoBanDB(t)
	channel := newAutoBanTestChannel(t, 4)
	keys := channel.GetKeys()
	require.Len(t, keys, 4)

	DisableChannelBecauseRelayError(autoBanErrorFor(channel, keys[2]), "status_code=429, Your account is suspended due to insufficient balance")

	loaded := loadAutoBanTestChannel(t, channel.Id)
	assert.Equal(t, common.ChannelStatusEnabled, loaded.Status, "three healthy keys must keep the channel enabled")
	assert.Equal(t, common.ChannelStatusAutoDisabled, loaded.ChannelInfo.MultiKeyStatusList[2])
	for _, position := range []int{0, 1, 3} {
		assert.NotContains(t, loaded.ChannelInfo.MultiKeyStatusList, position)
	}
}

// The failure must be attributable. An empty or stale UsingKey would otherwise
// take the entire key pool down for an error that may well be about one key.
func TestRelayAutoBanSkipsUnattributableFailure(t *testing.T) {
	setupMultiKeyAutoBanDB(t)
	channel := newAutoBanTestChannel(t, 4)

	for _, usingKey := range []string{"", "a-key-that-is-not-in-this-channel"} {
		DisableChannelBecauseRelayError(autoBanErrorFor(channel, usingKey), "status_code=429, unspecified")
		loaded := loadAutoBanTestChannel(t, channel.Id)
		assert.Equal(t, common.ChannelStatusEnabled, loaded.Status, "usingKey=%q must not disable the channel", usingKey)
		assert.Empty(t, loaded.ChannelInfo.MultiKeyStatusList, "usingKey=%q must not disable any key", usingKey)
	}
}

// When the failing key is the last usable one the channel really has no
// capacity left, so it is disabled and the channel-level notice is correct.
func TestRelayAutoBanDisablesChannelWhenLastKeyFails(t *testing.T) {
	setupMultiKeyAutoBanDB(t)
	channel := newAutoBanTestChannel(t, 2)
	keys := channel.GetKeys()
	require.Len(t, keys, 2)

	DisableChannelBecauseRelayError(autoBanErrorFor(channel, keys[0]), "key a failed")
	loaded := loadAutoBanTestChannel(t, channel.Id)
	require.Equal(t, common.ChannelStatusEnabled, loaded.Status)

	DisableChannelBecauseRelayError(autoBanErrorFor(channel, keys[1]), "key b failed")
	loaded = loadAutoBanTestChannel(t, channel.Id)
	assert.Equal(t, common.ChannelStatusAutoDisabled, loaded.Status)
}

// enabledMultiKeyCount is what the relay path consults before scoping a ban to
// the failing key, so it has to read the durable credential status first.
func TestEnabledMultiKeyCountPrefersCredentialStatus(t *testing.T) {
	channel := &model.Channel{
		Key:         "key-one\nkey-two\nkey-three",
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 3},
	}
	keys := channel.GetKeys()
	require.Len(t, keys, 3)
	channel.Credentials = []model.ChannelCredential{
		{ChannelID: 1, Position: 0, Fingerprint: model.ChannelCredentialFingerprint(keys[0]), Status: common.ChannelStatusEnabled},
		{ChannelID: 1, Position: 1, Fingerprint: model.ChannelCredentialFingerprint(keys[1]), Status: common.ChannelStatusAutoDisabled},
	}
	// Position 2 has no credential row: the legacy map decides, and an absent
	// entry means enabled.
	assert.Equal(t, 2, enabledMultiKeyCount(channel, keys))
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{2: common.ChannelStatusManuallyDisabled}
	assert.Equal(t, 1, enabledMultiKeyCount(channel, keys))
	channel.Credentials[1].Status = common.ChannelStatusEnabled
	assert.Equal(t, 2, enabledMultiKeyCount(channel, keys))
}
