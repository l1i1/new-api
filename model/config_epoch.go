package model

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/go-redis/redis/v8"
)

// Config epoch: a monotonically increasing counter in the shared Redis that
// every node bumps after it has committed a configuration change, and every
// node watches so it can reload its in-memory configuration immediately.
//
// Why this exists: configuration is authoritative in the database, and each
// node keeps an in-memory copy of it (common.OptionMap, the channel cache, the
// pricing cache, the authz policy). Those copies used to be refreshed only by
// the SYNC_FREQUENCY loops, so a change written on one node took up to
// SYNC_FREQUENCY seconds (default 60) to reach another — and the two nodes
// could disagree with each other for that whole window. Both deployments front
// several nodes with one nginx, and admin traffic is not routed to the same
// node as relay traffic, so that window is the normal case, not an edge case.
//
// The epoch is a level, not a message: a node that was restarting, disconnected
// or slow simply observes the newer value on its next poll, so nothing can be
// lost the way a pub/sub notification can. The database sync loops stay in
// place as the backstop, so a Redis outage degrades to exactly the previous
// behaviour.
//
// Every path here fails open: a Redis error must never fail a configuration
// write, and the watcher must never spin or panic because the cache is down.
const (
	configEpochKey = "config_epoch:v1"

	// DefaultConfigEpochWatchInterval is how often a node re-reads the epoch.
	// One GET per node per interval; the value changes only when an operator
	// changes configuration.
	DefaultConfigEpochWatchInterval = 2 * time.Second

	// minConfigEpochWatchInterval floors any configured interval so a caller
	// cannot turn the watcher into a hot loop against Redis.
	minConfigEpochWatchInterval = 250 * time.Millisecond

	// Kept tight so Redis latency cannot delay the watcher or a write path.
	configEpochRedisTimeout = 100 * time.Millisecond
)

var (
	// configEpochLastSeen is the epoch value this process has already applied.
	// NotifyConfigChanged stores the value it bumped to, because the caller has
	// just refreshed its own in-memory state and must not reload it again.
	configEpochLastSeen atomic.Int64

	// configEpochReloading serializes reloads so a slow reload cannot overlap
	// with the next tick.
	configEpochReloading sync.Mutex

	configEpochHooksMu sync.Mutex
	configEpochHooks   []func()

	// configEpochRedisDownLatched keeps a Redis outage to one log line instead
	// of one per interval.
	configEpochRedisDownLatched atomic.Bool
)

// RegisterConfigReloadHook adds a reload step that runs after the core
// configuration has been reloaded. Used for state that lives outside the model
// package (the authz policy enforcer) and would otherwise create an import
// cycle. Hooks must not panic; a panicking hook is recovered and logged so it
// cannot kill the watcher.
func RegisterConfigReloadHook(hook func()) {
	if hook == nil {
		return
	}
	configEpochHooksMu.Lock()
	defer configEpochHooksMu.Unlock()
	configEpochHooks = append(configEpochHooks, hook)
}

// NotifyConfigChanged publishes that this node just committed a configuration
// change, so every other node reloads on its next watch tick. Call it after the
// database commit and after this process has refreshed its own state.
//
// It is a no-op without Redis, and it never returns an error: a failed publish
// only means peers fall back to the SYNC_FREQUENCY loops.
func NotifyConfigChanged() {
	if !common.RedisAvailable() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), configEpochRedisTimeout)
	defer cancel()
	next, err := common.RDB.Incr(ctx, configEpochKey).Result()
	if err != nil {
		logConfigEpochRedisIssue("bump config epoch", err)
		return
	}
	configEpochRedisDownLatched.Store(false)
	// This process already applied the change it just committed; recording the
	// value here keeps the watcher from reloading the same state again.
	configEpochLastSeen.Store(next)
}

// InitChannelCacheAndNotify refreshes the local channel cache for a committed
// channel change and publishes it to the other nodes. Controllers call this in
// place of InitChannelCache after a mutation; the periodic sync keeps calling
// InitChannelCache so it never publishes a change it merely observed.
func InitChannelCacheAndNotify() {
	InitChannelCache()
	NotifyConfigChanged()
}

// StartConfigEpochWatcher runs the reload watcher for the lifetime of the
// process. Call it once, after the initial configuration load.
func StartConfigEpochWatcher(interval time.Duration) {
	if interval < minConfigEpochWatchInterval {
		interval = DefaultConfigEpochWatchInterval
	}
	go runConfigEpochWatcher(nil, interval, reloadConfigFromEpoch)
}

// runConfigEpochWatcher polls the shared epoch and calls reload when it differs
// from the value this process has already applied. A nil stop channel runs
// until the process exits.
func runConfigEpochWatcher(stop <-chan struct{}, interval time.Duration, reload func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		if !common.RedisAvailable() {
			// Without Redis peers cannot publish; the SYNC_FREQUENCY loops are
			// the only path, which is the pre-epoch behaviour.
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), configEpochRedisTimeout)
		value, err := common.RDB.Get(ctx, configEpochKey).Int64()
		cancel()
		if err != nil && !errors.Is(err, redis.Nil) {
			logConfigEpochRedisIssue("read config epoch", err)
			continue
		}
		if errors.Is(err, redis.Nil) {
			// No key yet (nothing published, or Redis was flushed). Treat it as
			// epoch zero: at most one extra reload after a flush, then stable.
			value = 0
		}
		configEpochRedisDownLatched.Store(false)
		if value == configEpochLastSeen.Load() {
			continue
		}
		started := time.Now()
		configEpochReloading.Lock()
		if err := applyConfigReload(reload); err != nil {
			configEpochReloading.Unlock()
			common.SysError("config epoch reload failed: " + err.Error())
			// Leave the applied value untouched so the next tick retries.
			continue
		}
		configEpochLastSeen.Store(value)
		configEpochReloading.Unlock()
		common.SysLog(fmt.Sprintf("config epoch %d applied in %s", value, time.Since(started)))
	}
}

// applyConfigReload runs the core reload and then the registered hooks. Only a
// core failure fails the epoch application (so it is retried on the next tick);
// a hook failure is logged and otherwise ignored, because a permanently broken
// hook must not turn every tick into a full reload plus an error line.
func applyConfigReload(reload func()) (err error) {
	if reload != nil {
		if err := runConfigReloadStep(reload); err != nil {
			return err
		}
	}
	configEpochHooksMu.Lock()
	hooks := append([]func(){}, configEpochHooks...)
	configEpochHooksMu.Unlock()
	for i, hook := range hooks {
		if hookErr := runConfigReloadStep(hook); hookErr != nil {
			common.SysError(fmt.Sprintf("config reload hook %d failed: %v", i, hookErr))
		}
	}
	return nil
}

// runConfigReloadStep converts a panic into an error so one broken reload step
// cannot take the process down.
func runConfigReloadStep(step func()) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	step()
	return nil
}

// reloadConfigFromEpoch refreshes every in-memory configuration this package
// owns. Kept identical to the SYNC_FREQUENCY reload path on purpose: the epoch
// only changes *when* the reload happens, never what it does.
func reloadConfigFromEpoch() {
	loadOptionsFromDatabase()
	InitChannelCache()
	InvalidatePricingCache()
}

// logConfigEpochRedisIssue logs the first failure of an outage at error level
// and stays quiet afterwards, so a long Redis outage cannot flood the log.
func logConfigEpochRedisIssue(action string, err error) {
	if configEpochRedisDownLatched.CompareAndSwap(false, true) {
		common.SysError(fmt.Sprintf("%s: %v (falling back to the periodic config sync)", action, err))
	}
}
