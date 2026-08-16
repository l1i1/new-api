package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useChannelAffinityMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
	return db
}

func TestMigrateChannelAffinityDefaultRulesSupportsHistoricalCodexHeaders(t *testing.T) {
	for index, historicalRules := range operation_setting.HistoricalDefaultChannelAffinityRules() {
		t.Run(map[int]string{0: "full-codex-headers", 1: "trimmed-codex-headers"}[index], func(t *testing.T) {
			db := useChannelAffinityMigrationDB(t)
			value, err := common.Marshal(historicalRules)
			require.NoError(t, err)
			require.NoError(t, db.Create(&Option{Key: channelAffinityRulesOptionKey, Value: string(value)}).Error)

			require.NoError(t, MigrateChannelAffinityDefaultRules())
			migrated := requireOptionValue(t, db, channelAffinityRulesOptionKey)
			expected, err := common.Marshal(operation_setting.GetChannelAffinitySetting().Rules)
			require.NoError(t, err)
			assert.JSONEq(t, string(expected), migrated)

			// A second startup must not rewrite the already-expanded value.
			require.NoError(t, MigrateChannelAffinityDefaultRules())
			assert.Equal(t, migrated, requireOptionValue(t, db, channelAffinityRulesOptionKey))
		})
	}
}

func TestMigrateChannelAffinityDefaultRulesPreservesCustomRules(t *testing.T) {
	db := useChannelAffinityMigrationDB(t)
	historical := operation_setting.HistoricalDefaultChannelAffinityRules()[0]
	historical[0].Name = "my custom Codex rule"
	historical[0].SkipRetryOnFailure = false
	value, err := common.Marshal(historical)
	require.NoError(t, err)
	require.NoError(t, db.Create(&Option{Key: channelAffinityRulesOptionKey, Value: string(value)}).Error)

	require.NoError(t, MigrateChannelAffinityDefaultRules())
	assert.Equal(t, string(value), requireOptionValue(t, db, channelAffinityRulesOptionKey))
}

func TestMigrateChannelAffinityDefaultRulesPreservesMalformedRules(t *testing.T) {
	db := useChannelAffinityMigrationDB(t)
	value := `[{"name":"codex cli trace"}]`
	require.NoError(t, db.Create(&Option{Key: channelAffinityRulesOptionKey, Value: value}).Error)

	require.NoError(t, MigrateChannelAffinityDefaultRules())
	assert.Equal(t, value, requireOptionValue(t, db, channelAffinityRulesOptionKey))
}
