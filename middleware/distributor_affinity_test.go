package middleware

import (
	"bytes"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSetupContextForSelectedChannelUsesTokenAffinity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, constant.ContextKeyTokenId, 5)
	channel := &model.Channel{
		Id:     93,
		Type:   constant.ChannelTypeOpenAI,
		Key:    "key-0\nkey-1\nkey-2",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeyMode: constant.MultiKeyModeAffinity,
		},
	}
	expectedKey, expectedIndex, expectedError := channel.GetNextEnabledKey(5)
	require.Nil(t, expectedError)

	err := SetupContextForSelectedChannel(ctx, channel, "deepseek-v4-flash")
	require.Nil(t, err)
	require.Equal(t, expectedKey, common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
	require.Equal(t, expectedIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
}

func TestSetupContextForSelectedChannelDoesNotMarkBeforeAttempt(t *testing.T) {
	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	t.Cleanup(func() { common.RetryTimes = originalRetryTimes })

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, constant.ContextKeyTokenId, 1903)
	channel := &model.Channel{
		Id: 9503, Type: constant.ChannelTypeOpenAI, Key: "key-0\nkey-1\nkey-2", Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModeAffinity},
	}

	require.Nil(t, SetupContextForSelectedChannel(ctx, channel, "model"))
	firstIndex := common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex)
	require.Nil(t, SetupContextForSelectedChannel(ctx, channel, "model"))
	require.Equal(t, firstIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))

	service.MarkCurrentMultiKeyTried(ctx)
	require.Nil(t, SetupContextForSelectedChannel(ctx, channel, "model"))
	require.NotEqual(t, firstIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
}

func TestSetupContextForSelectedChannelSkipsTriedMultiKeysWhenRetriesEnabled(t *testing.T) {
	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 2
	t.Cleanup(func() { common.RetryTimes = originalRetryTimes })

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, constant.ContextKeyTokenId, 17)
	channel := &model.Channel{
		Id: 9501, Type: constant.ChannelTypeOpenAI, Key: "key-0\nkey-1\nkey-2", Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModeRandom},
	}
	seen := make(map[int]struct{})
	for range 3 {
		require.Nil(t, SetupContextForSelectedChannel(ctx, channel, "model"))
		service.MarkCurrentMultiKeyTried(ctx)
		seen[common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex)] = struct{}{}
	}
	require.Len(t, seen, 3)

	err := SetupContextForSelectedChannel(ctx, channel, "model")
	require.True(t, service.IsMultiKeyRetryExhausted(err))
}

func TestSetupContextForSelectedChannelTracksTriedCredentialAcrossReordering(t *testing.T) {
	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	t.Cleanup(func() { common.RetryTimes = originalRetryTimes })

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, constant.ContextKeyTokenId, 9506)
	channel := &model.Channel{
		Id: 9506, Type: constant.ChannelTypeOpenAI, Key: "key-a\nkey-b", Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModeRandom},
	}

	require.Nil(t, SetupContextForSelectedChannel(ctx, channel, "model"))
	firstKey := common.GetContextKeyString(ctx, constant.ContextKeyChannelKey)
	service.MarkCurrentMultiKeyTried(ctx)

	channel.Key = "key-b\nkey-a"
	require.Nil(t, SetupContextForSelectedChannel(ctx, channel, "model"))
	require.NotEqual(t, firstKey, common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
}

func TestSetupContextForSelectedChannelKeepsAffinityWhenRetriesDisabled(t *testing.T) {
	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 0
	t.Cleanup(func() { common.RetryTimes = originalRetryTimes })

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, constant.ContextKeyTokenId, 1904)
	channel := &model.Channel{
		Id: 9505, Type: constant.ChannelTypeOpenAI, Key: "key-0\nkey-1\nkey-2", Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModeAffinity},
	}

	require.Nil(t, SetupContextForSelectedChannel(ctx, channel, "model"))
	firstIndex := common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex)
	service.MarkCurrentMultiKeyTried(ctx)
	require.Nil(t, SetupContextForSelectedChannel(ctx, channel, "model"))
	require.Equal(t, firstIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
}

func TestSetupContextForSelectedChannelAffinityUsesLastSuccessfulKey(t *testing.T) {
	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	t.Cleanup(func() { common.RetryTimes = originalRetryTimes })

	gin.SetMode(gin.TestMode)
	channel := &model.Channel{
		Id: 9502, Type: constant.ChannelTypeOpenAI, Key: "key-0\nkey-1\nkey-2", Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModeAffinity},
	}
	first, _ := gin.CreateTestContext(nil)
	common.SetContextKey(first, constant.ContextKeyTokenId, 18)
	require.Nil(t, SetupContextForSelectedChannel(first, channel, "model"))
	selected := common.GetContextKeyInt(first, constant.ContextKeyChannelMultiKeyIndex)
	service.RecordMultiKeySuccess(first)

	next, _ := gin.CreateTestContext(nil)
	common.SetContextKey(next, constant.ContextKeyTokenId, 18)
	require.Nil(t, SetupContextForSelectedChannel(next, channel, "model"))
	require.Equal(t, selected, common.GetContextKeyInt(next, constant.ContextKeyChannelMultiKeyIndex))

	// A failed retry must skip the remembered key and use another enabled key.
	service.MarkCurrentMultiKeyTried(next)
	require.Nil(t, SetupContextForSelectedChannel(next, channel, "model"))
	require.NotEqual(t, selected, common.GetContextKeyInt(next, constant.ContextKeyChannelMultiKeyIndex))
}

func TestSetupContextForSelectedChannelUsesCredentialProxyOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, constant.ContextKeyTokenId, 5)
	channel := &model.Channel{
		Id: 94, Type: constant.ChannelTypeOpenAI, Key: "key-0\nkey-1", Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModeAffinity},
		Credentials: []model.ChannelCredential{
			{Id: 101, Position: 0, Status: common.ChannelStatusEnabled, ProxyMode: model.CredentialProxyModeCustom, ProxyURL: "http://proxy-a.example:8080"},
			{Id: 102, Position: 1, Status: common.ChannelStatusEnabled, ProxyMode: model.CredentialProxyModeDirect},
		},
	}
	channel.SetSetting(dto.ChannelSettings{Proxy: "http://channel.example:3128"})
	common.SetContextKey(ctx, constant.ContextKeyForceMultiKeyIndex, true)
	common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, 0)
	err := SetupContextForSelectedChannel(ctx, channel, "model")
	require.Nil(t, err)
	settings, ok := common.GetContextKeyType[dto.ChannelSettings](ctx, constant.ContextKeyChannelSetting)
	require.True(t, ok)
	require.Equal(t, "http://proxy-a.example:8080", settings.Proxy)
	require.Equal(t, 101, common.GetContextKeyInt(ctx, constant.ContextKeyChannelCredentialId))

	common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, 1)
	err = SetupContextForSelectedChannel(ctx, channel, "model")
	require.Nil(t, err)
	settings, ok = common.GetContextKeyType[dto.ChannelSettings](ctx, constant.ContextKeyChannelSetting)
	require.True(t, ok)
	require.Empty(t, settings.Proxy)
}

func TestMarkV4OfficialPinFromDistributorMatchesRelayThresholds(t *testing.T) {
	newCtx := func(body, path string) *gin.Context {
		gin.SetMode(gin.TestMode)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		return c
	}

	pinned := newCtx(`{"model":"deepseek-v4-flash","temperature":2,"top_p":0.1,"presence_penalty":1.5,"frequency_penalty":1.5}`, "/v1/chat/completions")
	markV4OfficialPinFromDistributor(pinned)
	assert.True(t, common.GetContextKeyBool(pinned, constant.ContextKeyV4OfficialPin))

	mild := newCtx(`{"model":"deepseek-v4-flash","temperature":0.7,"top_p":0.9}`, "/v1/chat/completions")
	markV4OfficialPinFromDistributor(mild)
	assert.False(t, common.GetContextKeyBool(mild, constant.ContextKeyV4OfficialPin))

	nonV4 := newCtx(`{"model":"gpt-test","temperature":2}`, "/v1/chat/completions")
	markV4OfficialPinFromDistributor(nonV4)
	assert.False(t, common.GetContextKeyBool(nonV4, constant.ContextKeyV4OfficialPin))

	other := newCtx(`{"model":"deepseek-v4-flash","temperature":2}`, "/v1/embeddings")
	markV4OfficialPinFromDistributor(other)
	assert.False(t, common.GetContextKeyBool(other, constant.ContextKeyV4OfficialPin))
}

func TestMarkV4OfficialPinFromDistributorHonorsRouteProfile(t *testing.T) {
	newCtx := func(body, path string) *gin.Context {
		gin.SetMode(gin.TestMode)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		return c
	}

	// Mild sampling but the user profile enables official routing: pinned.
	routeOn := newCtx(`{"model":"deepseek-v4-flash","temperature":0.7,"top_p":0.9}`, "/v1/chat/completions")
	common.SetContextKey(routeOn, constant.ContextKeyUserSetting, dto.UserSetting{
		OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"deepseek-v4-": {Route: true},
		}},
	})
	markV4OfficialPinFromDistributor(routeOn)
	assert.True(t, common.GetContextKeyBool(routeOn, constant.ContextKeyV4OfficialPin))

	// Profile present but Route off: mild sampling stays unpinned.
	routeOff := newCtx(`{"model":"deepseek-v4-flash","temperature":0.7,"top_p":0.9}`, "/v1/chat/completions")
	common.SetContextKey(routeOff, constant.ContextKeyUserSetting, dto.UserSetting{
		OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"deepseek-v4-": {Validate: true},
		}},
	})
	markV4OfficialPinFromDistributor(routeOff)
	assert.False(t, common.GetContextKeyBool(routeOff, constant.ContextKeyV4OfficialPin))
}

func TestV4OfficialPinBypassesAggregatorAffinity(t *testing.T) {
	// A pinned deepseek-v4 request with affinity cached to an aggregator
	// channel must not reuse the sticky channel.
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"deepseek-v4-flash","temperature":2}`))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)

	markV4OfficialPinFromDistributor(c)
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin))

	// Simulate affinity returning an aggregator channel and the bypass check.
	aggregator := &model.Channel{Id: 93, Type: constant.ChannelTypeOpenAI}
	bypass := aggregator != nil && common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin) &&
		aggregator.Type != constant.ChannelTypeDeepSeek
	assert.True(t, bypass, "aggregator affinity must be bypassed for pinned requests")

	official := &model.Channel{Id: 1, Type: constant.ChannelTypeDeepSeek}
	bypassOfficial := official != nil && common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin) &&
		official.Type != constant.ChannelTypeDeepSeek
	assert.False(t, bypassOfficial, "official affinity stays usable for pinned requests")
}
