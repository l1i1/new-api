package model

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupLogBatchTest(t *testing.T) {
	t.Helper()
	previousDB := DB
	previousLogDB := LOG_DB
	previousMainType := common.MainDatabaseType()
	logBatchMu.Lock()
	previousQueue := logBatchQueue
	previousStop := logBatchStop
	previousDone := logBatchDone
	previousEnded := logBatchEnded
	logBatchQueue = nil
	logBatchStop = nil
	logBatchDone = nil
	logBatchEnded = false
	logBatchMu.Unlock()
	dsn := "file:log_batch_" + strconv.FormatInt(time.Now().UnixNano(), 10) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	DB = db
	LOG_DB = db
	require.NoError(t, db.AutoMigrate(&Log{}))
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
		common.SetMainDatabaseType(previousMainType)
		logBatchMu.Lock()
		logBatchQueue = previousQueue
		logBatchStop = previousStop
		logBatchDone = previousDone
		logBatchEnded = previousEnded
		logBatchMu.Unlock()
	})
}

func TestCreateLogBatchesWhenEnabled(t *testing.T) {
	setupLogBatchTest(t)
	logBatchQueue = make(chan *Log, logBatchQueueSize)

	for i := range 5 {
		require.NoError(t, createLog(&Log{UserId: 1, Type: LogTypeConsume, ModelName: "m-" + strconv.Itoa(i), Quota: i}))
	}
	require.Len(t, logBatchQueue, 5)

	buffer := make([]*Log, 0, len(logBatchQueue))
	for len(logBatchQueue) > 0 {
		buffer = append(buffer, <-logBatchQueue)
	}
	require.NoError(t, LOG_DB.CreateInBatches(buffer, 100).Error)

	var count int64
	require.NoError(t, LOG_DB.Model(&Log{}).Count(&count).Error)
	require.Equal(t, int64(5), count)
}

func TestEnqueueLogFallsBackWhenQueueFull(t *testing.T) {
	setupLogBatchTest(t)
	logBatchQueue = make(chan *Log, 1)
	logBatchQueue <- &Log{UserId: 1, Type: LogTypeConsume}

	require.NoError(t, enqueueLog(&Log{UserId: 2, Type: LogTypeConsume, ModelName: "overflow"}))

	var count int64
	require.NoError(t, LOG_DB.Model(&Log{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestFlushLogBatchDropsFailedBatchWithoutReplay(t *testing.T) {
	setupLogBatchTest(t)
	// A failed batch is not replayed because the commit state may be unknown.
	require.NoError(t, LOG_DB.Migrator().DropTable(&Log{}))

	buffer := []*Log{{UserId: 1, Type: LogTypeConsume}, {UserId: 2, Type: LogTypeConsume}}
	buffer = flushLogBatch(buffer)
	require.Empty(t, buffer)
}

func TestShutdownLogBatcherDrainsQueue(t *testing.T) {
	setupLogBatchTest(t)
	InitLogBatcher()

	for i := range 5 {
		require.NoError(t, createLog(&Log{UserId: 1, Type: LogTypeConsume, ModelName: "drain-" + strconv.Itoa(i)}))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, ShutdownLogBatcher(ctx))

	var count int64
	require.NoError(t, LOG_DB.Model(&Log{}).Count(&count).Error)
	require.Equal(t, int64(5), count)
}
