package helper

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// garbageUsage mirrors the 2026-10-13 upstream answer that reached customers'
// accounting: prompt_tokens=1.39e15, completion_tokens=5.6e14 and a
// cache_tokens of the same order.
func garbageUsage() *dto.Usage {
	return &dto.Usage{
		PromptTokens:     1388347719831046,
		CompletionTokens: 562949953421056,
		TotalTokens:      1951297673252102,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 322313980642816,
			CachedTokensDetails: &dto.CachedTokenDetails{
				ImageTokens: intPtr(999999999999),
			},
		},
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 562949953421000},
		InputTokensDetails:     &dto.InputTokenDetails{CachedTokens: 322313980642816},
		BillingUsage:           &dto.BillingUsage{Source: "openai"},
	}
}

func TestUsageForClientZeroesImplausibleUsage(t *testing.T) {
	usage := garbageUsage()
	before, err := json.Marshal(usage)
	if err != nil {
		t.Fatalf("marshal source: %v", err)
	}

	client := UsageForClient(usage)
	if client == nil {
		t.Fatal("UsageForClient returned nil for a non-nil usage")
	}

	for name, got := range map[string]int{
		"prompt_tokens":                 client.PromptTokens,
		"completion_tokens":             client.CompletionTokens,
		"total_tokens":                  client.TotalTokens,
		"prompt_tokens_details.cached":  client.PromptTokensDetails.CachedTokens,
		"completion_tokens_details.reas": client.CompletionTokenDetails.ReasoningTokens,
	} {
		if got != 0 {
			t.Errorf("client usage %s = %d, want 0", name, got)
		}
	}
	if client.PromptTokensDetails.CachedTokensDetails == nil {
		t.Fatal("cached_tokens_details dropped; shape must survive sanitation")
	}
	if got := *client.PromptTokensDetails.CachedTokensDetails.ImageTokens; got != 0 {
		t.Errorf("nested cached image tokens = %d, want 0", got)
	}
	if client.InputTokensDetails == nil || client.InputTokensDetails.CachedTokens != 0 {
		t.Errorf("input_tokens_details = %+v, want present and zeroed", client.InputTokensDetails)
	}
	if client.BillingUsage != nil {
		t.Error("billing_usage must never cross the client boundary")
	}

	after, err := json.Marshal(usage)
	if err != nil {
		t.Fatalf("marshal source after: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("source usage was mutated by the client view:\n before %s\n after  %s", before, after)
	}
	if usage.PromptTokensDetails.CachedTokensDetails == nil || *usage.PromptTokensDetails.CachedTokensDetails.ImageTokens == 0 {
		t.Error("shared nested details were written through")
	}
}

func TestUsageForClientKeepsPlausibleUsageByteIdentical(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     19610,
		CompletionTokens: 200,
		TotalTokens:      19810,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 9728,
		},
		BillingUsage: &dto.BillingUsage{Source: "openai"},
	}
	expected := *usage
	expected.BillingUsage = nil
	want, err := json.Marshal(&expected)
	if err != nil {
		t.Fatalf("marshal expected: %v", err)
	}

	got, err := json.Marshal(UsageForClient(usage))
	if err != nil {
		t.Fatalf("marshal client usage: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("plausible usage was rewritten:\n got  %s\n want %s", got, want)
	}
	if UsageCountAnomaly(usage) != nil {
		t.Error("plausible usage reported as anomalous")
	}
}

func TestUsageCountAnomalyCoversUnmodelledDimensions(t *testing.T) {
	cases := []struct {
		name  string
		usage *dto.Usage
	}{
		{"prompt", &dto.Usage{PromptTokens: 10_000_001}},
		{"negative", &dto.Usage{CompletionTokens: -1}},
		{"nested cached image", &dto.Usage{PromptTokensDetails: dto.InputTokenDetails{
			CachedTokensDetails: &dto.CachedTokenDetails{ImageTokens: intPtr(10_000_001)},
		}}},
		{"input tokens", &dto.Usage{InputTokens: 10_000_001}},
		{"output details", &dto.Usage{OutputTokensDetails: &dto.OutputTokenDetails{ReasoningTokens: 10_000_001}}},
		{"claude 1h cache", &dto.Usage{ClaudeCacheCreation1hTokens: 10_000_001}},
	}
	for _, tc := range cases {
		anomaly := UsageCountAnomaly(tc.usage)
		if anomaly == nil {
			t.Errorf("%s: no anomaly detected", tc.name)
			continue
		}
		if anomaly.Value <= 10_000_000 && anomaly.Value >= 0 {
			t.Errorf("%s: anomaly value %g is inside the plausible band", tc.name, anomaly.Value)
		}
	}
	if UsageCountAnomaly(&dto.Usage{PromptTokens: 10_000_000, CompletionTokens: 0}) != nil {
		t.Error("boundary value 10,000,000 must stay plausible")
	}
}

func TestClaudeAndGeminiClientViewsZeroed(t *testing.T) {
	claude := &dto.ClaudeResponse{
		Type: "message_delta",
		Usage: &dto.ClaudeUsage{
			InputTokens:              1388347719831046,
			OutputTokens:             562949953421056,
			CacheCreationInputTokens: 322313980642816,
			BillingUsage:             &dto.BillingUsage{Source: "claude"},
		},
	}
	claudeClient := ClaudeResponseForClient(claude)
	if claudeClient.Usage.InputTokens != 0 || claudeClient.Usage.OutputTokens != 0 || claudeClient.Usage.CacheCreationInputTokens != 0 {
		t.Errorf("claude client usage not zeroed: %+v", claudeClient.Usage)
	}
	if claudeClient.Usage.BillingUsage != nil {
		t.Error("claude billing_usage must never cross the client boundary")
	}
	if claude.Usage.InputTokens != 1388347719831046 {
		t.Error("claude source usage was mutated")
	}

	gemini := &dto.GeminiChatResponse{
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:     1388347719831046,
			CandidatesTokenCount: 562949953421056,
			TotalTokenCount:      1951297673252102,
			PromptTokensDetails: []dto.GeminiPromptTokensDetails{
				{Modality: "TEXT", TokenCount: 1388347719831046},
			},
			BillingUsage: &dto.BillingUsage{Source: "gemini"},
		},
	}
	geminiClient := GeminiResponseForClient(gemini)
	if geminiClient.UsageMetadata.PromptTokenCount != 0 || geminiClient.UsageMetadata.TotalTokenCount != 0 ||
		geminiClient.UsageMetadata.CandidatesTokenCount != 0 {
		t.Errorf("gemini client usage not zeroed: %+v", geminiClient.UsageMetadata)
	}
	if len(geminiClient.UsageMetadata.PromptTokensDetails) != 1 ||
		geminiClient.UsageMetadata.PromptTokensDetails[0].TokenCount != 0 {
		t.Error("gemini per-modality counts must be zeroed, not dropped")
	}
	if geminiClient.UsageMetadata.BillingUsage != nil {
		t.Error("gemini billing_usage must never cross the client boundary")
	}
	if gemini.UsageMetadata.PromptTokenCount != 1388347719831046 {
		t.Error("gemini source usage was mutated")
	}
}

func TestStreamAndTextResponseForClientZeroed(t *testing.T) {
	stream := &dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl-1",
		Usage: garbageUsage(),
	}
	clientStream := ChatCompletionsStreamResponseForClient(stream)
	if clientStream.Usage == nil {
		t.Fatal("stream usage dropped")
	}
	if clientStream.Usage.PromptTokens != 0 || clientStream.Usage.CompletionTokens != 0 {
		t.Errorf("stream usage not zeroed: %+v", clientStream.Usage)
	}
	if stream.Usage.PromptTokens != 1388347719831046 {
		t.Error("stream source usage was mutated")
	}

	text := &dto.OpenAITextResponse{
		Id:    "chatcmpl-2",
		Usage: *garbageUsage(),
	}
	clientText := OpenAITextResponseForClient(text)
	if clientText.Usage.PromptTokens != 0 || clientText.Usage.CompletionTokens != 0 || clientText.Usage.TotalTokens != 0 {
		t.Errorf("text response usage not zeroed: %+v", clientText.Usage)
	}
	if text.Usage.PromptTokens != 1388347719831046 {
		t.Error("text response source usage was mutated")
	}
}

// kitchenSinkUsage sets every modelled count of the OpenAI shape to a value
// that must never reach a client, so the generic assertion below fails if any
// single dimension is forgotten.
func kitchenSinkUsage() *dto.Usage {
	big := 1388347719831046
	small := 424242
	return &dto.Usage{
		PromptTokens:                big,
		CompletionTokens:            big,
		TotalTokens:                 big,
		PromptCacheHitTokens:        big,
		InputTokens:                 big,
		OutputTokens:                big,
		ClaudeCacheCreation5mTokens: big,
		ClaudeCacheCreation1hTokens: big,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         big,
			CachedCreationTokens: big,
			CacheWriteTokens:     big,
			TextTokens:           big,
			ImageTokens:          big,
			AudioTokens:          big,
			CachedTokensDetails: &dto.CachedTokenDetails{
				TextTokens:  intPtr(small),
				ImageTokens: intPtr(small),
				AudioTokens: intPtr(small),
			},
		},
		CompletionTokenDetails: dto.OutputTokenDetails{
			TextTokens:      big,
			AudioTokens:     big,
			ImageTokens:     big,
			ReasoningTokens: big,
		},
		InputTokensDetails: &dto.InputTokenDetails{
			CachedTokens:         big,
			CachedCreationTokens: big,
			CacheWriteTokens:     big,
			TextTokens:           big,
			ImageTokens:          big,
			AudioTokens:          big,
		},
		OutputTokensDetails: &dto.OutputTokenDetails{
			TextTokens:      big,
			AudioTokens:     big,
			ImageTokens:     big,
			ReasoningTokens: big,
		},
	}
}

// assertEveryUsageNumberIsZero walks a marshalled client payload and fails on
// any non-zero number inside a usage object. It is deliberately generic: a
// field added to the usage shapes later cannot silently escape sanitation.
func assertEveryUsageNumberIsZero(t *testing.T, payload []byte, label string) {
	t.Helper()
	var document any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("%s: payload is not JSON: %v (%s)", label, err, payload)
	}
	var walk func(path string, value any)
	walk = func(path string, value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, item := range typed {
				if key == "usage" || key == "usageMetadata" {
					assertUsageObjectAllZero(t, path+"."+key, item)
					continue
				}
				walk(path+"."+key, item)
			}
		case []any:
			for i, item := range typed {
				walk(fmt.Sprintf("%s[%d]", path, i), item)
			}
		}
	}
	walk("$", document)
}

func assertUsageObjectAllZero(t *testing.T, path string, value any) {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object: %v", path, value)
	}
	var walk func(string, any)
	walk = func(inner string, value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, item := range typed {
				if key == "billing_usage" {
					t.Errorf("%s.%s must never cross the client boundary", inner, key)
					continue
				}
				walk(inner+"."+key, item)
			}
		case []any:
			for i, item := range typed {
				walk(fmt.Sprintf("%s[%d]", inner, i), item)
			}
		case float64:
			if typed != 0 {
				t.Errorf("%s = %v, want 0", inner, typed)
			}
		}
	}
	walk(path, object)
}

func TestClientUsageViewsZeroEveryModelledField(t *testing.T) {
	openAIClient, err := json.Marshal(UsageForClient(kitchenSinkUsage()))
	if err != nil {
		t.Fatalf("marshal openai client usage: %v", err)
	}
	assertEveryUsageNumberIsZero(t, openAIClient, "openai")

	textClient, err := json.Marshal(OpenAITextResponseForClient(&dto.OpenAITextResponse{
		Id:    "chatcmpl-sink",
		Usage: *kitchenSinkUsage(),
	}))
	if err != nil {
		t.Fatalf("marshal text client response: %v", err)
	}
	assertEveryUsageNumberIsZero(t, textClient, "openai-text")

	claudeClient, err := json.Marshal(ClaudeResponseForClient(&dto.ClaudeResponse{
		Type: "message_delta",
		Usage: &dto.ClaudeUsage{
			InputTokens:                 1388347719831046,
			OutputTokens:                1388347719831046,
			CacheCreationInputTokens:    1388347719831046,
			CacheReadInputTokens:        1388347719831046,
			ClaudeCacheCreation5mTokens: 1388347719831046,
			ClaudeCacheCreation1hTokens: 1388347719831046,
			CacheCreation: &dto.ClaudeCacheCreationUsage{
				Ephemeral5mInputTokens: 1388347719831046,
				Ephemeral1hInputTokens: 1388347719831046,
			},
			ServerToolUse: &dto.ClaudeServerToolUse{
				WebSearchRequests:     999,
				WebFetchRequests:      999,
				CodeExecutionRequests: 999,
				ToolSearchRequests:    999,
			},
		},
	}))
	if err != nil {
		t.Fatalf("marshal claude client response: %v", err)
	}
	assertEveryUsageNumberIsZero(t, claudeClient, "claude")

	geminiClient, err := json.Marshal(GeminiResponseForClient(&dto.GeminiChatResponse{
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:           1388347719831046,
			ToolUsePromptTokenCount:    1388347719831046,
			CandidatesTokenCount:       1388347719831046,
			TotalTokenCount:            1388347719831046,
			ThoughtsTokenCount:         1388347719831046,
			CachedContentTokenCount:    1388347719831046,
			PromptTokensDetails:        []dto.GeminiPromptTokensDetails{{Modality: "TEXT", TokenCount: 1388347719831046}},
			ToolUsePromptTokensDetails: []dto.GeminiPromptTokensDetails{{Modality: "TEXT", TokenCount: 1388347719831046}},
			CandidatesTokensDetails:    []dto.GeminiPromptTokensDetails{{Modality: "TEXT", TokenCount: 1388347719831046}},
		},
	}))
	if err != nil {
		t.Fatalf("marshal gemini client response: %v", err)
	}
	assertEveryUsageNumberIsZero(t, geminiClient, "gemini")
}
