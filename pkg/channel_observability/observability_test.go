package channelobservability

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedisObservationKeyRoundTripPreservesSeparators(t *testing.T) {
	input := bucketKey{
		bucketTs: 100, channelId: 7, credentialId: 9,
		requestedModel: "model|with.dot", upstreamModel: "upstream/1",
		group: "group|a", protocol: "openai.responses",
	}
	decoded, ok := decodeRedisKey(encodeRedisKey(input))
	require.True(t, ok)
	assert.Equal(t, input, decoded)
}

func TestMergeAggregateCombinesCountersAndHistograms(t *testing.T) {
	left := model.ChannelModelPerfAggregate{
		ChannelId: 1, CredentialId: 2, RequestedModel: "m", Group: "g", Protocol: "openai",
		LatencyHistogram: model.NewObservationHistogram(), RequestLatencyHistogram: model.NewObservationHistogram(),
		TtftHistogram: model.NewObservationHistogram(), FRTHistogram: model.NewObservationHistogram(),
	}
	left.RequestCount = 1
	left.AttemptCount = 1
	left.RequestSuccessCount = 1
	left.LatencySumMs = 25
	left.LatencyCount = 1
	left.LatencyHistogram.Add(25)
	left.UpstreamModels = []string{"u1"}

	right := left
	right.RequestCount = 2
	right.AttemptCount = 2
	right.RequestSuccessCount = 1
	right.LatencySumMs = 1000
	right.LatencyHistogram = model.NewObservationHistogram()
	right.LatencyHistogram.Add(1000)
	right.UpstreamModels = []string{"u2"}

	merged := mergeAggregate([]model.ChannelModelPerfAggregate{left}, right)
	require.Len(t, merged, 1)
	assert.Equal(t, int64(3), merged[0].RequestCount)
	assert.Equal(t, int64(3), merged[0].AttemptCount)
	assert.Equal(t, int64(2), merged[0].RequestSuccessCount)
	assert.Equal(t, int64(1025), merged[0].LatencySumMs)
	assert.Equal(t, int64(2), merged[0].LatencyHistogram.Count())
	assert.Equal(t, []string{"u1", "u2"}, merged[0].UpstreamModels)
}

func TestMergeAggregateCombinesErrorTrends(t *testing.T) {
	left := model.ChannelModelPerfAggregate{
		ChannelId: 1, RequestedModel: "m", ErrorCounts: map[string]int64{"timeout": 2},
		LatencyHistogram: model.NewObservationHistogram(), RequestLatencyHistogram: model.NewObservationHistogram(),
		TtftHistogram: model.NewObservationHistogram(), FRTHistogram: model.NewObservationHistogram(),
	}
	right := model.ChannelModelPerfAggregate{
		ChannelId: 1, RequestedModel: "m", ErrorCounts: map[string]int64{"timeout": 3, "upstream": 1},
		LatencyHistogram: model.NewObservationHistogram(), RequestLatencyHistogram: model.NewObservationHistogram(),
		TtftHistogram: model.NewObservationHistogram(), FRTHistogram: model.NewObservationHistogram(),
	}
	merged := mergeAggregate([]model.ChannelModelPerfAggregate{left}, right)
	require.Len(t, merged, 1)
	assert.Equal(t, map[string]int64{"timeout": 5, "upstream": 1}, merged[0].ErrorCounts)
}

func TestAggregateResultMarksInsufficientSamples(t *testing.T) {
	value := model.ChannelModelPerfAggregate{
		RequestCount: 3, RequestSuccessCount: 1, SampleCount: 3, UsageCount: 1,
		ErrorCounts:      map[string]int64{"timeout": 2},
		LatencyHistogram: model.NewObservationHistogram(), RequestLatencyHistogram: model.NewObservationHistogram(),
		TtftHistogram: model.NewObservationHistogram(), FRTHistogram: model.NewObservationHistogram(),
	}
	result := aggregateResult(value)
	assert.True(t, result.SampleInsufficient)
	assert.False(t, result.SampleSufficient)
	assert.Equal(t, "insufficient", result.SampleStatus)
	assert.Equal(t, int64(3), result.SampleCount)
	assert.Equal(t, int64(1), result.RequestSuccessCount)
	assert.Equal(t, int64(2), result.RequestFailureCount)
	assert.Equal(t, map[string]int64{"timeout": 2}, result.ErrorTrends)
}

func TestAddAvailabilityMetricBuildsExactCountsAndWeightedLatency(t *testing.T) {
	points := map[int][]AvailabilityPoint{
		7: {{BucketStart: 100, BucketEnd: 200}},
	}
	channelIds := []int{7}
	addAvailabilityMetric(points, channelIds, 120, 7, 3, 2, 300, 3, 100, 100, 1)
	addAvailabilityMetric(points, channelIds, 150, 7, 1, 0, 300, 1, 100, 100, 1)
	point := &points[7][0]
	point.RequestFailureCount = nonNegative(point.RequestCount - point.RequestSuccessCount)
	point.RequestSuccessRate = percentage(point.RequestSuccessCount, point.RequestCount)

	assert.Equal(t, int64(4), point.RequestCount)
	assert.Equal(t, int64(2), point.RequestSuccessCount)
	assert.Equal(t, int64(2), point.RequestFailureCount)
	assert.Equal(t, 50.0, point.RequestSuccessRate)
	assert.Equal(t, int64(150), point.AvgLatencyMs)
}

func TestSortResultsHonorsRequestedOrder(t *testing.T) {
	results := []Result{
		{ChannelId: 1, RequestedModel: "a", RequestCount: 2},
		{ChannelId: 2, RequestedModel: "b", RequestCount: 5},
	}
	sortResults(results, "request_count", "asc")
	assert.Equal(t, 1, results[0].ChannelId)
	sortResults(results, "request_count", "desc")
	assert.Equal(t, 2, results[0].ChannelId)
}

func TestNormalizeErrorClassDropsSuccessAndBoundsFailures(t *testing.T) {
	assert.Equal(t, "", normalizeErrorClass("timeout", true))
	assert.Equal(t, "timeout", normalizeErrorClass(" Timeout ", false))
	assert.Equal(t, "unknown", normalizeErrorClass("", false))
	assert.Len(t, normalizeErrorClass("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz", false), 64)
}
