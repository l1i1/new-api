package helper

import (
	"encoding/json"
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
