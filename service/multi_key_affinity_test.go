package service

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
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
