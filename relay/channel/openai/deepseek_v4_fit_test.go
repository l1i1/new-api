package openai

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeepSeekV4UsagePayloadMatchesOfficialKeys(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:           8,
		CompletionTokens:       32,
		TotalTokens:            40,
		PromptTokensDetails:    dto.InputTokenDetails{CachedTokens: 4},
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 30},
	}

	encoded, err := json.Marshal(deepSeekV4UsagePayload(usage, true))
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(encoded, &payload))

	assert.Equal(t, []string{
		"completion_tokens",
		"completion_tokens_details",
		"prompt_cache_hit_tokens",
		"prompt_cache_miss_tokens",
		"prompt_tokens",
		"prompt_tokens_details",
		"total_tokens",
	}, keysOf(payload))
	assert.Equal(t, float64(4), payload["prompt_cache_hit_tokens"])
	assert.Equal(t, float64(4), payload["prompt_cache_miss_tokens"])
	assert.Equal(t, map[string]any{"cached_tokens": float64(4)}, payload["prompt_tokens_details"])
	assert.Equal(t, map[string]any{"reasoning_tokens": float64(30)}, payload["completion_tokens_details"])

	var details struct {
		PromptTokensDetails     map[string]any `json:"prompt_tokens_details"`
		CompletionTokensDetails map[string]any `json:"completion_tokens_details"`
	}
	require.NoError(t, json.Unmarshal(encoded, &details))
	assert.Equal(t, []string{"cached_tokens"}, keysOf(details.PromptTokensDetails))
	assert.Equal(t, []string{"reasoning_tokens"}, keysOf(details.CompletionTokensDetails))
}

func TestDeepSeekV4UsagePayloadFallsBackToHitTokensField(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:         16,
		CompletionTokens:     13,
		TotalTokens:          29,
		PromptCacheHitTokens: 10,
	}

	payload := deepSeekV4UsagePayload(usage, true)
	assert.Equal(t, 10, payload["prompt_cache_hit_tokens"])
	assert.Equal(t, 6, payload["prompt_cache_miss_tokens"])
}

func TestDeepSeekV4UsagePayloadEnforcesOfficialArithmetic(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:         8,
		CompletionTokens:     3,
		TotalTokens:          99,
		PromptCacheHitTokens: 10,
		CompletionTokenDetails: dto.OutputTokenDetails{
			ReasoningTokens: -1,
		},
	}

	payload := deepSeekV4UsagePayload(usage, true)
	assert.Equal(t, 8, payload["prompt_tokens"])
	assert.Equal(t, 3, payload["completion_tokens"])
	assert.Equal(t, 11, payload["total_tokens"])
	assert.Equal(t, 8, payload["prompt_cache_hit_tokens"])
	assert.Equal(t, 0, payload["prompt_cache_miss_tokens"])
	assert.Equal(t, map[string]any{"cached_tokens": 8}, payload["prompt_tokens_details"])
	assert.Equal(t, map[string]any{"reasoning_tokens": 0}, payload["completion_tokens_details"])
}

func TestDeepSeekV4UsagePayloadOmitsReasoningDetailsWhenThinkingDisabled(t *testing.T) {
	payload := deepSeekV4UsagePayload(&dto.Usage{
		PromptTokens:           9,
		CompletionTokens:       95,
		TotalTokens:            104,
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 90},
	}, false)

	assert.Equal(t, []string{
		"completion_tokens",
		"prompt_cache_hit_tokens",
		"prompt_cache_miss_tokens",
		"prompt_tokens",
		"prompt_tokens_details",
		"total_tokens",
	}, keysOf(payload))
	assert.NotContains(t, payload, "completion_tokens_details")
}

func TestFitDeepSeekV4TextResponseBodyStripsAggregatorExtensions(t *testing.T) {
	body := []byte(`{"id":"router-1","object":"chat.completion","created":1787661622,"model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":"stop","logprobs":null,"message":{"role":"assistant","content":"2","reasoning_content":"think","tool_calls":null}}],"usage":{"prompt_tokens":8,"completion_tokens":31,"total_tokens":39,"prompt_tokens_details":{}},"cost":"0"}`)
	usage := &dto.Usage{
		PromptTokens:           8,
		CompletionTokens:       31,
		TotalTokens:            39,
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 28},
	}

	fitted, err := fitDeepSeekV4TextResponseBody(body, usage, true)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(fitted, &payload))

	assert.NotContains(t, payload, "cost")
	assert.NotContains(t, payload, "system_fingerprint")

	choices := payload["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	assert.NotContains(t, message, "tool_calls")

	usagePayload := payload["usage"].(map[string]any)
	assert.Equal(t, []string{
		"completion_tokens",
		"completion_tokens_details",
		"prompt_cache_hit_tokens",
		"prompt_cache_miss_tokens",
		"prompt_tokens",
		"prompt_tokens_details",
		"total_tokens",
	}, keysOf(usagePayload))

}

func TestFitDeepSeekV4TextResponseBodyPreservesOfficialBody(t *testing.T) {
	body := []byte(`{"id":"d80a","object":"chat.completion","created":1787661619,"model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":"stop","logprobs":null,"message":{"role":"assistant","content":"2","reasoning_content":"think"}}],"usage":{"prompt_tokens":8,"completion_tokens":32,"total_tokens":40,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":30},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":8},"system_fingerprint":"a26a7955944dc5c60445bff77fac9c8e"}`)
	usage := &dto.Usage{
		PromptTokens:           8,
		CompletionTokens:       32,
		TotalTokens:            40,
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 30},
	}

	fitted, err := fitDeepSeekV4TextResponseBody(body, usage, true)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(fitted, &payload))
	assert.Equal(t, "a26a7955944dc5c60445bff77fac9c8e", payload["system_fingerprint"])

	usagePayload := payload["usage"].(map[string]any)
	assert.Equal(t, float64(8), usagePayload["prompt_cache_miss_tokens"])
	assert.Equal(t, map[string]any{"reasoning_tokens": float64(30)}, usagePayload["completion_tokens_details"])
}

func TestFitDeepSeekV4TextResponseBodyKeepsToolCalls(t *testing.T) {
	body := []byte(`{"id":"router-1","object":"chat.completion","created":1787661622,"model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}}]}}],"usage":{"prompt_tokens":371,"completion_tokens":63,"total_tokens":434},"cost":"0"}`)
	usage := &dto.Usage{PromptTokens: 371, CompletionTokens: 63, TotalTokens: 434}

	fitted, err := fitDeepSeekV4TextResponseBody(body, usage, false)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(fitted, &payload))
	choices := payload["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	assert.Contains(t, message, "tool_calls")
}

func TestFitDeepSeekV4StreamUsageEventInjectsOfficialUsage(t *testing.T) {
	data := `{"id":"router-145508b3edb6f4079306d8e9a4ee71e0","object":"chat.completion.chunk","created":1787662155,"model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":"stop","logprobs":null,"delta":{"reasoning_content":null}}],"usage":null}`
	usage := &dto.Usage{
		PromptTokens:           9,
		CompletionTokens:       64,
		TotalTokens:            73,
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 50},
	}

	patched, err := fitDeepSeekV4StreamEvent(data, usage, true, true)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(patched), &payload))

	assert.NotContains(t, payload, "system_fingerprint")
	usagePayload := payload["usage"].(map[string]any)
	assert.Equal(t, float64(9), usagePayload["prompt_tokens"])
	assert.Equal(t, float64(64), usagePayload["completion_tokens"])
	assert.Equal(t, float64(0), usagePayload["prompt_cache_hit_tokens"])
	assert.Equal(t, float64(9), usagePayload["prompt_cache_miss_tokens"])
	assert.Equal(t, map[string]any{"reasoning_tokens": float64(50)}, usagePayload["completion_tokens_details"])
}

func TestFitDeepSeekV4StreamUsageEventReplacesClaudeExtensionUsage(t *testing.T) {
	data := `{"choices":[],"created":1787662155,"id":"router-1","model":"deepseek-v4-flash","object":"chat.completion.chunk","usage":{"claude_cache_creation_1_h_tokens":0,"claude_cache_creation_5_m_tokens":0,"completion_tokens":1093,"completion_tokens_details":{"text_tokens":0,"audio_tokens":0,"image_tokens":0,"reasoning_tokens":0},"input_tokens":0,"input_tokens_details":null,"output_tokens":0,"prompt_tokens":9,"prompt_tokens_details":{"cached_tokens":0,"text_tokens":0,"audio_tokens":0,"image_tokens":0},"total_tokens":1102}}`
	usage := &dto.Usage{PromptTokens: 9, CompletionTokens: 1093, TotalTokens: 1102}

	patched, err := fitDeepSeekV4StreamEvent(data, usage, true, true)
	require.NoError(t, err)

	var payload struct {
		Usage map[string]any `json:"usage"`
	}
	require.NoError(t, json.Unmarshal([]byte(patched), &payload))
	assert.Equal(t, []string{
		"completion_tokens",
		"completion_tokens_details",
		"prompt_cache_hit_tokens",
		"prompt_cache_miss_tokens",
		"prompt_tokens",
		"prompt_tokens_details",
		"total_tokens",
	}, keysOf(payload.Usage))
	assert.NotContains(t, payload.Usage, "claude_cache_creation_1_h_tokens")
	assert.NotContains(t, payload.Usage, "input_tokens")
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func TestFitDeepSeekV4StreamEventDoesNotFabricateFingerprint(t *testing.T) {
	data := `{"id":"router-1","object":"chat.completion.chunk","created":1787662155,"model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":null,"logprobs":null,"delta":{"reasoning_content":"We"}}]}`

	patched, err := fitDeepSeekV4StreamEvent(data, nil, true, true)
	require.NoError(t, err)

	var payload struct {
		SystemFingerprint *string          `json:"system_fingerprint"`
		Usage             map[string]any   `json:"usage"`
		Choices           []map[string]any `json:"choices"`
	}
	require.NoError(t, json.Unmarshal([]byte(patched), &payload))
	assert.Nil(t, payload.SystemFingerprint)
	assert.Nil(t, payload.Usage, "usage must not be synthesized for intermediate events")
	require.Len(t, payload.Choices, 1)
}

func TestFitDeepSeekV4StreamEventPreservesNullFingerprint(t *testing.T) {
	data := `{"id":"router-1","object":"chat.completion.chunk","system_fingerprint":null,"choices":[]}`

	patched, err := fitDeepSeekV4StreamEvent(data, nil, true, true)
	require.NoError(t, err)

	var payload struct {
		SystemFingerprint *string `json:"system_fingerprint"`
	}
	require.NoError(t, json.Unmarshal([]byte(patched), &payload))
	assert.Nil(t, payload.SystemFingerprint)
}

func TestFitDeepSeekV4StreamEventSuppressesUsage(t *testing.T) {
	data := `{"choices":[{"index":0,"finish_reason":"stop","delta":{}}],"usage":{"prompt_tokens":1}}`

	patched, err := fitDeepSeekV4StreamEvent(data, &dto.Usage{PromptTokens: 1}, false, true)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(patched), &payload))
	assert.Nil(t, payload["usage"])
}
