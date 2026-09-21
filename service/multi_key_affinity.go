package service

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/samber/hot"
)

const (
	multiKeySuccessCacheNamespace = "new-api:multi_key_success:v2"
	multiKeySuccessCacheTTL       = 24 * time.Hour
	multiKeySuccessCacheCapacity  = 100_000
)

var (
	multiKeySuccessCacheOnce    sync.Once
	multiKeySuccessCache        *cachex.HybridCache[string]
	multiKeySuccessFallbackOnce sync.Once
	multiKeySuccessFallback     *hot.HotCache[string, string]
)

func getMultiKeySuccessCache() *cachex.HybridCache[string] {
	multiKeySuccessCacheOnce.Do(func() {
		multiKeySuccessCache = cachex.NewHybridCache[string](cachex.HybridCacheConfig[string]{
			Namespace: cachex.Namespace(multiKeySuccessCacheNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.StringCodec{},
			Memory: func() *hot.HotCache[string, string] {
				return hot.NewHotCache[string, string](hot.LRU, multiKeySuccessCacheCapacity).
					WithTTL(multiKeySuccessCacheTTL).
					WithJanitor().
					Build()
			},
		})
	})
	return multiKeySuccessCache
}

func getMultiKeySuccessFallback() *hot.HotCache[string, string] {
	multiKeySuccessFallbackOnce.Do(func() {
		multiKeySuccessFallback = hot.NewHotCache[string, string](hot.LRU, multiKeySuccessCacheCapacity).
			WithTTL(multiKeySuccessCacheTTL).
			WithJanitor().
			Build()
	})
	return multiKeySuccessFallback
}

func multiKeySuccessCacheKey(channelID, tokenID int) string {
	if channelID <= 0 || tokenID <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", channelID, tokenID)
}

func GetLastSuccessfulMultiKeyFingerprint(channelID, tokenID int) (string, bool) {
	key := multiKeySuccessCacheKey(channelID, tokenID)
	if key == "" {
		return "", false
	}
	fingerprint, found, err := getMultiKeySuccessCache().Get(key)
	if err != nil {
		common.SysError(fmt.Sprintf("multi-key success cache get failed: channel_id=%d token_id=%d err=%v", channelID, tokenID, err))
		fallbackFingerprint, fallbackFound, _ := getMultiKeySuccessFallback().Get(getMultiKeySuccessCache().FullKey(key))
		return fallbackFingerprint, fallbackFound
	}
	return fingerprint, found
}

// RecordMultiKeySuccess remembers the key that completed the current request.
// The request-local guard avoids duplicate writes from multiple success paths.
func RecordMultiKeySuccess(c *gin.Context) {
	if c == nil || !common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) ||
		common.GetContextKeyInt(c, constant.ContextKeyForceMultiKeyCredentialID) > 0 ||
		common.GetContextKeyBool(c, constant.ContextKeyForceMultiKeyIndex) ||
		common.GetContextKeyBool(c, constant.ContextKeyChannelMultiKeySuccessRecorded) {
		return
	}
	channelID := common.GetContextKeyInt(c, constant.ContextKeyChannelId)
	tokenID := common.GetContextKeyInt(c, constant.ContextKeyTokenId)
	selectedKey := common.GetContextKeyString(c, constant.ContextKeyChannelKey)
	if selectedKey == "" {
		return
	}
	keyFingerprint := model.ChannelCredentialFingerprint(selectedKey)
	key := multiKeySuccessCacheKey(channelID, tokenID)
	if key == "" {
		return
	}
	if err := getMultiKeySuccessCache().SetWithTTL(key, keyFingerprint, multiKeySuccessCacheTTL); err != nil {
		common.SysError(fmt.Sprintf("multi-key success cache set failed: channel_id=%d token_id=%d err=%v", channelID, tokenID, err))
		getMultiKeySuccessFallback().SetWithTTL(getMultiKeySuccessCache().FullKey(key), keyFingerprint, multiKeySuccessCacheTTL)
	}
	common.SetContextKey(c, constant.ContextKeyChannelMultiKeySuccessRecorded, true)
}

// MarkCurrentMultiKeyTried records the credential only when the selected
// channel key is about to be sent upstream. Selection may happen more than
// once before an attempt (for example in the distributor and controller), so
// recording inside SetupContextForSelectedChannel would consume keys that were
// never actually used.
func MarkCurrentMultiKeyTried(c *gin.Context) {
	if c == nil || common.RetryTimes <= 0 ||
		!common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) ||
		common.GetContextKeyInt(c, constant.ContextKeyForceMultiKeyCredentialID) > 0 ||
		common.GetContextKeyBool(c, constant.ContextKeyForceMultiKeyIndex) {
		return
	}

	channelID := common.GetContextKeyInt(c, constant.ContextKeyChannelId)
	selectedKey := common.GetContextKeyString(c, constant.ContextKeyChannelKey)
	if channelID <= 0 || selectedKey == "" {
		return
	}

	state, _ := common.GetContextKeyType[map[int]map[string]struct{}](c, constant.ContextKeyChannelMultiKeyTried)
	if state == nil {
		state = make(map[int]map[string]struct{})
		common.SetContextKey(c, constant.ContextKeyChannelMultiKeyTried, state)
	}
	tried := state[channelID]
	if tried == nil {
		tried = make(map[string]struct{})
		state[channelID] = tried
	}
	tried[model.ChannelCredentialFingerprint(selectedKey)] = struct{}{}
}

func IsMultiKeyRetryExhausted(err error) bool {
	return errors.Is(err, model.ErrNoUntriedMultiKey)
}

// ShouldRotateMultiKeyCredentialOn reports whether this failure marks the
// credential rather than the channel: either the status code is in the
// multi-key retry list, or the error text matches a credential-scoped keyword
// such as "insufficient credits". Callers own the surrounding gates -- a
// never-retry verdict outranks this, and only a multi-key channel can act on
// it; on a single-key channel the caller treats it as a plain channel retry.
func ShouldRotateMultiKeyCredentialOn(statusCode int, message string) bool {
	return operation_setting.ShouldRotateMultiKeyCredential(statusCode) ||
		operation_setting.MatchesMultiKeyCredentialRetryKeywords(message)
}
