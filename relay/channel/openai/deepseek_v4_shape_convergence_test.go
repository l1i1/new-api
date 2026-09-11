package openai

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The admission suite's "multiple response consistency" case failed because a
// single aggregator channel fans out to several sub-providers and leaks their
// routing metadata (provider / is_fallback / aiping_id / service_tier) plus
// non-official choice keys (flag). The official envelope is a fixed 7 keys and
// each choice a fixed 4 keys, so the fit layer must converge the envelope
// instead of only stripping top-level `cost`.

func TestFitDeepSeekV4TextBodyStripsNonOfficialTopLevelKeys(t *testing.T) {
	body := []byte(`{"id":"as-1","object":"chat.completion","created":1789120642,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"length","flag":0}],"usage":{"prompt_tokens":84,"completion_tokens":16,"total_tokens":100},"provider":"GMICloud","is_fallback":false,"aiping_id":"as-1","service_tier":null}`)
	usage := &dto.Usage{PromptTokens: 84, CompletionTokens: 16, TotalTokens: 100}

	fitted, err := fitDeepSeekV4TextResponseBody(body, usage, true, false)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(fitted, &payload))
	for _, key := range []string{"provider", "is_fallback", "aiping_id", "service_tier"} {
		assert.NotContains(t, payload, key, "aggregator routing metadata must not reach a fit client")
	}
	choice := payload["choices"].([]any)[0].(map[string]any)
	assert.NotContains(t, choice, "flag", "non-official choice keys must be stripped")
	// Official envelope survives.
	for _, key := range []string{"id", "object", "created", "model", "choices", "usage"} {
		assert.Contains(t, payload, key)
	}
}

func TestFitDeepSeekV4TextBodyPreservesOfficialEnvelopeByteIdentical(t *testing.T) {
	body := []byte(`{"id":"d80a","object":"chat.completion","created":1787661619,"model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":"stop","logprobs":null,"message":{"role":"assistant","content":"2","reasoning_content":"think"}}],"usage":{"prompt_tokens":8,"completion_tokens":32,"total_tokens":40,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":30},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":8},"system_fingerprint":"a26a7955944dc5c60445bff77fac9c8e"}`)
	usage := &dto.Usage{
		PromptTokens:           8,
		CompletionTokens:       32,
		TotalTokens:            40,
		PromptTokensDetails:    dto.InputTokenDetails{CachedTokens: 0},
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 30},
	}

	fitted, err := fitDeepSeekV4TextResponseBody(body, usage, true, true)
	require.NoError(t, err)

	assert.Equal(t, string(body), string(fitted),
		"an official envelope must be forwarded byte-identical, key order included")
}

func TestFitDeepSeekV4StreamEventStripsNonOfficialKeys(t *testing.T) {
	data := `{"id":"as-1","object":"chat.completion.chunk","created":1789120642,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"content":"hi"},"logprobs":null,"finish_reason":null,"flag":0}],"provider":"Wafer","is_fallback":false,"aiping_id":"as-1"}`

	patched, err := fitDeepSeekV4StreamEvent(data, nil, false, true, false)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(patched), &payload))
	for _, key := range []string{"provider", "is_fallback", "aiping_id"} {
		assert.NotContains(t, payload, key)
	}
	choice := payload["choices"].([]any)[0].(map[string]any)
	assert.NotContains(t, choice, "flag")
	assert.Contains(t, payload, "usage", "the official usage field is still spliced in")
}

func TestFitDeepSeekV4StreamEventLeavesOfficialChunkByteIdentical(t *testing.T) {
	data := `{"id":"4ba49bcb-e585-4194-b6a1-21fa19b5810a","object":"chat.completion.chunk","created":1787714309,"model":"deepseek-v4-flash","system_fingerprint":"a26a7955944dc5c60445bff77fac9c8e","choices":[{"index":0,"delta":{"content":"","reasoning_content":null},"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":28,"total_tokens":36,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":26},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":8}}`
	usage := &dto.Usage{
		PromptTokens:           8,
		CompletionTokens:       28,
		TotalTokens:            36,
		PromptTokensDetails:    dto.InputTokenDetails{CachedTokens: 0},
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 26},
	}

	patched, err := fitDeepSeekV4StreamEvent(data, usage, true, true, false)
	require.NoError(t, err)
	assert.Equal(t, data, patched, "the strip pass must be a no-op on official chunks")
}

func TestStripNonOfficialStreamKeysDeclinesMalformedInput(t *testing.T) {
	// A duplicated non-official key is a structural surprise: the surgical edit
	// must decline so the map-based fallback (last value wins) decides.
	_, ok := stripNonOfficialStreamKeys([]byte(`{"id":"a","provider":"x","provider":"y","choices":[]}`))
	assert.False(t, ok, "a duplicated non-official key must decline the surgical edit")

	// Non-JSON input cannot be scanned at all.
	_, ok = stripNonOfficialStreamKeys([]byte(`not json`))
	assert.False(t, ok)
}

// A sub-provider that omits the official required choice keys makes identical
// requests structurally inconsistent: the admission suite counts these as
// distinct variants. Official always emits choice.logprobs (null when not
// requested), so the fit layer must add it.
func TestFitDeepSeekV4TextBodyAddsMissingLogprobs(t *testing.T) {
	body := []byte(`{"id":"as-1","object":"chat.completion","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
	usage := &dto.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}

	fitted, err := fitDeepSeekV4TextResponseBody(body, usage, false, false)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(fitted, &payload))
	choice := payload["choices"].([]any)[0].(map[string]any)
	assert.Contains(t, choice, "logprobs", "the official required choice key must be present")
	assert.Nil(t, choice["logprobs"])
}

func TestFitDeepSeekV4TextBodyKeepsExistingLogprobs(t *testing.T) {
	body := []byte(`{"id":"as-1","object":"chat.completion","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"logprobs":{"content":[]},"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)

	fitted, err := fitDeepSeekV4TextResponseBody(body, &dto.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}, false, false)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(fitted, &payload))
	choice := payload["choices"].([]any)[0].(map[string]any)
	assert.Equal(t, map[string]any{"content": []any{}}, choice["logprobs"], "a real logprobs value must not be clobbered")
}

func TestFitDeepSeekV4StreamEventAddsMissingLogprobs(t *testing.T) {
	data := `{"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}],"usage":null}`

	patched, err := fitDeepSeekV4StreamEvent(data, nil, false, false, false)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(patched), &payload))
	choice := payload["choices"].([]any)[0].(map[string]any)
	assert.Contains(t, choice, "logprobs")
	assert.Nil(t, choice["logprobs"])
}

// The official envelope is byte-stable, so the key completion must be a no-op
// on a chunk that already carries every official key.
func TestEnsureOfficialChoiceKeysNoOpWhenComplete(t *testing.T) {
	choice := []byte(`{"index":0,"logprobs":null,"finish_reason":"stop","message":{"role":"assistant"}}`)
	out, ok := ensureOfficialChoiceKeys(choice)
	require.True(t, ok)
	assert.Equal(t, string(choice), string(out))
}

// The map-based fallback (used when the surgical editor declines) must also
// complete the official required choice keys, or a structurally surprising body
// would still miss logprobs.
func TestFitDeepSeekV4ChoicesFallbackAddsLogprobs(t *testing.T) {
	// A duplicated message key makes the surgical editor decline and the
	// fallback decide the outcome.
	body := []byte(`{"id":"as-1","object":"chat.completion","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"hi","x":1,"x":2},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)

	fitted, err := fitDeepSeekV4TextResponseBody(body, &dto.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}, false, false)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(fitted, &payload))
	choice := payload["choices"].([]any)[0].(map[string]any)
	assert.Contains(t, choice, "logprobs", "the fallback path must complete the official choice keys")
	assert.Nil(t, choice["logprobs"])
	// The non-official message key is still stripped by the fallback.
	msg := choice["message"].(map[string]any)
	assert.NotContains(t, msg, "x")
}

// A choice without a message block still gets the official required keys.
func TestFitDeepSeekV4ChoicesFallbackAddsLogprobsWithoutMessage(t *testing.T) {
	raw, err := fitDeepSeekV4Choices(json.RawMessage(`[{"index":0,"finish_reason":"stop"}]`), false, false)
	require.NoError(t, err)

	var choices []map[string]any
	require.NoError(t, json.Unmarshal(raw, &choices))
	assert.Contains(t, choices[0], "logprobs")
	assert.Nil(t, choices[0]["logprobs"])
}
