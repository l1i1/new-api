package perfmetrics

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestQuerySummaryAllAggregatesAverageTtftAcrossBuckets(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.PerfMetric{}))

	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		clearHotBuckets()
	})
	clearHotBuckets()

	now := time.Now().Unix()
	rows := []model.PerfMetric{
		{
			ModelName:      "test-model",
			Group:          "default",
			BucketTs:       now - 120,
			RequestCount:   3,
			SuccessCount:   3,
			TotalLatencyMs: 900,
			TtftSumMs:      200,
			TtftCount:      2,
		},
		{
			ModelName:      "test-model",
			Group:          "premium",
			BucketTs:       now - 60,
			RequestCount:   2,
			SuccessCount:   1,
			TotalLatencyMs: 1100,
			TtftSumMs:      400,
			TtftCount:      1,
		},
	}
	require.NoError(t, db.Create(&rows).Error)

	result, err := QuerySummaryAll(1, nil)
	require.NoError(t, err)
	require.Len(t, result.Models, 1)
	require.Equal(t, int64(200), result.Models[0].AvgTtftMs)
	require.Equal(t, int64(400), result.Models[0].AvgLatencyMs)
	require.Equal(t, float64(80), result.Models[0].SuccessRate)
}

func TestRecentSuccessSeriesAlignsPointsToHoursAndSkipsEmptyHours(t *testing.T) {
	hourTs := time.Now().Unix() / 3600 * 3600
	buckets := map[int64]counters{
		// Two sub-hour buckets inside the same hour must merge into one point.
		hourTs - 5*3600:        {requestCount: 1, successCount: 1},
		hourTs - 5*3600 + 1800: {requestCount: 1, successCount: 0},
		hourTs:                 {requestCount: 2, successCount: 2},
		hourTs - 3*3600 + 2400: {requestCount: 0, successCount: 0},
	}

	points := recentSuccessSeries(buckets)
	require.Len(t, points, 2)
	require.Equal(t, hourTs-5*3600, points[0].Ts)
	require.Equal(t, float64(50), points[0].SuccessRate)
	require.Equal(t, hourTs, points[1].Ts)
	require.Equal(t, float64(100), points[1].SuccessRate)
	require.Nil(t, recentSuccessSeries(nil))
}

func clearHotBuckets() {
	hotBuckets.Range(func(key, _ any) bool {
		hotBuckets.Delete(key)
		return true
	})
}
