package model

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// openFitCapabilityTestDB opens a throwaway database with the channel tables
// and, when asked, the capability table. Leaving the capability table out is how
// a test reproduces the designed fail-open state of a node the migration has not
// reached yet.
func openFitCapabilityTestDB(t *testing.T, withCapabilityTable bool) (*gorm.DB, error) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	models := []any{&Channel{}, &Ability{}}
	if withCapabilityTable {
		models = append(models, &ChannelFitCapability{})
	}
	if err := db.AutoMigrate(models...); err != nil {
		return nil, err
	}
	return db, nil
}

// The capability index sits between two locks with opposite requirements.
// Selection reads marks while holding channelSyncLock (read), and building the
// index is a full table scan. Doing the scan with the channel cache held does
// not deadlock, but it stops every channel write for its duration, and because
// a failed build leaves the index unbuilt it repeats per mark per candidate per
// request. These tests pin the two properties that fix it: the build is
// triggered outside the lock, and the read inside the lock can never start one.

// countFitCapabilityQueries registers a gorm callback that counts queries
// against the capability table, so a test can assert how many builds actually
// reached the database instead of inferring it from the result.
func countFitCapabilityQueries(t *testing.T, db *gorm.DB) *atomic.Int64 {
	t.Helper()
	var queries atomic.Int64
	const name = "fit_capability_query_counter"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "channel_fit_capabilities" {
			queries.Add(1)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
	return &queries
}

// TestFitChannelFilterForRequestBuildsTheIndexOutsideSelection is the M1 gate:
// constructing the request's filter is the out-of-lock trigger, so by the time
// the selector runs there is nothing left to build.
func TestFitChannelFilterForRequestBuildsTheIndexOutsideSelection(t *testing.T) {
	setupFitCapabilityDB(t)
	gin.SetMode(gin.TestMode)

	now := common.GetTimestamp()
	_, err := ApplyChannelFitCapability(FitCapabilityWrite{
		ChannelId: 7, Family: "kimi-k3", Model: "kimi-k3", Behavior: "tools.dynamic_names",
		Supported: true, Source: FitCapabilitySourceSuite, At: now, ExpectedRevision: 0,
	}, now)
	require.NoError(t, err)

	ResetFitCapabilityIndexForTest()
	require.False(t, FitCapabilityIndexReady(), "the fixture must start with no index")

	c, _ := gin.CreateTestContext(nil)
	common.SetContextKey(c, constant.ContextKeyFitRequirement, fitpolicy.Requirement{
		Family: "kimi-k3", Model: "kimi-k3", Marks: []string{"tools.dynamic_names"},
	})
	filter := FitChannelFilterForRequest(c)
	require.NotNil(t, filter)

	require.True(t, FitCapabilityIndexReady(),
		"the filter's constructor runs outside the selection lock and must be what builds the index")
	require.NotNil(t, filter.MarkSatisfied)
	assert.True(t, filter.MarkSatisfied(7),
		"the mark written before the request must be readable once the filter exists")
}

// TestSelectionPathLookupNeverBuildsTheIndex is the structural half of M1: the
// read that happens under channelSyncLock must answer from what is there, never
// start a table scan. An unbuilt index answers "not verified", which is the same
// conservative answer an empty one gives.
func TestSelectionPathLookupNeverBuildsTheIndex(t *testing.T) {
	setupFitCapabilityDB(t)

	now := common.GetTimestamp()
	_, err := ApplyChannelFitCapability(FitCapabilityWrite{
		ChannelId: 7, Family: "kimi-k3", Model: "kimi-k3", Behavior: "tools.dynamic_names",
		Supported: true, Source: FitCapabilitySourceSuite, At: now, ExpectedRevision: 0,
	}, now)
	require.NoError(t, err)

	ResetFitCapabilityIndexForTest()
	db := DB
	queries := countFitCapabilityQueries(t, db)

	assert.False(t, ChannelSatisfiesFitMarks(7, FitMarkRequirement{
		Model: "kimi-k3", Marks: []string{"tools.dynamic_names"},
	}), "an unbuilt index verifies nothing, even when the row exists")
	assert.False(t, FitCapabilityIndexReady(),
		"the selection-path lookup must not build the index: it runs with channelSyncLock held")
	assert.EqualValues(t, 0, queries.Load(), "no query may run from the selection path")

	// The out-of-lock entry point builds, which is what makes the miss above a
	// one-request state rather than a permanent one.
	ensureFitCapabilityIndexBuilt()
	assert.True(t, FitCapabilityIndexReady())
	assert.True(t, ChannelSatisfiesFitMarks(7, FitMarkRequirement{
		Model: "kimi-k3", Marks: []string{"tools.dynamic_names"},
	}))
}

// TestFailedIndexBuildIsSingleFlightAndCooldownBounded is the M2 gate. The
// failure it protects against is the designed fail-open state — a node whose
// table the migration has not reached yet — where built stays false and every
// mark lookup would otherwise retry the scan.
func TestFailedIndexBuildIsSingleFlightAndCooldownBounded(t *testing.T) {
	previousDB := DB
	previousMemoryCache := common.MemoryCacheEnabled
	// Channels and abilities only: channel_fit_capabilities deliberately does not
	// exist, which is the state a node runs in before the migration.
	db, err := openFitCapabilityTestDB(t, false)
	require.NoError(t, err)
	DB = db
	defer func() {
		DB = previousDB
		common.MemoryCacheEnabled = previousMemoryCache
		ResetFitCapabilityIndexForTest()
	}()

	ResetFitCapabilityIndexForTest()
	queries := countFitCapabilityQueries(t, db)

	// Every first-use lookup fails the same way; the point is how many of them
	// reach the database.
	for range 25 {
		ensureFitCapabilityIndexBuilt()
	}
	assert.EqualValues(t, 1, queries.Load(),
		"a failed build must be single-flight and cooled down, not retried per lookup")
	assert.False(t, FitCapabilityIndexReady())

	// The deliberate refresh on the channel-cache path is not a first-use guess
	// and must not be blocked by the cooldown, or a node could not pick the table
	// up until the window happened to expire.
	InitFitCapabilityIndex()
	assert.EqualValues(t, 2, queries.Load())

	// Once the window has passed, the first-use path may try again. The clock is
	// moved by expiring the deadline rather than by sleeping.
	fitCapabilityIndexRetryAfter = time.Time{}
	ensureFitCapabilityIndexBuilt()
	assert.EqualValues(t, 3, queries.Load())

	// And a successful build clears the cooldown for good: the next first-use
	// lookup must not even consider waiting.
	require.NoError(t, db.AutoMigrate(&ChannelFitCapability{}))
	InitFitCapabilityIndex()
	require.True(t, FitCapabilityIndexReady())
	before := queries.Load()
	ensureFitCapabilityIndexBuilt()
	assert.EqualValues(t, before, queries.Load(), "a built index is not rebuilt on lookup")
}

// TestFailedIndexBuildLogIsBudgetedNotSpent is the other half of M2: the failure
// line was outside the counter, so a missing table wrote one line per attempt,
// and the counter that was supposed to bound it never reset — a site that went
// quiet after its quota was spent could not report the fault coming back.
func TestFailedIndexBuildLogIsBudgetedNotSpent(t *testing.T) {
	previousDB := DB
	db, err := openFitCapabilityTestDB(t, false)
	require.NoError(t, err)
	DB = db
	defer func() {
		DB = previousDB
		ResetFitCapabilityIndexForTest()
	}()

	originalBudget := fitCapabilityIndexLog
	// A long window keeps the burst assertion deterministic: five attempts
	// cannot refill inside one hour. The refill arithmetic itself is covered
	// deterministically in common/log_budget_test.go, which can inject the
	// clock; asserting it here with a millisecond window made the test fail on
	// a loaded runner whenever the five attempts straddled the window boundary
	// (observed on the v1.0.0-rc.40-tokeness-intl.13 publish run).
	fitCapabilityIndexLog = common.NewLogBudget(1, time.Hour)
	defer func() { fitCapabilityIndexLog = originalBudget }()

	var logs strings.Builder
	previousWriter := gin.DefaultWriter
	gin.DefaultWriter = &logs
	defer func() { gin.DefaultWriter = previousWriter }()

	ResetFitCapabilityIndexForTest()
	for range 5 {
		InitFitCapabilityIndex()
	}
	require.Equal(t, 1, strings.Count(logs.String(), "failed to load channel fit capabilities"),
		"the burst is one line, not one line per attempt")

	// The window refills: the same fault a moment later is still reported, which
	// is what a spent one-shot counter cannot do.
	fitCapabilityIndexLog = common.NewLogBudget(1, time.Nanosecond)
	InitFitCapabilityIndex()
	assert.Equal(t, 2, strings.Count(logs.String(), "failed to load channel fit capabilities"),
		"the failure site must come back after the window instead of staying silent forever")
}
