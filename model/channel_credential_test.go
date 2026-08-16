package model

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func setupChannelCredentialSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&Channel{}, &ChannelCredential{}, &ChannelCredentialRevision{}))
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
		sqlDB, closeErr := db.DB()
		if closeErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestChannelCredentialFingerprintAndPublicViewDoNotExposeSecrets(t *testing.T) {
	credential, err := NewChannelCredential(11, 0, "test-credential-value")
	require.NoError(t, err)

	mode, proxyURL, err := NormalizeChannelCredentialProxy(
		ChannelCredentialProxyModeCustom,
		"http://proxy-user:proxy-password@proxy.example.test:8080",
	)
	require.NoError(t, err)
	credential.ProxyMode = mode
	credential.ProxyURL = proxyURL

	encoded, err := common.Marshal(credential)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), credential.Secret)
	assert.NotContains(t, string(encoded), "proxy-password")

	public := credential.PublicView()
	assert.Equal(t, credential.Fingerprint, public.Fingerprint)
	assert.NotContains(t, public.ProxySummary, "proxy-password")
	assert.NotContains(t, public.ProxySummary, "proxy-user")
	assert.Equal(t, "http://proxy.example.test:8080", public.ProxySummary)
	assert.Empty(t, RedactProxyURL("http://proxy-user:proxy-password@proxy.example.test:8080/path"))
}

func TestChannelCredentialProxyResolutionHonorsMode(t *testing.T) {
	mode, customURL, err := NormalizeChannelCredentialProxy(
		ChannelCredentialProxyModeCustom,
		"socks5://proxy.example.test:1080",
	)
	require.NoError(t, err)
	assert.Equal(t, ChannelCredentialProxyModeCustom, mode)
	assert.Equal(t, "socks5://proxy.example.test:1080", customURL)

	custom := ChannelCredential{ProxyMode: mode, ProxyURL: customURL}
	effective, err := custom.EffectiveProxyURL("http://channel.example.test:3128")
	require.NoError(t, err)
	assert.Equal(t, customURL, effective)

	direct := ChannelCredential{ProxyMode: ChannelCredentialProxyModeDirect}
	effective, err = direct.EffectiveProxyURL("http://channel.example.test:3128")
	require.NoError(t, err)
	assert.Empty(t, effective)

	inherited := ChannelCredential{ProxyMode: ChannelCredentialProxyModeInherit}
	effective, err = inherited.EffectiveProxyURL("socks5://channel.example.test")
	require.NoError(t, err)
	assert.Equal(t, "socks5://channel.example.test:1080", effective)

	_, _, err = NormalizeChannelCredentialProxy(ChannelCredentialProxyModeCustom, "http://proxy.example.test/path")
	assert.ErrorIs(t, err, ErrChannelCredentialProxyInvalid)
}

func TestMigrateLegacyChannelCredentialsPreservesIdentityAcrossReorderAndRemoval(t *testing.T) {
	db := setupChannelCredentialSQLite(t)
	channel := &Channel{
		Key:  "legacy-key-a\nlegacy-key-b",
		Name: "credential migration test",
		ChannelInfo: ChannelInfo{
			IsMultiKey:             true,
			MultiKeyStatusList:     map[int]int{1: common.ChannelStatusManuallyDisabled},
			MultiKeyDisabledReason: map[int]string{1: "legacy-invalid"},
			MultiKeyDisabledTime:   map[int]int64{1: 100},
		},
	}
	require.NoError(t, db.Create(channel).Error)

	require.NoError(t, MigrateChannelCredentialStore(db))
	credentials, err := ListChannelCredentials(db, channel.Id)
	require.NoError(t, err)
	require.Len(t, credentials, 2)
	firstIDs := map[string]int{
		credentials[0].Fingerprint: credentials[0].Id,
		credentials[1].Fingerprint: credentials[1].Id,
	}
	assert.Equal(t, common.ChannelStatusEnabled, credentials[0].Status)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, credentials[1].Status)
	assert.Equal(t, "legacy-invalid", credentials[1].DisabledReason)

	revision, err := GetChannelCredentialRevision(db, channel.Id)
	require.NoError(t, err)
	assert.Equal(t, int64(1), revision)

	require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Updates(map[string]interface{}{
		"key": "legacy-key-b\nlegacy-key-a",
	}).Error)
	require.NoError(t, MigrateLegacyChannelCredentialsWithDB(db))

	credentials, err = ListChannelCredentials(db, channel.Id)
	require.NoError(t, err)
	require.Len(t, credentials, 2)
	assert.Equal(t, firstIDs[credentials[0].Fingerprint], credentials[0].Id)
	assert.Equal(t, firstIDs[credentials[1].Fingerprint], credentials[1].Id)
	assert.Equal(t, int64(2), mustChannelCredentialRevision(t, db, channel.Id))

	require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Update("key", "legacy-key-b").Error)
	require.NoError(t, MigrateLegacyChannelCredentialsWithDB(db))
	credentials, err = ListChannelCredentials(db, channel.Id)
	require.NoError(t, err)
	require.Len(t, credentials, 2)
	var removed ChannelCredential
	require.NoError(t, db.Where("id = ?", firstIDs[ChannelCredentialFingerprint("legacy-key-a")]).First(&removed).Error)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, removed.Status)
	assert.Equal(t, ChannelCredentialDisabledReasonLegacyRemoved, removed.DisabledReason)
	assert.Equal(t, int64(3), mustChannelCredentialRevision(t, db, channel.Id))
}

func TestBumpChannelCredentialRevisionIsAtomicAtModelBoundary(t *testing.T) {
	db := setupChannelCredentialSQLite(t)
	next, err := BumpChannelCredentialRevision(db, 22)
	require.NoError(t, err)
	assert.Equal(t, int64(1), next)
	next, err = BumpChannelCredentialRevision(db, 22)
	require.NoError(t, err)
	assert.Equal(t, int64(2), next)
	assert.Equal(t, int64(2), mustChannelCredentialRevision(t, db, 22))
}

func TestBatchCredentialStatusSynchronizesChannelAggregate(t *testing.T) {
	db := setupChannelCredentialSQLite(t)
	channel := &Channel{Key: "key-a\nkey-b", Name: "batch status", ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeySize: 2}}
	require.NoError(t, db.Create(channel).Error)
	first, err := NewChannelCredential(channel.Id, 0, "key-a")
	require.NoError(t, err)
	second, err := NewChannelCredential(channel.Id, 1, "key-b")
	require.NoError(t, err)
	require.NoError(t, db.Create(first).Error)
	require.NoError(t, db.Create(second).Error)
	require.NoError(t, db.Create(&ChannelCredentialRevision{ChannelID: channel.Id, KeysRevision: 1}).Error)

	revision, err := UpdateChannelCredentialStatuses(db, ChannelCredentialStatusUpdate{
		ChannelID: channel.Id, All: true, Status: common.ChannelStatusManuallyDisabled, Reason: "probe failed", ExpectedRev: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), revision)
	var stored Channel
	require.NoError(t, db.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.Status)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.ChannelInfo.MultiKeyStatusList[0])

	revision, err = UpdateChannelCredentialStatuses(db, ChannelCredentialStatusUpdate{
		ChannelID: channel.Id, CredentialIDs: []int{first.Id}, Status: common.ChannelStatusEnabled, ExpectedRev: revision,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), revision)
	require.NoError(t, db.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	assert.NotContains(t, stored.ChannelInfo.MultiKeyStatusList, 0)

	_, err = UpdateChannelCredentialStatuses(db, ChannelCredentialStatusUpdate{
		ChannelID: channel.Id, All: true, Status: common.ChannelStatusEnabled, ExpectedRev: 1,
	})
	assert.ErrorIs(t, err, ErrChannelCredentialRevisionConflict)
}

func TestResolveTaskChannelAccessUsesStableCredentialAfterReorderAndRemoval(t *testing.T) {
	db := setupChannelCredentialSQLite(t)
	channel := &Channel{
		Key:  "key-a\nkey-b",
		Name: "async credential access",
		ChannelInfo: ChannelInfo{
			IsMultiKey: true,
		},
	}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, MigrateChannelCredentialStore(db))
	credentials, err := ListChannelCredentials(db, channel.Id)
	require.NoError(t, err)
	var keyA ChannelCredential
	for _, credential := range credentials {
		if credential.Fingerprint == ChannelCredentialFingerprint("key-a") {
			keyA = credential
			break
		}
	}
	require.NotZero(t, keyA.Id)
	proxyMode, proxyURL, err := NormalizeChannelCredentialProxy(ChannelCredentialProxyModeCustom, "http://proxy-key-a.example:8080")
	require.NoError(t, err)
	require.NoError(t, db.Model(&ChannelCredential{}).Where("id = ?", keyA.Id).Updates(map[string]interface{}{
		"proxy_mode": proxyMode,
		"proxy_url":  proxyURL,
	}).Error)

	// Reordering changes the legacy position but must not change the durable ID.
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Update("key", "key-b\nkey-a").Error)
	require.NoError(t, MigrateLegacyChannelCredentialsWithDB(db))
	reordered, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	task := &Task{ChannelId: channel.Id, PrivateData: TaskPrivateData{ChannelCredentialID: keyA.Id}}
	key, proxy := ResolveTaskChannelAccess(task, reordered)
	assert.Equal(t, "key-a", key)
	assert.Equal(t, proxyURL, proxy)

	// Removed credentials stay addressable for in-flight tasks and retain their
	// configured proxy instead of falling back to a different active key.
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Update("key", "key-b").Error)
	require.NoError(t, MigrateLegacyChannelCredentialsWithDB(db))
	removed, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	key, proxy = ResolveTaskChannelAccess(task, removed)
	assert.Equal(t, "key-a", key)
	assert.Equal(t, proxyURL, proxy)
}

func mustChannelCredentialRevision(t *testing.T, db *gorm.DB, channelID int) int64 {
	t.Helper()
	revision, err := GetChannelCredentialRevision(db, channelID)
	require.NoError(t, err)
	return revision
}

func TestChannelCredentialSchemaConfiguredDatabases(t *testing.T) {
	tests := []struct {
		name      string
		env       string
		dialector func(string) gorm.Dialector
		dbType    common.DatabaseType
	}{
		{
			name: "mysql", env: "TEST_MYSQL_DSN", dbType: common.DatabaseTypeMySQL,
			dialector: func(dsn string) gorm.Dialector { return mysql.Open(dsn) },
		},
		{
			name: "postgres", env: "TEST_POSTGRES_DSN", dbType: common.DatabaseTypePostgreSQL,
			dialector: func(dsn string) gorm.Dialector {
				return postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(test.env))
			if dsn == "" {
				t.Skip(test.env + " is not configured")
			}
			previousType := common.MainDatabaseType()
			common.SetMainDatabaseType(test.dbType)
			t.Cleanup(func() { common.SetMainDatabaseType(previousType) })
			db, err := gorm.Open(test.dialector(dsn), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			require.NoError(t, db.AutoMigrate(&ChannelCredential{}, &ChannelCredentialRevision{}))
		})
	}
}
