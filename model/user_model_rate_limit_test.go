package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNormalizeUserModelRateLimitsTrimsAndSortsRules(t *testing.T) {
	rules, err := NormalizeUserModelRateLimits(7, []UserModelRateLimit{
		{ModelName: " z-model ", WindowSeconds: 3600, MaxRequests: 100, Enabled: true},
		{ModelName: "a-model", WindowSeconds: 60, MaxRequests: 10, Enabled: true},
	})
	require.NoError(t, err)
	require.Len(t, rules, 2)
	assert.Equal(t, "a-model", rules[0].ModelName)
	assert.Equal(t, "z-model", rules[1].ModelName)
	assert.Equal(t, 7, rules[0].UserId)
	assert.Zero(t, rules[0].Id)
}

func TestNormalizeUserModelRateLimitsRejectsDuplicateAndOutOfRangeRules(t *testing.T) {
	_, err := NormalizeUserModelRateLimits(7, []UserModelRateLimit{
		{ModelName: "model", WindowSeconds: 60, MaxRequests: 1},
		{ModelName: " model ", WindowSeconds: 60, MaxRequests: 2},
	})
	require.Error(t, err)

	_, err = NormalizeUserModelRateLimits(7, []UserModelRateLimit{{
		ModelName: "model", WindowSeconds: MaxUserModelRateLimitWindowSeconds + 1, MaxRequests: 1,
	}})
	require.Error(t, err)
}

func TestReplaceUserModelRateLimitsReplacesRulesAtomically(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&UserModelRateLimit{}))
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	first, err := ReplaceUserModelRateLimits(9, []UserModelRateLimit{{
		ModelName: "model-a", WindowSeconds: 60, MaxRequests: 10, Enabled: true,
	}})
	require.NoError(t, err)
	require.Len(t, first, 1)

	second, err := ReplaceUserModelRateLimits(9, []UserModelRateLimit{{
		ModelName: "model-b", WindowSeconds: 3600, MaxRequests: 100, Enabled: false,
	}})
	require.NoError(t, err)
	require.Len(t, second, 1)
	persisted, err := GetUserModelRateLimits(9)
	require.NoError(t, err)
	require.Len(t, persisted, 1)
	assert.Equal(t, "model-b", persisted[0].ModelName)
	assert.False(t, persisted[0].Enabled)
}
