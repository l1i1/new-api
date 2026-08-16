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
