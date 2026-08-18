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

func TestMergeAggregatesByRequestedModelPreservesDimensionsAndQuantiles(t *testing.T) {
	left := model.ChannelModelPerfAggregate{
		ChannelId: 1, CredentialId: 11, RequestedModel: "m", UpstreamModel: "provider-a", Group: "g", Protocol: "openai",
		RequestCount: 2, RequestSuccessCount: 2, AttemptCount: 2, LatencyCount: 2,
		LatencyHistogram: model.NewObservationHistogram(), RequestLatencyHistogram: model.NewObservationHistogram(),
		TtftHistogram: model.NewObservationHistogram(), FRTHistogram: model.NewObservationHistogram(),
	}
	left.LatencyHistogram.Add(1234)
	left.LatencyHistogram.Add(1240)

	right := model.ChannelModelPerfAggregate{
		ChannelId: 1, CredentialId: 12, RequestedModel: "m", UpstreamModel: "provider-b", Group: "g", Protocol: "openai",
		RequestCount: 3, RequestSuccessCount: 2, AttemptCount: 3, LatencyCount: 3,
		LatencyHistogram: model.NewObservationHistogram(), RequestLatencyHistogram: model.NewObservationHistogram(),
		TtftHistogram: model.NewObservationHistogram(), FRTHistogram: model.NewObservationHistogram(),
	}
	right.LatencyHistogram.Add(2340)
	right.LatencyHistogram.Add(2345)
	right.LatencyHistogram.Add(2350)

	merged := mergeAggregatesByRequestedModel([]model.ChannelModelPerfAggregate{left, right})

	require.Len(t, merged, 1)
	assert.Equal(t, 0, merged[0].CredentialId)
	assert.Equal(t, int64(5), merged[0].RequestCount)
	assert.Equal(t, []string{"provider-a", "provider-b"}, merged[0].UpstreamModels)
	assert.Equal(t, int64(2350), merged[0].LatencyHistogram.P95())
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
	assert.False(t, result.TtftAvailable)
	assert.Equal(t, int64(0), result.TtftCount)
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

func TestAddAvailabilityMetricTracksTTFTSeparately(t *testing.T) {
	points := map[int][]AvailabilityPoint{
		7: {{BucketStart: 100, BucketEnd: 200}},
	}
	addAvailabilityMetricWithTTFT(points, []int{7}, 120, 7, 4, 3, 800, 4, 500, 2, 100, 100, 1)

	point := points[7][0]
	assert.Equal(t, int64(200), point.AvgLatencyMs)
	assert.Equal(t, int64(250), point.AvgTtftMs)
	assert.Equal(t, int64(2), point.TtftCount)
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

func TestCloneAggregateDoesNotShareMutableState(t *testing.T) {
	original := model.ChannelModelPerfAggregate{
		UpstreamModels:          []string{"provider-a"},
		LatencyHistogram:        model.NewObservationHistogram(),
		RequestLatencyHistogram: model.NewObservationHistogram(),
		TtftHistogram:           model.NewObservationHistogram(),
		FRTHistogram:            model.NewObservationHistogram(),
		ErrorCounts:             map[string]int64{"timeout": 1},
	}
	original.LatencyHistogram.Add(1234)
	original.RequestLatencyHistogram.Add(2345)
	original.TtftHistogram.Add(345)
	original.FRTHistogram.Add(456)

	cloned := cloneAggregate(original)
	original.UpstreamModels[0] = "provider-b"
	original.LatencyHistogram.Counts[0]++
	original.LatencyHistogram.Samples[0] = 9999
	original.LatencyHistogram.SampleWeights[0] = 9
	original.RequestLatencyHistogram.Counts[0]++
	original.TtftHistogram.Counts[0]++
	original.FRTHistogram.Counts[0]++
	original.ErrorCounts["timeout"]++

	assert.Equal(t, []string{"provider-a"}, cloned.UpstreamModels)
	assert.Equal(t, int64(1234), cloned.LatencyHistogram.P95())
	assert.Equal(t, int64(2345), cloned.RequestLatencyHistogram.P95())
	assert.Equal(t, int64(345), cloned.TtftHistogram.P95())
	assert.Equal(t, int64(456), cloned.FRTHistogram.P95())
	assert.Equal(t, map[string]int64{"timeout": 1}, cloned.ErrorCounts)
}

func TestRedisAggregateRestoresMillisecondSketchAndTTFTCount(t *testing.T) {
	key := bucketKey{channelId: 7, credentialId: 11, requestedModel: "model"}
	values := map[string]string{
		"request":                  "20",
		"request_ok":               "19",
		"request_hist_7":           "20",
		"request_hist_sample_1230": "19",
		"request_hist_sample_1240": "1",
		"ttft":                     "5000",
		"ttft_count":               "20",
		"ttft_hist_4":              "20",
		"ttft_hist_sample_250":     "20",
	}

	aggregate := redisValueToAggregate(key, values)

	assert.Equal(t, int64(1230), aggregate.RequestLatencyHistogram.P95())
	assert.Equal(t, int64(250), aggregate.TtftHistogram.P95())
	assert.Equal(t, int64(20), aggregate.TtftCount)
	assert.Equal(t, int64(20), aggregate.TtftHistogram.SampleCount)
}

func TestQuantizeRedisSketchValuePreservesLongLatency(t *testing.T) {
	tests := []struct {
		name  string
		input int64
		want  int64
	}{
		{name: "fine precision", input: 1234, want: 1230},
		{name: "fine precision boundary", input: 60004, want: 60000},
		{name: "medium precision after one minute", input: 61234, want: 61200},
		{name: "coarse precision after ten minutes", input: 601499, want: 601000},
		{name: "does not clip very long latency", input: 3723400, want: 3723000},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, quantizeRedisSketchValue(test.input))
		})
	}
}
