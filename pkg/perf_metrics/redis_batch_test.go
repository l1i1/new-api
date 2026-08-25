package perfmetrics

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

func TestFlushPerfMetricsRedisAggregatesRecords(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	previousEnabled := common.RedisEnabled
	previousRDB := common.RDB
	common.RedisEnabled = true
	common.RDB = client
	t.Cleanup(func() {
		common.RedisEnabled = previousEnabled
		common.RDB = previousRDB
		_ = client.Close()
	})

	key := "perf:test-model:default:100"
	records := []perfMetricsRedisRecord{
		{redisKey: key, sample: Sample{Success: true, LatencyMs: 10, OutputTokens: 3, GenerationMs: 5}},
		{redisKey: key, sample: Sample{Success: false, LatencyMs: 20}},
	}
	require.NoError(t, flushPerfMetricsRedis(records))

	values, err := client.HGetAll(context.Background(), key).Result()
	require.NoError(t, err)
	require.Equal(t, "2", values["req"])
	require.Equal(t, "1", values["ok"])
	require.Equal(t, "30", values["lat"])
	require.Equal(t, "3", values["out"])
	require.Equal(t, "5", values["gen_ms"])

	ttl, err := client.TTL(context.Background(), key).Result()
	require.NoError(t, err)
	require.Greater(t, ttl, time.Duration(0))
}

func TestShutdownDrainsPerfMetricsRedisQueue(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	previousEnabled := common.RedisEnabled
	previousRDB := common.RDB
	previousQueue := perfMetricsRedisQueue
	previousStop := perfMetricsRedisStop
	previousDone := perfMetricsRedisDone
	previousEnded := perfMetricsRedisEnded
	previousActive := perfMetricsRedisActive.Load()
	common.RedisEnabled = true
	common.RDB = client
	perfMetricsRedisMu.Lock()
	perfMetricsRedisQueue = make(chan perfMetricsRedisRecord, 1)
	perfMetricsRedisStop = make(chan struct{})
	perfMetricsRedisDone = make(chan struct{})
	perfMetricsRedisEnded = false
	perfMetricsRedisActive.Store(true)
	perfMetricsRedisMu.Unlock()
	t.Cleanup(func() {
		perfMetricsRedisMu.Lock()
		perfMetricsRedisQueue = previousQueue
		perfMetricsRedisStop = previousStop
		perfMetricsRedisDone = previousDone
		perfMetricsRedisEnded = previousEnded
		perfMetricsRedisActive.Store(previousActive)
		perfMetricsRedisMu.Unlock()
		common.RedisEnabled = previousEnabled
		common.RDB = previousRDB
		_ = client.Close()
	})

	go perfMetricsRedisLoop()
	key := "perf:test-model:default:200"
	require.True(t, enqueuePerfMetricsRedis(key, Sample{Success: true}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, Shutdown(ctx))

	count, err := client.HGet(context.Background(), key, "req").Int64()
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
}
