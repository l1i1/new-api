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

// deepSeekV4UsagePayload renders usage in the official DeepSeek shape: the
// seven documented keys only. The generic dto.Usage marshals Claude/OpenRouter
// extensions (claude_cache_*, input_tokens, output_tokens, cost), which the
// official endpoint never emits, so the payload is built as an explicit map.
// Missing upstream cache fields keep the official arithmetic identity
// prompt_cache_miss_tokens = prompt_tokens - prompt_cache_hit_tokens.
func deepSeekV4UsagePayload(usage *dto.Usage) map[string]any {
	if usage == nil {
		return nil
	}
	cacheHit := usage.PromptCacheHitTokens
	if cacheHit == 0 {
		cacheHit = usage.PromptTokensDetails.CachedTokens
	}
	cacheMiss := usage.PromptTokens - cacheHit
	if cacheMiss < 0 {
		cacheMiss = 0
	}
	return map[string]any{
		"prompt_tokens":             usage.PromptTokens,
		"completion_tokens":         usage.CompletionTokens,
		"total_tokens":              usage.TotalTokens,
		"prompt_tokens_details":     map[string]any{"cached_tokens": cacheHit},
		"completion_tokens_details": map[string]any{"reasoning_tokens": usage.CompletionTokenDetails.ReasoningTokens},
		"prompt_cache_hit_tokens":   cacheHit,
		"prompt_cache_miss_tokens":  cacheMiss,
	}
}

// fitDeepSeekV4TextResponseBody normalizes a non-stream chat completion body to
// the official DeepSeek V4 schema: strip aggregator extensions (top-level cost,
// null message.tool_calls), replace usage with the official seven-key shape,
// while preserving an upstream-provided system_fingerprint. Values that only
// the real upstream knows are never replaced with a fabricated identity.
func fitDeepSeekV4TextResponseBody(body []byte, usage *dto.Usage) ([]byte, error) {
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
		encodedUsage, err := common.Marshal(deepSeekV4UsagePayload(usage))
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
func fitDeepSeekV4StreamEvent(data string, usage *dto.Usage, includeUsage bool) (string, error) {
	if data == "" {
		return data, nil
	}
	var payload map[string]json.RawMessage
	if err := common.UnmarshalJsonStr(data, &payload); err != nil {
		return data, err
	}
	if includeUsage && usage != nil {
		encodedUsage, err := common.Marshal(deepSeekV4UsagePayload(usage))
		if err != nil {
			return data, err
		}
		payload["usage"] = encodedUsage
	} else if !includeUsage {
		if _, ok := payload["usage"]; ok {
			payload["usage"] = json.RawMessage("null")
		}
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
