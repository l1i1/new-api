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
	logBatchStop  chan struct{}
	logBatchDone  chan struct{}
	logBatchMu    sync.RWMutex
	logBatchEnded bool
)

// InitLogBatcher starts the background flush worker. Called at startup when
// LOG_BATCH_ENABLED=true.
func InitLogBatcher() {
	logBatchMu.Lock()
	defer logBatchMu.Unlock()
	if logBatchQueue != nil && !logBatchEnded {
		return
	}
	logBatchQueue = make(chan *Log, logBatchQueueSize)
	logBatchStop = make(chan struct{})
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
			select {
			case entry := <-queue:
				buffer = append(buffer, entry)
				if len(buffer) >= logBatchInsertSize {
					buffer = flushLogBatch(buffer)
				}
			case <-ticker.C:
				buffer = flushLogBatch(buffer)
			case <-stop:
				for {
					select {
					case entry := <-queue:
						buffer = append(buffer, entry)
						if len(buffer) >= logBatchInsertSize {
							buffer = flushLogBatch(buffer)
						}
					default:
						flushLogBatch(buffer)
						return
					}
				}
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
		close(logBatchStop)
	}
	done := logBatchDone
	logBatchMu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func flushLogBatch(buffer []*Log) []*Log {
	if len(buffer) == 0 {
		return buffer[:0]
	}
	if err := LOG_DB.CreateInBatches(buffer, logBatchInsertSize).Error; err != nil {
		// The database may have committed before returning a transport error.
		// Replaying individual inserts would duplicate audit rows, so preserve
		// at-most-once semantics and make the loss explicit.
		common.SysError("batched log insert failed; dropped " + fmt.Sprint(len(buffer)) + " records: " + err.Error())
	}
	return buffer[:0]
}
