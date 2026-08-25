package perfmetrics

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	perfMetricsRedisQueueSize     = 8192
	perfMetricsRedisBatchSize     = 128
	perfMetricsRedisBatchInterval = 10 * time.Millisecond
)

type perfMetricsRedisRecord struct {
	redisKey string
	sample   Sample
}

var (
	perfMetricsRedisOnce   sync.Once
	perfMetricsRedisQueue  chan perfMetricsRedisRecord
	perfMetricsRedisStop   chan struct{}
	perfMetricsRedisDone   chan struct{}
	perfMetricsRedisMu     sync.RWMutex
	perfMetricsRedisEnded  bool
	perfMetricsRedisActive atomic.Bool
)

func initPerfMetricsRedisBatcher() {
	perfMetricsRedisOnce.Do(func() {
		if !common.GetEnvOrDefaultBool("PERF_METRICS_ASYNC_REDIS", false) || !common.RedisAvailable() {
			return
		}
		perfMetricsRedisMu.Lock()
		perfMetricsRedisQueue = make(chan perfMetricsRedisRecord, perfMetricsRedisQueueSize)
		perfMetricsRedisStop = make(chan struct{})
		perfMetricsRedisDone = make(chan struct{})
		perfMetricsRedisEnded = false
		perfMetricsRedisActive.Store(true)
		perfMetricsRedisMu.Unlock()
		go perfMetricsRedisLoop()
	})
}

func enqueuePerfMetricsRedis(redisKey string, sample Sample) bool {
	if !perfMetricsRedisActive.Load() {
		return false
	}
	perfMetricsRedisMu.RLock()
	if perfMetricsRedisQueue == nil || perfMetricsRedisEnded {
		perfMetricsRedisMu.RUnlock()
		return false
	}
	record := perfMetricsRedisRecord{redisKey: redisKey, sample: sample}
	select {
	case perfMetricsRedisQueue <- record:
		perfMetricsRedisMu.RUnlock()
		return true
	default:
		perfMetricsRedisMu.RUnlock()
		return false
	}
}

func perfMetricsRedisLoop() {
	ticker := time.NewTicker(perfMetricsRedisBatchInterval)
	defer ticker.Stop()
	defer close(perfMetricsRedisDone)

	batch := make([]perfMetricsRedisRecord, 0, perfMetricsRedisBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		flushPerfMetricsRedisBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case record := <-perfMetricsRedisQueue:
			batch = append(batch, record)
			if len(batch) >= perfMetricsRedisBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-perfMetricsRedisStop:
			for {
				select {
				case record := <-perfMetricsRedisQueue:
					batch = append(batch, record)
					if len(batch) >= perfMetricsRedisBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// Shutdown drains queued Redis metrics during graceful process exit.
func Shutdown(ctx context.Context) error {
	perfMetricsRedisMu.Lock()
	if perfMetricsRedisQueue == nil || perfMetricsRedisEnded {
		perfMetricsRedisMu.Unlock()
		return nil
	}
	perfMetricsRedisEnded = true
	perfMetricsRedisActive.Store(false)
	close(perfMetricsRedisStop)
	done := perfMetricsRedisDone
	perfMetricsRedisMu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func flushPerfMetricsRedisBatch(records []perfMetricsRedisRecord) {
	if err := flushPerfMetricsRedis(records); err != nil {
		// Do not replay an unknown Redis commit: duplicate HINCRBY operations
		// corrupt aggregate metrics more than a clearly logged dropped batch.
		common.SysError(fmt.Sprintf("failed to flush perf metrics Redis batch; dropped %d records: %v", len(records), err))
	}
}

func flushPerfMetricsRedis(records []perfMetricsRedisRecord) error {
	if len(records) == 0 || !common.RedisAvailable() {
		return nil
	}

	fieldsByKey := make(map[string]map[string]int64)
	for _, record := range records {
		fields := fieldsByKey[record.redisKey]
		if fields == nil {
			fields = make(map[string]int64, 7)
			fieldsByKey[record.redisKey] = fields
		}
		addPerfMetricsRedisSample(fields, record.sample)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pipe := common.RDB.TxPipeline()
	for redisKey, fields := range fieldsByKey {
		for field, delta := range fields {
			pipe.HIncrBy(ctx, redisKey, field, delta)
		}
		pipe.Expire(ctx, redisKey, time.Hour)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func addPerfMetricsRedisSample(fields map[string]int64, sample Sample) {
	fields["req"]++
	if sample.Success {
		fields["ok"]++
	}
	if sample.LatencyMs > 0 {
		fields["lat"] += sample.LatencyMs
	}
	if sample.HasTtft && sample.TtftMs >= 0 {
		fields["ttft"] += sample.TtftMs
		fields["ttft_n"]++
	}
	if sample.OutputTokens > 0 && sample.GenerationMs > 0 {
		fields["out"] += sample.OutputTokens
		fields["gen_ms"] += sample.GenerationMs
	}
}
