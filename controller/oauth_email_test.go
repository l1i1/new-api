package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAutoBindGitHubEmailFillsMissingEmailWithoutTakingExistingAddress(t *testing.T) {
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
	})

	missingEmailUser := &model.User{
		Username: "github-user",
		Password: "password",
		Status:   common.UserStatusEnabled,
		AffCode:  "ghbind1",
		GitHubId: "12345",
	}
	require.NoError(t, db.Create(missingEmailUser).Error)

	autoBindGitHubEmail(missingEmailUser, &oauth.OAuthUser{Email: "GitHub@Example.com"})
	var bound model.User
	require.NoError(t, db.First(&bound, missingEmailUser.Id).Error)
	require.Equal(t, "github@example.com", bound.Email)
	require.Equal(t, "12345", bound.GitHubId)

	conflictingUser := &model.User{
		Username: "github-conflict",
		Password: "password",
		Status:   common.UserStatusEnabled,
		AffCode:  "ghbind2",
	}
	require.NoError(t, db.Create(conflictingUser).Error)
	autoBindGitHubEmail(conflictingUser, &oauth.OAuthUser{Email: "github@example.com"})

	var unchanged model.User
	require.NoError(t, db.First(&unchanged, conflictingUser.Id).Error)
	require.Empty(t, unchanged.Email)
}
