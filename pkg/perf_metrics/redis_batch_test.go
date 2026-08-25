package perfmetrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

type failAfterPipelineHook struct {
	failed bool
}

func (hook *failAfterPipelineHook) BeforeProcess(ctx context.Context, _ redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (hook *failAfterPipelineHook) AfterProcess(context.Context, redis.Cmder) error {
	return nil
}

func (hook *failAfterPipelineHook) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (hook *failAfterPipelineHook) AfterProcessPipeline(_ context.Context, _ []redis.Cmder) error {
	if hook.failed {
		return nil
	}
	hook.failed = true
	return errors.New("pipeline response unavailable")
}

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

func TestFlushPerfMetricsRedisDoesNotReplayUnknownCommit(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	hook := &failAfterPipelineHook{}
	client.AddHook(hook)
	previousEnabled := common.RedisEnabled
	previousRDB := common.RDB
	common.RedisEnabled = true
	common.RDB = client
	t.Cleanup(func() {
		_ = client.Close()
		common.RedisEnabled = previousEnabled
		common.RDB = previousRDB
	})

	key := "perf:test-model:default:150"
	flushPerfMetricsRedisBatch([]perfMetricsRedisRecord{
		{redisKey: key, sample: Sample{Success: true, LatencyMs: 10, OutputTokens: 3, GenerationMs: 5}},
		{redisKey: key, sample: Sample{Success: false, LatencyMs: 20}},
	})

	require.True(t, hook.failed)
	values, err := client.HGetAll(context.Background(), key).Result()
	require.NoError(t, err)
	require.Equal(t, "2", values["req"])
	require.Equal(t, "1", values["ok"])
	require.Equal(t, "30", values["lat"])
	require.Equal(t, "3", values["out"])
	require.Equal(t, "5", values["gen_ms"])
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
