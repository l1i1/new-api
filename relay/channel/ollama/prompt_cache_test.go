package ollama

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"

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

func TestApplyOllamaPromptCacheEstimation_RequiresSessionIdentifier(t *testing.T) {
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

	assert.Zero(t, usage2.PromptTokensDetails.CachedTokens)
	assert.Nil(t, usage2.BillingUsage)
	assert.Empty(t, buildPromptCacheKey(info1))
}

// Test strict prefix hit.
func TestApplyOllamaPromptCacheEstimation_StrictPrefixHit(t *testing.T) {
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

	// estimated = floor(200 * 0.5) = 100
	assert.Equal(t, 100, usage2.PromptTokensDetails.CachedTokens)
	require.NotNil(t, usage2.BillingUsage)
	assert.True(t, usage2.BillingUsage.Estimated)
	assert.Equal(t, 100, usage2.BillingUsage.OpenAIUsage.PromptTokensDetails.CachedTokens)
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

func TestApplyOllamaPromptCacheEstimation_RealZeroCachedTokensPriority(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	first := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "hello"), "u1"))
	applyOllamaPromptCacheEstimation(first, &dto.Usage{PromptTokens: 200})

	second := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "hello", "assistant", "hi"), "u1"))
	usage := &dto.Usage{PromptTokens: 300}
	applyOllamaPromptCacheEstimationWithUpstreamUsage(second, usage, true)

	assert.Zero(t, usage.PromptTokensDetails.CachedTokens)
	assert.Nil(t, usage.BillingUsage)
}

func TestApplyOllamaPromptCacheEstimation_RecordsRealUsageForLaterFallback(t *testing.T) {
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	first := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "hello"), "u1"))
	applyOllamaPromptCacheEstimation(first, &dto.Usage{PromptTokens: 100})

	real := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "hello", "assistant", "hi"), "u1"))
	realUsage := &dto.Usage{PromptTokens: 300}
	applyOllamaPromptCacheEstimationWithUpstreamUsage(real, realUsage, true)
	assert.Zero(t, realUsage.PromptTokensDetails.CachedTokens)

	later := infoWith(1, 10, "llama3", setting, openAIRequest(msgs("user", "hello", "assistant", "hi", "user", "bye"), "u1"))
	laterUsage := &dto.Usage{PromptTokens: 400}
	applyOllamaPromptCacheEstimation(later, laterUsage)
	assert.Equal(t, 150, laterUsage.PromptTokensDetails.CachedTokens)
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

	// estimated = floor(10000 * 0.5) = 5000, but capped at 10
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
	assert.Equal(t, 100, usage2.BillingUsage.OpenAIUsage.PromptTokensDetails.CachedTokens)
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

func TestBuildMessageHashes_SkipsOversizedConversations(t *testing.T) {
	request := openAIRequest(make([]dto.Message, promptCacheMaxMessages+1), "u1")
	assert.Empty(t, buildMessageHashes(request))
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
	promptCacheOnce = sync.Once{}
	promptCacheInst = nil
}
