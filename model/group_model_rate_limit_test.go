package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNormalizeGroupModelRateLimitsTrimsAndSortsRules(t *testing.T) {
	rules, err := NormalizeGroupModelRateLimits([]GroupModelRateLimit{
		{GroupName: " vip ", ModelName: " z-model ", WindowSeconds: 3600, MaxRequests: 100, Enabled: true},
		{GroupName: "default", ModelName: "a-model", WindowSeconds: 60, MaxRequests: 10, Enabled: true},
	})
	require.NoError(t, err)
	require.Len(t, rules, 2)
	assert.Equal(t, "default", rules[0].GroupName)
	assert.Equal(t, "a-model", rules[0].ModelName)
	assert.Equal(t, "vip", rules[1].GroupName)
	assert.Equal(t, "z-model", rules[1].ModelName)
	assert.Zero(t, rules[0].Id)
}

func TestNormalizeGroupModelRateLimitsRejectsDuplicateAndInvalidRules(t *testing.T) {
	_, err := NormalizeGroupModelRateLimits([]GroupModelRateLimit{
		{GroupName: "default", ModelName: "model", WindowSeconds: 60, MaxRequests: 1},
		{GroupName: " default ", ModelName: "model", WindowSeconds: 60, MaxRequests: 2},
	})
	require.Error(t, err)

	_, err = NormalizeGroupModelRateLimits([]GroupModelRateLimit{{
		GroupName: "default", ModelName: "model", WindowSeconds: MaxGroupModelRateLimitWindowSeconds + 1, MaxRequests: 1,
	}})
	require.Error(t, err)

	_, err = NormalizeGroupModelRateLimits([]GroupModelRateLimit{{
		GroupName: "", ModelName: "model", WindowSeconds: 60, MaxRequests: 1,
	}})
	require.Error(t, err)

	_, err = NormalizeGroupModelRateLimits([]GroupModelRateLimit{{
		GroupName: "default", ModelName: "", WindowSeconds: 60, MaxRequests: 1,
	}})
	require.Error(t, err)
}

func TestReplaceGroupModelRateLimitsReplacesRulesAtomically(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&GroupModelRateLimit{}))
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	first, err := ReplaceGroupModelRateLimits([]GroupModelRateLimit{{
		GroupName: "default", ModelName: "model-a", WindowSeconds: 60, MaxRequests: 10, Enabled: true,
	}})
	require.NoError(t, err)
	require.Len(t, first, 1)

	second, err := ReplaceGroupModelRateLimits([]GroupModelRateLimit{{
		GroupName: "vip", ModelName: "model-b", WindowSeconds: 3600, MaxRequests: 100, Enabled: false,
	}})
	require.NoError(t, err)
	require.Len(t, second, 1)
	persisted, err := GetGroupModelRateLimits()
	require.NoError(t, err)
	require.Len(t, persisted, 1)
	assert.Equal(t, "vip", persisted[0].GroupName)
	assert.Equal(t, "model-b", persisted[0].ModelName)
	assert.False(t, persisted[0].Enabled)
}
