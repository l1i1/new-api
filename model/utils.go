package model

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/bytedance/gopkg/util/gopool"
	"gorm.io/gorm"
)

const (
	BatchUpdateTypeUserQuota = iota
	BatchUpdateTypeTokenQuota
	BatchUpdateTypeUsedQuota
	BatchUpdateTypeRequestCount
)

const batchUpdateRetryInterval = 100 * time.Millisecond

type userBatchUpdate struct {
	quota        int
	usedQuota    int
	requestCount int
}

var (
	batchUpdateMu      sync.Mutex
	batchUserUpdates   = make(map[int]userBatchUpdate)
	batchTokenUpdates  = make(map[int]int)
	batchUpdaterMu     sync.RWMutex
	batchUpdaterStop   chan context.Context
	batchUpdaterDone   chan struct{}
	batchUpdaterAccept bool
)

func InitBatchUpdater() {
	batchUpdaterMu.Lock()
	if batchUpdaterAccept {
		batchUpdaterMu.Unlock()
		return
	}
	stop := make(chan context.Context, 1)
	done := make(chan struct{})
	batchUpdaterStop = stop
	batchUpdaterDone = done
	batchUpdaterAccept = true
	batchUpdaterMu.Unlock()

	gopool.Go(func() {
		defer close(done)
		interval := time.Duration(common.BatchUpdateInterval) * time.Second
		if interval <= 0 {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				batchUpdate(context.Background())
			case ctx := <-stop:
				drainBatchUpdates(ctx)
				return
			}
		}
	})
}

func ShutdownBatchUpdater(ctx context.Context) error {
	batchUpdaterMu.Lock()
	stop := batchUpdaterStop
	done := batchUpdaterDone
	if stop == nil || done == nil {
		batchUpdaterAccept = false
		batchUpdaterMu.Unlock()
		return drainBatchUpdates(ctx)
	}
	if batchUpdaterAccept {
		batchUpdaterAccept = false
		stop <- ctx
	}
	batchUpdaterMu.Unlock()

	select {
	case <-done:
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	case <-ctx.Done():
		<-done
		return ctx.Err()
	}
}

func drainBatchUpdates(ctx context.Context) error {
	for batchUpdate(ctx) {
		timer := time.NewTimer(batchUpdateRetryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func addNewRecord(type_ int, id int, value int) bool {
	batchUpdaterMu.RLock()
	if !batchUpdaterAccept {
		batchUpdaterMu.RUnlock()
		return false
	}
	batchUpdateMu.Lock()
	if type_ == BatchUpdateTypeTokenQuota {
		batchTokenUpdates[id] += value
	} else {
		update := batchUserUpdates[id]
		switch type_ {
		case BatchUpdateTypeUserQuota:
			update.quota += value
		case BatchUpdateTypeUsedQuota:
			update.usedQuota += value
		case BatchUpdateTypeRequestCount:
			update.requestCount += value
		}
		batchUserUpdates[id] = update
	}
	batchUpdateMu.Unlock()
	batchUpdaterMu.RUnlock()
	return true
}

func addUserBatchUpdate(id int, quota int, usedQuota int, requestCount int) bool {
	batchUpdaterMu.RLock()
	if !batchUpdaterAccept {
		batchUpdaterMu.RUnlock()
		return false
	}
	batchUpdateMu.Lock()
	update := batchUserUpdates[id]
	update.quota += quota
	update.usedQuota += usedQuota
	update.requestCount += requestCount
	batchUserUpdates[id] = update
	batchUpdateMu.Unlock()
	batchUpdaterMu.RUnlock()
	return true
}

func batchUpdate(ctx context.Context) bool {
	batchUpdateMu.Lock()
	userUpdates := batchUserUpdates
	tokenUpdates := batchTokenUpdates
	batchUserUpdates = make(map[int]userBatchUpdate)
	batchTokenUpdates = make(map[int]int)
	batchUpdateMu.Unlock()

	if len(userUpdates) == 0 && len(tokenUpdates) == 0 {
		return false
	}

	common.SysLog("batch update started")
	for id, value := range tokenUpdates {
		if err := applyTokenQuotaBatch(ctx, id, value); err != nil {
			common.SysLog("failed to batch update token quota: " + err.Error())
			batchUpdateMu.Lock()
			batchTokenUpdates[id] += value
			batchUpdateMu.Unlock()
		}
	}
	for id, update := range userUpdates {
		if err := applyUserAccountingBatch(ctx, id, update); err != nil {
			common.SysLog("failed to batch update user counters: " + err.Error())
			batchUpdateMu.Lock()
			pending := batchUserUpdates[id]
			pending.quota += update.quota
			pending.usedQuota += update.usedQuota
			pending.requestCount += update.requestCount
			batchUserUpdates[id] = pending
			batchUpdateMu.Unlock()
		}
	}
	common.SysLog("batch update finished")
	return true
}

func applyTokenQuotaBatch(ctx context.Context, id int, quota int) error {
	result := DB.WithContext(ctx).Model(&Token{}).Where("id = ?", id).Updates(
		map[string]interface{}{
			"remain_quota":  gorm.Expr("remain_quota + ?", quota),
			"used_quota":    gorm.Expr("used_quota - ?", quota),
			"accessed_time": common.GetTimestamp(),
		},
	)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func applyUserAccountingBatch(ctx context.Context, id int, update userBatchUpdate) error {
	if update.quota == 0 && update.usedQuota == 0 && update.requestCount == 0 {
		return nil
	}
	result := DB.WithContext(ctx).Model(&User{}).Where("id = ?", id).Updates(
		map[string]interface{}{
			"quota":         gorm.Expr("quota + ?", update.quota),
			"used_quota":    gorm.Expr("used_quota + ?", update.usedQuota),
			"request_count": gorm.Expr("request_count + ?", update.requestCount),
		},
	)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func RecordExist(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return false, err
}

func shouldUpdateRedis(fromDB bool, err error) bool {
	return common.RedisEnabled && fromDB && err == nil
}
