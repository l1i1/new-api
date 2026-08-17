package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelModelPerfMetricRoundTripsErrorCounts(t *testing.T) {
	aggregate := ChannelModelPerfAggregate{
		ChannelId: 1, RequestedModel: "model", ErrorCounts: map[string]int64{"timeout": 3, "upstream": 1},
		LatencyHistogram: NewObservationHistogram(), RequestLatencyHistogram: NewObservationHistogram(),
		TtftHistogram: NewObservationHistogram(), FRTHistogram: NewObservationHistogram(),
	}
	metric, err := BuildChannelModelPerfMetric(aggregate, "node-a", 100)
	require.NoError(t, err)
	require.NotEmpty(t, metric.ErrorCounts)

	var recovered ChannelModelPerfAggregate
	recovered.merge(metric)
	assert.Equal(t, aggregate.ErrorCounts, recovered.ErrorCounts)
}

func TestObservationHistogramP95PreservesMillisecondSamples(t *testing.T) {
	histogram := NewObservationHistogram()
	for latency := int64(1234); latency < 1254; latency++ {
		histogram.Add(latency)
	}

	// The 95th percentile is the 19th value in this twenty-sample set. The
	// fixed buckets would round it to 2500 ms, which is the regression this
	// sketch prevents for newly recorded metrics.
	assert.Equal(t, int64(1252), histogram.P95())
}

func TestObservationHistogramP95FallsBackForLegacyRows(t *testing.T) {
	histogram := ObservationHistogram{Counts: make([]int64, len(ObservationHistogramBounds)+1)}
	histogram.Add(1234)

	// Simulate an old serialized histogram that has no quantile samples.
	histogram.Samples = nil
	histogram.SampleCount = 0
	assert.Equal(t, int64(2500), histogram.P95())
}

func TestObservationHistogramP95WeightsMergedSketchesByPopulation(t *testing.T) {
	fast := NewObservationHistogram()
	for range 10_000 {
		fast.Add(1234)
	}
	slow := NewObservationHistogram()
	for range 400 {
		slow.Add(54_321)
	}

	fast.Merge(slow)

	// Slow requests are less than five percent of the merged population, so
	// an equally weighted merge of the two bounded sketches would be wrong.
	assert.Equal(t, int64(1234), fast.P95())
	assert.LessOrEqual(t, len(fast.Samples), observationQuantileSampleLimit)
	assert.Equal(t, fast.Count(), fast.SampleCount)
}

func TestObservationHistogramP95KeepsPreciseSamplesWhenMergingLegacyBuckets(t *testing.T) {
	precise := NewObservationHistogram()
	for latency := int64(1200); latency < 1300; latency++ {
		precise.Add(latency)
	}
	legacy := NewObservationHistogram()
	legacy.Add(60_000)
	legacy.Samples = nil
	legacy.SampleWeights = nil
	legacy.SampleCount = 0

	precise.Merge(legacy)

	assert.Equal(t, int64(1295), precise.P95())
	assert.Equal(t, precise.Count(), precise.SampleCount)
}

func TestMarshalObservationHistogramCompactsBufferedSamples(t *testing.T) {
	histogram := NewObservationHistogram()
	for latency := int64(0); latency < 400; latency++ {
		histogram.Add(latency)
	}
	require.Greater(t, len(histogram.Samples), observationQuantileSampleLimit)

	recovered := unmarshalObservationHistogram(marshalObservationHistogram(histogram))

	assert.LessOrEqual(t, len(recovered.Samples), observationQuantileSampleLimit)
	assert.Equal(t, histogram.Count(), recovered.SampleCount)
	assert.Equal(t, int64(379), recovered.P95())
}
