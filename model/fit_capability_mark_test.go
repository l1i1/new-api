package model

import (
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSuiteMarkSatisfiesWhenEveryBindingMatches walks the exact path a request
// takes to decide whether a channel has proven a behaviour: write a mark the way
// the report endpoint does, rebuild the index the way startup does, then ask.
//
// It exists because the two halves are written at different times by different
// code — the report endpoint writes rows, the index build reads them — and a
// mismatch between them makes every channel look unverified while the table looks
// perfect. That failure mode is silent and sends traffic back to the fallback
// set, so it needs a test that spans both halves rather than each alone.
func TestSuiteMarkSatisfiesWhenEveryBindingMatches(t *testing.T) {
	previousRedis := common.RedisEnabled
	previousMemoryCache := common.MemoryCacheEnabled
	previousPath := common.SQLitePath
	previousDB := DB
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	common.SQLitePath = filepath.Join(t.TempDir(), "fit-marks.db")
	require.NoError(t, InitDB())
	require.NoError(t, DB.AutoMigrate(&Channel{}, &ChannelFitCapability{}))
	testDB := DB
	t.Cleanup(func() {
		if sqlDB, err := testDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		DB = previousDB
		ResetFitCapabilityIndexForTest()
		common.RedisEnabled = previousRedis
		common.MemoryCacheEnabled = previousMemoryCache
		common.SQLitePath = previousPath
	})

	require.NoError(t, DB.Create(&Channel{Id: 8, Type: 25, Name: "fit-mark-moonshot", Status: 1, Models: "kimi-k3"}).Error)

	const policyHash = "e08236295404c2c1b55d6c91bab4c9070faa4b704e95d76cf9725c350f53ea60"
	now := common.GetTimestamp()
	_, err := ApplyChannelFitCapability(FitCapabilityWrite{
		ChannelId:        8,
		Family:           "kimi-k3",
		Model:            "kimi-k3",
		Behavior:         "family.whole",
		Supported:        true,
		Source:           FitCapabilitySourceSuite,
		At:               now,
		ExpiresAt:        now + 14*24*60*60,
		PolicyVersion:    1,
		PolicyHash:       policyHash,
		ExpectedRevision: 0,
	}, 0)
	require.NoError(t, err)

	// Deliberately no explicit index build: the look-up has to reach the row by
	// itself. Requiring the caller to have built the index first is exactly the
	// wiring that was missing, and it fails silently — the table looks right while
	// every channel reads as unverified.
	mark, found := LookupFitCapability(8, "kimi-k3", "family.whole")
	require.True(t, found, "a look-up must find a row that was just written, without a manual index build")
	assert.True(t, mark.Supported)
	assert.Equal(t, policyHash, mark.PolicyHash)

	satisfied := ChannelSatisfiesFitMarks(8, FitMarkRequirement{
		Model:        "kimi-k3",
		Marks:        []string{"family.whole"},
		PolicyHash:   policyHash,
		BaselineHash: "",
	})
	assert.True(t, satisfied, "a fresh suite mark under the request's own binding must satisfy")

	// The counterpart that keeps the fallback honest: a different policy must not
	// be vouched for by a measurement taken under this one.
	assert.False(t, ChannelSatisfiesFitMarks(8, FitMarkRequirement{
		Model:      "kimi-k3",
		Marks:      []string{"family.whole"},
		PolicyHash: "0000000000000000000000000000000000000000000000000000000000000000",
	}), "a mark bound to another policy must not satisfy this one")
}
