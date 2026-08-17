package controller

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseObservabilityQuerySupportsContractFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("GET", "/api/observability/channel-model?start=1700000000&end=1700003600&channel_id=7&model=gpt-test&group=vip&protocol=openai&page=2&page_size=25&sort_by=p95_ttft_ms&sort_order=asc&aggregate_by_model=true", nil)

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
	assert.True(t, query.AggregateByModel)
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

	context.Request = httptest.NewRequest("GET", "/api/observability/channel-model?aggregate_by_model=maybe", nil)
	_, err = parseObservabilityQuery(context, true)
	assert.Error(t, err)
}

func TestParseObservationChannelIdsDeduplicatesAndValidates(t *testing.T) {
	ids, err := parseObservationChannelIds("7, 8,7")
	require.NoError(t, err)
	assert.Equal(t, []int{7, 8}, ids)

	_, err = parseObservationChannelIds("")
	assert.Error(t, err)
	_, err = parseObservationChannelIds("7,invalid")
	assert.Error(t, err)
}

func TestBuildMultiKeyTestResultIncludesSafeErrorDetails(t *testing.T) {
	credential := model.ChannelCredential{Id: 11, Position: 2, Fingerprint: "fingerprint"}
	apiError := relaytypes.NewErrorWithStatusCode(
		errors.New("upstream https://user:secret@example.com/v1 failed\nwith details"),
		relaytypes.ErrorCode("upstream_error"),
		429,
	)
	result := buildMultiKeyTestResult(credential, testResult{newAPIError: apiError}, 0, nil)

	assert.Equal(t, "failed", result.Status)
	assert.Equal(t, 429, result.HTTPStatus)
	assert.Equal(t, "upstream_error", result.ErrorCode)
	assert.Equal(t, "rate_limited", result.ErrorClass)
	assert.NotContains(t, result.ErrorMessage, "secret")
	assert.NotContains(t, result.ErrorMessage, "\n")
	assert.Contains(t, result.ErrorMessage, "https://***")
}

func TestSanitizeMultiKeyTestErrorBoundsResponseSize(t *testing.T) {
	message := sanitizeMultiKeyTestError(strings.Repeat("x", 600))
	assert.Len(t, []rune(message), 515)
	assert.True(t, strings.HasSuffix(message, "..."))
}
