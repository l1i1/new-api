package service

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func buildChannelAffinityEvictContextForTest(t *testing.T) (*gin.Context, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	cacheKey := "test:codex_rule:default:fp_evict"
	setChannelAffinityContext(ctx, channelAffinityMeta{
		CacheKey:   cacheKey,
		TTLSeconds: 600,
		RuleName:   "codex cli trace",
	})
	return ctx, cacheKey
}

func TestEvictChannelAffinityOnUpstreamFailure_RemovesBindingForEmptyOutput(t *testing.T) {
	ctx, cacheKey := buildChannelAffinityEvictContextForTest(t)
	cache := getChannelAffinityCache()
	t.Cleanup(func() { _, _ = cache.DeleteMany([]string{cacheKey}) })

	require.NoError(t, cache.SetWithTTL(cacheKey, 13, 600*time.Second))
	_, found, err := cache.Get(cacheKey)
	require.NoError(t, err)
	require.True(t, found, "precondition: binding must exist")

	emptyErr := types.NewOpenAIError(
		errors.New("upstream returned empty final content"),
		types.ErrorCode("server_error"), 502, types.ErrOptionWithEmptyOutput(),
	)
	require.True(t, EvictChannelAffinityOnUpstreamFailure(ctx, emptyErr))

	_, found, err = cache.Get(cacheKey)
	require.NoError(t, err)
	require.False(t, found, "empty-output failure must evict the binding")
}

func TestEvictChannelAffinityOnUpstreamFailure_RemovesBindingForInBandFailure(t *testing.T) {
	ctx, cacheKey := buildChannelAffinityEvictContextForTest(t)
	cache := getChannelAffinityCache()
	t.Cleanup(func() { _, _ = cache.DeleteMany([]string{cacheKey}) })

	require.NoError(t, cache.SetWithTTL(cacheKey, 46, 600*time.Second))
	_, found, err := cache.Get(cacheKey)
	require.NoError(t, err)
	require.True(t, found, "precondition: binding must exist")

	// Mirrors a committed Responses stream that ends with response.failed
	// ("Upstream request failed"), where the transport status stays 200.
	inBandErr := types.NewOpenAIError(
		errors.New("Upstream request failed"),
		types.ErrorCode("upstream_error"), 503, types.ErrOptionWithUpstreamFailure(),
	)
	require.True(t, EvictChannelAffinityOnUpstreamFailure(ctx, inBandErr))

	_, found, err = cache.Get(cacheKey)
	require.NoError(t, err)
	require.False(t, found, "in-band upstream failure must evict the binding")
}

func TestEvictChannelAffinityOnUpstreamFailure_KeepsBindingForOtherErrors(t *testing.T) {
	ctx, cacheKey := buildChannelAffinityEvictContextForTest(t)
	cache := getChannelAffinityCache()
	t.Cleanup(func() { _, _ = cache.DeleteMany([]string{cacheKey}) })

	require.NoError(t, cache.SetWithTTL(cacheKey, 13, 600*time.Second))

	otherErr := types.NewOpenAIError(
		errors.New("upstream returned 500"), types.ErrorCode("server_error"), 500,
	)
	require.False(t, EvictChannelAffinityOnUpstreamFailure(ctx, otherErr))
	_, found, err := cache.Get(cacheKey)
	require.NoError(t, err)
	require.True(t, found, "unrelated failures must not evict the binding")

	unflagged := types.NewOpenAIError(
		errors.New("Upstream request failed"),
		types.ErrorCode("upstream_error"), 503,
	)
	require.False(t, EvictChannelAffinityOnUpstreamFailure(ctx, unflagged))
	_, found, err = cache.Get(cacheKey)
	require.NoError(t, err)
	require.True(t, found)
}

func TestEvictChannelAffinityOnUpstreamFailure_NilSafe(t *testing.T) {
	require.False(t, EvictChannelAffinityOnUpstreamFailure(nil, nil))
	rec := httptest.NewRecorder()
	ctxNoMeta, _ := gin.CreateTestContext(rec)
	flagged := types.NewOpenAIError(
		errors.New("upstream returned empty final content"),
		types.ErrorCode("server_error"), 502, types.ErrOptionWithEmptyOutput(),
	)
	require.False(t, EvictChannelAffinityOnUpstreamFailure(ctxNoMeta, flagged))
}

func TestRecordChannelAffinity_SkipsFailedRelay(t *testing.T) {
	ctx, cacheKey := buildChannelAffinityEvictContextForTest(t)
	cache := getChannelAffinityCache()
	t.Cleanup(func() { _, _ = cache.DeleteMany([]string{cacheKey}) })

	// A relay that failed after committing the stream reports 200 to the
	// client; without the marker the channel would be re-bound as healthy.
	common.SetContextKey(ctx, constant.ContextKeyRelayFailed, true)
	RecordChannelAffinity(ctx, 46)

	_, found, err := cache.Get(cacheKey)
	require.NoError(t, err)
	require.False(t, found, "a failed relay must not re-bind the affinity key")
}

func TestRecordChannelAffinity_RecordsSuccessfulRelay(t *testing.T) {
	ctx, cacheKey := buildChannelAffinityEvictContextForTest(t)
	cache := getChannelAffinityCache()
	t.Cleanup(func() { _, _ = cache.DeleteMany([]string{cacheKey}) })

	RecordChannelAffinity(ctx, 137)

	got, found, err := cache.Get(cacheKey)
	require.NoError(t, err)
	require.True(t, found, "a successful relay must bind the affinity key")
	require.Equal(t, 137, got)
}
