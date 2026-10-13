package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

func TestSanitizeClientUsageJSONZeroesGarbageUsage(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1388347719831046,"completion_tokens":562949953421056,"total_tokens":1951297673252102,"prompt_tokens_details":{"cached_tokens":322313980642816}}}`)

	patched := SanitizeClientUsageJSON(body)
	var payload struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(patched, &payload); err != nil {
		t.Fatalf("patched body is not JSON: %v (%s)", err, patched)
	}
	if payload.ID != "chatcmpl-1" || len(payload.Choices) != 1 || payload.Choices[0].Message.Content != "hi" {
		t.Errorf("non-usage fields were altered: %s", patched)
	}
	if got := payload.Usage["prompt_tokens"].(float64); got != 0 {
		t.Errorf("prompt_tokens = %v, want 0", got)
	}
	if got := payload.Usage["total_tokens"].(float64); got != 0 {
		t.Errorf("total_tokens = %v, want 0", got)
	}
	details, ok := payload.Usage["prompt_tokens_details"].(map[string]any)
	if !ok {
		t.Fatalf("prompt_tokens_details lost its shape: %v", payload.Usage["prompt_tokens_details"])
	}
	if got := details["cached_tokens"].(float64); got != 0 {
		t.Errorf("cached_tokens = %v, want 0", got)
	}
}

func TestSanitizeClientUsageJSONLeavesPlausibleBodyIdentical(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{"usage":{"prompt_tokens":19610,"completion_tokens":200,"total_tokens":19810,"prompt_tokens_details":{"cached_tokens":9728}}}`),
		[]byte(`{"id":"x","choices":[{"delta":{"content":"hello"}}]}`),
		[]byte(`{"usage":null}`),
		[]byte(`data: {"usage":{"prompt_tokens":1}}`),
		[]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), // binary payload (audio)
		[]byte(`not json at all`),
		[]byte(`{"usage":{"prompt_tokens":10000000}}`), // boundary stays plausible
	}
	for _, body := range bodies {
		if got := SanitizeClientUsageJSON(body); !bytes.Equal(got, body) {
			t.Errorf("body rewritten:\n in  %s\n out %s", body, got)
		}
	}
}

func TestSanitizeClientUsageJSONCoversEveryShape(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		expect []string
	}{
		{
			name:   "responses api",
			body:   `{"id":"resp_1","usage":{"input_tokens":1388347719831046,"output_tokens":12,"total_tokens":1388347719831058,"input_tokens_details":{"cached_tokens":7}}}`,
			expect: []string{`"input_tokens":0`, `"output_tokens":0`, `"total_tokens":0`, `"cached_tokens":0`},
		},
		{
			name:   "gemini usage metadata",
			body:   `{"candidates":[],"usageMetadata":{"promptTokenCount":1388347719831046,"candidatesTokenCount":5,"totalTokenCount":1388347719831051,"promptTokensDetails":[{"modality":"TEXT","tokenCount":1388347719831046}]}}`,
			expect: []string{`"promptTokenCount":0`, `"candidatesTokenCount":0`, `"totalTokenCount":0`, `"tokenCount":0`},
		},
		{
			name:   "claude messages",
			body:   `{"type":"message_delta","usage":{"input_tokens":1388347719831046,"output_tokens":5,"cache_read_input_tokens":322313980642816}}`,
			expect: []string{`"input_tokens":0`, `"output_tokens":0`, `"cache_read_input_tokens":0`},
		},
		{
			name:   "negative count",
			body:   `{"usage":{"prompt_tokens":-1,"completion_tokens":5}}`,
			expect: []string{`"prompt_tokens":0`, `"completion_tokens":0`},
		},
	}
	for _, tc := range cases {
		got := string(SanitizeClientUsageJSON([]byte(tc.body)))
		for _, want := range tc.expect {
			if !bytes.Contains([]byte(got), []byte(want)) {
				t.Errorf("%s: patched body %s is missing %s", tc.name, got, want)
			}
		}
		for _, raw := range []string{"1388347719831046", "322313980642816"} {
			if bytes.Contains([]byte(got), []byte(raw)) {
				t.Errorf("%s: garbage value %s survived: %s", tc.name, raw, got)
			}
		}
	}
}

// usageKitchenSinkBody is the union of every count name the platform or its
// upstreams put in a usage object, including keys this fork does not model
// (completion_thinking_tokens, prompt_cache_write_tokens, credit, ...). The
// generic assertion below fails if any single number survives.
const usageKitchenSinkBody = `{"id":"chatcmpl-sink","created":1710000000123456789,"choices":[],"usage":{"prompt_tokens":1388347719831046,"completion_tokens":1388347719831046,"total_tokens":1388347719831046,"prompt_cache_hit_tokens":1388347719831046,"input_tokens":1388347719831046,"output_tokens":1388347719831046,"claude_cache_creation_5_m_tokens":1388347719831046,"claude_cache_creation_1_h_tokens":1388347719831046,"completion_thinking_tokens":1388347719831046,"prompt_cache_write_tokens":1388347719831046,"cache_read_input_tokens":1388347719831046,"credit":1388347719831046,"cost":0.000123,"usage_semantic":"anthropic","prompt_tokens_details":{"cached_tokens":1388347719831046,"cached_creation_tokens":1388347719831046,"cache_write_tokens":1388347719831046,"text_tokens":1388347719831046,"image_tokens":1388347719831046,"audio_tokens":1388347719831046,"cached_tokens_details":{"text_tokens":11,"image_tokens":22,"audio_tokens":33},"unmodelled_detail":1388347719831046},"completion_tokens_details":{"text_tokens":1388347719831046,"audio_tokens":1388347719831046,"image_tokens":1388347719831046,"reasoning_tokens":1388347719831046},"input_tokens_details":{"cached_tokens":1388347719831046},"output_tokens_details":{"reasoning_tokens":1388347719831046}}}`

func TestSanitizeClientUsageJSONZeroesEveryNumberInUsage(t *testing.T) {
	patched := SanitizeClientUsageJSON([]byte(usageKitchenSinkBody))
	if bytes.Equal(patched, []byte(usageKitchenSinkBody)) {
		t.Fatal("garbage body was not rewritten")
	}

	var document map[string]any
	if err := json.Unmarshal(patched, &document); err != nil {
		t.Fatalf("patched body is not JSON: %v (%s)", err, patched)
	}
	usage, ok := document["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage object lost: %s", patched)
	}
	var walk func(string, any)
	walk = func(path string, value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, item := range typed {
				walk(path+"."+key, item)
			}
		case []any:
			for i, item := range typed {
				walk(path+"."+fmt.Sprintf("%d", i), item)
			}
		case float64:
			if typed != 0 {
				t.Errorf("%s = %v, want 0", path, typed)
			}
		}
	}
	walk("usage", usage)

	// Shape and non-count data survive: strings stay, and a number outside the
	// usage object keeps its exact digits (json.Number, not float64).
	if got := usage["usage_semantic"]; got != "anthropic" {
		t.Errorf("usage_semantic = %v, want anthropic", got)
	}
	if _, ok := usage["prompt_tokens_details"].(map[string]any); !ok {
		t.Error("prompt_tokens_details lost its shape")
	}
	if got := patched; !bytes.Contains(got, []byte(`"created":1710000000123456789`)) {
		t.Errorf("a number outside usage lost precision: %s", got)
	}
}
