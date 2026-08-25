package model

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/bytedance/gopkg/util/gopool"
)

// Async consume-log batching. When enabled, createLog enqueues entries into a
// bounded channel and a background worker flushes them with batched INSERTs.
// Billing deduction paths are unaffected: only the log write moves off the
// request path, matching the durability contract of LOG_DB as an audit trail
// rather than an authorization source.

const logBatchQueueSize = 10000
const logBatchFlushInterval = time.Second
const logBatchInsertSize = 500

var (
	logBatchQueue chan *Log
	logBatchStop  chan context.Context
	logBatchDone  chan struct{}
	logBatchMu    sync.RWMutex
	logBatchEnded bool
)

// InitLogBatcher starts the background flush worker. Called at startup when
// LOG_BATCH_ENABLED=true.
func InitLogBatcher() {
	logBatchMu.Lock()
	defer logBatchMu.Unlock()
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		common.SysError("log batching requires transactional log storage; using synchronous ClickHouse inserts")
		return
	}
	if logBatchQueue != nil && !logBatchEnded {
		return
	}
	logBatchQueue = make(chan *Log, logBatchQueueSize)
	logBatchStop = make(chan context.Context, 1)
	logBatchDone = make(chan struct{})
	logBatchEnded = false
	queue := logBatchQueue
	stop := logBatchStop
	done := logBatchDone
	gopool.Go(func() {
		defer close(done)
		buffer := make([]*Log, 0, logBatchInsertSize)
		ticker := time.NewTicker(logBatchFlushInterval)
		defer ticker.Stop()
		for {
			if len(buffer) >= logBatchInsertSize {
				select {
				case <-ticker.C:
					buffer = flushLogBatch(context.Background(), buffer)
				case ctx := <-stop:
					drainLogBatchQueue(ctx, queue, buffer)
					return
				}
				continue
			}
			select {
			case entry := <-queue:
				buffer = append(buffer, entry)
				if len(buffer) >= logBatchInsertSize {
					buffer = flushLogBatch(context.Background(), buffer)
				}
			case <-ticker.C:
				buffer = flushLogBatch(context.Background(), buffer)
			case ctx := <-stop:
				drainLogBatchQueue(ctx, queue, buffer)
				return
			}
		}
	})
}

// enqueueLog delivers a log entry to the batcher. A full queue falls back to a
// synchronous insert so log delivery never silently drops under backpressure.
func enqueueLog(entry *Log) error {
	logBatchMu.RLock()
	if logBatchQueue == nil || logBatchEnded {
		logBatchMu.RUnlock()
		return LOG_DB.Create(entry).Error
	}
	select {
	case logBatchQueue <- entry:
		logBatchMu.RUnlock()
		return nil
	default:
		logBatchMu.RUnlock()
		return LOG_DB.Create(entry).Error
	}
}

// ShutdownLogBatcher drains queued audit logs during graceful process exit.
func ShutdownLogBatcher(ctx context.Context) error {
	logBatchMu.Lock()
	if logBatchQueue == nil {
		logBatchMu.Unlock()
		return nil
	}
	if !logBatchEnded {
		logBatchEnded = true
		logBatchStop <- ctx
	}
	done := logBatchDone
	logBatchMu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		<-done
		return ctx.Err()
	}
}

func drainLogBatchQueue(ctx context.Context, queue <-chan *Log, buffer []*Log) {
	for {
		if len(buffer) >= logBatchInsertSize {
			buffer = flushLogBatch(ctx, buffer)
			if len(buffer) > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(100 * time.Millisecond):
				}
				continue
			}
		}
		select {
		case entry := <-queue:
			buffer = append(buffer, entry)
		default:
			for len(buffer) > 0 && ctx.Err() == nil {
				buffer = flushLogBatch(ctx, buffer)
				if len(buffer) > 0 {
					select {
					case <-ctx.Done():
						return
					case <-time.After(100 * time.Millisecond):
					}
				}
			}
			return
		}
	}
}

func flushLogBatch(ctx context.Context, buffer []*Log) []*Log {
	if len(buffer) == 0 {
		return buffer[:0]
	}
	tx := LOG_DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		common.SysError("batched log transaction failed to start; retained " + fmt.Sprint(len(buffer)) + " records: " + tx.Error.Error())
		return buffer
	}
	if err := tx.CreateInBatches(buffer, logBatchInsertSize).Error; err != nil {
		_ = tx.Rollback().Error
		common.SysError("batched log insert failed before commit; retained " + fmt.Sprint(len(buffer)) + " records: " + err.Error())
		return buffer
	}
	if err := tx.Commit().Error; err != nil {
		// Commit errors have unknown outcome. Replaying could duplicate audit
		// rows, so preserve at-most-once semantics only at this boundary.
		common.SysError("batched log commit outcome unknown; dropped " + fmt.Sprint(len(buffer)) + " records without replay: " + err.Error())
	}
	return buffer[:0]
}
