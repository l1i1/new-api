package ollama

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func infoWith(channelId, userId int, model string, channelSetting dto.ChannelSettings, request dto.Request) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		UserId:  userId,
		Request: request,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         channelId,
			UpstreamModelName: model,
			ChannelSetting:    channelSetting,
		},
	}
}

func openAIRequest(messages []dto.Message, user string) *dto.GeneralOpenAIRequest {
	return &dto.GeneralOpenAIRequest{
		Messages: messages,
		User:     marshalRaw(user),
	}
}

func marshalRaw(v string) json.RawMessage {
	b, _ := common.Marshal(v)
	return b
}

func msgs(pairs ...string) []dto.Message {
	msgs := make([]dto.Message, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		msgs = append(msgs, dto.Message{Role: pairs[i], Content: pairs[i+1]})
	}
	return msgs
}

// Test applyOllamaPromptCacheEstimation with the switch off.
func TestApplyOllamaPromptCacheEstimation_SwitchOff(t *testing.T) {
	resetPromptCache()
	info := infoWith(1, 10, "llama3", dto.ChannelSettings{
		OllamaCacheEstimationEnabled: false,
	}, openAIRequest(msgs("user", "hello"), "u1"))

	usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	applyOllamaPromptCacheEstimation(info, usage)

	assert.Equal(t, 0, usage.PromptTokensDetails.CachedTokens)
	assert.Nil(t, usage.BillingUsage)
}

func TestApplyOllamaPromptCacheEstimation_UsesUserFallbackWithoutSessionIdentifier(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	info1 := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{
		Messages: msgs("user", "hello"),
	})
	applyOllamaPromptCacheEstimation(info1, &dto.Usage{PromptTokens: 200})

	info2 := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{
		Messages: msgs("user", "hello", "assistant", "hi"),
	})
	usage2 := &dto.Usage{PromptTokens: 300}
	applyOllamaPromptCacheEstimation(info2, usage2)

	assert.Equal(t, 200, usage2.PromptTokensDetails.CachedTokens)
	require.NotNil(t, usage2.BillingUsage)
	assert.True(t, usage2.BillingUsage.Estimated)
	assert.NotEmpty(t, buildPromptCacheKey(info1))
}

// Test prefix hit when the current request appends messages.
func TestApplyOllamaPromptCacheEstimation_PrefixHit(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	req1 := openAIRequest(msgs("user", "hello"), "u1")
	info1 := infoWith(1, 10, "llama3", setting, req1)

	// First request: no estimation (no previous entry).
	usage1 := &dto.Usage{PromptTokens: 200, CompletionTokens: 50, TotalTokens: 250}
	applyOllamaPromptCacheEstimation(info1, usage1)
	assert.Equal(t, 0, usage1.PromptTokensDetails.CachedTokens)

	// Second request: same prefix + new message.
	req2 := openAIRequest(msgs("user", "hello", "assistant", "hi", "user", "bye"), "u1")
	info2 := infoWith(1, 10, "llama3", setting, req2)
	usage2 := &dto.Usage{PromptTokens: 300, CompletionTokens: 50, TotalTokens: 350}
	applyOllamaPromptCacheEstimation(info2, usage2)

	assert.Equal(t, 200, usage2.PromptTokensDetails.CachedTokens)
	require.NotNil(t, usage2.BillingUsage)
	assert.True(t, usage2.BillingUsage.Estimated)
	assert.Equal(t, 200, usage2.BillingUsage.OpenAIUsage.PromptTokensDetails.CachedTokens)
}

func TestApplyOllamaPromptCacheEstimation_ExactReplayHit(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	request := openAIRequest(msgs("user", "hello", "assistant", "hi"), "u1")

	first := infoWith(1, 10, "llama3", setting, request)
	applyOllamaPromptCacheEstimation(first, &dto.Usage{PromptTokens: 200})

	second := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "hello", "assistant", "hi"), "u1"))
	usage := &dto.Usage{PromptTokens: 200}
	applyOllamaPromptCacheEstimation(second, usage)

	assert.Equal(t, 200, usage.PromptTokensDetails.CachedTokens)
	require.NotNil(t, usage.BillingUsage)
	assert.True(t, usage.BillingUsage.Estimated)
}

// Test non-prefix doesn't hit.
func TestApplyOllamaPromptCacheEstimation_NonPrefixNoHit(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	req1 := openAIRequest(msgs("user", "hello"), "u1")
	info1 := infoWith(1, 10, "llama3", setting, req1)

	usage1 := &dto.Usage{PromptTokens: 200, CompletionTokens: 50, TotalTokens: 250}
	applyOllamaPromptCacheEstimation(info1, usage1)

	// Different messages (not a prefix extension).
	req2 := openAIRequest(msgs("user", "different"), "u1")
	info2 := infoWith(1, 10, "llama3", setting, req2)
	usage2 := &dto.Usage{PromptTokens: 300, CompletionTokens: 50, TotalTokens: 350}
	applyOllamaPromptCacheEstimation(info2, usage2)

	assert.Equal(t, 0, usage2.PromptTokensDetails.CachedTokens)
	assert.Nil(t, usage2.BillingUsage)
}

// Test TTL expiry.
func TestApplyOllamaPromptCacheEstimation_TTLExpiry(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	// Manually insert an expired entry.
	cache := getPromptCache()
	key := buildPromptCacheKey(infoWith(1, 10, "llama3", setting,
		openAIRequest(msgs("user", "hello"), "u1")))
	entry := promptCacheEntry{
		MessageHashes:        buildMessageHashes(openAIRequest(msgs("user", "hello"), "u1")),
		PreviousPromptTokens: 500,
	}
	cache.SetWithTTL(key, entry, 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	// Same request should not find the expired entry.
	req := openAIRequest(msgs("user", "hello", "assistant", "hi"), "u1")
	info := infoWith(1, 10, "llama3", setting, req)
	usage := &dto.Usage{PromptTokens: 300, CompletionTokens: 50, TotalTokens: 350}
	applyOllamaPromptCacheEstimation(info, usage)

	assert.Equal(t, 0, usage.PromptTokensDetails.CachedTokens)
}

// Test different users are isolated.
func TestApplyOllamaPromptCacheEstimation_UserIsolation(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	req1 := openAIRequest(msgs("user", "hello"), "userA")
	info1 := infoWith(1, 10, "llama3", setting, req1)
	usage1 := &dto.Usage{PromptTokens: 200, CompletionTokens: 50, TotalTokens: 250}
	applyOllamaPromptCacheEstimation(info1, usage1)

	// Different user, same messages.
	req2 := openAIRequest(msgs("user", "hello", "assistant", "hi"), "userB")
	info2 := infoWith(1, 20, "llama3", setting, req2)
	usage2 := &dto.Usage{PromptTokens: 300, CompletionTokens: 50, TotalTokens: 350}
	applyOllamaPromptCacheEstimation(info2, usage2)

	assert.Equal(t, 0, usage2.PromptTokensDetails.CachedTokens)
}

// Test different models are isolated.
func TestApplyOllamaPromptCacheEstimation_ModelIsolation(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	req1 := openAIRequest(msgs("user", "hello"), "u1")
	info1 := infoWith(1, 10, "llama3", setting, req1)
	usage1 := &dto.Usage{PromptTokens: 200, CompletionTokens: 50, TotalTokens: 250}
	applyOllamaPromptCacheEstimation(info1, usage1)

	// Different model, same user/messages.
	req2 := openAIRequest(msgs("user", "hello", "assistant", "hi"), "u1")
	info2 := infoWith(1, 10, "mistral", setting, req2)
	usage2 := &dto.Usage{PromptTokens: 300, CompletionTokens: 50, TotalTokens: 350}
	applyOllamaPromptCacheEstimation(info2, usage2)

	assert.Equal(t, 0, usage2.PromptTokensDetails.CachedTokens)
}

// Test different channels/credentials are isolated.
func TestApplyOllamaPromptCacheEstimation_ChannelCredentialIsolation(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	req1 := openAIRequest(msgs("user", "hello"), "u1")
	info1 := infoWith(1, 10, "llama3", setting, req1)
	usage1 := &dto.Usage{PromptTokens: 200, CompletionTokens: 50, TotalTokens: 250}
	applyOllamaPromptCacheEstimation(info1, usage1)

	// Different channel, same user/model/messages.
	req2 := openAIRequest(msgs("user", "hello", "assistant", "hi"), "u1")
	info2 := infoWith(2, 10, "llama3", setting, req2)
	info2.ChannelCredentialId = 99
	usage2 := &dto.Usage{PromptTokens: 300, CompletionTokens: 50, TotalTokens: 350}
	applyOllamaPromptCacheEstimation(info2, usage2)

	assert.Equal(t, 0, usage2.PromptTokensDetails.CachedTokens)
}

// Test real cached_tokens takes precedence.
func TestApplyOllamaPromptCacheEstimation_RealCachedTokensPriority(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	req1 := openAIRequest(msgs("user", "hello"), "u1")
	info1 := infoWith(1, 10, "llama3", setting, req1)
	usage1 := &dto.Usage{PromptTokens: 200, CompletionTokens: 50, TotalTokens: 250}
	applyOllamaPromptCacheEstimation(info1, usage1)

	// Second request already has cached_tokens from upstream.
	req2 := openAIRequest(msgs("user", "hello", "assistant", "hi"), "u1")
	info2 := infoWith(1, 10, "llama3", setting, req2)
	usage2 := &dto.Usage{
		PromptTokens:     300,
		CompletionTokens: 50,
		TotalTokens:      350,
	}
	usage2.PromptTokensDetails.CachedTokens = 150 // real value
	applyOllamaPromptCacheEstimation(info2, usage2)

	// Should keep the real value, not override with estimation.
	assert.Equal(t, 150, usage2.PromptTokensDetails.CachedTokens)
	assert.Nil(t, usage2.BillingUsage)
}

func TestApplyOllamaPromptCacheEstimation_RealZeroAllowsEstimation(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	first := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "hello"), "u1"))
	applyOllamaPromptCacheEstimation(first, &dto.Usage{PromptTokens: 200})

	second := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "hello", "assistant", "hi"), "u1"))
	usage := &dto.Usage{PromptTokens: 300}
	applyOllamaPromptCacheEstimationWithUpstreamUsage(second, usage, true)

	assert.Equal(t, 200, usage.PromptTokensDetails.CachedTokens)
	require.NotNil(t, usage.BillingUsage)
	assert.True(t, usage.BillingUsage.Estimated)
}

func TestApplyOllamaPromptCacheEstimation_RecordsRealUsageForLaterFallback(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	first := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "hello"), "u1"))
	applyOllamaPromptCacheEstimation(first, &dto.Usage{PromptTokens: 100})

	real := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "hello", "assistant", "hi"), "u1"))
	realUsage := &dto.Usage{PromptTokens: 300}
	applyOllamaPromptCacheEstimationWithUpstreamUsage(real, realUsage, true)
	assert.Equal(t, 100, realUsage.PromptTokensDetails.CachedTokens)
	require.NotNil(t, realUsage.BillingUsage)
	assert.True(t, realUsage.BillingUsage.Estimated)

	later := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "hello", "assistant", "hi", "user", "bye"), "u1"))
	laterUsage := &dto.Usage{PromptTokens: 400}
	applyOllamaPromptCacheEstimation(later, laterUsage)
	assert.Equal(t, 300, laterUsage.PromptTokensDetails.CachedTokens)
}

func TestPromptCacheCandidatesRetainInterleavedChainsAndChooseLongestPrefix(t *testing.T) {
	short := promptCacheCandidate{MessageHashes: []string{"a"}, PreviousPromptTokens: 100}
	long := promptCacheCandidate{MessageHashes: []string{"a", "b"}, PreviousPromptTokens: 200}
	entry := prependPromptCacheCandidate([]promptCacheCandidate{short}, long)
	assert.Equal(t, 200, mustLongestPromptCachePrefix(t, entry.Candidates, []string{"a", "b", "c"}))

	other := prependPromptCacheCandidate(entry.Candidates, promptCacheCandidate{
		MessageHashes: []string{"x"}, PreviousPromptTokens: 50,
	})
	assert.Equal(t, 200, mustLongestPromptCachePrefix(t, other.Candidates, []string{"a", "b", "c"}))
	assert.Equal(t, 50, mustLongestPromptCachePrefix(t, other.Candidates, []string{"x", "y"}))
}

func TestPromptCacheCandidatesUseCompactPrefixIdentity(t *testing.T) {
	current := []string{"a", "b", "c"}
	candidate := newPromptCacheCandidate(current[:2], 200, time.Now().Add(promptCacheTTL))
	assert.Empty(t, candidate.MessageHashes)
	assert.Equal(t, 2, candidate.MessageCount)
	assert.Equal(t, 200, mustLongestPromptCachePrefix(t, []promptCacheCandidate{candidate}, current))

	encoded, err := common.Marshal(promptCacheEntry{Candidates: []promptCacheCandidate{candidate}})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `"h"`)
}

func TestPromptCacheEntryStaysCompactForLongInterleavedChains(t *testing.T) {
	var entry promptCacheEntry
	for chain := 0; chain < promptCacheMaxCandidates; chain++ {
		hashes := make([]string, 1_000)
		for i := range hashes {
			hashes[i] = fmt.Sprintf("%064x", chain*len(hashes)+i)
		}
		entry = prependPromptCacheCandidate(entry.Candidates, newPromptCacheCandidate(hashes, 10_000+chain, time.Now().Add(promptCacheTTL)))
	}
	encoded, err := common.Marshal(entry)
	require.NoError(t, err)
	assert.Less(t, len(encoded), 4_096)
}

func TestActivePromptCacheCandidatesDropsExpiredShortKeepAlive(t *testing.T) {
	now := time.Now()
	candidates := []promptCacheCandidate{
		newPromptCacheCandidate([]string{"expired"}, 100, now.Add(-time.Second)),
		newPromptCacheCandidate([]string{"active"}, 200, now.Add(time.Second)),
		{MessageHashes: []string{"legacy"}, PreviousPromptTokens: 300},
	}
	active := activePromptCacheCandidates(candidates, now)
	require.Len(t, active, 2)
	assert.Equal(t, 200, active[0].PreviousPromptTokens)
	assert.Equal(t, 300, active[1].PreviousPromptTokens)
}

func TestApplyOllamaPromptCacheEstimationSupportsLongConversations(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	messages := make([]dto.Message, 0, promptCacheMaxLegacyMessages+2)
	for i := 0; i <= promptCacheMaxLegacyMessages; i++ {
		messages = append(messages, dto.Message{Role: "user", Content: fmt.Sprintf("message-%d", i)})
	}
	first := infoWith(1, 10, "llama3", setting, openAIRequest(messages, "long-session"))
	applyOllamaPromptCacheEstimation(first, &dto.Usage{PromptTokens: 10_000})

	messages = append(messages, dto.Message{Role: "assistant", Content: "response"})
	second := infoWith(1, 10, "llama3", setting, openAIRequest(messages, "long-session"))
	usage := &dto.Usage{PromptTokens: 10_100}
	applyOllamaPromptCacheEstimation(second, usage)

	assert.Equal(t, 10_000, usage.PromptTokensDetails.CachedTokens)
}

func TestPromptCacheCandidatesReadLegacyEntry(t *testing.T) {
	entry := promptCacheEntry{MessageHashes: []string{"legacy"}, PreviousPromptTokens: 80}
	candidates := promptCacheCandidates(entry)
	require.Len(t, candidates, 1)
	assert.Equal(t, entry.MessageHashes, candidates[0].MessageHashes)
	assert.Equal(t, 80, candidates[0].PreviousPromptTokens)
}

func TestPromptCacheEntryLegacyJSONCompatibility(t *testing.T) {
	raw, err := common.Marshal(promptCacheEntry{MessageHashes: []string{"legacy"}, PreviousPromptTokens: 80})
	require.NoError(t, err)
	var decoded promptCacheEntry
	require.NoError(t, common.Unmarshal(raw, &decoded))
	candidates := promptCacheCandidates(decoded)
	require.Len(t, candidates, 1)
	assert.Equal(t, []string{"legacy"}, candidates[0].MessageHashes)
	assert.Equal(t, 80, candidates[0].PreviousPromptTokens)
}

func TestApplyOllamaPromptCacheEstimation_InterleavedChains(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	firstA := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{Messages: msgs("user", "a")})
	firstA.TokenId = 42
	applyOllamaPromptCacheEstimation(firstA, &dto.Usage{PromptTokens: 200})

	firstB := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{Messages: msgs("user", "b")})
	firstB.TokenId = 42
	applyOllamaPromptCacheEstimation(firstB, &dto.Usage{PromptTokens: 300})

	followA := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{Messages: msgs("user", "a", "assistant", "ok")})
	followA.TokenId = 42
	usageA := &dto.Usage{PromptTokens: 400}
	applyOllamaPromptCacheEstimation(followA, usageA)
	assert.Equal(t, 200, usageA.PromptTokensDetails.CachedTokens)

	followB := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{Messages: msgs("user", "b", "assistant", "ok")})
	followB.TokenId = 42
	usageB := &dto.Usage{PromptTokens: 400}
	applyOllamaPromptCacheEstimation(followB, usageB)
	assert.Equal(t, 300, usageB.PromptTokensDetails.CachedTokens)
}

func TestPromptCacheCandidatesBoundedAndDeduplicated(t *testing.T) {
	existing := make([]promptCacheCandidate, 0, promptCacheMaxCandidates+2)
	for i := 0; i < promptCacheMaxCandidates+2; i++ {
		existing = append(existing, promptCacheCandidate{
			MessageHashes: []string{fmt.Sprintf("h-%d", i)}, PreviousPromptTokens: i + 1,
		})
	}
	entry := prependPromptCacheCandidate(existing, existing[0])
	assert.LessOrEqual(t, len(entry.Candidates), promptCacheMaxCandidates)
	for i := 1; i < len(entry.Candidates); i++ {
		assert.False(t, samePromptCacheCandidate(entry.Candidates[0], entry.Candidates[i]))
	}
}

func TestGetPromptCacheRebuildsWhenRedisBecomesAvailable(t *testing.T) {
	oldRedisEnabled := common.RedisEnabled
	oldRedis := common.RDB
	t.Cleanup(func() {
		common.RedisEnabled = oldRedisEnabled
		common.RDB = oldRedis
		resetPromptCache()
	})

	common.RedisEnabled = false
	common.RDB = nil
	resetPromptCache()
	memoryCache := getPromptCache()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	common.RDB = client
	common.RedisEnabled = true
	redisCache := getPromptCache()

	assert.NotSame(t, memoryCache, redisCache)
	require.NoError(t, redisCache.SetWithTTL("restored", promptCacheEntry{
		MessageHashes:        []string{"hash"},
		PreviousPromptTokens: 10,
	}, time.Minute))
	assert.True(t, server.Exists(redisCache.FullKey("restored")))
}

func mustLongestPromptCachePrefix(t *testing.T, candidates []promptCacheCandidate, current []string) int {
	t.Helper()
	tokens, found := longestPromptCachePrefix(candidates, current)
	require.True(t, found)
	return tokens
}

func TestOllamaCachedTokensPresence(t *testing.T) {
	zero := 0
	positive := 12

	value, present := ollamaCachedTokens(ollamaChatStreamChunk{
		PromptTokensDetails: &struct {
			CachedTokens *int `json:"cached_tokens"`
		}{CachedTokens: &zero},
	})
	assert.True(t, present)
	assert.Zero(t, value)

	value, present = ollamaCachedTokens(ollamaChatStreamChunk{CachedTokens: &positive})
	assert.True(t, present)
	assert.Equal(t, positive, value)

	value, present = ollamaCachedTokens(ollamaChatStreamChunk{})
	assert.False(t, present)
	assert.Zero(t, value)
}

func TestApplyOllamaPromptCacheEstimation_IsolatesPromptAffectingSettings(t *testing.T) {
	settingA := dto.ChannelSettings{
		OllamaCacheEstimationEnabled: true,
		SystemPrompt:                 "system-a",
	}
	settingB := settingA
	settingB.SystemPrompt = "system-b"

	req1 := openAIRequest(msgs("user", "hello"), "u1")
	info1 := infoWith(1, 10, "llama3", settingA, req1)
	applyOllamaPromptCacheEstimation(info1, &dto.Usage{PromptTokens: 200})

	req2 := openAIRequest(msgs("user", "hello", "assistant", "hi"), "u1")
	info2 := infoWith(1, 10, "llama3", settingB, req2)
	usage2 := &dto.Usage{PromptTokens: 300}
	applyOllamaPromptCacheEstimation(info2, usage2)
	assert.Zero(t, usage2.PromptTokensDetails.CachedTokens)

	resetPromptCache()
	req3 := openAIRequest(msgs("user", "hello"), "u1")
	req3.Tools = []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "tool-a"}}}
	info3 := infoWith(1, 10, "llama3", settingA, req3)
	applyOllamaPromptCacheEstimation(info3, &dto.Usage{PromptTokens: 200})

	req4 := openAIRequest(msgs("user", "hello", "assistant", "hi"), "u1")
	req4.Tools = []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "tool-b"}}}
	info4 := infoWith(1, 10, "llama3", settingA, req4)
	usage4 := &dto.Usage{PromptTokens: 300}
	applyOllamaPromptCacheEstimation(info4, usage4)
	assert.Zero(t, usage4.PromptTokensDetails.CachedTokens)
}

// Test estimation is capped at current prompt_tokens.
func TestApplyOllamaPromptCacheEstimation_CappedAtPromptTokens(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	req1 := openAIRequest(msgs("user", "hello"), "u1")
	info1 := infoWith(1, 10, "llama3", setting, req1)
	usage1 := &dto.Usage{PromptTokens: 10000, CompletionTokens: 50, TotalTokens: 10050}
	applyOllamaPromptCacheEstimation(info1, usage1)

	// Second request: very small prompt compared to previous.
	req2 := openAIRequest(msgs("user", "hello", "assistant", "hi"), "u1")
	info2 := infoWith(1, 10, "llama3", setting, req2)
	usage2 := &dto.Usage{PromptTokens: 10, CompletionTokens: 50, TotalTokens: 60}
	applyOllamaPromptCacheEstimation(info2, usage2)

	// The reusable prefix is capped at the current prompt token count.
	assert.Equal(t, 10, usage2.PromptTokensDetails.CachedTokens)
}

// Test BillingUsage is set correctly for settlement path.
func TestApplyOllamaPromptCacheEstimation_BillingUsageSet(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	req1 := openAIRequest(msgs("user", "hello"), "u1")
	info1 := infoWith(1, 10, "llama3", setting, req1)
	usage1 := &dto.Usage{PromptTokens: 200, CompletionTokens: 50, TotalTokens: 250}
	applyOllamaPromptCacheEstimation(info1, usage1)

	req2 := openAIRequest(msgs("user", "hello", "assistant", "hi", "user", "bye"), "u1")
	info2 := infoWith(1, 10, "llama3", setting, req2)
	usage2 := &dto.Usage{PromptTokens: 300, CompletionTokens: 50, TotalTokens: 350}
	applyOllamaPromptCacheEstimation(info2, usage2)

	require.NotNil(t, usage2.BillingUsage)
	assert.True(t, usage2.BillingUsage.Estimated)
	assert.Equal(t, dto.BillingUsageSourceOAIChat, usage2.BillingUsage.Source)
	assert.Equal(t, dto.BillingUsageSemanticOpenAI, usage2.BillingUsage.Semantic)
	require.NotNil(t, usage2.BillingUsage.OpenAIUsage)
	assert.Equal(t, 200, usage2.BillingUsage.OpenAIUsage.PromptTokensDetails.CachedTokens)
}

// Test that nil info or usage is handled gracefully.
func TestApplyOllamaPromptCacheEstimation_NilSafety(t *testing.T) {
	applyOllamaPromptCacheEstimation(nil, &dto.Usage{PromptTokens: 100})
	applyOllamaPromptCacheEstimation(&relaycommon.RelayInfo{}, nil)
	// Should not panic.
}

func TestBuildPromptCacheKey_RequiresChannelMetadata(t *testing.T) {
	assert.Empty(t, buildPromptCacheKey(&relaycommon.RelayInfo{
		UserId:  10,
		Request: openAIRequest(msgs("user", "hello"), "u1"),
	}))
}

// Test prompt cache key isolation by credential.
func TestBuildPromptCacheKey_DifferentCredentials(t *testing.T) {
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	req := openAIRequest(msgs("user", "hello"), "u1")

	info1 := infoWith(1, 10, "llama3", setting, req)
	info2 := infoWith(1, 10, "llama3", setting, req)
	info2.ChannelCredentialId = 42

	key1 := buildPromptCacheKey(info1)
	key2 := buildPromptCacheKey(info2)
	assert.NotEqual(t, key1, key2)
}

func TestBuildPromptCacheKey_DifferentMultiKeyIndexes(t *testing.T) {
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	req := openAIRequest(msgs("user", "hello"), "u1")

	info1 := infoWith(1, 10, "llama3", setting, req)
	info1.ChannelIsMultiKey = true
	info1.ChannelMultiKeyIndex = 0
	info2 := infoWith(1, 10, "llama3", setting, req)
	info2.ChannelIsMultiKey = true
	info2.ChannelMultiKeyIndex = 1

	assert.NotEqual(t, buildPromptCacheKey(info1), buildPromptCacheKey(info2))
}

func TestBuildPromptCacheKey_DifferentRelayModes(t *testing.T) {
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	req := openAIRequest(msgs("user", "hello"), "u1")

	chat := infoWith(1, 10, "llama3", setting, req)
	chat.RelayMode = relayconstant.RelayModeChatCompletions
	completion := infoWith(1, 10, "llama3", setting, req)
	completion.RelayMode = relayconstant.RelayModeCompletions

	assert.NotEqual(t, buildPromptCacheKey(chat), buildPromptCacheKey(completion))
}

// Test extractSessionIdentifier with prompt_cache_key.
func TestExtractSessionIdentifier_PromptCacheKey(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		PromptCacheKey: "session-abc",
		User:           marshalRaw("user1"),
		Metadata:       []byte(`{"user_id":"u123"}`),
	}
	assert.Equal(t, "session-abc", extractSessionIdentifier(req))
}

// Test extractSessionIdentifier with metadata.user_id fallback.
func TestExtractSessionIdentifier_MetadataUserId(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		User:     marshalRaw("user1"),
		Metadata: []byte(`{"user_id":"u123"}`),
	}
	assert.Equal(t, "u123", extractSessionIdentifier(req))
}

// Test extractSessionIdentifier with user field fallback.
func TestExtractSessionIdentifier_User(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		User: marshalRaw("user1"),
	}
	// User is a JSON string, should be unmarshaled.
	uid := extractSessionIdentifier(req)
	assert.Equal(t, "user1", uid)
}

func TestExtractSessionIdentifier_RejectsNullAndStructuredValues(t *testing.T) {
	assert.Empty(t, extractSessionIdentifier(&dto.GeneralOpenAIRequest{
		Metadata: json.RawMessage(`{"user_id":null}`),
	}))
	assert.Empty(t, extractSessionIdentifier(&dto.GeneralOpenAIRequest{
		User: json.RawMessage(`null`),
	}))
	assert.Empty(t, extractSessionIdentifier(&dto.GeneralOpenAIRequest{
		Metadata: json.RawMessage(`{"user_id":{"id":"nested"}}`),
	}))
}

func TestBuildPromptCacheKey_UsesSessionHeaderFallback(t *testing.T) {
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	info := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{
		Messages: msgs("user", "hello"),
	})
	info.RequestHeaders = map[string]string{"X-Session-Id": "header-session"}

	assert.NotEmpty(t, buildPromptCacheKey(info))
}

func TestBuildPromptCacheKey_UsesTokenFallback(t *testing.T) {
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	info := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{
		Messages: msgs("user", "hello"),
	})
	info.TokenId = 42

	assert.NotEmpty(t, buildPromptCacheKey(info))
}

func TestApplyOllamaPromptCacheEstimation_TokenFallbackHit(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	first := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{
		Messages: msgs("user", "hello"),
	})
	first.TokenId = 42
	applyOllamaPromptCacheEstimation(first, &dto.Usage{PromptTokens: 200})

	second := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{
		Messages: msgs("user", "hello", "assistant", "hi"),
	})
	second.TokenId = 42
	usage := &dto.Usage{PromptTokens: 300}
	applyOllamaPromptCacheEstimation(second, usage)

	require.Equal(t, 200, usage.PromptTokensDetails.CachedTokens)
	require.NotNil(t, usage.BillingUsage)
	assert.True(t, usage.BillingUsage.Estimated)
}

func TestApplyOllamaPromptCacheEstimation_TokenFallbackIsolatesTokens(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	first := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{
		Messages: msgs("user", "hello"),
	})
	first.TokenId = 42
	applyOllamaPromptCacheEstimation(first, &dto.Usage{PromptTokens: 200})

	second := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{
		Messages: msgs("user", "hello", "assistant", "hi"),
	})
	second.TokenId = 43
	usage := &dto.Usage{PromptTokens: 300}
	applyOllamaPromptCacheEstimation(second, usage)

	assert.Zero(t, usage.PromptTokensDetails.CachedTokens)
	assert.Nil(t, usage.BillingUsage)
}

func TestBuildPromptCacheKey_UsesUserFallbackWithoutToken(t *testing.T) {
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	info := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{
		Messages: msgs("user", "hello"),
	})

	assert.NotEmpty(t, buildPromptCacheKey(info))
}

func TestApplyOllamaPromptCacheEstimation_HeaderSessionHit(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	first := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{
		Messages: msgs("user", "hello"),
	})
	first.RequestHeaders = map[string]string{"X-Session-Id": "header-session"}
	applyOllamaPromptCacheEstimation(first, &dto.Usage{PromptTokens: 200})

	second := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{
		Messages: msgs("user", "hello", "assistant", "hi"),
	})
	second.RequestHeaders = map[string]string{"conversation-id": "header-session"}
	usage := &dto.Usage{PromptTokens: 300}
	applyOllamaPromptCacheEstimation(second, usage)

	assert.Equal(t, 200, usage.PromptTokensDetails.CachedTokens)
}

func TestApplyOllamaPromptCacheEstimation_HeaderSessionIsolation(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	first := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{
		Messages: msgs("user", "hello"),
	})
	first.RequestHeaders = map[string]string{"session_id": "session-a"}
	applyOllamaPromptCacheEstimation(first, &dto.Usage{PromptTokens: 200})

	second := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{
		Messages: msgs("user", "hello", "assistant", "hi"),
	})
	second.RequestHeaders = map[string]string{"session_id": "session-b"}
	usage := &dto.Usage{PromptTokens: 300}
	applyOllamaPromptCacheEstimation(second, usage)

	assert.Zero(t, usage.PromptTokensDetails.CachedTokens)
	assert.Nil(t, usage.BillingUsage)
}

func TestApplyOllamaPromptCacheEstimation_ClaudeMessages(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	first := infoWith(1, 10, "llama3", setting, &dto.ClaudeRequest{
		Model:    "llama3",
		Metadata: json.RawMessage(`{"user_id":"claude-session"}`),
		Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
	})
	applyOllamaPromptCacheEstimation(first, &dto.Usage{PromptTokens: 200})

	second := infoWith(1, 10, "llama3", setting, &dto.ClaudeRequest{
		Model:    "llama3",
		Metadata: json.RawMessage(`{"user_id":"claude-session"}`),
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi"},
		},
	})
	usage := &dto.Usage{PromptTokens: 300}
	applyOllamaPromptCacheEstimation(second, usage)

	assert.Equal(t, 200, usage.PromptTokensDetails.CachedTokens)
	require.NotNil(t, usage.BillingUsage)
	assert.True(t, usage.BillingUsage.Estimated)
}

func TestClaudeMessagesFinalOllamaBodyReceivesEstimation(t *testing.T) {
	resetPromptCache()
	gin.SetMode(gin.TestMode)
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	adaptor := &Adaptor{}

	run := func(messages []dto.ClaudeMessage, promptTokens int) *dto.Usage {
		request := &dto.ClaudeRequest{
			Model:    "llama3",
			Metadata: json.RawMessage(`{"user_id":"claude-final-session"}`),
			Messages: messages,
		}
		info := infoWith(1, 10, "llama3", setting, request)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		converted, err := adaptor.ConvertClaudeRequest(c, info, request)
		require.NoError(t, err)
		bodyJSON, err := common.Marshal(converted)
		require.NoError(t, err)
		body, closer, err := relaycommon.NewOutboundJSONBody(bodyJSON)
		require.NoError(t, err)
		defer closer.Close()
		captureOllamaPromptCacheIdentity(c, info, body)
		usage := &dto.Usage{PromptTokens: promptTokens}
		applyOllamaPromptCacheEstimation(info, usage, c)
		return usage
	}

	first := run([]dto.ClaudeMessage{{Role: "user", Content: "hello"}}, 200)
	assert.Zero(t, first.PromptTokensDetails.CachedTokens)
	second := run([]dto.ClaudeMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}, 300)
	assert.Equal(t, 200, second.PromptTokensDetails.CachedTokens)
}

func TestBuildPromptCacheKey_EquivalentOpenAIAndClaudeChat(t *testing.T) {
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	openAI := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{
		Messages: msgs("user", "hello"),
		Metadata: json.RawMessage(`{"user_id":"shared-session"}`),
	})
	claude := infoWith(1, 10, "llama3", setting, &dto.ClaudeRequest{
		Model:    "llama3",
		Metadata: json.RawMessage(`{"user_id":"shared-session"}`),
		Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
	})
	assert.Equal(t, buildPromptCacheKey(openAI), buildPromptCacheKey(claude))
}

func TestApplyOllamaPromptCacheEstimation_LongConversationExceedsTargetTokenRate(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	messages := []dto.Message{{Role: "user", Content: "hello"}}
	var promptTokens, cachedTokens int
	for i := 0; i < 40; i++ {
		info := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{
			Messages:       append([]dto.Message(nil), messages...),
			PromptCacheKey: "long-session",
		})
		usage := &dto.Usage{PromptTokens: 10_000 + i*100}
		applyOllamaPromptCacheEstimation(info, usage)
		promptTokens += usage.PromptTokens
		cachedTokens += usage.PromptTokensDetails.CachedTokens
		messages = append(messages, dto.Message{Role: "assistant", Content: fmt.Sprintf("turn-%d", i)})
	}
	assert.Greater(t, float64(cachedTokens)/float64(promptTokens), 0.95)
}

func TestApplyOllamaPromptCacheEstimation_RecordsOutcome(t *testing.T) {
	resetPromptCache()
	gin.SetMode(gin.TestMode)
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	request := openAIRequest(msgs("user", "hello"), "observed-session")

	firstContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	applyOllamaPromptCacheEstimation(infoWith(1, 10, "llama3", setting, request), &dto.Usage{PromptTokens: 200}, firstContext)
	firstValue, exists := firstContext.Get(string(constant.ContextKeyOllamaPromptCache))
	require.True(t, exists)
	first, ok := firstValue.(promptCacheObservation)
	require.True(t, ok)
	assert.Equal(t, "cold_miss", first.Outcome)
	assert.NotEmpty(t, first.PartitionHash)
	assert.NotEmpty(t, first.ChainHash)

	secondContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	usage := &dto.Usage{PromptTokens: 200}
	applyOllamaPromptCacheEstimation(infoWith(1, 10, "llama3", setting, request), usage, secondContext)
	secondValue, exists := secondContext.Get(string(constant.ContextKeyOllamaPromptCache))
	require.True(t, exists)
	second, ok := secondValue.(promptCacheObservation)
	require.True(t, ok)
	assert.Equal(t, "hit_estimated", second.Outcome)
}

func TestCaptureOllamaPromptCacheIdentityUsesFinalGenerateRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	info := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{Prompt: "Tell me"})
	info.RelayMode = relayconstant.RelayModeCompletions
	info.RequestURLPath = "/v1/completions"
	jsonData, err := common.Marshal(OllamaGenerateRequest{
		Model:  "llama3",
		Prompt: "Tell me",
		Suffix: "suffix-a",
	})
	require.NoError(t, err)
	body, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
	require.NoError(t, err)
	defer closer.Close()

	captureOllamaPromptCacheIdentity(c, info, body)
	value, exists := c.Get(promptCacheIdentityContextKey)
	require.True(t, exists)
	finalIdentity, ok := value.(promptCacheIdentity)
	require.True(t, ok)
	require.Len(t, finalIdentity.MessageHashes, 1)

	other := buildOllamaGeneratePromptCacheIdentity(&OllamaGenerateRequest{Model: "llama3", Prompt: "Tell me", Suffix: "suffix-b"})
	assert.NotEqual(t, finalIdentity.MessageHashes, other.MessageHashes)
}

func TestCaptureOllamaPromptCacheIdentityKeepsFinalImageRequestUncacheable(t *testing.T) {
	resetPromptCache()
	gin.SetMode(gin.TestMode)
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	request := openAIRequest(msgs("user", "describe this image"), "image-session")
	info := infoWith(1, 10, "llama3", setting, request)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	jsonData, err := common.Marshal(OllamaChatRequest{
		Model: "llama3",
		Messages: []OllamaChatMessage{{
			Role: "user", Content: "describe this image", Images: []string{"base64-data"},
		}},
	})
	require.NoError(t, err)
	body, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
	require.NoError(t, err)
	defer closer.Close()

	captureOllamaPromptCacheIdentity(c, info, body)
	usage := &dto.Usage{PromptTokens: 200}
	applyOllamaPromptCacheEstimation(info, usage, c)

	assert.Zero(t, usage.PromptTokensDetails.CachedTokens)
	value, exists := c.Get(string(constant.ContextKeyOllamaPromptCache))
	require.True(t, exists)
	observation, ok := value.(promptCacheObservation)
	require.True(t, ok)
	assert.Equal(t, "uncacheable", observation.Outcome)
}

func TestCaptureOllamaPromptCacheIdentityKeepAliveZeroIsUncacheable(t *testing.T) {
	resetPromptCache()
	gin.SetMode(gin.TestMode)
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	request := openAIRequest(msgs("user", "hello"), "keep-alive-session")
	info := infoWith(1, 10, "llama3", setting, request)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	jsonData, err := common.Marshal(OllamaChatRequest{
		Model: "llama3", Messages: []OllamaChatMessage{{Role: "user", Content: "hello"}}, KeepAlive: float64(0),
	})
	require.NoError(t, err)
	body, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
	require.NoError(t, err)
	defer closer.Close()

	captureOllamaPromptCacheIdentity(c, info, body)
	usage := &dto.Usage{PromptTokens: 200}
	applyOllamaPromptCacheEstimation(info, usage, c)

	assert.Zero(t, usage.PromptTokensDetails.CachedTokens)
}

func TestBuildPromptCacheKeyIsolatesUpstreamBaseURL(t *testing.T) {
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	request := openAIRequest(msgs("user", "hello"), "session")
	first := infoWith(1, 10, "llama3", setting, request)
	first.ChannelBaseUrl = "https://ollama-a.example/api/"
	second := infoWith(1, 10, "llama3", setting, request)
	second.ChannelBaseUrl = "https://ollama-b.example/api"
	third := infoWith(1, 10, "llama3", setting, request)
	third.ChannelBaseUrl = "https://ollama-a.example/api"

	assert.NotEqual(t, buildPromptCacheKey(first), buildPromptCacheKey(second))
	assert.Equal(t, buildPromptCacheKey(first), buildPromptCacheKey(third))
}

func TestPromptCacheTTLForKeepAlive(t *testing.T) {
	tests := []struct {
		name      string
		value     any
		wantTTL   time.Duration
		cacheable bool
	}{
		{name: "default", wantTTL: promptCacheTTL, cacheable: true},
		{name: "zero number", value: float64(0), cacheable: false},
		{name: "zero string", value: "0s", cacheable: false},
		{name: "zero bare string", value: "0", cacheable: false},
		{name: "short duration", value: "30s", wantTTL: 30 * time.Second, cacheable: true},
		{name: "long duration capped", value: "1h", wantTTL: promptCacheTTL, cacheable: true},
		{name: "negative keeps loaded", value: float64(-1), wantTTL: promptCacheTTL, cacheable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ttl, cacheable := promptCacheTTLForKeepAlive(tt.value)
			assert.Equal(t, tt.cacheable, cacheable)
			assert.Equal(t, tt.wantTTL, ttl)
		})
	}
}

func TestPromptCacheUsesShortKeepAliveForRedisKeyTTL(t *testing.T) {
	oldRedisEnabled := common.RedisEnabled
	oldRedis := common.RDB
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		common.RedisEnabled = oldRedisEnabled
		common.RDB = oldRedis
		_ = client.Close()
		resetPromptCache()
	})
	common.RedisEnabled = true
	common.RDB = client
	resetPromptCache()

	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	request := openAIRequest(msgs("user", "hello"), "short-ttl")
	info := infoWith(1, 10, "llama3", setting, request)
	jsonData, err := common.Marshal(OllamaChatRequest{
		Model: "llama3", Messages: []OllamaChatMessage{{Role: "user", Content: "hello"}}, KeepAlive: "30s",
	})
	require.NoError(t, err)
	body, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
	require.NoError(t, err)
	defer closer.Close()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	captureOllamaPromptCacheIdentity(c, info, body)
	applyOllamaPromptCacheEstimation(info, &dto.Usage{PromptTokens: 100}, c)

	key := buildPromptCacheKeyWithIdentity(info, resolvePromptCacheIdentity(info, c))
	ttl := server.TTL(getPromptCache().FullKey(key))
	assert.Greater(t, ttl, 20*time.Second)
	assert.LessOrEqual(t, ttl, 30*time.Second)
}

func TestPromptCacheKeepAliveZeroClearsExistingPartition(t *testing.T) {
	oldRedisEnabled := common.RedisEnabled
	oldRedis := common.RDB
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		common.RedisEnabled = oldRedisEnabled
		common.RDB = oldRedis
		_ = client.Close()
		resetPromptCache()
	})
	common.RedisEnabled = true
	common.RDB = client
	resetPromptCache()

	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	request := openAIRequest(msgs("user", "hello"), "clear-session")
	info := infoWith(1, 10, "llama3", setting, request)
	firstBody, firstCloser, err := relaycommon.NewOutboundJSONBody([]byte(`{"model":"llama3","messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	firstContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	captureOllamaPromptCacheIdentity(firstContext, info, firstBody)
	applyOllamaPromptCacheEstimation(info, &dto.Usage{PromptTokens: 100}, firstContext)
	firstCloser.Close()

	key := buildPromptCacheKeyWithIdentity(info, resolvePromptCacheIdentity(info, firstContext))
	require.True(t, server.Exists(getPromptCache().FullKey(key)))
	zeroBody, zeroCloser, err := relaycommon.NewOutboundJSONBody([]byte(`{"model":"llama3","messages":[{"role":"user","content":"hello"}],"keep_alive":"0"}`))
	require.NoError(t, err)
	defer zeroCloser.Close()
	zeroContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	captureOllamaPromptCacheIdentity(zeroContext, info, zeroBody)
	applyOllamaPromptCacheEstimation(info, &dto.Usage{PromptTokens: 100}, zeroContext)

	assert.False(t, server.Exists(getPromptCache().FullKey(key)))
}

func TestPromptCacheUsesRequestStartSnapshot(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	first := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "a"), "snapshot-session"))
	firstBody, firstCloser, err := relaycommon.NewOutboundJSONBody([]byte(`{"model":"llama3","messages":[{"role":"user","content":"a"}]}`))
	require.NoError(t, err)
	defer firstCloser.Close()
	firstContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	captureOllamaPromptCacheIdentity(firstContext, first, firstBody)
	applyOllamaPromptCacheEstimation(first, &dto.Usage{PromptTokens: 100}, firstContext)

	current := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "a", "assistant", "b", "user", "c"), "snapshot-session"))
	body, closer, err := relaycommon.NewOutboundJSONBody([]byte(`{"model":"llama3","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`))
	require.NoError(t, err)
	defer closer.Close()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	captureOllamaPromptCacheIdentity(c, current, body)

	// A longer matching candidate appears after the request has started. It
	// must not affect this request's cache estimate.
	concurrent := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "a", "assistant", "b"), "snapshot-session"))
	applyOllamaPromptCacheEstimation(concurrent, &dto.Usage{PromptTokens: 200})
	usage := &dto.Usage{PromptTokens: 300}
	applyOllamaPromptCacheEstimation(current, usage, c)

	assert.Equal(t, 100, usage.PromptTokensDetails.CachedTokens)
}

func TestRecordOllamaPromptCacheResponseRequiresExactAssistantPrefix(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	first := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "a"), "response-session"))
	firstBody, firstCloser, err := relaycommon.NewOutboundJSONBody([]byte(`{"model":"llama3","messages":[{"role":"user","content":"a"}]}`))
	require.NoError(t, err)
	defer firstCloser.Close()
	firstContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	captureOllamaPromptCacheIdentity(firstContext, first, firstBody)
	firstUsage := &dto.Usage{PromptTokens: 100, CompletionTokens: 20}
	applyOllamaPromptCacheEstimation(first, firstUsage, firstContext)
	recordOllamaPromptCacheResponse(first, firstUsage, &OllamaChatMessage{Role: "assistant", Content: "answer"}, firstContext)

	matching := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "a", "assistant", "answer"), "response-session"))
	matchingBody, matchingCloser, err := relaycommon.NewOutboundJSONBody([]byte(`{"model":"llama3","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"answer"}]}`))
	require.NoError(t, err)
	defer matchingCloser.Close()
	matchingContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	captureOllamaPromptCacheIdentity(matchingContext, matching, matchingBody)
	matchingUsage := &dto.Usage{PromptTokens: 150}
	applyOllamaPromptCacheEstimation(matching, matchingUsage, matchingContext)
	assert.Equal(t, 120, matchingUsage.PromptTokensDetails.CachedTokens)

	different := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "a", "assistant", "different"), "response-session"))
	differentBody, differentCloser, err := relaycommon.NewOutboundJSONBody([]byte(`{"model":"llama3","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"different"}]}`))
	require.NoError(t, err)
	defer differentCloser.Close()
	differentContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	captureOllamaPromptCacheIdentity(differentContext, different, differentBody)
	differentUsage := &dto.Usage{PromptTokens: 150}
	applyOllamaPromptCacheEstimation(different, differentUsage, differentContext)
	assert.Equal(t, 100, differentUsage.PromptTokensDetails.CachedTokens)
}

func TestRecordOllamaPromptCacheResponseSkipsMissingPromptUsage(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	info := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "a"), "zero-prompt-session"))
	recordOllamaPromptCacheResponse(info, &dto.Usage{CompletionTokens: 20}, &OllamaChatMessage{
		Role: "assistant", Content: "answer",
	})

	key := buildPromptCacheKey(info)
	entry, found, err := getPromptCache().Get(key)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, entry.Candidates)
}

func TestExtractHeaderSessionIdentifier_NormalizesNamesAndPrefersSession(t *testing.T) {
	headers := map[string]string{
		"X-Conversation-ID": "conversation-session",
		"SESSION_ID":        "session-id",
	}
	assert.Equal(t, "session-id", extractHeaderSessionIdentifier(headers))
	assert.Equal(t, "conversation-session", extractHeaderSessionIdentifier(map[string]string{
		"conversation-id": "conversation-session",
	}))
	assert.Equal(t, "opencode-session", extractHeaderSessionIdentifier(map[string]string{
		"x-opencode-session": "opencode-session",
	}))
	assert.Equal(t, "affinity-session", extractHeaderSessionIdentifier(map[string]string{
		"x-session-affinity": "affinity-session",
	}))
}

func TestExtractHeaderSessionIdentifier_UsesFixedAliasPriority(t *testing.T) {
	headers := map[string]string{
		"x-session-affinity": "affinity-session",
		"x-opencode-session": "opencode-session",
		"conversation-id":    "conversation-session",
		"session-id":         "session-session",
	}
	for i := 0; i < 20; i++ {
		assert.Equal(t, "session-session", extractHeaderSessionIdentifier(headers))
	}
}

func TestBuildPromptCacheKey_BodyIdentifierPrecedesHeader(t *testing.T) {
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	body := &dto.GeneralOpenAIRequest{
		Messages:       msgs("user", "hello"),
		PromptCacheKey: "body-session",
	}
	info := infoWith(1, 10, "llama3", setting, body)
	info.RequestHeaders = map[string]string{"session_id": "header-session"}

	bodyOnly := infoWith(1, 10, "llama3", setting, body)
	headerOnly := infoWith(1, 10, "llama3", setting, &dto.GeneralOpenAIRequest{Messages: body.Messages})
	headerOnly.RequestHeaders = info.RequestHeaders

	assert.NotEqual(t, buildPromptCacheKey(bodyOnly), buildPromptCacheKey(headerOnly))
}

func TestExtractHeaderSessionIdentifierRejectsBlankAndOversizedValues(t *testing.T) {
	assert.Empty(t, extractHeaderSessionIdentifier(map[string]string{"session_id": "  "}))
	assert.Empty(t, extractHeaderSessionIdentifier(map[string]string{
		"session_id": strings.Repeat("x", promptCacheMaxSessionIDRunes+1),
	}))
}

func TestExtractSessionIdentifierFallsBackAfterBlankMetadataUserID(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Metadata: []byte(`{"user_id":""}`),
		User:     marshalRaw("request-user"),
	}
	assert.Equal(t, "request-user", extractSessionIdentifier(req))
}

func TestBuildMessageHashesSupportsLongConversations(t *testing.T) {
	request := openAIRequest(make([]dto.Message, promptCacheMaxLegacyMessages+1), "u1")
	assert.Len(t, buildMessageHashes(request), promptCacheMaxLegacyMessages+1)
}

// Test buildMessageHashes determinism.
func TestBuildMessageHashes_Deterministic(t *testing.T) {
	req := openAIRequest(msgs("user", "hello", "assistant", "hi"), "u1")
	h1 := buildMessageHashes(req)
	h2 := buildMessageHashes(req)
	assert.Equal(t, h1, h2)
	require.Len(t, h1, 2)
	assert.NotEmpty(t, h1[0])
	assert.NotEmpty(t, h1[1])
}

// Test buildMessageHashes different content produces different hashes.
func TestBuildMessageHashes_DifferentContent(t *testing.T) {
	req1 := openAIRequest(msgs("user", "hello"), "u1")
	req2 := openAIRequest(msgs("user", "world"), "u1")
	h1 := buildMessageHashes(req1)
	h2 := buildMessageHashes(req2)
	assert.NotEqual(t, h1[0], h2[0])
}

// Test buildMessageHashes with prompt field (completions path).
func TestBuildMessageHashes_PromptField(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Prompt: "Tell me a joke",
	}
	h := buildMessageHashes(req)
	require.Len(t, h, 1)
	assert.NotEmpty(t, h[0])

	// Different prompt yields different hash.
	req2 := &dto.GeneralOpenAIRequest{
		Prompt: "Tell me a story",
	}
	h2 := buildMessageHashes(req2)
	assert.NotEqual(t, h[0], h2[0])
}

func TestBuildMessageHashes_CompletionsSuffixIsolation(t *testing.T) {
	first := &dto.GeneralOpenAIRequest{Prompt: "Tell me", Suffix: "A"}
	second := &dto.GeneralOpenAIRequest{Prompt: "Tell me", Suffix: "B"}
	assert.NotEqual(t, buildMessageHashes(first), buildMessageHashes(second))
}

func TestBuildPromptCacheKey_ClaudeMetadataSessionIsolation(t *testing.T) {
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	first := infoWith(1, 10, "llama3", setting, &dto.ClaudeRequest{
		Model:    "llama3",
		Metadata: json.RawMessage(`{"user_id":"claude-a"}`),
		Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
	})
	second := infoWith(1, 10, "llama3", setting, &dto.ClaudeRequest{
		Model:    "llama3",
		Metadata: json.RawMessage(`{"user_id":"claude-b"}`),
		Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
	})
	assert.NotEqual(t, buildPromptCacheKey(first), buildPromptCacheKey(second))
}

// Test that estimation is a no-op for embeddings.
func TestApplyOllamaPromptCacheEstimation_SkipEmbeddings(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	req := &dto.GeneralOpenAIRequest{
		Prompt: "test embedding",
	}
	info := infoWith(1, 10, "llama3", setting, req)
	info.RelayMode = 7 // RelayModeEmbeddings
	usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 0, TotalTokens: 100}
	applyOllamaPromptCacheEstimation(info, usage)
	assert.Equal(t, 0, usage.PromptTokensDetails.CachedTokens)
}

// resetPromptCache clears the global prompt cache between tests.
func resetPromptCache() {
	promptCacheMu.Lock()
	defer promptCacheMu.Unlock()
	promptCacheInst = nil
	promptCacheRedis = nil
}
