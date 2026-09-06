package service

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

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

func TestEvictChannelAffinityOnEmptyOutput_RemovesBinding(t *testing.T) {
	ctx, cacheKey := buildChannelAffinityEvictContextForTest(t)
	cache := getChannelAffinityCache()
	t.Cleanup(func() { _, _ = cache.DeleteMany([]string{cacheKey}) })

	require.NoError(t, cache.SetWithTTL(cacheKey, 13, 600 * time.Second))
	_, found, err := cache.Get(cacheKey)
	require.NoError(t, err)
	require.True(t, found, "precondition: binding must exist")

	emptyErr := types.NewOpenAIError(
		errors.New("upstream returned empty final content"),
		types.ErrorCode("server_error"), 502, types.ErrOptionWithEmptyOutput(),
	)
	require.True(t, EvictChannelAffinityOnEmptyOutput(ctx, emptyErr))

	_, found, err = cache.Get(cacheKey)
	require.NoError(t, err)
	require.False(t, found, "empty-output failure must evict the binding")
}

func TestEvictChannelAffinityOnEmptyOutput_KeepsBindingForOtherErrors(t *testing.T) {
	ctx, cacheKey := buildChannelAffinityEvictContextForTest(t)
	cache := getChannelAffinityCache()
	t.Cleanup(func() { _, _ = cache.DeleteMany([]string{cacheKey}) })

	require.NoError(t, cache.SetWithTTL(cacheKey, 13, 600 * time.Second))

	otherErr := types.NewOpenAIError(
		errors.New("upstream returned 500"), types.ErrorCode("server_error"), 500,
	)
	require.False(t, EvictChannelAffinityOnEmptyOutput(ctx, otherErr))
	_, found, err := cache.Get(cacheKey)
	require.NoError(t, err)
	require.True(t, found, "unrelated failures must not evict the binding")

	emptyWithoutFlag := types.NewOpenAIError(
		errors.New("upstream returned empty final content"),
		types.ErrorCode("server_error"), 502,
	)
	require.False(t, EvictChannelAffinityOnEmptyOutput(ctx, emptyWithoutFlag))
	_, found, err = cache.Get(cacheKey)
	require.NoError(t, err)
	require.True(t, found)
}

func TestEvictChannelAffinityOnEmptyOutput_NilSafe(t *testing.T) {
	require.False(t, EvictChannelAffinityOnEmptyOutput(nil, nil))
	rec := httptest.NewRecorder()
	ctxNoMeta, _ := gin.CreateTestContext(rec)
	flagged := types.NewOpenAIError(
		errors.New("upstream returned empty final content"),
		types.ErrorCode("server_error"), 502, types.ErrOptionWithEmptyOutput(),
	)
	require.False(t, EvictChannelAffinityOnEmptyOutput(ctxNoMeta, flagged))
}
