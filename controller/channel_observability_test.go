package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseObservabilityQuerySupportsContractFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("GET", "/api/observability/channel-model?start=1700000000&end=1700003600&channel_id=7&model=gpt-test&group=vip&protocol=openai&page=2&page_size=25&sort_by=p95_ttft_ms&sort_order=asc", nil)

	query, err := parseObservabilityQuery(context, true)
	require.NoError(t, err)
	assert.Equal(t, int64(1700000000), query.StartTs)
	assert.Equal(t, int64(1700003600), query.EndTs)
	assert.Equal(t, 7, query.ChannelId)
	assert.Equal(t, "gpt-test", query.RequestedModel)
	assert.Equal(t, "vip", query.Group)
	assert.Equal(t, "openai", query.Protocol)
	assert.Equal(t, 2, query.Page)
	assert.Equal(t, 25, query.PageSize)
	assert.Equal(t, "p95_ttft_ms", query.SortBy)
	assert.Equal(t, "asc", query.SortOrder)
}

func TestParseObservabilityQueryRejectsInvalidSortAndRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("GET", "/api/observability/channel-model?sort_by=secret_metric", nil)
	_, err := parseObservabilityQuery(context, true)
	require.Error(t, err)

	context.Request = httptest.NewRequest("GET", "/api/observability/channel-model?start=1700003600&end=1700000000", nil)
	_, err = parseObservabilityQuery(context, true)
	assert.Error(t, err)
}
