package ollama

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/pkg/cachex"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/samber/hot"
)

const (
	promptCacheTTL = 5 * time.Minute
	// Legacy entries can contain up to 256 per-message hashes. New candidates
	// use one cumulative hash and remain bounded for longer conversations.
	promptCacheCapacity           = 5_000
	promptCacheMaxLegacyMessages  = 256
	promptCacheMaxCandidates      = 16
	promptCacheMaxCandidateHashes = promptCacheMaxCandidates * promptCacheMaxLegacyMessages
	promptCacheMaxSessionIDRunes  = 256
	promptCacheNamespace          = "ollama_prompt_cache:v1"
	promptCacheIdentityContextKey = "ollama_prompt_cache_identity"
)

type promptCacheIdentity struct {
	Family        string
	MessageHashes []string
	RootHash      string
	KeyMaterial   []byte
	TTL           time.Duration
	Clear         bool
	Uncacheable   bool
}

type promptCacheSnapshot struct {
	CacheKey   string
	Candidates []promptCacheCandidate
	ReadError  bool
}

type promptCacheObservation struct {
	Outcome       string `json:"outcome"`
	Family        string `json:"family,omitempty"`
	PartitionHash string `json:"partition_hash,omitempty"`
	ChainHash     string `json:"message_chain_hash,omitempty"`
}

type promptCacheCandidate struct {
	MessageHashes        []string `json:"h,omitempty"`
	PrefixHash           string   `json:"x,omitempty"`
	MessageCount         int      `json:"n,omitempty"`
	PreviousPromptTokens int      `json:"p"`
	ExpiresAt            int64    `json:"e,omitempty"`
}

// promptCacheEntry stores a bounded set of recent conversation fingerprints.
// Legacy payload fields remain readable; the expanded partition key intentionally
// starts a fresh cache window when deployed.
type promptCacheEntry struct {
	Candidates           []promptCacheCandidate `json:"c,omitempty"`
	MessageHashes        []string               `json:"h,omitempty"`
	PreviousPromptTokens int                    `json:"p,omitempty"`
}

var (
	promptCacheMu    sync.Mutex
	promptCacheInst  *cachex.HybridCache[promptCacheEntry]
	promptCacheRedis *redis.Client
)

func getPromptCache() *cachex.HybridCache[promptCacheEntry] {
	promptCacheMu.Lock()
	defer promptCacheMu.Unlock()
	if promptCacheInst != nil && promptCacheRedis == common.RDB {
		return promptCacheInst
	}
	promptCacheRedis = common.RDB
	promptCacheInst = cachex.NewHybridCache[promptCacheEntry](cachex.HybridCacheConfig[promptCacheEntry]{
		Namespace:  cachex.Namespace(promptCacheNamespace),
		Redis:      promptCacheRedis,
		RedisCodec: cachex.JSONCodec[promptCacheEntry]{},
		RedisEnabled: func() bool {
			return common.RedisEnabled && common.RDB != nil
		},
		Memory: func() *hot.HotCache[string, promptCacheEntry] {
			return hot.NewHotCache[string, promptCacheEntry](hot.LRU, promptCacheCapacity).
				WithTTL(promptCacheTTL).
				WithJanitor().
				Build()
		},
	})
	return promptCacheInst
}

// applyOllamaPromptCacheEstimation estimates cached prompt tokens for Ollama
// channels that lack real upstream cache data. It is a no-op when the channel
// setting is disabled, real cached_tokens are present, or the request type
// does not support conversation-prefix matching.
//
// On success it sets usage.PromptTokensDetails.CachedTokens and attaches a
// BillingUsage with Estimated=true so settlement uses the cache ratio and the
// billing path log distinguishes estimated billing.
func applyOllamaPromptCacheEstimation(info *relaycommon.RelayInfo, usage *dto.Usage, contexts ...*gin.Context) {
	applyOllamaPromptCacheEstimationWithUpstreamUsage(info, usage, false, contexts...)
}

func applyOllamaPromptCacheEstimationWithUpstreamUsage(info *relaycommon.RelayInfo, usage *dto.Usage, upstreamCachedTokensPresent bool, contexts ...*gin.Context) {
	if info == nil {
		return
	}
	if info.ChannelMeta == nil {
		return
	}
	if !info.ChannelSetting.OllamaCacheEstimationEnabled {
		return
	}
	// Do not apply to embeddings.
	if info.RelayMode == relayconstant.RelayModeEmbeddings {
		return
	}
	identity := resolvePromptCacheIdentity(info, contexts...)
	cacheKey := buildPromptCacheKeyWithIdentity(info, identity)
	if cacheKey == "" {
		setPromptCacheObservation(contexts, promptCacheObservation{Outcome: "uncacheable", Family: identity.Family})
		return
	}
	// keep_alive=0 means the upstream model is being unloaded. Remove the
	// whole simulated partition even when the response has no usage fields.
	if identity.Clear {
		if _, err := cacheDeletePromptCache(cacheKey); err != nil {
			logger.LogWarn(nil, fmt.Sprintf("ollama prompt cache clear failed channel=%d model=%q: %v", info.ChannelId, info.UpstreamModelName, err))
		}
		setPromptCacheObservation(contexts, promptCacheObservation{Outcome: "uncacheable", Family: identity.Family, PartitionHash: cacheKey})
		return
	}
	if identity.Uncacheable {
		setPromptCacheObservation(contexts, promptCacheObservation{Outcome: "uncacheable", Family: identity.Family, PartitionHash: cacheKey})
		return
	}
	if len(identity.MessageHashes) == 0 {
		setPromptCacheObservation(contexts, promptCacheObservation{Outcome: "uncacheable", Family: identity.Family, PartitionHash: cacheKey})
		return
	}
	if usage == nil || usage.PromptTokens <= 0 {
		return
	}

	// A positive upstream value is authoritative. An explicit zero is an
	// upstream miss, so it must still allow the channel estimator to run.
	allowEstimation := usage.PromptTokensDetails.CachedTokens <= 0
	if upstreamCachedTokensPresent {
		// The stream/non-stream parser has already normalized an upstream
		// positive value above zero. Only an explicit upstream zero is a miss.
		allowEstimation = usage.PromptTokensDetails.CachedTokens == 0
	}
	messageHashes := identity.MessageHashes

	cache := getPromptCache()
	ttl := identity.TTL
	if ttl <= 0 {
		ttl = promptCacheTTL
	}

	estimated := 0
	found := false
	matched := false
	cacheReadErr := false
	var candidates []promptCacheCandidate
	snapshot, hasSnapshot := promptCacheSnapshotFromContexts(contexts, cacheKey)
	if hasSnapshot {
		candidates = snapshot.Candidates
		found = len(candidates) > 0
		cacheReadErr = snapshot.ReadError
	} else if previous, cacheFound, err := cache.Get(cacheKey); err == nil {
		candidates = activePromptCacheCandidates(promptCacheCandidates(previous), time.Now())
		found = cacheFound && len(candidates) > 0
	} else {
		setPromptCacheObservation(contexts, promptCacheObservation{Outcome: "cache_error", Family: identity.Family, PartitionHash: cacheKey, ChainHash: promptCacheChainHash(messageHashes)})
		logger.LogWarn(nil, fmt.Sprintf("ollama prompt cache read failed channel=%d model=%q: %v", info.ChannelId, info.UpstreamModelName, err))
	}
	if allowEstimation {
		if previousTokens, ok := longestPromptCachePrefix(candidates, messageHashes); ok && previousTokens > 0 {
			matched = true
			estimated = previousTokens
			if estimated > usage.PromptTokens {
				estimated = usage.PromptTokens
			}
		}
	}
	cacheErr := cache.UpdateWithTTL(cacheKey, ttl, func(previous promptCacheEntry, _ bool) (promptCacheEntry, error) {
		now := time.Now()
		candidates := activePromptCacheCandidates(promptCacheCandidates(previous), now)
		return prependPromptCacheCandidate(candidates, newPromptCacheCandidate(messageHashes, usage.PromptTokens, now.Add(ttl))), nil
	})
	if cacheErr != nil {
		estimated = 0
		setPromptCacheObservation(contexts, promptCacheObservation{
			Outcome:       "cache_error",
			Family:        identity.Family,
			PartitionHash: cacheKey,
			ChainHash:     promptCacheChainHash(messageHashes),
		})
		logger.LogWarn(nil, fmt.Sprintf("ollama prompt cache update failed channel=%d model=%q: %v", info.ChannelId, info.UpstreamModelName, cacheErr))
		return
	}
	if cacheReadErr {
		setPromptCacheObservation(contexts, promptCacheObservation{
			Outcome:       "cache_error",
			Family:        identity.Family,
			PartitionHash: cacheKey,
			ChainHash:     promptCacheChainHash(messageHashes),
		})
		return
	}

	if estimated <= 0 {
		outcome := "cold_miss"
		if usage.PromptTokensDetails.CachedTokens > 0 {
			outcome = "hit_upstream"
		} else if found && !matched {
			outcome = "prefix_mismatch"
		}
		setPromptCacheObservation(contexts, promptCacheObservation{
			Outcome:       outcome,
			Family:        identity.Family,
			PartitionHash: cacheKey,
			ChainHash:     promptCacheChainHash(messageHashes),
		})
		return
	}
	usage.PromptTokensDetails.CachedTokens = estimated

	// Attach BillingUsage so settlement uses cache ratio and log path
	// reports billing-usage-openai-estimated.
	billingUsage := dto.NewOpenAIChatBillingUsage(usage)
	if billingUsage != nil {
		billingUsage.Estimated = true
		billingUsage.OpenAIUsage.PromptTokensDetails.CachedTokens = estimated
		usage.BillingUsage = billingUsage
	}
	setPromptCacheObservation(contexts, promptCacheObservation{
		Outcome:       "hit_estimated",
		Family:        identity.Family,
		PartitionHash: cacheKey,
		ChainHash:     promptCacheChainHash(messageHashes),
	})
}

// recordOllamaPromptCacheResponse stores the completed assistant turn as a
// separate prompt prefix. Ollama counts the next request's assistant history
// as input, while eval_count is only an output count for the current request.
// Keeping the response as a distinct, content-bound candidate avoids treating
// an unrelated assistant response as cached input.
func recordOllamaPromptCacheResponse(info *relaycommon.RelayInfo, usage *dto.Usage, response *OllamaChatMessage, contexts ...*gin.Context) {
	if info == nil || usage == nil || response == nil || usage.PromptTokens <= 0 || usage.CompletionTokens <= 0 || !info.ChannelSetting.OllamaCacheEstimationEnabled {
		return
	}
	if response.Content == "" && len(response.ToolCalls) == 0 && len(response.Thinking) == 0 {
		return
	}
	identity := resolvePromptCacheIdentity(info, contexts...)
	if identity.Family != "chat" || identity.Clear || identity.Uncacheable || len(identity.MessageHashes) == 0 {
		return
	}
	cacheKey := buildPromptCacheKeyWithIdentity(info, identity)
	if cacheKey == "" {
		return
	}
	responseHash := hashPromptCacheResponse(response)
	if responseHash == "" {
		return
	}
	hashes := append(append([]string(nil), identity.MessageHashes...), responseHash)
	totalTokens := promptCacheTokenSum(usage.PromptTokens, usage.CompletionTokens)
	if totalTokens <= usage.PromptTokens {
		return
	}
	ttl := identity.TTL
	if ttl <= 0 {
		ttl = promptCacheTTL
	}
	if err := getPromptCache().UpdateWithTTL(cacheKey, ttl, func(previous promptCacheEntry, _ bool) (promptCacheEntry, error) {
		now := time.Now()
		candidates := activePromptCacheCandidates(promptCacheCandidates(previous), now)
		return prependPromptCacheCandidate(candidates, newPromptCacheCandidate(hashes, totalTokens, now.Add(ttl))), nil
	}); err != nil {
		setPromptCacheObservation(contexts, promptCacheObservation{
			Outcome:       "cache_error",
			Family:        identity.Family,
			PartitionHash: cacheKey,
			ChainHash:     promptCacheChainHash(hashes),
		})
		logger.LogWarn(nil, fmt.Sprintf("ollama prompt cache response update failed channel=%d model=%q: %v", info.ChannelId, info.UpstreamModelName, err))
	}
}

func hashPromptCacheResponse(response *OllamaChatMessage) string {
	if response == nil {
		return ""
	}
	// Hash the final Ollama message shape. Converting through dto.Message loses
	// thinking and changes tool-call JSON, so the next request would miss even
	// when it contains the exact response that Ollama cached.
	return hashPromptCacheValue(*response)
}

func promptCacheTokenSum(promptTokens, completionTokens int) int {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens <= 0 {
		return promptTokens
	}
	maxInt := int(^uint(0) >> 1)
	if promptTokens > maxInt-completionTokens {
		return maxInt
	}
	return promptTokens + completionTokens
}

func promptCacheSnapshotFromContexts(contexts []*gin.Context, cacheKey string) (promptCacheSnapshot, bool) {
	if len(contexts) == 0 || contexts[0] == nil {
		return promptCacheSnapshot{}, false
	}
	value, ok := contexts[0].Get(promptCacheIdentityContextKey + ".snapshot")
	if !ok {
		return promptCacheSnapshot{}, false
	}
	snapshot, ok := value.(promptCacheSnapshot)
	return snapshot, ok && snapshot.CacheKey == cacheKey
}

func cacheDeletePromptCache(cacheKey string) (int, error) {
	if cacheKey == "" {
		return 0, nil
	}
	result, err := getPromptCache().DeleteMany([]string{cacheKey})
	if err != nil {
		return 0, err
	}
	if result[getPromptCache().FullKey(cacheKey)] {
		return 1, nil
	}
	return 0, nil
}

func setPromptCacheObservation(contexts []*gin.Context, observation promptCacheObservation) {
	if len(contexts) == 0 || contexts[0] == nil {
		return
	}
	contexts[0].Set(string(constant.ContextKeyOllamaPromptCache), observation)
}

func promptCacheChainHash(messageHashes []string) string {
	h := sha256.New()
	for _, hash := range messageHashes {
		h.Write([]byte(hash))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func promptCacheCandidates(entry promptCacheEntry) []promptCacheCandidate {
	available := entry.Candidates
	if len(available) == 0 && len(entry.MessageHashes) > 0 && entry.PreviousPromptTokens > 0 {
		available = []promptCacheCandidate{{
			MessageHashes:        entry.MessageHashes,
			PreviousPromptTokens: entry.PreviousPromptTokens,
		}}
	}
	result := make([]promptCacheCandidate, 0, promptCacheMaxCandidates)
	totalHashes := 0
	for _, candidate := range available {
		if len(result) >= promptCacheMaxCandidates || candidate.PreviousPromptTokens <= 0 {
			continue
		}
		if candidate.ExpiresAt == 0 {
			// Legacy entries had no per-candidate expiry. Bound them during
			// migration instead of extending stale state indefinitely on writes.
			candidate.ExpiresAt = time.Now().Add(promptCacheTTL).UnixNano()
		}
		if candidate.PrefixHash != "" && candidate.MessageCount > 0 {
			result = append(result, candidate)
			continue
		}
		if len(candidate.MessageHashes) == 0 || len(candidate.MessageHashes) > promptCacheMaxLegacyMessages || totalHashes+len(candidate.MessageHashes) > promptCacheMaxCandidateHashes {
			continue
		}
		result = append(result, promptCacheCandidate{
			MessageHashes:        append([]string(nil), candidate.MessageHashes...),
			PreviousPromptTokens: candidate.PreviousPromptTokens,
			ExpiresAt:            candidate.ExpiresAt,
		})
		totalHashes += len(candidate.MessageHashes)
	}
	return result
}

func activePromptCacheCandidates(candidates []promptCacheCandidate, now time.Time) []promptCacheCandidate {
	result := candidates[:0]
	for _, candidate := range candidates {
		if candidate.ExpiresAt > 0 && candidate.ExpiresAt <= now.UnixNano() {
			continue
		}
		result = append(result, candidate)
	}
	return result
}

func longestPromptCachePrefix(candidates []promptCacheCandidate, current []string) (int, bool) {
	bestLength := 0
	bestTokens := 0
	for _, candidate := range candidates {
		if candidate.PreviousPromptTokens <= 0 {
			continue
		}
		candidateLength := candidate.MessageCount
		matched := false
		if candidate.PrefixHash != "" && candidateLength > 0 && candidateLength <= len(current) {
			matched = candidate.PrefixHash == promptCacheChainHash(current[:candidateLength])
		} else if len(candidate.MessageHashes) > 0 && len(candidate.MessageHashes) <= len(current) {
			candidateLength = len(candidate.MessageHashes)
			matched = true
			for i, hash := range candidate.MessageHashes {
				if current[i] != hash {
					matched = false
					break
				}
			}
		}
		if matched && candidateLength > bestLength {
			bestLength = candidateLength
			bestTokens = candidate.PreviousPromptTokens
		}
	}
	return bestTokens, bestLength > 0
}

func prependPromptCacheCandidate(existing []promptCacheCandidate, current promptCacheCandidate) promptCacheEntry {
	result := make([]promptCacheCandidate, 0, promptCacheMaxCandidates)
	result = append(result, current)
	totalHashes := len(current.MessageHashes)
	for _, candidate := range existing {
		if len(result) >= promptCacheMaxCandidates {
			break
		}
		duplicate := false
		for _, retained := range result {
			if samePromptCacheCandidate(candidate, retained) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		if candidate.PreviousPromptTokens <= 0 {
			continue
		}
		if candidate.PrefixHash == "" && (len(candidate.MessageHashes) == 0 || totalHashes+len(candidate.MessageHashes) > promptCacheMaxCandidateHashes) {
			continue
		}
		result = append(result, candidate)
		totalHashes += len(candidate.MessageHashes)
	}
	return promptCacheEntry{Candidates: result}
}

func samePromptCacheCandidate(a, b promptCacheCandidate) bool {
	aHash, aCount := promptCacheCandidateIdentity(a)
	bHash, bCount := promptCacheCandidateIdentity(b)
	if aHash != "" || bHash != "" {
		return aHash != "" && aHash == bHash && aCount == bCount
	}
	if len(a.MessageHashes) != len(b.MessageHashes) {
		return false
	}
	for i := range a.MessageHashes {
		if a.MessageHashes[i] != b.MessageHashes[i] {
			return false
		}
	}
	return true
}

func newPromptCacheCandidate(messageHashes []string, promptTokens int, expiresAt time.Time) promptCacheCandidate {
	return promptCacheCandidate{
		PrefixHash:           promptCacheChainHash(messageHashes),
		MessageCount:         len(messageHashes),
		PreviousPromptTokens: promptTokens,
		ExpiresAt:            expiresAt.UnixNano(),
	}
}

func promptCacheCandidateIdentity(candidate promptCacheCandidate) (string, int) {
	if candidate.PrefixHash != "" && candidate.MessageCount > 0 {
		return candidate.PrefixHash, candidate.MessageCount
	}
	if len(candidate.MessageHashes) > 0 {
		return promptCacheChainHash(candidate.MessageHashes), len(candidate.MessageHashes)
	}
	return "", 0
}

func captureOllamaPromptCacheIdentity(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) {
	if c == nil || info == nil || requestBody == nil || !info.ChannelSetting.OllamaCacheEstimationEnabled {
		return
	}
	// The outbound body is the source of truth. Default to uncacheable so an
	// unreadable or unsupported final body never falls back to a looser DTO.
	c.Set(promptCacheIdentityContextKey, promptCacheIdentity{})
	replayable, ok := requestBody.(interface {
		NewReader() (io.ReadCloser, error)
	})
	if !ok {
		return
	}
	reader, err := replayable.NewReader()
	if err != nil {
		return
	}
	defer reader.Close()

	var identity promptCacheIdentity
	if info.RelayMode == relayconstant.RelayModeCompletions || strings.Contains(info.RequestURLPath, "/v1/completions") {
		var request OllamaGenerateRequest
		if common.DecodeJson(reader, &request) != nil {
			return
		}
		identity = buildOllamaGeneratePromptCacheIdentity(&request)
	} else {
		var request OllamaChatRequest
		if common.DecodeJson(reader, &request) != nil {
			return
		}
		identity = buildOllamaChatPromptCacheIdentity(&request)
	}
	// A successfully decoded final Ollama body is authoritative. Store even an
	// empty identity so an uncacheable body cannot fall back to the client DTO.
	c.Set(promptCacheIdentityContextKey, identity)
	cacheKey := buildPromptCacheKeyWithIdentity(info, identity)
	if cacheKey == "" {
		return
	}
	if identity.Clear {
		if _, err := cacheDeletePromptCache(cacheKey); err != nil {
			logger.LogWarn(nil, fmt.Sprintf("ollama prompt cache clear failed channel=%d model=%q: %v", info.ChannelId, info.UpstreamModelName, err))
		}
		return
	}
	if len(identity.MessageHashes) == 0 || identity.Uncacheable {
		return
	}
	cache := getPromptCache()
	entry, _, err := cache.Get(cacheKey)
	snapshot := promptCacheSnapshot{CacheKey: cacheKey}
	if err != nil {
		snapshot.ReadError = true
		logger.LogWarn(nil, fmt.Sprintf("ollama prompt cache read failed channel=%d model=%q: %v", info.ChannelId, info.UpstreamModelName, err))
	} else {
		snapshot.Candidates = activePromptCacheCandidates(promptCacheCandidates(entry), time.Now())
	}
	c.Set(promptCacheIdentityContextKey+".snapshot", snapshot)
}

func resolvePromptCacheIdentity(info *relaycommon.RelayInfo, contexts ...*gin.Context) promptCacheIdentity {
	if len(contexts) > 0 && contexts[0] != nil {
		if value, ok := contexts[0].Get(promptCacheIdentityContextKey); ok {
			if identity, ok := value.(promptCacheIdentity); ok {
				return identity
			}
		}
	}
	request := promptCacheOpenAIRequest(info)
	if request == nil {
		return promptCacheIdentity{}
	}
	return buildOpenAIPromptCacheIdentity(info, request)
}

func buildOllamaChatPromptCacheIdentity(request *OllamaChatRequest) promptCacheIdentity {
	if request == nil {
		return promptCacheIdentity{}
	}
	ttl, cacheable := promptCacheTTLForKeepAlive(request.KeepAlive)
	keyMaterial, _ := common.Marshal(struct {
		Model   string          `json:"model"`
		Tools   any             `json:"tools,omitempty"`
		Format  any             `json:"format,omitempty"`
		Options map[string]any  `json:"options,omitempty"`
		Think   json.RawMessage `json:"think,omitempty"`
	}{Model: request.Model, Tools: request.Tools, Format: request.Format, Options: request.Options, Think: request.Think})
	if len(request.Messages) == 0 {
		return promptCacheIdentity{Family: "chat", KeyMaterial: keyMaterial, TTL: ttl, Clear: !cacheable}
	}
	hashes := make([]string, 0, len(request.Messages))
	rootHash := ""
	for _, message := range request.Messages {
		if len(message.Images) > 0 {
			// Do not serialize/hash image base64 data. Keep a stable root based
			// on the non-image fields only so keep_alive=0 can clear the same
			// fallback partition without retaining multimodal content.
			return promptCacheIdentity{
				Family:      "chat",
				RootHash:    promptCacheRootHashWithoutImages(request.Messages),
				KeyMaterial: keyMaterial,
				TTL:         ttl,
				Clear:       !cacheable,
				Uncacheable: true,
			}
		}
		hash := hashPromptCacheValue(message)
		hashes = append(hashes, hash)
		if rootHash == "" && message.Role == "user" {
			rootHash = hash
		}
	}
	if rootHash == "" {
		rootHash = hashes[0]
	}
	return promptCacheIdentity{Family: "chat", MessageHashes: hashes, RootHash: rootHash, KeyMaterial: keyMaterial, TTL: ttl, Clear: !cacheable}
}

func buildOllamaGeneratePromptCacheIdentity(request *OllamaGenerateRequest) promptCacheIdentity {
	if request == nil {
		return promptCacheIdentity{}
	}
	ttl, cacheable := promptCacheTTLForKeepAlive(request.KeepAlive)
	hash := hashPromptCacheValue(struct {
		Prompt string `json:"prompt"`
		Suffix string `json:"suffix,omitempty"`
	}{Prompt: request.Prompt, Suffix: request.Suffix})
	keyMaterial, _ := common.Marshal(struct {
		Model   string          `json:"model"`
		Format  any             `json:"format,omitempty"`
		Options map[string]any  `json:"options,omitempty"`
		Think   json.RawMessage `json:"think,omitempty"`
	}{Model: request.Model, Format: request.Format, Options: request.Options, Think: request.Think})
	if request.Prompt == "" {
		return promptCacheIdentity{Family: "generate", KeyMaterial: keyMaterial, TTL: ttl, Clear: !cacheable}
	}
	if len(request.Images) > 0 {
		// The prompt/suffix hash is safe; never include image base64 in the
		// identity and never store this request as cacheable.
		return promptCacheIdentity{Family: "generate", RootHash: hash, KeyMaterial: keyMaterial, TTL: ttl, Clear: !cacheable, Uncacheable: true}
	}
	return promptCacheIdentity{Family: "generate", MessageHashes: []string{hash}, RootHash: hash, KeyMaterial: keyMaterial, TTL: ttl, Clear: !cacheable, Uncacheable: len(request.Images) > 0}
}

func promptCacheRootHashWithoutImages(messages []OllamaChatMessage) string {
	for _, message := range messages {
		if message.Role == "user" {
			return hashPromptCacheMessageWithoutImages(message)
		}
	}
	if len(messages) > 0 {
		return hashPromptCacheMessageWithoutImages(messages[0])
	}
	return ""
}

func hashPromptCacheMessageWithoutImages(message OllamaChatMessage) string {
	return hashPromptCacheValue(struct {
		Role       string           `json:"role"`
		Content    string           `json:"content,omitempty"`
		ToolCalls  []OllamaToolCall `json:"tool_calls,omitempty"`
		ToolName   string           `json:"tool_name,omitempty"`
		ToolCallID string           `json:"tool_call_id,omitempty"`
		Thinking   json.RawMessage  `json:"thinking,omitempty"`
	}{
		Role:       message.Role,
		Content:    message.Content,
		ToolCalls:  message.ToolCalls,
		ToolName:   message.ToolName,
		ToolCallID: message.ToolCallID,
		Thinking:   message.Thinking,
	})
}

func promptCacheTTLForKeepAlive(value any) (time.Duration, bool) {
	if value == nil {
		return promptCacheTTL, true
	}
	var duration time.Duration
	switch value := value.(type) {
	case string:
		value = strings.TrimSpace(value)
		if value == "" {
			return promptCacheTTL, true
		}
		if value == "0" {
			return 0, false
		}
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return promptCacheTTL, true
		}
		duration = parsed
	case float64:
		duration = time.Duration(value * float64(time.Second))
	case int:
		duration = time.Duration(value) * time.Second
	case int64:
		duration = time.Duration(value) * time.Second
	case json.Number:
		seconds, err := strconv.ParseFloat(string(value), 64)
		if err != nil {
			return promptCacheTTL, true
		}
		duration = time.Duration(seconds * float64(time.Second))
	default:
		return promptCacheTTL, true
	}
	if duration == 0 {
		return 0, false
	}
	if duration < 0 {
		return promptCacheTTL, true
	}
	if duration < promptCacheTTL {
		return duration, true
	}
	return promptCacheTTL, true
}

func buildOpenAIPromptCacheIdentity(info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) promptCacheIdentity {
	if info == nil || info.ChannelMeta == nil || request == nil {
		return promptCacheIdentity{}
	}
	hashes := buildMessageHashes(request)
	if len(hashes) == 0 {
		return promptCacheIdentity{}
	}
	family := "chat"
	rootHash := ""
	for i, message := range request.Messages {
		if message.Role == "user" {
			rootHash = hashes[i]
			break
		}
	}
	if info != nil && info.RelayMode == relayconstant.RelayModeCompletions {
		family = "generate"
	}
	if rootHash == "" {
		rootHash = hashes[0]
	}
	keyMaterial, _ := common.Marshal(struct {
		SystemPrompt         string                `json:"system_prompt,omitempty"`
		SystemPromptOverride bool                  `json:"system_prompt_override,omitempty"`
		Tools                []dto.ToolCallRequest `json:"tools,omitempty"`
		Think                json.RawMessage       `json:"think,omitempty"`
	}{
		SystemPrompt:         info.ChannelSetting.SystemPrompt,
		SystemPromptOverride: info.ChannelSetting.SystemPromptOverride,
		Tools:                request.Tools,
		Think:                request.Think,
	})
	return promptCacheIdentity{Family: family, MessageHashes: hashes, RootHash: rootHash, KeyMaterial: keyMaterial}
}

func hashPromptCacheValue(value any) string {
	h := sha256.New()
	if data, err := common.Marshal(value); err == nil {
		h.Write(data)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// buildPromptCacheKey constructs a per-channel-model-user-session cache key.
// The session partition is derived from explicit body/header identifiers first,
// then falls back to the same token/user partition used by channel affinity.
func buildPromptCacheKey(info *relaycommon.RelayInfo) string {
	if info == nil || info.ChannelMeta == nil {
		return ""
	}
	return buildPromptCacheKeyWithIdentity(info, resolvePromptCacheIdentity(info))
}

func buildPromptCacheKeyWithIdentity(info *relaycommon.RelayInfo, identity promptCacheIdentity) string {
	if info == nil || info.ChannelMeta == nil {
		return ""
	}
	channelId := info.ChannelId
	credentialId := info.ChannelCredentialId
	multiKeyIndex := 0
	if info.ChannelIsMultiKey {
		multiKeyIndex = info.ChannelMultiKeyIndex
	}
	model := info.UpstreamModelName
	userId := info.UserId
	if userId <= 0 {
		return ""
	}

	sessionId := extractRequestSessionIdentifier(info.Request)
	if sessionId == "" {
		sessionId = extractHeaderSessionIdentifier(info.RequestHeaders)
	}
	fallbackPartition := sessionId == ""
	if sessionId == "" {
		// Match channel affinity's established fallback for clients such as
		// OpenCode that do not send a conversation identifier. The token is
		// the narrowest stable partition; playground requests have no token
		// id, so fall back to the authenticated user.
		if info.TokenId > 0 {
			sessionId = "token:" + strconv.Itoa(info.TokenId)
		} else {
			sessionId = "user:" + strconv.Itoa(userId)
		}
	}

	h := sha256.New()
	h.Write([]byte(strconv.Itoa(channelId)))
	h.Write([]byte{0})
	h.Write([]byte(strings.TrimRight(strings.TrimSpace(info.ChannelBaseUrl), "/")))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(credentialId)))
	h.Write([]byte{0})
	h.Write([]byte(info.ApiKey))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatBool(info.ChannelIsMultiKey)))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(multiKeyIndex)))
	h.Write([]byte{0})
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write([]byte(identity.Family))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(userId)))
	h.Write([]byte{0})
	h.Write([]byte(sessionId))
	if fallbackPartition && identity.RootHash != "" {
		h.Write([]byte{0})
		h.Write([]byte(identity.RootHash))
	}
	h.Write([]byte{0})
	h.Write(identity.KeyMaterial)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// promptCacheOpenAIRequest normalizes all Ollama chat-compatible inputs to the
// same OpenAI message representation used by the adaptor. This keeps Claude
// /v1/messages and OpenAI chat requests in one prompt family without sharing
// completions prompts.
func promptCacheOpenAIRequest(info *relaycommon.RelayInfo) *dto.GeneralOpenAIRequest {
	if info == nil {
		return nil
	}
	if request, ok := info.Request.(*dto.GeneralOpenAIRequest); ok {
		return request
	}
	request, ok := info.Request.(*dto.ClaudeRequest)
	if !ok || request == nil || info.RelayMode == relayconstant.RelayModeCompletions {
		return nil
	}
	converted, err := relayconvert.ClaudeMessagesRequestToOpenAIChat(*request, info)
	if err != nil {
		return nil
	}
	return converted
}

func extractRequestSessionIdentifier(request dto.Request) string {
	switch request := request.(type) {
	case *dto.GeneralOpenAIRequest:
		return extractSessionIdentifier(request)
	case *dto.ClaudeRequest:
		if request == nil || len(request.Metadata) == 0 {
			return ""
		}
		var metadata dto.ClaudeMetadata
		if common.Unmarshal(request.Metadata, &metadata) != nil {
			return ""
		}
		return normalizePromptCacheSessionIdentifier(metadata.UserId)
	default:
		return ""
	}
}

// extractSessionIdentifier returns a stable conversation identifier from the
// request. Priority: prompt_cache_key > metadata.user_id > user.
func extractSessionIdentifier(req *dto.GeneralOpenAIRequest) string {
	if req == nil {
		return ""
	}
	if sessionID := normalizePromptCacheSessionIdentifier(req.PromptCacheKey); sessionID != "" {
		return sessionID
	}
	if len(req.Metadata) > 0 {
		var meta map[string]any
		if err := common.Unmarshal(req.Metadata, &meta); err == nil {
			if uid, ok := meta["user_id"]; ok {
				if sessionID := promptCacheScalarIdentifier(uid); sessionID != "" {
					return sessionID
				}
			}
		}
	}
	if len(req.User) > 0 {
		var userStr string
		if err := common.Unmarshal(req.User, &userStr); err == nil {
			return normalizePromptCacheSessionIdentifier(userStr)
		}
		var scalar any
		if err := common.Unmarshal(req.User, &scalar); err == nil {
			return promptCacheScalarIdentifier(scalar)
		}
	}
	return ""
}

// extractHeaderSessionIdentifier returns a stable conversation identifier from
// the request headers. Header names are normalized so HTTP clients may use
// underscores, hyphens, case variants, or one x- prefix. Body identifiers are
// checked separately and always take precedence over these headers.
func extractHeaderSessionIdentifier(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	headerNames := make([]string, 0, len(headers))
	for name := range headers {
		headerNames = append(headerNames, name)
	}
	sort.Slice(headerNames, func(i, j int) bool {
		return strings.ToLower(headerNames[i]) < strings.ToLower(headerNames[j])
	})
	for _, name := range []string{
		"session_id", "x_session_id", "x_opencode_session", "x_session_affinity",
		"conversation_id", "x_conversation_id",
	} {
		for _, headerName := range headerNames {
			normalizedName := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(headerName), "-", "_"))
			if normalizedName != name {
				continue
			}
			if sessionID := normalizePromptCacheSessionIdentifier(headers[headerName]); sessionID != "" {
				return sessionID
			}
		}
	}
	return ""
}

func normalizePromptCacheSessionIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > promptCacheMaxSessionIDRunes {
		return ""
	}
	return value
}

func promptCacheScalarIdentifier(value any) string {
	switch value := value.(type) {
	case string:
		return normalizePromptCacheSessionIdentifier(value)
	case float64, bool:
		return normalizePromptCacheSessionIdentifier(fmt.Sprintf("%v", value))
	default:
		return ""
	}
}

// buildMessageHashes produces one SHA-256 hex string per message, capturing
// role, content, and tool_calls for conversation-prefix identity.
func buildMessageHashes(req *dto.GeneralOpenAIRequest) []string {
	if req == nil {
		return nil
	}
	if len(req.Messages) > 0 {
		hashes := make([]string, 0, len(req.Messages))
		for _, msg := range req.Messages {
			hashes = append(hashes, hashMessage(msg))
		}
		return hashes
	}
	// /v1/completions path: treat prompt as a single-message conversation.
	if req.Prompt != nil {
		content := normalizeOllamaGeneratePrompt(req.Prompt)
		if content == "" {
			return nil
		}
		suffix, _ := req.Suffix.(string)
		h := sha256.New()
		h.Write([]byte("user"))
		h.Write([]byte{0})
		h.Write([]byte(content))
		h.Write([]byte{0})
		h.Write([]byte(suffix))
		return []string{fmt.Sprintf("%x", h.Sum(nil))}
	}
	return nil
}

func normalizeOllamaGeneratePrompt(prompt any) string {
	switch value := prompt.(type) {
	case string:
		return value
	case []any:
		var builder strings.Builder
		for _, item := range value {
			if text, ok := item.(string); ok {
				builder.WriteString(text)
			}
		}
		return builder.String()
	default:
		return fmt.Sprintf("%v", prompt)
	}
}

func hashMessage(msg dto.Message) string {
	h := sha256.New()
	if data, err := common.Marshal(struct {
		Role             string  `json:"role"`
		Content          string  `json:"content"`
		Name             *string `json:"name,omitempty"`
		Prefix           *bool   `json:"prefix,omitempty"`
		ReasoningContent *string `json:"reasoning_content,omitempty"`
		Reasoning        *string `json:"reasoning,omitempty"`
		ToolCalls        string  `json:"tool_calls,omitempty"`
		ToolCallID       string  `json:"tool_call_id,omitempty"`
	}{
		Role:             msg.Role,
		Content:          resolveMessageContent(msg.Content),
		Name:             msg.Name,
		Prefix:           msg.Prefix,
		ReasoningContent: msg.ReasoningContent,
		Reasoning:        msg.Reasoning,
		ToolCalls:        string(msg.ToolCalls),
		ToolCallID:       msg.ToolCallId,
	}); err == nil {
		h.Write(data)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// resolveMessageContent produces a stable string representation of the
// message content field, which may be a plain string or a structured
// content-parts array.
func resolveMessageContent(content any) string {
	if content == nil {
		return ""
	}
	switch v := content.(type) {
	case string:
		return v
	default:
		b, err := common.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}
