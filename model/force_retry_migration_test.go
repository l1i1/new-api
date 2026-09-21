package model

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useForceRetryMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	t.Cleanup(func() { DB = previousDB })
	return db
}

func requireOptionRow(t *testing.T, db *gorm.DB, key string) (Option, bool) {
	t.Helper()
	var option Option
	err := db.Where(&Option{Key: key}).First(&option).Error
	if err == gorm.ErrRecordNotFound {
		return Option{}, false
	}
	require.NoError(t, err)
	return option, true
}

// TestMigrateForceRetryStatusCodesMergesIntoFailoverList pins the cutover for
// the retired force-retry field: its codes join the failover list and the row
// is removed, so the page edits one field and a later sync cannot resurrect a
// code an operator has since deleted.
func TestMigrateForceRetryStatusCodesMergesIntoFailoverList(t *testing.T) {
	db := useForceRetryMigrationDB(t)
	require.NoError(t, db.Create(&Option{Key: "AutomaticRetryStatusCodes", Value: "500-599"}).Error)
	require.NoError(t, db.Create(&Option{Key: "ForceRetryStatusCodes", Value: "400,422"}).Error)

	require.NoError(t, MigrateRetiredFrontendOptions())

	merged, ok := requireOptionRow(t, db, "AutomaticRetryStatusCodes")
	require.True(t, ok)
	require.Equal(t, "400,422,500-599", merged.Value)

	_, stillThere := requireOptionRow(t, db, "ForceRetryStatusCodes")
	require.False(t, stillThere, "the retired row must be removed so the merge happens once")

	// A second startup must not change or recreate anything.
	require.NoError(t, MigrateRetiredFrontendOptions())
	merged, _ = requireOptionRow(t, db, "AutomaticRetryStatusCodes")
	require.Equal(t, "400,422,500-599", merged.Value)
}

// TestMigrateForceRetryStatusCodesKeepsCompiledDefaults covers the install that
// never saved the failover list: with no persisted row the effective value is
// the compiled default, so the union must start from it instead of writing the
// legacy codes alone.
func TestMigrateForceRetryStatusCodesKeepsCompiledDefaults(t *testing.T) {
	db := useForceRetryMigrationDB(t)
	require.NoError(t, db.Create(&Option{Key: "ForceRetryStatusCodes", Value: "408"}).Error)

	require.NoError(t, MigrateRetiredFrontendOptions())

	merged, ok := requireOptionRow(t, db, "AutomaticRetryStatusCodes")
	require.True(t, ok)
	expected, err := operation_setting.MergeRetryStatusCodes(
		operation_setting.AutomaticRetryStatusCodesToString(),
		"408",
	)
	require.NoError(t, err)
	require.Equal(t, expected, merged.Value)
	require.Contains(t, merged.Value, "100-199", "the compiled defaults must survive the merge")
}

// TestMigrateForceRetryStatusCodesDropsBlankRow keeps the migration a no-op for
// the common shape an operator leaves behind after clearing the field.
func TestMigrateForceRetryStatusCodesDropsBlankRow(t *testing.T) {
	db := useForceRetryMigrationDB(t)
	require.NoError(t, db.Create(&Option{Key: "ForceRetryStatusCodes", Value: "  "}).Error)

	require.NoError(t, MigrateRetiredFrontendOptions())

	_, ok := requireOptionRow(t, db, "AutomaticRetryStatusCodes")
	require.False(t, ok, "a blank legacy value must not create the failover row")
	_, stillThere := requireOptionRow(t, db, "ForceRetryStatusCodes")
	require.False(t, stillThere)
}
