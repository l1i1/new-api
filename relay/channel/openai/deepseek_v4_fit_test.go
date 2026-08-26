package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Official usage key order observed from api.deepseek.com on 2026-08-26
// (K01/K04/K13 evidence under E:\Temp\ds-fit, untracked).
const officialV4UsageKeyOrder = "prompt_tokens,completion_tokens,total_tokens,prompt_tokens_details,completion_tokens_details,prompt_cache_hit_tokens,prompt_cache_miss_tokens"

func TestDeepSeekV4UsageJSONMatchesOfficialKeyOrder(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:           8,
		CompletionTokens:       32,
		TotalTokens:            40,
		PromptTokensDetails:    dto.InputTokenDetails{CachedTokens: 4},
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 30},
	}

	encoded, err := deepSeekV4UsageJSON(usage, true)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(encoded, &payload))

	assert.Equal(t, []string{
		"prompt_tokens",
		"completion_tokens",
		"total_tokens",
		"prompt_tokens_details",
		"completion_tokens_details",
		"prompt_cache_hit_tokens",
		"prompt_cache_miss_tokens",
	}, rawKeyOrder(string(encoded)))
	assert.Equal(t, float64(4), payload["prompt_cache_hit_tokens"])
	assert.Equal(t, float64(4), payload["prompt_cache_miss_tokens"])
	assert.Equal(t, map[string]any{"cached_tokens": float64(4)}, payload["prompt_tokens_details"])
	assert.Equal(t, map[string]any{"reasoning_tokens": float64(30)}, payload["completion_tokens_details"])
}

func TestDeepSeekV4UsageJSONFallsBackToHitTokensField(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:         16,
		CompletionTokens:     13,
		TotalTokens:          29,
		PromptCacheHitTokens: 10,
	}

	encoded, err := deepSeekV4UsageJSON(usage, true)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(encoded, &payload))
	assert.Equal(t, float64(10), payload["prompt_cache_hit_tokens"])
	assert.Equal(t, float64(6), payload["prompt_cache_miss_tokens"])
}

func TestDeepSeekV4UsageJSONEnforcesOfficialArithmetic(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:         8,
		CompletionTokens:     3,
		TotalTokens:          99,
		PromptCacheHitTokens: 10,
		CompletionTokenDetails: dto.OutputTokenDetails{
			ReasoningTokens: -1,
		},
	}

	encoded, err := deepSeekV4UsageJSON(usage, true)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(encoded, &payload))
	assert.Equal(t, float64(8), payload["prompt_tokens"])
	assert.Equal(t, float64(3), payload["completion_tokens"])
	assert.Equal(t, float64(11), payload["total_tokens"])
	assert.Equal(t, float64(8), payload["prompt_cache_hit_tokens"])
	assert.Equal(t, float64(0), payload["prompt_cache_miss_tokens"])
	assert.Equal(t, map[string]any{"cached_tokens": float64(8)}, payload["prompt_tokens_details"])
	assert.Equal(t, map[string]any{"reasoning_tokens": float64(0)}, payload["completion_tokens_details"])
	assert.Equal(t, 99, usage.TotalTokens, "response shaping must not mutate billing usage")
	assert.Equal(t, 10, usage.PromptCacheHitTokens)
	assert.Equal(t, -1, usage.CompletionTokenDetails.ReasoningTokens)
}

func TestDeepSeekV4UsageJSONOmitsReasoningDetailsWhenThinkingDisabled(t *testing.T) {
	encoded, err := deepSeekV4UsageJSON(&dto.Usage{
		PromptTokens:           9,
		CompletionTokens:       95,
		TotalTokens:            104,
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 90},
	}, false)
	require.NoError(t, err)

	assert.Equal(t,
		"prompt_tokens,completion_tokens,total_tokens,prompt_tokens_details,prompt_cache_hit_tokens,prompt_cache_miss_tokens",
		joinedKeyOrder(string(encoded)))
}

func TestDeepSeekV4UsageJSONNilUsageRendersNull(t *testing.T) {
	encoded, err := deepSeekV4UsageJSON(nil, true)
	require.NoError(t, err)
	assert.Equal(t, "null", string(encoded))
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
	assert.NotContains(t, payload, "system_fingerprint", "fingerprint is never fabricated")

	choices := payload["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	assert.NotContains(t, message, "tool_calls")

	usageEncoded := usageValueOf(t, fitted)
	assert.Equal(t, officialV4UsageKeyOrder, joinedKeyOrder(usageEncoded))
}

func TestFitDeepSeekV4TextResponseBodyPreservesOfficialBody(t *testing.T) {
	// Key order mirrors the observed official non-stream response exactly,
	// including system_fingerprint last.
	body := []byte(`{"id":"d80a","object":"chat.completion","created":1787661619,"model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":"stop","logprobs":null,"message":{"role":"assistant","content":"2","reasoning_content":"think"}}],"usage":{"prompt_tokens":8,"completion_tokens":32,"total_tokens":40,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":30},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":8},"system_fingerprint":"a26a7955944dc5c60445bff77fac9c8e"}`)
	usage := &dto.Usage{
		PromptTokens:           8,
		CompletionTokens:       32,
		TotalTokens:            40,
		PromptTokensDetails:    dto.InputTokenDetails{CachedTokens: 0},
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 30},
	}

	fitted, err := fitDeepSeekV4TextResponseBody(body, usage, true)
	require.NoError(t, err)

	want := `{"id":"d80a","object":"chat.completion","created":1787661619,"model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":"stop","logprobs":null,"message":{"role":"assistant","content":"2","reasoning_content":"think"}}],"usage":{"prompt_tokens":8,"completion_tokens":32,"total_tokens":40,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":30},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":8},"system_fingerprint":"a26a7955944dc5c60445bff77fac9c8e"}`
	assert.Equal(t, want, string(fitted), "an official-shaped body must be forwarded byte-identical")
}

func TestFitDeepSeekV4TextResponseBodyReplacesUsageInPlace(t *testing.T) {
	// Aggregator body whose usage needs replacement; every other byte,
	// including key order, must survive.
	body := []byte(`{"id":"router-1","object":"chat.completion","created":1787661622,"model":"deepseek-v4-flash","system_fingerprint":null,"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"2"}}],"usage":{"prompt_tokens":8,"completion_tokens":31,"total_tokens":39}}`)
	usage := &dto.Usage{
		PromptTokens:           8,
		CompletionTokens:       31,
		TotalTokens:            39,
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 28},
	}

	fitted, err := fitDeepSeekV4TextResponseBody(body, usage, true)
	require.NoError(t, err)

	want := `{"id":"router-1","object":"chat.completion","created":1787661622,"model":"deepseek-v4-flash","system_fingerprint":null,"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"2"}}],"usage":{"prompt_tokens":8,"completion_tokens":31,"total_tokens":39,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":28},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":8}}`
	assert.Equal(t, want, string(fitted))
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

	assert.NotContains(t, payload, "system_fingerprint", "fingerprint is never fabricated")
	usagePayload := payload["usage"].(map[string]any)
	assert.Equal(t, float64(9), usagePayload["prompt_tokens"])
	assert.Equal(t, float64(64), usagePayload["completion_tokens"])
	assert.Equal(t, float64(0), usagePayload["prompt_cache_hit_tokens"])
	assert.Equal(t, float64(9), usagePayload["prompt_cache_miss_tokens"])
	assert.Equal(t, map[string]any{"reasoning_tokens": float64(50)}, usagePayload["completion_tokens_details"])
	assert.Equal(t, officialV4UsageKeyOrder, joinedKeyOrder(usageValueOf(t, []byte(patched))))
}

func TestFitDeepSeekV4StreamEventForwardsOfficialChunkByteIdentical(t *testing.T) {
	// Byte-for-byte official terminal chunk from K13 evidence: usage must be
	// re-rendered identically, leaving the whole chunk unchanged.
	data := `{"id":"4ba49bcb-e585-4194-b6a1-21fa19b5810a","object":"chat.completion.chunk","created":1787714309,"model":"deepseek-v4-flash","system_fingerprint":"a26a7955944dc5c60445bff77fac9c8e","choices":[{"index":0,"delta":{"content":"","reasoning_content":null},"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":28,"total_tokens":36,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":26},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":8}}`
	usage := &dto.Usage{
		PromptTokens:           8,
		CompletionTokens:       28,
		TotalTokens:            36,
		PromptTokensDetails:    dto.InputTokenDetails{CachedTokens: 0},
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 26},
	}

	patched, err := fitDeepSeekV4StreamEvent(data, usage, true, true)
	require.NoError(t, err)
	assert.Equal(t, data, patched, "an official-shaped chunk must be forwarded byte-identical")
}

func TestFitDeepSeekV4StreamEventInjectsUsageLastOnAggregatorChunk(t *testing.T) {
	// Aggregator chunk without usage: the null lands last, matching official.
	data := `{"id":"router-1","object":"chat.completion.chunk","created":1787662155,"model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":null,"logprobs":null,"delta":{"reasoning_content":"We"}}]}`

	patched, err := fitDeepSeekV4StreamEvent(data, nil, false, true)
	require.NoError(t, err)

	want := `{"id":"router-1","object":"chat.completion.chunk","created":1787662155,"model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":null,"logprobs":null,"delta":{"reasoning_content":"We"}}],"usage":null}`
	assert.Equal(t, want, patched)
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
	assert.Equal(t, officialV4UsageKeyOrder, joinedKeyOrder(usageValueOf(t, []byte(patched))))
	assert.NotContains(t, payload.Usage, "claude_cache_creation_1_h_tokens")
	assert.NotContains(t, payload.Usage, "input_tokens")
}

func TestFitDeepSeekV4StreamEventSuppressesUsage(t *testing.T) {
	data := `{"choices":[{"index":0,"finish_reason":"stop","delta":{}}],"usage":{"prompt_tokens":1}}`

	patched, err := fitDeepSeekV4StreamEvent(data, &dto.Usage{PromptTokens: 1}, false, true)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(patched), &payload))
	assert.Nil(t, payload["usage"])
}

func TestFitDeepSeekV4StreamEventKeepUpstreamBytesWhenUsageRequestedButMissing(t *testing.T) {
	data := `{"id":"router-1","object":"chat.completion.chunk","choices":[{"index":0,"finish_reason":"stop","delta":{}}]}`

	patched, err := fitDeepSeekV4StreamEvent(data, nil, true, true)
	require.NoError(t, err)
	assert.Equal(t, data, patched, "usage requested but nothing to render keeps upstream bytes")
}

func TestFitDeepSeekV4StreamEventHandlesDuplicateUsageKeyFallback(t *testing.T) {
	// Duplicate keys defeat the surgical splice; the fallback must still
	// emit exactly one official usage value.
	data := `{"id":"router-1","usage":{"prompt_tokens":1},"usage":{"prompt_tokens":2},"choices":[]}`
	usage := &dto.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}

	patched, err := fitDeepSeekV4StreamEvent(data, usage, true, true)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(patched), &payload))
	usagePayload := payload["usage"].(map[string]any)
	assert.Equal(t, float64(2), usagePayload["prompt_tokens"])
	assert.Equal(t, officialV4UsageKeyOrder, joinedKeyOrder(usageValueOf(t, []byte(patched))))
}

func TestFitDeepSeekV4StreamEventHandlesWeirdWhitespace(t *testing.T) {
	data := " {\n \"id\" : \"router-1\" , \"choices\": [ ] } "

	patched, err := fitDeepSeekV4StreamEvent(data, nil, false, true)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(patched), &payload))
	assert.Nil(t, payload["usage"])
	assert.Equal(t, "router-1", payload["id"])
}

func TestFitDeepSeekV4StreamEventDoesNotFabricateFingerprint(t *testing.T) {
	data := `{"id":"router-1","object":"chat.completion.chunk","created":1787662155,"model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":null,"logprobs":null,"delta":{"reasoning_content":"We"}}]}`

	patched, err := fitDeepSeekV4StreamEvent(data, nil, false, true)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(patched), &payload))
	assert.NotContains(t, payload, "system_fingerprint", "fingerprint is never fabricated")
	usage, ok := payload["usage"]
	require.True(t, ok, "official intermediate chunks carry an explicit usage field")
	assert.Nil(t, usage)
	require.Len(t, payload["choices"].([]any), 1)
}

func TestFitDeepSeekV4StreamEventPreservesNullFingerprint(t *testing.T) {
	data := `{"id":"router-1","object":"chat.completion.chunk","system_fingerprint":null,"choices":[]}`

	patched, err := fitDeepSeekV4StreamEvent(data, nil, false, true)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(patched), &payload))
	fingerprint, ok := payload["system_fingerprint"]
	require.True(t, ok)
	assert.Nil(t, fingerprint)
	usage, ok := payload["usage"]
	require.True(t, ok)
	assert.Nil(t, usage)
}

func TestFitDeepSeekV4TextResponseBodySoleCostPair(t *testing.T) {
	// Degenerate single-pair bodies exercise the delete path's brace handling.
	body := []byte(`{"cost":"0"}`)
	fitted, err := fitDeepSeekV4TextResponseBody(body, nil, true)
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(fitted))
}

func TestFitDeepSeekV4TextResponseBodyMultipleNullToolCalls(t *testing.T) {
	body := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","tool_calls":null},"finish_reason":"stop"},{"index":1,"message":{"role":"assistant","tool_calls":null},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	usage := &dto.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}

	fitted, err := fitDeepSeekV4TextResponseBody(body, usage, false)
	require.NoError(t, err)

	want := `{"choices":[{"index":0,"message":{"role":"assistant"},"finish_reason":"stop"},{"index":1,"message":{"role":"assistant"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"prompt_tokens_details":{"cached_tokens":0},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":1}}`
	assert.Equal(t, want, string(fitted))
}

func rawKeyOrder(s string) []string {
	var keys []string
	depth := 0
	inString := false
	escaped := false
	expectKey := false
	var current strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
				if expectKey && depth == 1 {
					keys = append(keys, current.String())
				}
			} else {
				current.WriteByte(c)
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			current.Reset()
			if depth == 1 {
				expectKey = true
			}
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case ':':
			if depth == 1 {
				expectKey = false
			}
		}
	}
	return keys
}

func usageValueOf(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Usage json.RawMessage `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(body, &payload))
	return string(payload.Usage)
}

// joinedKeyOrder formats rawKeyOrder output for direct comparison.
func joinedKeyOrder(s string) string {
	return strings.Join(rawKeyOrder(s), ",")
}

func TestStripReasoningContentPreservesKeyOrder(t *testing.T) {
	// Regression for the thinking-disabled path: reasoning keys are removed
	// surgically so the upstream key order survives (K05/K10 evidence).
	body := []byte(`{"id":"router-1","object":"chat.completion","created":1787728809,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"生机","reasoning_content":"think"},"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11}}`)

	stripped, err := stripReasoningContentFromResponseBody(body)
	require.NoError(t, err)

	want := `{"id":"router-1","object":"chat.completion","created":1787728809,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"生机"},"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11}}`
	assert.Equal(t, want, string(stripped))
}

func TestStripReasoningContentRemovesReasoningLogprobs(t *testing.T) {
	body := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok","reasoning_content":"t"},"logprobs":{"content":[{"token":"a"}],"reasoning_content":[{"token":"b"}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)

	stripped, err := stripReasoningContentFromResponseBody(body)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(stripped, &payload))
	choice := payload["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	assert.NotContains(t, message, "reasoning_content")
	logprobs := choice["logprobs"].(map[string]any)
	assert.NotContains(t, logprobs, "reasoning_content")
	assert.Contains(t, logprobs, "content")
}

func TestStripReasoningContentFallbackOnDuplicateKeys(t *testing.T) {
	// Duplicate keys defeat the splice scan; the map fallback still strips.
	body := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"t"},"message":{"role":"assistant","reasoning_content":"t"}}],"usage":{"prompt_tokens":1}}`)

	stripped, err := stripReasoningContentFromResponseBody(body)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(stripped, &payload))
	choice := payload["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	assert.NotContains(t, message, "reasoning_content")
}
