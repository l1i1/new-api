package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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

	// maxConfigEpochReloadBackoff caps the wait between retries of a reload that
	// keeps failing. A failed attempt is cheap when the cause is a transient
	// read error, but a deterministic panic would otherwise re-run the whole
	// options + channels + pricing + policy reload on every tick, on every node,
	// for as long as the fault lasts.
	maxConfigEpochReloadBackoff = 10 * time.Second
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

	// configEpochReloadFailureLog bounds the reload-failure line for the same
	// reason: a persistent reload failure is retried every tick, and unbudgeted
	// it would write an error line every couple of seconds forever.
	configEpochReloadFailureLog = common.NewLogBudget(3, time.Minute)
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
	bumpConfigEpoch(true)
}

// bumpConfigEpoch advances the shared epoch. claim says whether this node has
// already applied the change locally and may therefore skip its own watcher's
// reload; it must be false when the local refresh failed, so the writer node
// reloads like everybody else instead of staying the only stale node.
func bumpConfigEpoch(claim bool) {
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
	if !claim {
		return
	}
	// This process already applied the change it just committed, so it does not
	// need to reload its own bump. Only claim that when this bump is the very
	// next value after the one this process has observed: if a peer published in
	// between (its bump is not applied here yet), leaving the observed value
	// untouched keeps that peer change pending, so the watcher reloads it
	// instead of silently absorbing it until the periodic sync.
	if next == configEpochLastSeen.Load()+1 {
		configEpochLastSeen.Store(next)
	}
}

// InitChannelCacheAndNotify refreshes the local channel cache for a committed
// channel change and publishes it to the other nodes. Controllers call this in
// place of InitChannelCache after a mutation; the periodic sync keeps calling
// InitChannelCache so it never publishes a change it merely observed.
//
// The publish is deferred so it still happens when the local refresh panics
// (InitChannelCache has a known panic path, which is why its startup caller
// recovers and retries): the database row is committed either way, so the other
// nodes must learn about it. On that path the claim is refused, because this
// process did not apply the change and must reload like any other node.
func InitChannelCacheAndNotify() {
	applied := false
	defer func() { bumpConfigEpoch(applied) }()
	InitChannelCache()
	applied = true
}

// StartConfigEpochWatcher runs the reload watcher for the lifetime of the
// process. Call it once, after the initial configuration load. A non-positive
// interval takes the default; anything faster than the floor is clamped to it so
// a caller cannot turn the watcher into a hot loop against Redis.
func StartConfigEpochWatcher(interval time.Duration) {
	if interval <= 0 {
		interval = DefaultConfigEpochWatchInterval
	}
	if interval < minConfigEpochWatchInterval {
		interval = minConfigEpochWatchInterval
	}
	if !common.RedisAvailable() {
		// Say it once at startup: without Redis this watcher can never fire, and
		// silently doing nothing is indistinguishable from "no changes happened".
		common.SysLog("config epoch: Redis is unavailable; configuration changes propagate only through the periodic sync")
	}
	go runConfigEpochWatcher(nil, interval, reloadConfigFromEpoch)
}

// runConfigEpochWatcher polls the shared epoch and calls reload when it differs
// from the value this process has already applied. A nil stop channel runs
// until the process exits.
func runConfigEpochWatcher(stop <-chan struct{}, interval time.Duration, reload func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	failures := 0
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
		if err := applyConfigReloadExclusively(reload); err != nil {
			logConfigEpochReloadFailure(err)
			failures++
			// Bound the work a permanently failing reload can do. A transient read
			// error should be retried immediately, but a deterministic panic would
			// otherwise re-run the whole reload on every tick, on every node, for
			// as long as the fault lasts.
			if backoff := configEpochReloadRetryBackoff(failures); backoff > 0 {
				select {
				case <-stop:
					return
				case <-time.After(backoff):
				}
			}
			// Leave the applied value untouched so the next attempt retries.
			continue
		}
		failures = 0
		configEpochLastSeen.Store(value)
		common.SysLog(fmt.Sprintf("config epoch %d applied in %s on node %s", value, time.Since(started), configEpochNodeLabel()))
	}
}

// applyConfigReloadExclusively serialises reloads so a slow one cannot overlap
// with the next tick.
func applyConfigReloadExclusively(reload func()) error {
	configEpochReloading.Lock()
	defer configEpochReloading.Unlock()
	return applyConfigReload(reload)
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
//
// Pricing is not invalidated here: InitChannelCache already does it on both of
// its paths (memory-cache and database-only), and it does so after releasing
// channelSyncLock — the ordering that avoids a deadlock with GetPricing.
func reloadConfigFromEpoch() {
	loadOptionsFromDatabase()
	InitChannelCache()
}

// configEpochReloadRetryBackoff returns how long to wait before retrying a reload
// that has failed `failures` times in a row; zero when nothing has failed.
func configEpochReloadRetryBackoff(failures int) time.Duration {
	switch {
	case failures <= 0:
		return 0
	case failures == 1:
		return time.Second
	case failures == 2:
		return 2 * time.Second
	case failures == 3:
		return 4 * time.Second
	case failures == 4:
		return 8 * time.Second
	default:
		return maxConfigEpochReloadBackoff
	}
}

// configEpochNodeLabel identifies this process in the epoch log lines. The
// configuration-epoch lines are the only evidence that a change reached a given
// replica, and several replicas can share a NODE_NAME, so the label carries the
// configured name and falls back to the hostname when it is empty.
func configEpochNodeLabel() string {
	if name := strings.TrimSpace(common.GetNodeIdentity().Name); name != "" {
		return name
	}
	if hostname, err := os.Hostname(); err == nil && strings.TrimSpace(hostname) != "" {
		return hostname
	}
	return "unknown"
}

// logConfigEpochReloadFailure reports a reload failure within a log budget. A
// failing reload is deliberately retried at full rate (a transient database
// error should recover on the next tick, and a failed attempt is cheap), so
// unbudgeted this line would be written every couple of seconds for as long as
// the fault lasts.
func logConfigEpochReloadFailure(err error) {
	if configEpochReloadFailureLog.Allow() {
		common.SysError("config epoch reload failed: " + err.Error())
	}
}

// logConfigEpochRedisIssue logs the first failure of an outage at error level
// and stays quiet afterwards, so a long Redis outage cannot flood the log.
func logConfigEpochRedisIssue(action string, err error) {
	if configEpochRedisDownLatched.CompareAndSwap(false, true) {
		common.SysError(fmt.Sprintf("%s: %v (falling back to the periodic config sync)", action, err))
	}
}
