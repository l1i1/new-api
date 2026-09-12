package service

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

func setupChannelConcurrencyTest(t *testing.T) (*miniredis.Miniredis, *gin.Context) {
	t.Helper()
	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	previousMemoryCacheEnabled := common.MemoryCacheEnabled
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	require.NoError(t, redisClient.Ping(context.Background()).Err())
	common.RedisEnabled = true
	common.RDB = redisClient
	// The limit resolver must read the passed channel's settings instead of the
	// not-initialized channel cache index.
	common.MemoryCacheEnabled = false
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	t.Cleanup(func() {
		_ = redisClient.Close()
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
		common.MemoryCacheEnabled = previousMemoryCacheEnabled
	})
	return redisServer, c
}

func concurrencyTestChannel(id int, limit int) *model.Channel {
	setting := ""
	if limit > 0 {
		setting = fmt.Sprintf(`{"concurrency_limit":%d}`, limit)
	}
	return &model.Channel{Id: id, Setting: &setting}
}

func TestAcquireChannelConcurrencyCapsInFlightAndReusesReleasedSlots(t *testing.T) {
	_, c := setupChannelConcurrencyTest(t)
	channel := concurrencyTestChannel(101, 1)

	release, saturated := AcquireChannelConcurrency(c, channel)
	require.False(t, saturated)
	require.NotNil(t, release)

	_, saturated = AcquireChannelConcurrency(c, channel)
	require.True(t, saturated, "the second acquire must be denied while the slot is held")

	release()

	release, saturated = AcquireChannelConcurrency(c, channel)
	require.False(t, saturated, "a released slot must become acquirable again")
	require.NotNil(t, release)
	release()
}

func TestAcquireChannelConcurrencySelfHealsAbandonedSlots(t *testing.T) {
	redisServer, c := setupChannelConcurrencyTest(t)
	channel := concurrencyTestChannel(102, 1)

	release, saturated := AcquireChannelConcurrency(c, channel)
	require.False(t, saturated)
	require.NotNil(t, release)
	// Simulate a node that died without releasing: the lease expiry must free
	// the slot instead of draining the channel's quota forever.
	redisServer.FastForward(31 * time.Minute)
	redisServer.SetTime(time.Now().Add(31 * time.Minute))

	second, saturated := AcquireChannelConcurrency(c, channel)
	require.False(t, saturated, "an abandoned slot must be purged after the lease TTL")
	require.NotNil(t, second)

	// A late release after expiry must be harmless, not poison the count.
	release()
	second()

	third, saturated := AcquireChannelConcurrency(c, channel)
	require.False(t, saturated)
	require.NotNil(t, third)
	third()
}

func TestAcquireChannelConcurrencyFailsOpenWhenRedisUnavailable(t *testing.T) {
	redisServer, c := setupChannelConcurrencyTest(t)
	channel := concurrencyTestChannel(103, 1)
	redisServer.Close()

	release, saturated := AcquireChannelConcurrency(c, channel)
	require.False(t, saturated, "a Redis outage must not block relay traffic")
	require.Nil(t, release)
}

func TestAcquireChannelConcurrencyLocalFallbackWithoutRedis(t *testing.T) {
	_, c := setupChannelConcurrencyTest(t)
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = true })

	// Unique channel id: the fallback counter is process-global.
	channel := concurrencyTestChannel(910001, 1)
	release, saturated := AcquireChannelConcurrency(c, channel)
	require.False(t, saturated)
	require.NotNil(t, release)

	_, saturated = AcquireChannelConcurrency(c, channel)
	require.True(t, saturated)

	release()

	release, saturated = AcquireChannelConcurrency(c, channel)
	require.False(t, saturated)
	require.NotNil(t, release)
	release()
}

func TestAcquireChannelConcurrencySkipsUnlimitedChannels(t *testing.T) {
	_, c := setupChannelConcurrencyTest(t)

	release, saturated := AcquireChannelConcurrency(c, &model.Channel{Id: 910002})
	require.False(t, saturated)
	require.Nil(t, release, "channels without a limit must not take a slot")
}
