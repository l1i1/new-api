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
	promptCacheCapacity          = 5_000
	promptCacheMaxMessages       = 256
	promptCacheMaxSessionIDRunes = 256
	promptCacheNamespace         = "ollama_prompt_cache:v1"
)

// promptCacheEntry stores the previous request's conversation fingerprint
// so a follow-up request with the same prefix can estimate cache hits.
type promptCacheEntry struct {
	MessageHashes        []string `json:"h"`
	PreviousPromptTokens int      `json:"p"`
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
	// Real cached_tokens from upstream take precedence. We still record the
	// current prompt so a later response that omits the field has fresh state.
	allowEstimation := !upstreamCachedTokensPresent && usage.PromptTokensDetails.CachedTokens <= 0
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

	// Look up previous entry before overwriting with current state.
	prev, found, _ := cache.Get(cacheKey)
	estimated := 0
	if allowEstimation && found && prev.PreviousPromptTokens > 0 && len(prev.MessageHashes) > 0 {
		// Validate prefix: previous hashes must be a strict prefix of current.
		if len(prev.MessageHashes) < len(messageHashes) {
			prefixMatch := true
			for i, h := range prev.MessageHashes {
				if messageHashes[i] != h {
					prefixMatch = false
					break
				}
			}
			if prefixMatch {
				estimated = int(math.Floor(float64(prev.PreviousPromptTokens) * promptCacheEstimation))
				if estimated > usage.PromptTokens {
					estimated = usage.PromptTokens
				}
				if estimated > 0 {
					usage.PromptTokensDetails.CachedTokens = estimated
				}
			}
		}
	}

	// Store current state for next request.
	entry := promptCacheEntry{
		MessageHashes:        messageHashes,
		PreviousPromptTokens: usage.PromptTokens,
	}
	_ = cache.SetWithTTL(cacheKey, entry, promptCacheTTL)

	if estimated <= 0 {
		return
	}

	// Attach BillingUsage so settlement uses cache ratio and log path
	// reports billing-usage-openai-estimated.
	billingUsage := dto.NewOpenAIChatBillingUsage(usage)
	if billingUsage != nil {
		billingUsage.Estimated = true
		billingUsage.OpenAIUsage.PromptTokensDetails.CachedTokens = estimated
		usage.BillingUsage = billingUsage
	}
}

// buildPromptCacheKey constructs a per-channel-model-user-session cache key.
// The session identifier is derived from prompt_cache_key, metadata.user_id,
// or the request user field, in that priority order.
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
		return ""
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
