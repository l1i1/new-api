package model

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupConfigEpochRedis points common.RDB at a throwaway miniredis and resets
// the package-level watcher state, so each test starts from "nothing applied".
func setupConfigEpochRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	server := miniredis.RunT(t)
	previousEnabled := common.RedisEnabled
	previousRDB := common.RDB
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	resetConfigEpochState()
	t.Cleanup(func() {
		if common.RDB != nil {
			_ = common.RDB.Close()
		}
		common.RedisEnabled = previousEnabled
		common.RDB = previousRDB
		resetConfigEpochState()
	})
	return server
}

func resetConfigEpochState() {
	configEpochLastSeen.Store(0)
	configEpochRedisDownLatched.Store(false)
	configEpochHooksMu.Lock()
	configEpochHooks = nil
	configEpochHooksMu.Unlock()
}

// waitForCondition polls until the condition holds or the deadline passes; the
// watcher is asynchronous, so every assertion on its effect needs this.
func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return condition()
}

// startConfigEpochWatcherForTest runs the watcher and returns a stop function
// that waits for the goroutine to exit.
//
// Waiting matters: a test that returns while its watcher is still between ticks
// leaves a goroutine that reads package state (the Redis client, the log writers,
// the log budget) after the next test has replaced it — which the race detector
// reports as a defect in the production code even though the production code
// never swaps those values.
func startConfigEpochWatcherForTest(interval time.Duration, reload func()) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		runConfigEpochWatcher(stop, interval, reload)
	}()
	return func() {
		close(stop)
		<-done
	}
}

func TestNotifyConfigChangedIsNoopWithoutRedis(t *testing.T) {
	previousEnabled := common.RedisEnabled
	previousRDB := common.RDB
	resetConfigEpochState()
	t.Cleanup(func() {
		common.RedisEnabled = previousEnabled
		common.RDB = previousRDB
		resetConfigEpochState()
	})

	// Redis disabled entirely: the write path must still succeed.
	common.RedisEnabled = false
	common.RDB = nil
	assert.NotPanics(t, NotifyConfigChanged)

	// Enabled but not connected (the window between option parsing and the
	// first successful Ping) must be tolerated the same way.
	common.RedisEnabled = true
	common.RDB = nil
	assert.NotPanics(t, NotifyConfigChanged)
}

func TestNotifyConfigChangedIncrementsSharedEpoch(t *testing.T) {
	server := setupConfigEpochRedis(t)

	NotifyConfigChanged()
	NotifyConfigChanged()

	stored, err := common.RDB.Get(t.Context(), configEpochKey).Int64()
	require.NoError(t, err)
	assert.Equal(t, int64(2), stored, "each committed change must advance the shared epoch")
	assert.Equal(t, int64(2), configEpochLastSeen.Load(), "the writing node has already applied its own change")
	storedValue, err := server.Get(configEpochKey)
	require.NoError(t, err)
	assert.Equal(t, "2", storedValue)
}

func TestConfigEpochWatcherAppliesPeerChange(t *testing.T) {
	server := setupConfigEpochRedis(t)
	var reloads atomic.Int64
	stopWatcher := startConfigEpochWatcherForTest(10*time.Millisecond, func() { reloads.Add(1) })
	defer stopWatcher()

	// A peer commits a configuration change.
	require.NoError(t, common.RDB.Incr(t.Context(), configEpochKey).Err())

	require.True(t, waitForCondition(t, 3*time.Second, func() bool { return reloads.Load() == 1 }),
		"the watcher must reload after a peer publishes a new epoch")
	assert.Equal(t, int64(1), configEpochLastSeen.Load())

	// Once applied, an unchanged epoch must not reload again.
	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, int64(1), reloads.Load(), "an unchanged epoch must not trigger repeated reloads")
	assert.NotEmpty(t, server.Addr())
}

func TestConfigEpochWatcherCoalescesPeerBurst(t *testing.T) {
	setupConfigEpochRedis(t)
	var reloads atomic.Int64
	stopWatcher := startConfigEpochWatcherForTest(15*time.Millisecond, func() { reloads.Add(1) })
	defer stopWatcher()

	// A bulk configuration write publishes many times in a row. The watcher
	// reloads the whole configuration, so one reload per observed value is
	// enough — it must not reload once per publish.
	for range 50 {
		require.NoError(t, common.RDB.Incr(t.Context(), configEpochKey).Err())
	}

	require.True(t, waitForCondition(t, 3*time.Second, func() bool { return reloads.Load() >= 1 }))
	time.Sleep(80 * time.Millisecond)
	assert.Equal(t, int64(1), reloads.Load(), "a burst of publishes must coalesce into a single reload")
}

func TestConfigEpochWatcherToleratesRedisOutageAndRecovers(t *testing.T) {
	server := setupConfigEpochRedis(t)
	var reloads atomic.Int64
	stopWatcher := startConfigEpochWatcherForTest(10*time.Millisecond, func() { reloads.Add(1) })
	defer stopWatcher()

	// Take the server down rather than swapping common.RDB: the watcher reads
	// that global from its own goroutine, so a test that reassigns it races with
	// the production code (the race detector flags exactly that). Closing the
	// server leaves the client pointed at a dead endpoint — the same unreachable
	// path, with no shared state touched by the test.
	server.Close()
	require.True(t, waitForCondition(t, 3*time.Second, func() bool { return configEpochRedisDownLatched.Load() }),
		"the outage should be logged exactly once")
	assert.Equal(t, int64(0), reloads.Load(), "an unreachable Redis must not trigger reloads")

	// Recovery: the next successful read of a newer epoch must be applied.
	require.NoError(t, server.Restart())
	require.NoError(t, common.RDB.Incr(t.Context(), configEpochKey).Err())
	require.True(t, waitForCondition(t, 3*time.Second, func() bool { return reloads.Load() == 1 }),
		"the watcher must resume after Redis comes back")
	assert.False(t, configEpochRedisDownLatched.Load(), "recovery clears the log latch")
}

func TestNotifyConfigChangedKeepsAPendingPeerChangeUnapplied(t *testing.T) {
	setupConfigEpochRedis(t)

	NotifyConfigChanged() // epoch 1: this node's own change, applied locally
	require.Equal(t, int64(1), configEpochLastSeen.Load())

	// A peer commits a change, but this node has not watched the tick yet.
	require.NoError(t, common.RDB.Incr(t.Context(), configEpochKey).Err())

	// This node now commits its own change. The bump must not claim the peer's
	// value as applied, or the watcher would skip it and the peer's change would
	// sit unapplied until the periodic sync.
	NotifyConfigChanged()
	assert.Equal(t, int64(1), configEpochLastSeen.Load(),
		"a peer change published since the last observation must stay pending")
	stored, err := common.RDB.Get(t.Context(), configEpochKey).Int64()
	require.NoError(t, err)
	assert.Equal(t, int64(3), stored, "both bumps must still reach the other nodes")
}

func TestConfigEpochWatcherRetriesFailedReload(t *testing.T) {
	setupConfigEpochRedis(t)
	var calls atomic.Int64
	stopWatcher := startConfigEpochWatcherForTest(10*time.Millisecond, func() {
		// A reload step that panics fails the application of that epoch: the
		// value must stay unapplied so the next tick retries it instead of
		// silently staying stale forever.
		if calls.Add(1) == 1 {
			panic("boom")
		}
	})
	defer stopWatcher()

	require.NoError(t, common.RDB.Incr(t.Context(), configEpochKey).Err())

	require.True(t, waitForCondition(t, 3*time.Second, func() bool { return calls.Load() >= 2 }),
		"a failed reload must be retried on the next tick")
	assert.Equal(t, int64(1), configEpochLastSeen.Load(), "the epoch is recorded once a reload succeeds")
}

func TestConfigEpochWatcherTreatsFlushedKeyAsOneExtraChange(t *testing.T) {
	server := setupConfigEpochRedis(t)
	var reloads atomic.Int64
	stopWatcher := startConfigEpochWatcherForTest(10*time.Millisecond, func() { reloads.Add(1) })
	defer stopWatcher()

	require.NoError(t, common.RDB.Incr(t.Context(), configEpochKey).Err())
	require.True(t, waitForCondition(t, 3*time.Second, func() bool { return reloads.Load() == 1 }))

	// A flushed Redis loses the counter. That is indistinguishable from "no
	// change was ever published", so the watcher applies one more reload and
	// then settles instead of reloading on every tick.
	server.FlushAll()
	require.True(t, waitForCondition(t, 3*time.Second, func() bool { return reloads.Load() == 2 }))
	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, int64(2), reloads.Load())
	assert.Equal(t, int64(0), configEpochLastSeen.Load())
}

func TestApplyConfigReloadRunsHooksAndIsolatesPanic(t *testing.T) {
	resetConfigEpochState()
	t.Cleanup(resetConfigEpochState)

	var coreCalls, goodHookCalls atomic.Int64
	RegisterConfigReloadHook(func() { panic("hook exploded") })
	RegisterConfigReloadHook(func() { goodHookCalls.Add(1) })

	// A panicking core reload fails the whole application so the epoch is
	// retried on the next tick; hooks are skipped on that attempt, because they
	// would otherwise run again for the same change when the retry succeeds.
	err := applyConfigReload(func() { coreCalls.Add(1); panic("core exploded") })
	require.Error(t, err)
	assert.Equal(t, int64(1), coreCalls.Load())
	assert.Equal(t, int64(0), goodHookCalls.Load())

	// On the successful attempt every hook runs, and a panicking hook neither
	// fails the application nor stops the hooks registered after it.
	err = applyConfigReload(func() { coreCalls.Add(1) })
	require.NoError(t, err)
	assert.Equal(t, int64(2), coreCalls.Load())
	assert.Equal(t, int64(1), goodHookCalls.Load())
}

func TestRegisterConfigReloadHookIgnoresNil(t *testing.T) {
	resetConfigEpochState()
	t.Cleanup(resetConfigEpochState)

	RegisterConfigReloadHook(nil)
	require.NoError(t, applyConfigReload(nil))
}

// A channel write is committed before the local cache is refreshed, so the other
// nodes must be told even when that refresh fails outright: otherwise the change
// exists in the database and nowhere else until the periodic sync.
func TestInitChannelCacheAndNotifyPublishesWhenTheLocalRefreshPanics(t *testing.T) {
	setupConfigEpochRedis(t)

	previousMemoryCache := common.MemoryCacheEnabled
	previousDB := DB
	// A nil database with the memory cache enabled makes InitChannelCache panic.
	common.MemoryCacheEnabled = true
	DB = nil
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		DB = previousDB
	})

	require.Panics(t, InitChannelCacheAndNotify,
		"the test needs InitChannelCache to panic, which is the case it guards")
	stored, err := common.RDB.Get(t.Context(), configEpochKey).Int64()
	require.NoError(t, err)
	assert.Equal(t, int64(1), stored,
		"a committed channel change must still be published when the local refresh panics")
	assert.Equal(t, int64(0), configEpochLastSeen.Load(),
		"the node whose refresh failed must not claim the epoch, or it stays stale while the fleet moves on")
}

// Nothing else in this suite fails if reloadConfigFromEpoch stops reloading
// anything (verified by mutation), so this pins the two pieces of state a relay
// node actually decides with: the option map and the channel cache.
func TestReloadConfigFromEpochAppliesCommittedChanges(t *testing.T) {
	setupConfigEpochRedis(t)

	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })

	channelSyncLock.Lock()
	previousChannels := channelsIDM
	previousGroup2Model2Channels := group2model2channels
	channelSyncLock.Unlock()
	const channelID = 9002
	// The shared test fixture migrates neither the options table nor an option
	// map (the production sync loops ignore that read error), so this test brings
	// its own and restores the previous map wholesale afterwards.
	require.NoError(t, DB.AutoMigrate(&Option{}))
	t.Cleanup(func() { DB.Migrator().DropTable(&Option{}) })
	common.OptionMapRWMutex.Lock()
	previousOptionMap := common.OptionMap
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		channelSyncLock.Lock()
		channelsIDM = previousChannels
		group2model2channels = previousGroup2Model2Channels
		channelSyncLock.Unlock()
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
		DB.Exec("DELETE FROM channels WHERE id = ?", channelID)
		// The reload rebuilds the fit-capability index against the fixture
		// database; leaving it built would hide rows that a later test writes and
		// expects the lazy build to pick up.
		ResetFitCapabilityIndexForTest()
	})

	// What a peer's committed write looks like from this node's side: rows in the
	// shared database and nothing in memory yet.
	require.NoError(t, DB.Create(&Channel{
		Id:     channelID,
		Name:   "config-epoch-reload",
		Key:    "sk-config-epoch-reload",
		Status: common.ChannelStatusEnabled,
		Group:  "default",
		Models: "config-epoch-reload-model",
	}).Error)
	require.NoError(t, DB.Save(&Option{Key: "HeaderNavModules", Value: `{"marker":"reloaded"}`}).Error)

	reloadConfigFromEpoch()

	common.OptionMapRWMutex.RLock()
	optionValue := common.OptionMap["HeaderNavModules"]
	common.OptionMapRWMutex.RUnlock()
	assert.Equal(t, `{"marker":"reloaded"}`, optionValue,
		"the epoch reload must apply an option row committed by another node")
	channelSyncLock.RLock()
	_, cached := channelsIDM[channelID]
	channelSyncLock.RUnlock()
	assert.True(t, cached, "the epoch reload must rebuild the channel cache from the database")
}

// The happy path of the publish helper: a successful local refresh publishes, and
// the node may claim its own bump because it really did apply the change.
func TestInitChannelCacheAndNotifyPublishesAfterARefresh(t *testing.T) {
	setupConfigEpochRedis(t)

	previousMemoryCache := common.MemoryCacheEnabled
	// The database-only path keeps this test to the publish itself; the cache
	// rebuild is covered by the reload test above.
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		ResetFitCapabilityIndexForTest()
	})

	InitChannelCacheAndNotify()

	stored, err := common.RDB.Get(t.Context(), configEpochKey).Int64()
	require.NoError(t, err)
	assert.Equal(t, int64(1), stored, "a committed channel change must be published")
	assert.Equal(t, int64(1), configEpochLastSeen.Load(),
		"the node applied the refresh locally, so it may claim its own bump")
}

func TestConfigEpochReloadRetryBackoffGrowsAndIsCapped(t *testing.T) {
	assert.Equal(t, time.Duration(0), configEpochReloadRetryBackoff(0))
	assert.Equal(t, time.Second, configEpochReloadRetryBackoff(1))
	assert.Equal(t, 2*time.Second, configEpochReloadRetryBackoff(2))
	assert.Equal(t, 4*time.Second, configEpochReloadRetryBackoff(3))
	assert.Equal(t, 8*time.Second, configEpochReloadRetryBackoff(4))
	assert.Equal(t, maxConfigEpochReloadBackoff, configEpochReloadRetryBackoff(5))
	assert.Equal(t, maxConfigEpochReloadBackoff, configEpochReloadRetryBackoff(50))
}

func TestConfigEpochNodeLabelIsNeverEmpty(t *testing.T) {
	assert.NotEmpty(t, configEpochNodeLabel())
}

// A failed channel read must not be published as "this node has no channels":
// the epoch reload runs this path on every configuration change on every node,
// so a transient read error would take a replica out of rotation until the next
// periodic sync, while the epoch was recorded as applied.
func TestInitChannelCacheKeepsThePreviousCacheWhenTheReadFails(t *testing.T) {
	const channelID = 9003
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	channelSyncLock.Lock()
	previousChannels := channelsIDM
	previousGroup2Model2Channels := group2model2channels
	channelsIDM = map[int]*Channel{channelID: {Id: channelID, Status: common.ChannelStatusEnabled}}
	group2model2channels = map[string]map[string][]int{"default": {"m": {channelID}}}
	channelSyncLock.Unlock()

	require.NoError(t, DB.Migrator().DropTable(&Channel{}))
	t.Cleanup(func() {
		channelSyncLock.Lock()
		channelsIDM = previousChannels
		group2model2channels = previousGroup2Model2Channels
		channelSyncLock.Unlock()
		common.MemoryCacheEnabled = previousMemoryCache
		require.NoError(t, DB.AutoMigrate(&Channel{}))
	})

	InitChannelCache()

	channelSyncLock.RLock()
	_, stillCached := channelsIDM[channelID]
	cachedModels := group2model2channels["default"]["m"]
	channelSyncLock.RUnlock()
	assert.True(t, stillCached, "a failed channel read must keep the previous channels in the cache")
	assert.Equal(t, []int{channelID}, cachedModels,
		"a failed channel read must keep the previous model routing table")
}

// A failing reload is retried at full rate on purpose, so its error line has to
// be budgeted: unbudgeted it is written once per retry for as long as the fault
// lasts. The helper is exercised directly rather than through the watcher,
// because a test that swaps the writer and budget while the watcher goroutine is
// live is itself a data race.
func TestConfigEpochReloadFailureLogIsBudgeted(t *testing.T) {
	previousBudget := configEpochReloadFailureLog
	configEpochReloadFailureLog = common.NewLogBudget(2, time.Hour)
	t.Cleanup(func() { configEpochReloadFailureLog = previousBudget })

	// SysError writes to the error writer, not the default one.
	var logs strings.Builder
	previousWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logs
	t.Cleanup(func() { gin.DefaultErrorWriter = previousWriter })

	for attempt := range 20 {
		logConfigEpochReloadFailure(fmt.Errorf("reload attempt %d failed", attempt))
	}
	assert.Equal(t, 2, strings.Count(logs.String(), "config epoch reload failed"),
		"the failure line must be budgeted, not written once per retry")
}

// A channel that the relay auto-disables (dead key, exhausted balance) must stop
// receiving traffic on every replica, not only on the one that saw the error —
// and the same holds for the automatic recovery afterwards.
//
// The cache is seeded and restored directly rather than through InitChannelCache:
// that call rebuilds the process-wide selection maps and the fit-capability
// index, which other tests in this package read, so using it here would make
// their results depend on this test having run first.
func TestUpdateChannelStatusPublishesTheChange(t *testing.T) {
	setupConfigEpochRedis(t)

	const channelID = 9001
	channel := Channel{
		Id:     channelID,
		Name:   "config-epoch-test",
		Key:    "sk-config-epoch-test",
		Status: common.ChannelStatusEnabled,
		Group:  "default",
		Models: "config-epoch-test-model",
	}
	require.NoError(t, DB.Create(&channel).Error)

	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	channelSyncLock.Lock()
	previousChannels := channelsIDM
	previousGroup2Model2Channels := group2model2channels
	channelsIDM = map[int]*Channel{channelID: {Id: channelID, Status: common.ChannelStatusEnabled}}
	group2model2channels = map[string]map[string][]int{}
	channelSyncLock.Unlock()
	t.Cleanup(func() {
		channelSyncLock.Lock()
		channelsIDM = previousChannels
		group2model2channels = previousGroup2Model2Channels
		channelSyncLock.Unlock()
		common.MemoryCacheEnabled = previousMemoryCache
		DB.Exec("DELETE FROM channels WHERE id = ?", channelID)
		DB.Exec("DELETE FROM abilities WHERE channel_id = ?", channelID)
	})

	require.True(t, UpdateChannelStatus(channelID, "", common.ChannelStatusAutoDisabled, "config epoch test"))

	stored, err := common.RDB.Get(t.Context(), configEpochKey).Int64()
	require.NoError(t, err)
	assert.Equal(t, int64(1), stored,
		"an auto-disable must be published so the other replicas stop using the channel now")
}
