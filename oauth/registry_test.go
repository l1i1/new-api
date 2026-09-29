package oauth

import (
	"maps"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The registry is process memory that the login path reads, and the epoch reload
// hook calls LoadCustomProviders on every configuration change, so a failed read
// there must not be able to empty it.
func newOAuthRegistryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.CustomOAuthProvider{}))

	previousDB := model.DB
	model.DB = db
	mu.Lock()
	previousProviders := maps.Clone(providers)
	previousSlugs := maps.Clone(customProviderSlugs)
	previousConflicts := maps.Clone(customProviderConflicts)
	mu.Unlock()
	t.Cleanup(func() {
		model.DB = previousDB
		mu.Lock()
		providers = previousProviders
		customProviderSlugs = previousSlugs
		customProviderConflicts = previousConflicts
		mu.Unlock()
		_ = sqlDB.Close()
	})
	return db
}

func seedCustomProvider(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Create(&model.CustomOAuthProvider{
		Id:           1,
		Name:         "Acme SSO",
		Slug:         "acme",
		Enabled:      true,
		ClientId:     "client-id",
		ClientSecret: "client-secret",
	}).Error)
}

func TestLoadCustomProvidersRegistersCommittedProviders(t *testing.T) {
	db := newOAuthRegistryTestDB(t)
	seedCustomProvider(t, db)

	require.NoError(t, LoadCustomProviders())

	assert.True(t, IsCustomProvider("acme"))
	assert.True(t, IsProviderRegistered("acme"))
	assert.NotNil(t, GetProvider("acme"))
	assert.Len(t, GetEnabledCustomProviders(), 1)
}

// After a provider is deleted from the database the next load must drop it from
// the registry, or the node keeps accepting logins with a provider an operator
// has removed.
func TestLoadCustomProvidersDropsDeletedProviders(t *testing.T) {
	db := newOAuthRegistryTestDB(t)
	seedCustomProvider(t, db)
	require.NoError(t, LoadCustomProviders())
	require.True(t, IsCustomProvider("acme"))

	require.NoError(t, db.Where("slug = ?", "acme").Delete(&model.CustomOAuthProvider{}).Error)
	require.NoError(t, LoadCustomProviders())

	assert.False(t, IsCustomProvider("acme"))
	assert.Nil(t, GetProvider("acme"))
}

// A failed read used to clear the registry before it read, so one transient
// database error removed every custom provider from the node — and since the
// epoch hook now reloads on every configuration change, that would have become a
// recurring outage rather than a startup-only one.
func TestLoadCustomProvidersKeepsTheRegistryWhenTheReadFails(t *testing.T) {
	db := newOAuthRegistryTestDB(t)
	seedCustomProvider(t, db)
	require.NoError(t, LoadCustomProviders())
	require.True(t, IsCustomProvider("acme"))

	require.NoError(t, db.Migrator().DropTable(&model.CustomOAuthProvider{}))

	require.Error(t, LoadCustomProviders())
	assert.True(t, IsCustomProvider("acme"),
		"a failed read must not unregister the providers that are already loaded")
	assert.NotNil(t, GetProvider("acme"))
}
