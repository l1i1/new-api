package service

import (
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

func TestMultiKeyAffinityFallsBackToProcessCacheWhenRedisFails(t *testing.T) {
	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	common.RedisEnabled = true
	common.RDB = client
	multiKeySuccessCacheOnce = sync.Once{}
	multiKeySuccessCache = nil
	multiKeySuccessFallbackOnce = sync.Once{}
	multiKeySuccessFallback = nil
	t.Cleanup(func() {
		_ = client.Close()
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
		multiKeySuccessCacheOnce = sync.Once{}
		multiKeySuccessCache = nil
		multiKeySuccessFallbackOnce = sync.Once{}
		multiKeySuccessFallback = nil
	})

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 9621)
	common.SetContextKey(ctx, constant.ContextKeyTokenId, 9622)
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "fallback-key")

	RecordMultiKeySuccess(ctx)
	fingerprint, found := GetLastSuccessfulMultiKeyFingerprint(9621, 9622)
	require.True(t, found)
	require.Equal(t, model.ChannelCredentialFingerprint("fallback-key"), fingerprint)
}

// TestDecideRelayRetryClearsMultiKeyAffinityOn429 pins the 2026-10-29 capacity
// stickiness fix: a 429 from the credential that affinity picked must drop the
// token->key binding, so the next selection starts from a sibling key instead of
// draining the same one (ch8 key #4 carried 61% of a token's prompt tokens and
// 33 of its 56 429s while three enabled keys kept headroom).
func TestDecideRelayRetryClearsMultiKeyAffinityOn429(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 9631)
	common.SetContextKey(ctx, constant.ContextKeyTokenId, 9632)
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "capacity-key")
	RecordMultiKeySuccess(ctx)
	_, found := GetLastSuccessfulMultiKeyFingerprint(9631, 9632)
	require.True(t, found)

	rateLimited := types.NewOpenAIError(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	_ = DecideRelayRetry(ctx, rateLimited, 3)

	_, found = GetLastSuccessfulMultiKeyFingerprint(9631, 9632)
	require.False(t, found, "a 429 must forget the pinned key so the retry can rotate")
}

// A 400 is a statement about the request, not about capacity: the binding has to
// survive so the conversation keeps its prompt-cache key.
func TestDecideRelayRetryKeepsMultiKeyAffinityOn400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 9641)
	common.SetContextKey(ctx, constant.ContextKeyTokenId, 9642)
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "bad-request-key")
	RecordMultiKeySuccess(ctx)

	badRequest := types.NewOpenAIError(errors.New("bad request"), types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest)
	_ = DecideRelayRetry(ctx, badRequest, 3)

	_, found := GetLastSuccessfulMultiKeyFingerprint(9641, 9642)
	require.True(t, found, "a 400 must not throw the credential binding away")
}
