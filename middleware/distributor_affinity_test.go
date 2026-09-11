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

func TestMarkV4OfficialPinFromDistributorNoSamplingAutoPin(t *testing.T) {
	newCtx := func(body, path string) *gin.Context {
		gin.SetMode(gin.TestMode)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		return c
	}

	// Extreme sampling, thinking toggles, and logprobs no longer auto-pin:
	// the official pin is controlled solely by the user's Official Fit
	// route dimension, so non-Route traffic keeps aggregator affinity.
	extreme := newCtx(`{"model":"deepseek-v4-flash","temperature":2,"top_p":0.1,"presence_penalty":1.5,"frequency_penalty":1.5,"thinking":{"type":"disabled"},"logprobs":true}`, "/v1/chat/completions")
	markV4OfficialPinFromDistributor(extreme)
	assert.False(t, common.GetContextKeyBool(extreme, constant.ContextKeyV4OfficialPin))

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

	// kimi-k3 with the Route profile pins to the Moonshot official channel;
	// without the profile it stays unpinned even for "extreme" sampling.
	k3Route := newCtx(`{"model":"kimi-k3","temperature":0.7}`, "/v1/chat/completions")
	common.SetContextKey(k3Route, constant.ContextKeyUserSetting, dto.UserSetting{
		OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"kimi-k3": {Route: true},
		}},
	})
	markV4OfficialPinFromDistributor(k3Route)
	assert.True(t, common.GetContextKeyBool(k3Route, constant.ContextKeyV4OfficialPin))

	k3Plain := newCtx(`{"model":"kimi-k3","temperature":2}`, "/v1/chat/completions")
	markV4OfficialPinFromDistributor(k3Plain)
	assert.False(t, common.GetContextKeyBool(k3Plain, constant.ContextKeyV4OfficialPin))
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

	officialType := model.OfficialFitChannelType("deepseek-v4-flash")
	pinned := common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin)
	assert.False(t, officialPinAllowsAffinity(pinned, officialType, constant.ChannelTypeOpenAI, false),
		"aggregator affinity must be bypassed for pinned requests")
	assert.True(t, officialPinAllowsAffinity(pinned, officialType, constant.ChannelTypeDeepSeek, true),
		"official affinity stays usable for pinned requests")
}

// A request that is NOT pinned must never be dragged onto the official channel
// by an affinity binding. The binding for an official channel can only have
// been written by earlier pinned traffic, so reusing it would keep the whole
// affinity key (token-level when key sources fall back to token_id) on the
// official channel forever — including disabled-thinking requests, and even
// after the user's Route dimension was switched off.
func TestOfficialPinAllowsAffinityDropsStaleOfficialBindingWhenUnpinned(t *testing.T) {
	officialType := model.OfficialFitChannelType("deepseek-v4-flash")
	require.NotZero(t, officialType)

	// Unpinned: aggregators are fine, the official type is excluded.
	assert.True(t, officialPinAllowsAffinity(false, officialType, constant.ChannelTypeOpenAI, false))
	assert.False(t, officialPinAllowsAffinity(false, officialType, officialType, false))

	// Pinned: the cached channel must be official-behaving (type OR allowlist).
	assert.False(t, officialPinAllowsAffinity(true, officialType, constant.ChannelTypeOpenAI, false))
	assert.True(t, officialPinAllowsAffinity(true, officialType, officialType, true))
	assert.True(t, officialPinAllowsAffinity(true, officialType, constant.ChannelTypeOpenAI, true),
		"a channel that declared the model is a valid pin target")

	// Families without an official channel are never restricted.
	assert.True(t, officialPinAllowsAffinity(false, 0, constant.ChannelTypeOpenAI, false))
	assert.True(t, officialPinAllowsAffinity(true, 0, constant.ChannelTypeOpenAI, false))
}

// The unpinned exclusion targets the official channel *type* only. A channel
// that declares a model in official_fit_models but carries an aggregator type
// is normal traffic for unpinned requests and must keep its affinity, or every
// unpinned request to a verified reseller would have its prompt cache broken.
func TestOfficialPinAllowsAffinityKeepsAllowlistAggregatorForUnpinned(t *testing.T) {
	officialType := model.OfficialFitChannelType("deepseek-v4.1-flash")
	require.NotZero(t, officialType)

	// Aggregator type (not the official one) that declared official behavior.
	assert.True(t, officialPinAllowsAffinity(false, officialType, constant.ChannelTypeOpenAI, true),
		"an allowlist aggregator keeps affinity for unpinned requests")
	// Pinned requests may use it too.
	assert.True(t, officialPinAllowsAffinity(true, officialType, constant.ChannelTypeOpenAI, true))
	// But an aggregator that did NOT declare is still rejected when pinned.
	assert.False(t, officialPinAllowsAffinity(true, officialType, constant.ChannelTypeOpenAI, false))
}

// The decision is family-generic: it keys off the model's own official family,
// not a hardcoded DeepSeek type. Classification stays the model package's job
// (OfficialFitChannelType); the affinity helper only consumes the result.
func TestOfficialPinAllowsAffinityIsFamilyGeneric(t *testing.T) {
	require.Equal(t, constant.ChannelTypeMoonshot, model.OfficialFitChannelType("kimi-k3"))
	require.Equal(t, constant.ChannelTypeZhipu_v4, model.OfficialFitChannelType("glm-5.3"))
	require.Equal(t, constant.ChannelTypeDeepSeek, model.OfficialFitChannelType("deepseek-v4.1-flash"))

	// Pinned kimi-k3 keeps its official (Moonshot) binding, drops aggregators.
	assert.True(t, officialPinAllowsAffinity(true, constant.ChannelTypeMoonshot, constant.ChannelTypeMoonshot, true))
	assert.False(t, officialPinAllowsAffinity(true, constant.ChannelTypeMoonshot, constant.ChannelTypeOpenAI, false))

	// A DeepSeek-type channel is not official for glm-5.3, so a pinned glm
	// request must drop it even though the channel type is itself an official
	// type for a different family.
	glmOfficialType := model.OfficialFitChannelType("glm-5.3")
	deepSeekType := model.OfficialFitChannelType("deepseek-v4-flash")
	assert.False(t, officialPinAllowsAffinity(true, glmOfficialType, deepSeekType, false))
	assert.True(t, officialPinAllowsAffinity(true, glmOfficialType, constant.ChannelTypeZhipu_v4, true))
}

// A channel that declares a model in official_fit_models is official-behaving
// for that model even though its channel type is a plain aggregator (type 1).
// This is what lets a reseller that maps deepseek-v4.1-flash onto the official
// deepseek-flash serve pinned v4.1 traffic. Its unlisted models stay
// non-official, so a mixed channel is never wholesale promoted.
func TestOfficialFitChannelAllowlistIsPerModel(t *testing.T) {
	mixed := &model.Channel{Type: constant.ChannelTypeOpenAI}
	mixed.SetOtherSettings(dto.ChannelOtherSettings{
		OfficialFitModels: []string{" DeepSeek-V4.1-Flash ", "deepseek-v4.1-flash"},
	})

	assert.True(t, mixed.IsOfficialFitChannelForModel("deepseek-v4.1-flash"))
	assert.True(t, mixed.IsOfficialFitChannelForModel("DEEPSEEK-V4.1-FLASH"))
	// A model the channel did not declare is not official on it, even though it
	// belongs to the same family.
	assert.False(t, mixed.IsOfficialFitChannelForModel("deepseek-v4-flash"))

	// The official channel type qualifies without any allowlist entry.
	official := &model.Channel{Type: constant.ChannelTypeDeepSeek}
	assert.True(t, official.IsOfficialFitChannelForModel("deepseek-v4.1-flash"))

	// The marker cannot promote a model outside an official-fit family.
	outsider := &model.Channel{Type: constant.ChannelTypeOpenAI}
	outsider.SetOtherSettings(dto.ChannelOtherSettings{OfficialFitModels: []string{"gpt-4o"}})
	assert.False(t, outsider.IsOfficialFitChannelForModel("gpt-4o"))
}

// The exclusion is driven by the current request's pin, not by whether the
// user still has Route enabled: unpinned requests drop official affinity even
// when no official-fit profile is present at all (the stale-binding case).
func TestUnpinnedRequestDropsOfficialAffinityWithoutRouteProfile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"deepseek-v4-flash"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	// No UserSetting / Route profile on the context, as in the production
	// incident: the pin is off because the profile no longer enables Route.
	markV4OfficialPinFromDistributor(c)
	require.False(t, common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin))

	officialType := model.OfficialFitChannelType("deepseek-v4-flash")
	assert.False(t, officialPinAllowsAffinity(common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin), officialType, officialType, false),
		"a stale official binding must be dropped for an unpinned request")
}
