package model

import (
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// The capability table must migrate and behave identically on SQLite, MySQL and
// PostgreSQL. The portability decisions this exercises are deliberate:
//
//   - timestamps are int64 Unix seconds, not a dialect-specific time type;
//   - the composite unique key uses bounded varchar lengths so it fits inside
//     the MySQL index prefix limit;
//   - the CAS is a conditional UPDATE, which every dialect supports, instead of
//     SELECT ... FOR UPDATE, which SQLite silently drops;
//   - migration runs twice to prove a second start is a no-op rather than an
//     error (no destructive ALTER COLUMN).
//
// SQLite always runs. MySQL and PostgreSQL run only when their isolated test
// DSNs are configured, so this file is the executable half of the three-database
// gate: run it where those servers exist, with
//
//	TEST_FITCAP_MYSQL_DSN=... TEST_FITCAP_POSTGRES_DSN=... go test ./model/ -run TestFitCapabilityDatabaseMatrix
func TestFitCapabilityDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct {
		name common.DatabaseType
		env  string
	}{
		{common.DatabaseTypeSQLite, ""},
		{common.DatabaseTypeMySQL, "TEST_FITCAP_MYSQL_DSN"},
		{common.DatabaseTypePostgreSQL, "TEST_FITCAP_POSTGRES_DSN"},
	} {
		t.Run(string(dialect.name), func(t *testing.T) {
			var driver gorm.Dialector = sqlite.Open(":memory:")
			if dialect.env != "" {
				dsn := os.Getenv(dialect.env)
				if dsn == "" {
					t.Skip(dialect.env + " is not configured")
				}
				if dialect.name == common.DatabaseTypeMySQL {
					driver = mysql.Open(dsn)
				} else {
					driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = sqlDB.Close() })

			previousDB := DB
			DB = db
			ResetFitCapabilityIndexForTest()
			t.Cleanup(func() {
				DB = previousDB
				ResetFitCapabilityIndexForTest()
			})

			// Fresh install, then a second start: the migration must be a no-op.
			require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &ChannelFitCapability{}))
			require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &ChannelFitCapability{}))
			require.True(t, db.Migrator().HasTable(&ChannelFitCapability{}))

			// The unique key must actually exist, not merely be declared.
			require.NoError(t, db.Create(&Channel{Id: 11, Name: "ch", Status: common.ChannelStatusEnabled, Group: "default", Models: "kimi-k3", Key: "sk"}).Error)
			first, err := ApplyChannelFitCapability(FitCapabilityWrite{
				ChannelId: 11, Family: "KIMI-K3 ", Model: " kimi-k3", Behavior: "Tools.Dynamic_Names",
				Supported: true, Source: FitCapabilitySourceSuite, At: 1_700_000_000,
				ExpectedRevision: 0,
			}, 1_700_000_100)
			require.NoError(t, err)
			require.Equal(t, int64(1), first.Revision)
			require.Equal(t, "kimi-k3", first.Family, "keys must be normalized before they reach the unique index")
			require.Equal(t, "kimi-k3", first.Model)
			require.Equal(t, "tools.dynamic_names", first.Behavior)

			duplicate := buildFitCapabilityRow(FitCapabilityWrite{
				ChannelId: 11, Family: "kimi-k3", Model: "kimi-k3", Behavior: "tools.dynamic_names",
				Supported: false, Source: FitCapabilitySourceSuite,
			}, 1_700_000_200, 1)
			require.Error(t, db.Create(duplicate).Error, "the unique key must reject a second row for the same mark")

			// CAS: correct revision advances, stale revision is rejected.
			updated, err := ApplyChannelFitCapability(FitCapabilityWrite{
				ChannelId: 11, Family: "kimi-k3", Model: "kimi-k3", Behavior: "tools.dynamic_names",
				// A fresh timestamp: the index derives freshness from the row, so a
				// fixture dated in the past would legitimately read as suite_stale.
				Supported: true, Source: FitCapabilitySourceSuite, At: common.GetTimestamp(),
				ExpectedRevision: 1,
			}, 1_700_000_400)
			require.NoError(t, err)
			require.Equal(t, int64(2), updated.Revision)

			_, err = ApplyChannelFitCapability(FitCapabilityWrite{
				ChannelId: 11, Family: "kimi-k3", Model: "kimi-k3", Behavior: "tools.dynamic_names",
				Supported: false, Source: FitCapabilitySourceSuite, ExpectedRevision: 1,
			}, 1_700_000_500)
			require.True(t, IsFitCapabilityConflict(err), "a stale revision must be a conflict on every dialect")

			// The index query and the cleanup delete must be portable too.
			InitFitCapabilityIndex()
			require.True(t, FitCapabilityIndexReady())
			require.True(t, ChannelSatisfiesFitMarks(11, FitMarkRequirement{Model: "kimi-k3", Marks: []string{"tools.dynamic_names"}}))

			require.NoError(t, (&Channel{Id: 11}).Delete())
			rows, err := ListChannelFitCapabilities(11)
			require.NoError(t, err)
			require.Empty(t, rows)
		})
	}
}
