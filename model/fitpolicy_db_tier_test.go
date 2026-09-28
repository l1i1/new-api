package model

import (
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFitNarrowingFiltersBeforeTieringOnTheDBPath is the regression test for the
// subtlest failure of retiring the compiled-in predicates.
//
// On the database path the candidate query used to fetch every priority tier and
// narrow afterwards only when the legacy pin was on. Retiring the predicates left
// that flag permanently false, which silently restored the tier-first order: the
// query reduced the set to the top tier, the narrowing then looked for an
// official channel inside that tier alone, found none, and every shape the policy
// pins failed with "no available channel" — a 503 in production rather than a
// narrower but working route. The fix makes an opinion of the policy's own
// trigger the same order the pin used to.
//
// The official channel deliberately sits at the lowest priority, because that is
// the arrangement that makes tier-first fail and it is the real one: resellers
// carry the traffic and the vendor endpoint is the fallback. The memory-cache
// path never had this defect — it narrows before tiering regardless of the pin —
// and both sites run this path.
func TestFitNarrowingFiltersBeforeTieringOnTheDBPath(t *testing.T) {
	previousRedis := common.RedisEnabled
	previousMemoryCache := common.MemoryCacheEnabled
	previousPath := common.SQLitePath
	previousDB := DB
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	common.SQLitePath = filepath.Join(t.TempDir(), "fit-tier.db")
	require.NoError(t, InitDB())
	require.NoError(t, DB.AutoMigrate(&Channel{}, &Ability{}))
	testDB := DB
	t.Cleanup(func() {
		if sqlDB, err := testDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		DB = previousDB
		common.RedisEnabled = previousRedis
		common.MemoryCacheEnabled = previousMemoryCache
		common.SQLitePath = previousPath
	})

	const group = "fit-tier"
	high := int64(50)
	low := int64(1)
	reseller := Channel{Id: 9001, Type: 1, Name: "fit-tier-reseller", Status: 1, Models: "kimi-k3", Group: group, Priority: &high}
	official := Channel{Id: 9002, Type: 25, Name: "fit-tier-official", Status: 1, Models: "kimi-k3", Group: group, Priority: &low}
	require.NoError(t, DB.Create(&reseller).Error)
	require.NoError(t, DB.Create(&official).Error)
	require.NoError(t, DB.Create(&Ability{Group: group, Model: "kimi-k3", ChannelId: 9001, Enabled: true, Priority: &high}).Error)
	require.NoError(t, DB.Create(&Ability{Group: group, Model: "kimi-k3", ChannelId: 9002, Enabled: true, Priority: &low}).Error)

	// An opinion of the policy's own, with every mark satisfied, so the only thing
	// that can empty the candidate set is the order the two steps run in.
	filter := &FitChannelFilter{
		Requirement: fitpolicy.Requirement{
			Family: "kimi-k3",
			Model:  "kimi-k3",
			Marks:  []string{fitpolicy.BehaviorToolsChoiceSemantics},
		},
		MarkSatisfied: func(int) bool { return true },
	}

	channel, err := GetChannelWithBlockedChannelsPinnedWithFit(group, "kimi-k3", 0, "", nil, false, false, filter)
	require.NoError(t, err)
	require.NotNil(t, channel,
		"the official channel sits below the top tier; tiering before narrowing hides it and the request fails")
	assert.Equal(t, 9002, channel.Id)

	// The same call without an opinion must keep the ordinary tier-first result, or
	// the fix would have changed unpinned routing.
	plain, err := GetChannelWithBlockedChannelsPinnedWithFit(group, "kimi-k3", 0, "", nil, false, false, nil)
	require.NoError(t, err)
	require.NotNil(t, plain)
	assert.Equal(t, 9001, plain.Id, "an unpinned request still takes the top tier")
}
