package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupGroupAccessPolicyModelTest(t *testing.T) {
	t.Helper()
	previousDB := DB
	previousRedisEnabled := common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&GroupAccessPolicy{}))
	DB = db
	common.RedisEnabled = false
	groupAccessPolicyLocalCache.Delete("default")
	t.Cleanup(func() {
		groupAccessPolicyLocalCache.Delete("default")
		DB = previousDB
		common.RedisEnabled = previousRedisEnabled
	})
}

func TestNormalizeGroupAccessPolicyDeduplicatesAndSorts(t *testing.T) {
	policy, err := NormalizeGroupAccessPolicy(GroupAccessPolicy{
		GroupName:                 " default ",
		BlockedChannelIDs:         GroupAccessPolicyIntList{9, 2, 9, 1},
		BlockedModels:             GroupAccessPolicyStringList{" z-model ", "a-model", "a-model"},
		BlockedGroups:             GroupAccessPolicyStringList{" vip ", "default", "vip"},
		ContentModerationDisabled: true,
		Id:                        99,
		CreatedAt:                 100,
		UpdatedAt:                 200,
	})
	require.NoError(t, err)
	assert.Equal(t, "default", policy.GroupName)
	assert.Equal(t, GroupAccessPolicyIntList{1, 2, 9}, policy.BlockedChannelIDs)
	assert.Equal(t, GroupAccessPolicyStringList{"a-model", "z-model"}, policy.BlockedModels)
	assert.Equal(t, GroupAccessPolicyStringList{"default", "vip"}, policy.BlockedGroups)
	assert.True(t, policy.ContentModerationDisabled)
	assert.Zero(t, policy.Id)
	assert.Zero(t, policy.CreatedAt)
	assert.Zero(t, policy.UpdatedAt)
}

func TestReplaceGroupAccessPolicyIsAtomicAndRefreshesLocalCache(t *testing.T) {
	setupGroupAccessPolicyModelTest(t)

	saved, err := ReplaceGroupAccessPolicy(GroupAccessPolicy{
		GroupName:         "default",
		BlockedChannelIDs: GroupAccessPolicyIntList{7},
		BlockedModels:     GroupAccessPolicyStringList{"model-a"},
		BlockedGroups:     GroupAccessPolicyStringList{"vip"},
	})
	require.NoError(t, err)
	assert.Equal(t, GroupAccessPolicyIntList{7}, saved.BlockedChannelIDs)

	cached, err := GetCachedGroupAccessPolicy("default")
	require.NoError(t, err)
	assert.Equal(t, saved.BlockedModels, cached.BlockedModels)
	assert.True(t, cached.IsChannelBlocked(7))

	_, err = ReplaceGroupAccessPolicy(GroupAccessPolicy{
		GroupName:         "default",
		BlockedChannelIDs: GroupAccessPolicyIntList{0},
	})
	require.Error(t, err)
	current, err := GetGroupAccessPolicy("default")
	require.NoError(t, err)
	assert.Equal(t, GroupAccessPolicyIntList{7}, current.BlockedChannelIDs)
}

func TestReplaceGroupAccessPolicyRejectsUnknownTargetGroup(t *testing.T) {
	setupGroupAccessPolicyModelTest(t)
	_, err := ReplaceGroupAccessPolicy(GroupAccessPolicy{
		GroupName:     "default",
		BlockedGroups: GroupAccessPolicyStringList{"does-not-exist"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestGroupAccessPolicyBlocksModelAliases(t *testing.T) {
	policy := GroupAccessPolicy{
		BlockedModels: GroupAccessPolicyStringList{"gpt-5.5", "gemini-2.5-flash-openai-compact"},
	}
	assert.True(t, policy.BlocksModel("gpt-5.5-openai-compact"))
	assert.True(t, policy.BlocksModel("gpt-5.5"))
	assert.True(t, policy.BlocksModel("gemini-2.5-flash"))
	assert.False(t, policy.BlocksModel("gpt-5.4"))
}

func TestGroupAccessPolicyBlocksExactThinkingModels(t *testing.T) {
	policy := GroupAccessPolicy{
		BlockedModels: GroupAccessPolicyStringList{"gemini-2.5-flash-thinking-001"},
	}
	assert.True(t, policy.BlocksModel("gemini-2.5-flash-thinking-001"))
	assert.False(t, policy.BlocksModel("gemini-2.5-flash-thinking-002"))
}

func TestGroupAccessPolicyModelsMatchCompactAliases(t *testing.T) {
	assert.True(t, GroupAccessPolicyModelsMatch("gpt-5.5", "gpt-5.5-openai-compact"))
	assert.True(t, GroupAccessPolicyModelsMatch("gemini-2.5-flash-openai-compact", "gemini-2.5-flash"))
	assert.False(t, GroupAccessPolicyModelsMatch("gpt-5.4", "gpt-5.5"))
}
