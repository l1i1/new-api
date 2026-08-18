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
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &ChannelCredential{}, &ChannelCredentialRevision{}))
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

func TestRecordChannelCredentialTestPersistsFailureDetails(t *testing.T) {
	db := setupChannelCredentialSQLite(t)
	credential, err := NewChannelCredential(11, 0, "test-credential-value")
	require.NoError(t, err)
	require.NoError(t, db.Create(credential).Error)

	require.NoError(t, RecordChannelCredentialTest(
		db, 11, credential.Id, "failed", 123, 429,
		"rate_limit_exceeded", "rate_limited", "upstream rejected the request",
	))

	var stored ChannelCredential
	require.NoError(t, db.First(&stored, credential.Id).Error)
	assert.Equal(t, "failed", stored.LastTestStatus)
	assert.Equal(t, int64(123), stored.LastTestLatencyMs)
	assert.Equal(t, 429, stored.LastTestHTTPStatus)
	assert.Equal(t, "rate_limit_exceeded", stored.LastTestErrorCode)
	assert.Equal(t, "rate_limited", stored.LastTestErrorClass)
	assert.Equal(t, "upstream rejected the request", stored.LastTestErrorMessage)

	public := stored.PublicView()
	assert.Equal(t, 429, public.LastTestHTTPStatus)
	assert.Equal(t, "upstream rejected the request", public.LastTestErrorMessage)
}

func TestChannelCredentialLastTestErrorMessageUsesPortableColumnType(t *testing.T) {
	db := setupChannelCredentialSQLite(t)

	columns, err := db.Migrator().ColumnTypes(&ChannelCredential{})
	require.NoError(t, err)
	for _, column := range columns {
		if !strings.EqualFold(column.Name(), "last_test_error_message") {
			continue
		}

		assert.Contains(t, strings.ToLower(column.DatabaseTypeName()), "char")
		return
	}

	t.Fatal("last_test_error_message column was not migrated")
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

func TestParseMultiKeyCredentialTextPairsOnlyStrictProxyLines(t *testing.T) {
	inputs, err := ParseMultiKeyCredentialText("key-a\n\nhttp://proxy-user:proxy-password@proxy-a.example.test:8080\nkey-b\nnot-a-proxy\nsocks5h://proxy-c.example.test:1080\n")
	require.NoError(t, err)
	require.Equal(t, []ChannelCredentialInput{
		{Secret: "key-a", ProxyURL: "http://proxy-user:proxy-password@proxy-a.example.test:8080"},
		{Secret: "key-b"},
		{Secret: "not-a-proxy", ProxyURL: "socks5h://proxy-c.example.test:1080"},
	}, inputs)
}

func TestStructuredMultiKeyCredentialsRejectEmbeddedLineSeparators(t *testing.T) {
	for _, secret := range []string{
		"key-a\nkey-b",
		"key-a\rkey-b",
		"key-a\u0085key-b",
		"key-a\u2028key-b",
		"key-a\u2029key-b",
	} {
		_, err := NormalizeMultiKeyCredentialInputs([]ChannelCredentialInput{{Secret: secret}})
		assert.ErrorIs(t, err, ErrChannelCredentialInvalid)
	}

	db := setupChannelCredentialSQLite(t)
	channel := &Channel{
		Name:   "reject multiline secret",
		Models: "test-model",
		Group:  "default",
		ChannelInfo: ChannelInfo{
			IsMultiKey: true,
		},
	}
	err := channel.InsertWithCredentialInputs([]ChannelCredentialInput{{Secret: "key-a\nkey-b"}})
	assert.ErrorIs(t, err, ErrChannelCredentialInvalid)
	var count int64
	require.NoError(t, db.Model(&Channel{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestStructuredMultiKeyCredentialsPersistAndReconcileProxyPairs(t *testing.T) {
	db := setupChannelCredentialSQLite(t)
	channel := &Channel{
		Name:   "structured credential channel",
		Models: "test-model",
		Group:  "default",
		ChannelInfo: ChannelInfo{
			IsMultiKey: true,
		},
	}
	require.NoError(t, channel.InsertWithCredentialInputs([]ChannelCredentialInput{
		{Secret: "key-a", ProxyURL: "http://proxy-a.example.test:8080"},
		{Secret: "key-b"},
	}))
	require.Equal(t, "key-a\nkey-b", channel.Key)

	credentials, err := ListChannelCredentials(db, channel.Id)
	require.NoError(t, err)
	require.Len(t, credentials, 2)
	assert.Equal(t, ChannelCredentialProxyModeCustom, credentials[0].ProxyMode)
	assert.Equal(t, "http://proxy-a.example.test:8080", credentials[0].ProxyURL)
	assert.Equal(t, ChannelCredentialProxyModeInherit, credentials[1].ProxyMode)
	firstID := credentials[0].Id
	_, _, _, err = UpdateChannelCredentialProxy(db, channel.Id, credentials[1].Id, ChannelCredentialProxyModeDirect, "", 0)
	require.NoError(t, err)

	oldProxies, newProxies, err := channel.UpdateWithCredentialInputs([]ChannelCredentialInput{
		{Secret: "key-c", ProxyURL: "socks5://proxy-c.example.test:1080"},
	}, "append")
	require.NoError(t, err)
	assert.Contains(t, oldProxies, "http://proxy-a.example.test:8080")
	assert.Contains(t, newProxies, "http://proxy-a.example.test:8080")
	assert.Contains(t, newProxies, "socks5://proxy-c.example.test:1080")
	require.Equal(t, "key-a\nkey-b\nkey-c", channel.Key)

	credentials, err = ListChannelCredentials(db, channel.Id)
	require.NoError(t, err)
	require.Len(t, credentials, 3)
	assert.Equal(t, firstID, credentials[0].Id)
	assert.Equal(t, "http://proxy-a.example.test:8080", credentials[0].ProxyURL)
	assert.Equal(t, ChannelCredentialProxyModeDirect, credentials[1].ProxyMode)

	oldProxies, _, err = channel.UpdateWithCredentialInputs([]ChannelCredentialInput{
		{Secret: "key-b"},
		{Secret: "key-c", ProxyURL: "socks5://proxy-c.example.test:1080"},
	}, "replace")
	require.NoError(t, err)
	assert.Contains(t, oldProxies, "http://proxy-a.example.test:8080")
	require.Equal(t, "key-b\nkey-c", channel.Key)

	credentials, err = ListChannelCredentials(db, channel.Id)
	require.NoError(t, err)
	require.Len(t, credentials, 3)
	bySecret := make(map[string]ChannelCredential, len(credentials))
	for _, credential := range credentials {
		bySecret[credential.Secret] = credential
	}
	assert.Equal(t, ChannelCredentialProxyModeInherit, bySecret["key-b"].ProxyMode)
	assert.Empty(t, bySecret["key-b"].ProxyURL)
	assert.Equal(t, 0, bySecret["key-b"].Position)
	assert.Equal(t, 1, bySecret["key-c"].Position)
	assert.Equal(t, "socks5://proxy-c.example.test:1080", bySecret["key-c"].ProxyURL)
	assert.Equal(t, -firstID, bySecret["key-a"].Position)

	revealed, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "key-b\nkey-c\nsocks5://proxy-c.example.test:1080", FormatChannelKeyForReveal(revealed))
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
