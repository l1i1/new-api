package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

// Channel concurrency gating caps the number of in-flight relay attempts per
// channel (across all keys of a multi-key channel). The count is global: the
// slot lives in a shared Redis ZSET so the limit holds across every node.
// With Redis disabled the gate degrades to a process-local counter, which is
// only meaningful for single-node development.
//
// The gate is protective, not a billing/abuse control, so Redis failures fail
// OPEN (the request proceeds ungated) — an outage must not take the relay
// down. This is the opposite of the user/group rate limiters, which fail
// closed on purpose; do not "fix" the inconsistency without revisiting that
// rationale.
//
// Each acquired member carries an expiry score, so a node that dies between
// acquire and release cannot leak its slots forever: the next acquire on any
// node purges expired members. Requests that legitimately outlive the lease
// TTL (very long streams) temporarily lose their slot's protection and the
// channel may overshoot its limit until they finish — accepted softness in
// exchange for crash self-healing.

const (
	channelConcurrencyKeyPrefix = "channel_concurrency:v1:"
	// channelConcurrencyLeaseTTL bounds how long one acquired slot may outlive
	// its holder. It must comfortably exceed the normal relay attempt duration;
	// see the self-healing note above.
	channelConcurrencyLeaseTTL = 30 * time.Minute
	// Kept tight so a Redis outage cannot add latency to the relay hot path.
	channelConcurrencyRedisTimeout = 100 * time.Millisecond
)

// channelConcurrencyAcquireScript claims one in-flight slot. Member scores are
// absolute expiry timestamps derived from Redis TIME, so multi-node clock
// skew cannot resurrect or prematurely evict members.
const channelConcurrencyAcquireScript = `
local nowParts = redis.call("TIME")
local now = (tonumber(nowParts[1]) * 1000) + math.floor(tonumber(nowParts[2]) / 1000)
redis.call("ZREMRANGEBYSCORE", KEYS[1], "-inf", now)
if redis.call("ZCARD", KEYS[1]) >= tonumber(ARGV[1]) then
  return 0
end
local expiresAt = now + tonumber(ARGV[2])
redis.call("ZADD", KEYS[1], expiresAt, ARGV[3])
redis.call("PEXPIRE", KEYS[1], tonumber(ARGV[2]))
return 1`

const channelConcurrencyReleaseScript = `
if redis.call("ZSCORE", KEYS[1], ARGV[1]) then
  redis.call("ZREM", KEYS[1], ARGV[1])
  if redis.call("ZCARD", KEYS[1]) == 0 then
    redis.call("DEL", KEYS[1])
  end
  return 1
end
return 0`

var channelConcurrencyLocal = struct {
	mu       sync.Mutex
	inFlight map[int]int
}{inFlight: make(map[int]int)}

func channelConcurrencyKey(channelID int) string {
	return fmt.Sprintf("%s%d", channelConcurrencyKeyPrefix, channelID)
}

// AcquireChannelConcurrency tries to claim one in-flight slot for the channel.
// The returned release func must be called exactly once when the relay attempt
// ends (defer); it is nil whenever no slot was taken. saturated reports that
// the channel is at its configured limit and the caller should route the
// request elsewhere. Channels without a limit (0) never touch Redis.
func AcquireChannelConcurrency(c *gin.Context, channel *model.Channel) (release func(), saturated bool) {
	if channel == nil {
		return nil, false
	}
	limit := model.CacheGetChannelConcurrencyLimit(channel)
	if limit <= 0 {
		return nil, false
	}
	if common.RedisEnabled && common.RDB != nil {
		return acquireChannelConcurrencyRedis(c, channel.Id, limit)
	}
	return acquireChannelConcurrencyLocal(channel.Id, limit)
}

func acquireChannelConcurrencyRedis(c *gin.Context, channelID int, limit int) (func(), bool) {
	ctx, cancel := context.WithTimeout(context.Background(), channelConcurrencyRedisTimeout)
	defer cancel()
	member := common.NewRequestId()
	acquired, err := common.RDB.Eval(ctx, channelConcurrencyAcquireScript,
		[]string{channelConcurrencyKey(channelID)},
		limit, channelConcurrencyLeaseTTL.Milliseconds(), member,
	).Int()
	if err != nil {
		// Fail open: a Redis outage must not block relay traffic.
		logger.LogWarn(c, "channel concurrency check failed, proceeding ungated: channel_id=%d: %v", channelID, err)
		return nil, false
	}
	if acquired == 0 {
		return nil, true
	}
	return func() {
		releaseChannelConcurrencyRedis(channelID, member)
	}, false
}

func releaseChannelConcurrencyRedis(channelID int, member string) {
	ctx, cancel := context.WithTimeout(context.Background(), channelConcurrencyRedisTimeout)
	defer cancel()
	if err := common.RDB.Eval(ctx, channelConcurrencyReleaseScript,
		[]string{channelConcurrencyKey(channelID)}, member,
	).Err(); err != nil && !errors.Is(err, redis.Nil) {
		// The member expires on its own via the lease TTL, so a lost release
		// self-heals; log it for visibility.
		common.SysLog(fmt.Sprintf("failed to release channel concurrency slot: channel_id=%d: %v", channelID, err))
	}
}

func acquireChannelConcurrencyLocal(channelID int, limit int) (func(), bool) {
	channelConcurrencyLocal.mu.Lock()
	defer channelConcurrencyLocal.mu.Unlock()
	if channelConcurrencyLocal.inFlight[channelID] >= limit {
		return nil, true
	}
	channelConcurrencyLocal.inFlight[channelID]++
	return func() {
		channelConcurrencyLocal.mu.Lock()
		defer channelConcurrencyLocal.mu.Unlock()
		remaining := channelConcurrencyLocal.inFlight[channelID] - 1
		if remaining <= 0 {
			delete(channelConcurrencyLocal.inFlight, channelID)
			return
		}
		channelConcurrencyLocal.inFlight[channelID] = remaining
	}, false
}
