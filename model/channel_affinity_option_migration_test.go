package model

import (
	"strings"
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

func TestMigrateChannelAffinityDefaultRulesSupportsEveryHistoricalDefault(t *testing.T) {
	names := []string{
		"original-rules",
		"templates-added",
		"skip-retry-enabled",
		"model-name-field-added",
		"codex-headers-expanded",
	}
	require.Len(t, historicalChannelAffinityRuleValues, len(names))

	for index, historicalValue := range historicalChannelAffinityRuleValues {
		t.Run(names[index], func(t *testing.T) {
			db := useChannelAffinityMigrationDB(t)
			require.NoError(t, db.Create(&Option{Key: channelAffinityRulesOptionKey, Value: historicalValue}).Error)

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
	value := `[{"name":"my custom Codex rule"}]`
	require.NoError(t, db.Create(&Option{Key: channelAffinityRulesOptionKey, Value: value}).Error)

	require.NoError(t, MigrateChannelAffinityDefaultRules())
	assert.Equal(t, value, requireOptionValue(t, db, channelAffinityRulesOptionKey))
}

func TestMigrateChannelAffinityDefaultRulesPreservesMalformedRules(t *testing.T) {
	db := useChannelAffinityMigrationDB(t)
	value := `[{"name":"codex cli trace"}]`
	require.NoError(t, db.Create(&Option{Key: channelAffinityRulesOptionKey, Value: value}).Error)

	require.NoError(t, MigrateChannelAffinityDefaultRules())
	assert.Equal(t, value, requireOptionValue(t, db, channelAffinityRulesOptionKey))
}

func TestMigrateChannelAffinityDefaultRulesPreservesNonHistoricalNumberLexemes(t *testing.T) {
	tests := map[string]string{
		"decimal":   `"ttl_seconds":0.0`,
		"exponent":  `"ttl_seconds":0e0`,
		"negative":  `"ttl_seconds":-0`,
		"duplicate": `"ttl_seconds":0,"ttl_seconds":0`,
	}
	for name, replacement := range tests {
		t.Run(name, func(t *testing.T) {
			db := useChannelAffinityMigrationDB(t)
			value := strings.Replace(
				historicalChannelAffinityRuleValues[0],
				`"ttl_seconds":0`,
				replacement,
				1,
			)
			require.NoError(t, db.Create(&Option{Key: channelAffinityRulesOptionKey, Value: value}).Error)

			require.NoError(t, MigrateChannelAffinityDefaultRules())
			assert.Equal(t, value, requireOptionValue(t, db, channelAffinityRulesOptionKey))
		})
	}
}

func TestReplaceChannelAffinityDefaultRulesCASRejectsStaleValue(t *testing.T) {
	db := useChannelAffinityMigrationDB(t)
	require.NoError(t, db.Create(&Option{
		Key:   channelAffinityRulesOptionKey,
		Value: historicalChannelAffinityRuleValues[0],
	}).Error)
	var stale Option
	require.NoError(t, db.Where(&Option{Key: channelAffinityRulesOptionKey}).First(&stale).Error)

	const adminValue = `[{"name":"admin saved while migration was running"}]`
	require.NoError(t, db.Model(&Option{}).
		Where(&Option{Key: channelAffinityRulesOptionKey}).
		Update("value", adminValue).Error)

	replaced, err := replaceChannelAffinityDefaultRulesIfUnchanged(db, stale, "migrated")
	require.NoError(t, err)
	assert.False(t, replaced)
	assert.Equal(t, adminValue, requireOptionValue(t, db, channelAffinityRulesOptionKey))
}
