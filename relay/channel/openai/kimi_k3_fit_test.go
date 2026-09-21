package openai

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// usageFromUpstream builds the aggregator-shaped usage object the NeurVibe
// upstream returns: 13 keys with a five-key prompt_tokens_details /
// completion_tokens_details pair.
func usageFromUpstream(t *testing.T) *dto.Usage {
	t.Helper()
	raw := `{"prompt_tokens":700,"completion_tokens":64,"total_tokens":764,
		"completion_tokens_details":{"accepted_prediction_tokens":0,"audio_tokens":0,"reasoning_tokens":61,"rejected_prediction_tokens":0,"cached_tokens":0},
		"prompt_tokens_details":{"accepted_prediction_tokens":0,"audio_tokens":0,"reasoning_tokens":0,"rejected_prediction_tokens":0,"cached_tokens":512},
		"prompt_cache_hit_tokens":512,"prompt_cache_miss_tokens":188,"cache_read_input_tokens":0,
		"cache_creation_input_tokens":0,"prompt_cache_write_tokens":0,"completion_thinking_tokens":61,"credit":0.2,"cached_tokens":0}`
	var usage dto.Usage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return &usage
}

func TestFitKimiK3UsageDropsAggregatorKeys(t *testing.T) {
	usage := usageFromUpstream(t)
	encoded, err := kimiK3UsageJSON(usage, 0)
	if err != nil {
		t.Fatalf("kimiK3UsageJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	allowed := map[string]bool{
		"prompt_tokens": true, "completion_tokens": true, "total_tokens": true,
		"cached_tokens": true, "completion_tokens_details": true, "prompt_tokens_details": true,
	}
	for key := range got {
		if !allowed[key] {
			t.Errorf("official usage must not carry %q", key)
		}
	}
	if got["cached_tokens"] != float64(512) {
		t.Errorf("cached_tokens = %v, want 512", got["cached_tokens"])
	}
	details, _ := got["prompt_tokens_details"].(map[string]any)
	if len(details) != 2 || details["cache_write_tokens"] != float64(0) {
		t.Errorf("prompt_tokens_details = %v, want the official two-key shape", details)
	}
	if _, ok := got["prompt_tokens_details"].(map[string]any)["accepted_prediction_tokens"]; ok {
		t.Error("prompt_tokens_details must not carry accepted_prediction_tokens")
	}
	completion, _ := got["completion_tokens_details"].(map[string]any)
	if len(completion) != 1 || completion["reasoning_tokens"] != float64(61) {
		t.Errorf("completion_tokens_details = %v, want reasoning_tokens only", completion)
	}
}

func TestFitKimiK3UsageOmitsColdCachedTokens(t *testing.T) {
	usage := &dto.Usage{PromptTokens: 89, CompletionTokens: 11}
	encoded, err := kimiK3UsageJSON(usage, 0)
	if err != nil {
		t.Fatalf("kimiK3UsageJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Official omits cached_tokens on a cold request and omits the details
	// object when the response produced no reasoning.
	if _, present := got["cached_tokens"]; present {
		t.Error("cached_tokens must be omitted when nothing was cached")
	}
	if _, present := got["completion_tokens_details"]; present {
		t.Error("completion_tokens_details must be omitted without reasoning tokens")
	}
	if got["total_tokens"] != float64(100) {
		t.Errorf("total_tokens = %v, want the prompt+completion sum", got["total_tokens"])
	}
}

func TestFitKimiK3TextResponseBodyStripsExtensions(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"kimi-k3",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"2","reasoning_content":"thinking",` +
		`"provider":"neurvibe"},"finish_reason":"stop","flag":"x"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"credit":0.1}}`)
	fitted, err := fitKimiK3TextResponseBody(body, &dto.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12})
	if err != nil {
		t.Fatalf("fit: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(fitted, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := got["provider"]; present {
		t.Error("top-level aggregator key survived the fit")
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(got["choices"], &choices); err != nil {
		t.Fatalf("choices: %v", err)
	}
	if _, present := choices[0]["flag"]; present {
		t.Error("choice-level aggregator key survived the fit")
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(choices[0]["message"], &message); err != nil {
		t.Fatalf("message: %v", err)
	}
	if _, present := message["provider"]; present {
		t.Error("message-level aggregator key survived the fit")
	}
	var usage map[string]any
	if err := json.Unmarshal(got["usage"], &usage); err != nil {
		t.Fatalf("usage: %v", err)
	}
	if _, present := usage["credit"]; present {
		t.Error("usage credit survived the fit")
	}
}

func TestFitKimiK3StreamEventMovesUsageIntoChoice(t *testing.T) {
	// The upstream terminal chunk: usage at the top level, no
	// system_fingerprint, and choice.logprobs on every event.
	chunk := `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"kimi-k3",` +
		`"choices":[{"index":0,"delta":{},"finish_reason":"stop","logprobs":null}],` +
		`"usage":{"prompt_tokens":93,"completion_tokens":55,"total_tokens":148,"credit":0.2}}`
	patched, err := fitKimiK3StreamEvent(chunk, &dto.Usage{PromptTokens: 93, CompletionTokens: 55, TotalTokens: 148}, true, false)
	if err != nil {
		t.Fatalf("fit: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(patched), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := got["usage"]; present {
		t.Error("official stream chunks never carry a top-level usage")
	}
	if _, present := got["system_fingerprint"]; present {
		t.Error("the fit must not invent a system_fingerprint")
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(got["choices"], &choices); err != nil {
		t.Fatalf("choices: %v", err)
	}
	if _, present := choices[0]["usage"]; !present {
		t.Fatal("official carries usage on choices[0] of the terminal chunk")
	}
	if _, present := choices[0]["logprobs"]; present {
		t.Error("choice.logprobs must be stripped when the request did not ask for logprobs")
	}
	var choiceUsage map[string]any
	if err := json.Unmarshal(choices[0]["usage"], &choiceUsage); err != nil {
		t.Fatalf("choice usage: %v", err)
	}
	if _, present := choiceUsage["credit"]; present {
		t.Error("choice usage must use the official six-key shape")
	}
}

func TestFitKimiK3StreamEventKeepsRequestedLogprobs(t *testing.T) {
	chunk := `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"kimi-k3",` +
		`"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null,"logprobs":{"content":[]}}]}`
	patched, err := fitKimiK3StreamEvent(chunk, nil, false, true)
	if err != nil {
		t.Fatalf("fit: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(patched), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(got["choices"], &choices); err != nil {
		t.Fatalf("choices: %v", err)
	}
	if _, present := choices[0]["logprobs"]; !present {
		t.Error("a request that asked for logprobs keeps the choice value")
	}
}

func TestFitKimiK3StreamEventDropsUsageOnlyEvent(t *testing.T) {
	// A usage-only event has no choices to attach usage to; the official
	// stream never emits one, so the fit drops it entirely.
	chunk := `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	patched, err := fitKimiK3StreamEvent(chunk, &dto.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}, true, false)
	if err != nil {
		t.Fatalf("fit: %v", err)
	}
	if patched != "" {
		t.Errorf("usage-only event should be dropped, got %q", patched)
	}
}

func TestFitKimiK3StreamEventInjectsUsageWhenUpstreamOmits(t *testing.T) {
	// The upstream only sends usage when the request asked for it, while the
	// official terminal chunk always carries the usage on choices[0]. The fit
	// therefore injects the official usage shape into terminal chunks that
	// arrived without one.
	chunk := `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"kimi-k3",` +
		`"choices":[{"index":0,"delta":{"content":"2"},"finish_reason":"stop"}]}`
	patched, err := fitKimiK3StreamEvent(chunk, &dto.Usage{PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15}, true, false)
	if err != nil {
		t.Fatalf("fit: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(patched), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := got["usage"]; present {
		t.Error("usage belongs on choices[0], never at the top level")
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(got["choices"], &choices); err != nil {
		t.Fatalf("choices: %v", err)
	}
	if _, present := choices[0]["usage"]; !present {
		t.Error("the terminal chunk must carry the official usage even when the upstream omitted it")
	}
	// A non-terminal chunk must not gain usage.
	nonFinal, err := fitKimiK3StreamEvent(chunk, &dto.Usage{PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15}, false, false)
	if err != nil {
		t.Fatalf("fit: %v", err)
	}
	var nonFinalPayload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(nonFinal), &nonFinalPayload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var nonFinalChoices []map[string]json.RawMessage
	if err := json.Unmarshal(nonFinalPayload["choices"], &nonFinalChoices); err != nil {
		t.Fatalf("choices: %v", err)
	}
	if _, present := nonFinalChoices[0]["usage"]; present {
		t.Error("non-terminal chunks never carry usage")
	}
}
