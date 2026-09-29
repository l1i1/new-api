package model

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/alicebob/miniredis/v2"
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
	stop := make(chan struct{})
	defer close(stop)

	var reloads atomic.Int64
	go runConfigEpochWatcher(stop, 10*time.Millisecond, func() { reloads.Add(1) })

	// A peer commits a configuration change.
	require.NoError(t, common.RDB.Incr(t.Context(), configEpochKey).Err())

	require.True(t, waitForCondition(t, time.Second, func() bool { return reloads.Load() == 1 }),
		"the watcher must reload after a peer publishes a new epoch")
	assert.Equal(t, int64(1), configEpochLastSeen.Load())

	// Once applied, an unchanged epoch must not reload again.
	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, int64(1), reloads.Load(), "an unchanged epoch must not trigger repeated reloads")
	assert.NotEmpty(t, server.Addr())
}

func TestConfigEpochWatcherCoalescesPeerBurst(t *testing.T) {
	setupConfigEpochRedis(t)
	stop := make(chan struct{})
	defer close(stop)

	var reloads atomic.Int64
	go runConfigEpochWatcher(stop, 15*time.Millisecond, func() { reloads.Add(1) })

	// A bulk configuration write publishes many times in a row. The watcher
	// reloads the whole configuration, so one reload per observed value is
	// enough — it must not reload once per publish.
	for range 50 {
		require.NoError(t, common.RDB.Incr(t.Context(), configEpochKey).Err())
	}

	require.True(t, waitForCondition(t, time.Second, func() bool { return reloads.Load() >= 1 }))
	time.Sleep(80 * time.Millisecond)
	assert.Equal(t, int64(1), reloads.Load(), "a burst of publishes must coalesce into a single reload")
}

func TestConfigEpochWatcherToleratesRedisOutageAndRecovers(t *testing.T) {
	server := setupConfigEpochRedis(t)
	stop := make(chan struct{})
	defer close(stop)

	var reloads atomic.Int64
	go runConfigEpochWatcher(stop, 10*time.Millisecond, func() { reloads.Add(1) })

	// Take the server down rather than swapping common.RDB: the watcher reads
	// that global from its own goroutine, so a test that reassigns it races with
	// the production code (the race detector flags exactly that). Closing the
	// server leaves the client pointed at a dead endpoint — the same unreachable
	// path, with no shared state touched by the test.
	server.Close()
	require.True(t, waitForCondition(t, time.Second, func() bool { return configEpochRedisDownLatched.Load() }),
		"the outage should be logged exactly once")
	assert.Equal(t, int64(0), reloads.Load(), "an unreachable Redis must not trigger reloads")

	// Recovery: the next successful read of a newer epoch must be applied.
	require.NoError(t, server.Restart())
	require.NoError(t, common.RDB.Incr(t.Context(), configEpochKey).Err())
	require.True(t, waitForCondition(t, time.Second, func() bool { return reloads.Load() == 1 }),
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
	stop := make(chan struct{})
	defer close(stop)

	var calls atomic.Int64
	go runConfigEpochWatcher(stop, 10*time.Millisecond, func() {
		// A reload step that panics fails the application of that epoch: the
		// value must stay unapplied so the next tick retries it instead of
		// silently staying stale forever.
		if calls.Add(1) == 1 {
			panic("boom")
		}
	})

	require.NoError(t, common.RDB.Incr(t.Context(), configEpochKey).Err())

	require.True(t, waitForCondition(t, time.Second, func() bool { return calls.Load() >= 2 }),
		"a failed reload must be retried on the next tick")
	assert.Equal(t, int64(1), configEpochLastSeen.Load(), "the epoch is recorded once a reload succeeds")
}

func TestConfigEpochWatcherTreatsFlushedKeyAsOneExtraChange(t *testing.T) {
	server := setupConfigEpochRedis(t)
	stop := make(chan struct{})
	defer close(stop)

	var reloads atomic.Int64
	go runConfigEpochWatcher(stop, 10*time.Millisecond, func() { reloads.Add(1) })

	require.NoError(t, common.RDB.Incr(t.Context(), configEpochKey).Err())
	require.True(t, waitForCondition(t, time.Second, func() bool { return reloads.Load() == 1 }))

	// A flushed Redis loses the counter. That is indistinguishable from "no
	// change was ever published", so the watcher applies one more reload and
	// then settles instead of reloading on every tick.
	server.FlushAll()
	require.True(t, waitForCondition(t, time.Second, func() bool { return reloads.Load() == 2 }))
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
