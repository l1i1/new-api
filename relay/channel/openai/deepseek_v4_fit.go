package openai

import (
	"encoding/json"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// isDeepSeekV4ChatModel reports whether the client-facing request targets a
// DeepSeek V4 chat-completions model. The fit layer must key off the origin
// model name (not the upstream-mapped one) so validation and response shaping
// observe the same contract the client requested.
func isDeepSeekV4ChatModel(info *relaycommon.RelayInfo) bool {
	if info == nil || info.RelayMode != relayconstant.RelayModeChatCompletions {
		return false
	}
	modelName := strings.ToLower(strings.TrimSpace(info.OriginModelName))
	return strings.HasPrefix(modelName, "deepseek-v4-")
}

// deepSeekV4UsagePayload renders usage in the observed official DeepSeek
// shape. Disabled-thinking responses omit completion_tokens_details; thinking
// responses include reasoning_tokens. Generic provider extensions never cross
// this client boundary. The billing usage is never mutated; normalization is
// applied to a copy.
func deepSeekV4UsagePayload(usage *dto.Usage, includeReasoningDetails bool) map[string]any {
	if usage == nil {
		return nil
	}
	normalized := *usage
	normalizeDeepSeekV4Usage(&normalized)
	cacheHit := normalized.PromptCacheHitTokens
	cacheMiss := normalized.PromptTokens - cacheHit
	payload := map[string]any{
		"prompt_tokens":            normalized.PromptTokens,
		"completion_tokens":        normalized.CompletionTokens,
		"total_tokens":             normalized.TotalTokens,
		"prompt_tokens_details":    map[string]any{"cached_tokens": cacheHit},
		"prompt_cache_hit_tokens":  cacheHit,
		"prompt_cache_miss_tokens": cacheMiss,
	}
	if includeReasoningDetails {
		payload["completion_tokens_details"] = map[string]any{"reasoning_tokens": normalized.CompletionTokenDetails.ReasoningTokens}
	}
	return payload
}

func normalizeDeepSeekV4Usage(usage *dto.Usage) {
	if usage == nil {
		return
	}
	if usage.PromptTokens < 0 {
		usage.PromptTokens = 0
	}
	if usage.CompletionTokens < 0 {
		usage.CompletionTokens = 0
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

	cacheHit := usage.PromptCacheHitTokens
	if cacheHit == 0 {
		cacheHit = usage.PromptTokensDetails.CachedTokens
	}
	if cacheHit < 0 {
		cacheHit = 0
	}
	if cacheHit > usage.PromptTokens {
		cacheHit = usage.PromptTokens
	}
	usage.PromptCacheHitTokens = cacheHit
	usage.PromptTokensDetails.CachedTokens = cacheHit
	if usage.CompletionTokenDetails.ReasoningTokens < 0 {
		usage.CompletionTokenDetails.ReasoningTokens = 0
	}
}

// fitDeepSeekV4TextResponseBody normalizes a non-stream chat completion body to
// the official DeepSeek V4 schema: strip aggregator extensions (top-level cost,
// null message.tool_calls), replace usage with the official seven-key shape,
// while preserving an upstream-provided system_fingerprint. Values that only
// the real upstream knows are never replaced with a fabricated identity.
func fitDeepSeekV4TextResponseBody(body []byte, usage *dto.Usage, includeReasoningDetails bool) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := common.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	delete(payload, "cost")

	if rawChoices, ok := payload["choices"]; ok {
		choices, err := fitDeepSeekV4Choices(rawChoices)
		if err != nil {
			return nil, err
		}
		payload["choices"] = choices
	}
	if _, ok := payload["usage"]; ok {
		encodedUsage, err := common.Marshal(deepSeekV4UsagePayload(usage, includeReasoningDetails))
		if err != nil {
			return nil, err
		}
		payload["usage"] = encodedUsage
	}
	return common.Marshal(payload)
}

func fitDeepSeekV4Choices(rawChoices json.RawMessage) (json.RawMessage, error) {
	var choices []map[string]json.RawMessage
	if err := common.Unmarshal(rawChoices, &choices); err != nil {
		return nil, err
	}
	for i, choice := range choices {
		rawMessage, ok := choice["message"]
		if !ok {
			continue
		}
		var message map[string]json.RawMessage
		if err := common.Unmarshal(rawMessage, &message); err != nil {
			return nil, err
		}
		if isJSONNull(message["tool_calls"]) {
			delete(message, "tool_calls")
		}
		encodedMessage, err := common.Marshal(message)
		if err != nil {
			return nil, err
		}
		choice["message"] = encodedMessage
		choices[i] = choice
	}
	return common.Marshal(choices)
}

// fitDeepSeekV4StreamEvent renders usage in the official seven-key shape while
// preserving only a real upstream-provided system_fingerprint.
func fitDeepSeekV4StreamEvent(data string, usage *dto.Usage, includeUsage bool, includeReasoningDetails bool) (string, error) {
	if data == "" {
		return data, nil
	}
	var payload map[string]json.RawMessage
	if err := common.UnmarshalJsonStr(data, &payload); err != nil {
		return data, err
	}
	if includeUsage && usage != nil {
		encodedUsage, err := common.Marshal(deepSeekV4UsagePayload(usage, includeReasoningDetails))
		if err != nil {
			return data, err
		}
		payload["usage"] = encodedUsage
	} else if !includeUsage {
		// Official chunks always carry a usage field; non-carriers are null.
		payload["usage"] = json.RawMessage("null")
	}
	patched, err := common.Marshal(payload)
	if err != nil {
		return data, err
	}
	return string(patched), nil
}

func isJSONNull(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	return strings.TrimSpace(string(raw)) == "null"
}
