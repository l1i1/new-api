package ollama

import (
	"crypto/sha256"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/pkg/cachex"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/samber/hot"
)

const (
	promptCacheTTL        = 5 * time.Minute
	promptCacheEstimation = 0.5
	// A local fallback entry can contain up to 256 hashes; keep its memory
	// ceiling bounded even when Redis is disabled or unavailable at startup.
	promptCacheCapacity           = 5_000
	promptCacheMaxMessages        = 256
	promptCacheMaxCandidates      = 16
	promptCacheMaxCandidateHashes = promptCacheMaxCandidates * promptCacheMaxMessages
	promptCacheMaxSessionIDRunes  = 256
	promptCacheNamespace          = "ollama_prompt_cache:v1"
)

type promptCacheCandidate struct {
	MessageHashes        []string `json:"h"`
	PreviousPromptTokens int      `json:"p"`
}

// promptCacheEntry stores a bounded set of recent conversation fingerprints.
// The legacy fields remain readable so existing Redis entries survive the
// rollout without a cache flush.
type promptCacheEntry struct {
	Candidates           []promptCacheCandidate `json:"c,omitempty"`
	MessageHashes        []string               `json:"h,omitempty"`
	PreviousPromptTokens int                    `json:"p,omitempty"`
}

var (
	promptCacheOnce sync.Once
	promptCacheInst *cachex.HybridCache[promptCacheEntry]
)

func getPromptCache() *cachex.HybridCache[promptCacheEntry] {
	promptCacheOnce.Do(func() {
		promptCacheInst = cachex.NewHybridCache[promptCacheEntry](cachex.HybridCacheConfig[promptCacheEntry]{
			Namespace:  cachex.Namespace(promptCacheNamespace),
			Redis:      common.RDB,
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
func applyOllamaPromptCacheEstimation(info *relaycommon.RelayInfo, usage *dto.Usage) {
	applyOllamaPromptCacheEstimationWithUpstreamUsage(info, usage, false)
}

func applyOllamaPromptCacheEstimationWithUpstreamUsage(info *relaycommon.RelayInfo, usage *dto.Usage, upstreamCachedTokensPresent bool) {
	if info == nil || usage == nil {
		return
	}
	if info.ChannelMeta == nil {
		return
	}
	if !info.ChannelSetting.OllamaCacheEstimationEnabled {
		return
	}
	if usage.PromptTokens <= 0 {
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
	// Do not apply to embeddings.
	if info.RelayMode == relayconstant.RelayModeEmbeddings {
		return
	}

	request, _ := info.Request.(*dto.GeneralOpenAIRequest)
	if request == nil {
		return
	}

	cacheKey := buildPromptCacheKey(info)
	if cacheKey == "" {
		return
	}
	messageHashes := buildMessageHashes(request)
	if len(messageHashes) == 0 {
		return
	}

	cache := getPromptCache()

	estimated := 0
	cacheErr := cache.UpdateWithTTL(cacheKey, promptCacheTTL, func(previous promptCacheEntry, _ bool) (promptCacheEntry, error) {
		candidateEstimated := 0
		candidates := promptCacheCandidates(previous)
		if allowEstimation {
			if previousTokens, ok := longestPromptCachePrefix(candidates, messageHashes); ok && previousTokens > 0 {
				candidateEstimated = int(math.Floor(float64(previousTokens) * promptCacheEstimation))
				if candidateEstimated > usage.PromptTokens {
					candidateEstimated = usage.PromptTokens
				}
			}
		}
		estimated = candidateEstimated
		return prependPromptCacheCandidate(candidates, promptCacheCandidate{
			MessageHashes:        append([]string(nil), messageHashes...),
			PreviousPromptTokens: usage.PromptTokens,
		}), nil
	})
	if cacheErr != nil {
		estimated = 0
		logger.LogWarn(nil, fmt.Sprintf("ollama prompt cache update failed channel=%d model=%q: %v", info.ChannelId, info.UpstreamModelName, cacheErr))
	}

	if estimated <= 0 {
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
		if len(result) >= promptCacheMaxCandidates || len(candidate.MessageHashes) == 0 || len(candidate.MessageHashes) > promptCacheMaxMessages || candidate.PreviousPromptTokens <= 0 {
			continue
		}
		if totalHashes+len(candidate.MessageHashes) > promptCacheMaxCandidateHashes {
			continue
		}
		result = append(result, promptCacheCandidate{
			MessageHashes:        append([]string(nil), candidate.MessageHashes...),
			PreviousPromptTokens: candidate.PreviousPromptTokens,
		})
		totalHashes += len(candidate.MessageHashes)
	}
	return result
}

func longestPromptCachePrefix(candidates []promptCacheCandidate, current []string) (int, bool) {
	bestLength := 0
	bestTokens := 0
	for _, candidate := range candidates {
		if candidate.PreviousPromptTokens <= 0 || len(candidate.MessageHashes) == 0 || len(candidate.MessageHashes) > len(current) {
			continue
		}
		matched := true
		for i, hash := range candidate.MessageHashes {
			if current[i] != hash {
				matched = false
				break
			}
		}
		if matched && len(candidate.MessageHashes) > bestLength {
			bestLength = len(candidate.MessageHashes)
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
		if len(result) >= promptCacheMaxCandidates || totalHashes >= promptCacheMaxCandidateHashes {
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
		if len(candidate.MessageHashes) == 0 || candidate.PreviousPromptTokens <= 0 {
			continue
		}
		if totalHashes+len(candidate.MessageHashes) > promptCacheMaxCandidateHashes {
			continue
		}
		result = append(result, candidate)
		totalHashes += len(candidate.MessageHashes)
	}
	return promptCacheEntry{Candidates: result}
}

func samePromptCacheCandidate(a, b promptCacheCandidate) bool {
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

// buildPromptCacheKey constructs a per-channel-model-user-session cache key.
// The session partition is derived from explicit body/header identifiers first,
// then falls back to the same token/user partition used by channel affinity.
func buildPromptCacheKey(info *relaycommon.RelayInfo) string {
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

	sessionId := ""
	if req, ok := info.Request.(*dto.GeneralOpenAIRequest); ok {
		sessionId = extractSessionIdentifier(req)
	}
	if sessionId == "" {
		sessionId = extractHeaderSessionIdentifier(info.RequestHeaders)
	}
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
	h.Write([]byte(strconv.Itoa(credentialId)))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatBool(info.ChannelIsMultiKey)))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(multiKeyIndex)))
	h.Write([]byte{0})
	h.Write([]byte(model))
	h.Write([]byte{0})
	// Chat and completions use different Ollama prompt templates and must not
	// share estimated cache state even when the visible message prefix matches.
	h.Write([]byte(strconv.Itoa(info.RelayMode)))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(userId)))
	h.Write([]byte{0})
	h.Write([]byte(sessionId))
	if req, ok := info.Request.(*dto.GeneralOpenAIRequest); ok {
		h.Write([]byte{0})
		h.Write([]byte(info.ChannelSetting.SystemPrompt))
		h.Write([]byte{0})
		if info.ChannelSetting.SystemPromptOverride {
			h.Write([]byte("system_prompt_override"))
		}
		h.Write([]byte{0})
		if tools, err := common.Marshal(req.Tools); err == nil {
			h.Write(tools)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
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
				return promptCacheScalarIdentifier(uid)
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
	for _, target := range []string{"session_id", "conversation_id"} {
		for name, value := range headers {
			normalizedName := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
			normalizedName = strings.TrimPrefix(normalizedName, "x_")
			if normalizedName == "opencode_session" || normalizedName == "session_affinity" {
				normalizedName = "session_id"
			}
			if normalizedName != target {
				continue
			}
			if sessionID := normalizePromptCacheSessionIdentifier(value); sessionID != "" {
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
		if len(req.Messages) > promptCacheMaxMessages {
			return nil
		}
		hashes := make([]string, 0, len(req.Messages))
		for _, msg := range req.Messages {
			hashes = append(hashes, hashMessage(msg))
		}
		return hashes
	}
	// /v1/completions path: treat prompt as a single-message conversation.
	if req.Prompt != nil {
		content := resolveMessageContent(req.Prompt)
		h := sha256.New()
		h.Write([]byte("user"))
		h.Write([]byte{0})
		h.Write([]byte(content))
		return []string{fmt.Sprintf("%x", h.Sum(nil))}
	}
	return nil
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
