package common

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

func usageCorrectionInfo(document string) *RelayInfo {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(document), &parsed); err != nil {
		panic(err)
	}
	return &RelayInfo{
		ChannelMeta: &ChannelMeta{ResponseOverride: parsed},
	}
}

func usageCorrectionRequest(t *testing.T, firstRole string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model": "kimi-k3.1-pro",
		"messages": []map[string]any{
			{"role": firstRole, "content": "Hi"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// TestResponseUsageCorrectionSubtractsOnlyOnMatch pins the operator's rule from
// 2026-10-30: an upstream that injects a framing message for a user-first
// conversation makes prompt_tokens 1-2 higher than official; the channel's
// response_override subtracts it, but only for the requests that carry that
// characteristic.
func TestResponseUsageCorrectionSubtractsOnlyOnMatch(t *testing.T) {
	const document = `{"usage_correction": {"prompt_tokens_delta": -2,
		"conditions": [{"path": "messages.0.role", "mode": "full", "value": "user"}]}}`
	userFirst := usageCorrectionRequest(t, "user")
	systemFirst := usageCorrectionRequest(t, "system")

	cases := []struct {
		name   string
		body   []byte
		prompt int
		want   int
	}{
		{"user first matches", userFirst, 88, 86},
		{"system first does not match", systemFirst, 88, 88},
		{"clamped at zero", userFirst, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usage := &dto.Usage{PromptTokens: tc.prompt, CompletionTokens: 10, TotalTokens: tc.prompt + 10}
			ApplyResponseUsageCorrection(tc.body, usageCorrectionInfo(document), usage)
			if usage.PromptTokens != tc.want {
				t.Fatalf("prompt tokens = %d, want %d", usage.PromptTokens, tc.want)
			}
			if usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
				t.Fatalf("total %d does not equal prompt %d + completion %d", usage.TotalTokens, usage.PromptTokens, usage.CompletionTokens)
			}
		})
	}
}

// TestResponseUsageCorrectionKeepsTheInvoiceInStep pins apply_to_billing: the
// billing sidecar the settlement reads must be corrected with the top-level
// count, otherwise the invoice would disagree with what the caller was shown.
func TestResponseUsageCorrectionKeepsTheInvoiceInStep(t *testing.T) {
	const document = `{"usage_correction": {"prompt_tokens_delta": -2}}`
	body := usageCorrectionRequest(t, "user")

	billed := &dto.Usage{
		PromptTokens: 88, CompletionTokens: 10, TotalTokens: 98,
		BillingUsage: &dto.BillingUsage{OpenAIUsage: &dto.Usage{PromptTokens: 88, CompletionTokens: 10, TotalTokens: 98}},
	}
	ApplyResponseUsageCorrection(body, usageCorrectionInfo(document), billed)
	if billed.PromptTokens != 86 || billed.BillingUsage.OpenAIUsage.PromptTokens != 86 || billed.BillingUsage.OpenAIUsage.TotalTokens != 96 {
		t.Fatalf("billing sidecar not corrected in step: %+v / %+v", billed, billed.BillingUsage.OpenAIUsage)
	}

	clientOnlyDocument := `{"usage_correction": {"prompt_tokens_delta": -2, "apply_to_billing": false}}`
	clientOnly := &dto.Usage{
		PromptTokens: 88, CompletionTokens: 10, TotalTokens: 98,
		BillingUsage: &dto.BillingUsage{OpenAIUsage: &dto.Usage{PromptTokens: 88, CompletionTokens: 10, TotalTokens: 98}},
	}
	ApplyResponseUsageCorrection(body, usageCorrectionInfo(clientOnlyDocument), clientOnly)
	if clientOnly.PromptTokens != 86 {
		t.Fatalf("client view not corrected: %d", clientOnly.PromptTokens)
	}
	if clientOnly.BillingUsage.OpenAIUsage.PromptTokens != 88 {
		t.Fatalf("apply_to_billing=false must leave the invoice alone, got %d", clientOnly.BillingUsage.OpenAIUsage.PromptTokens)
	}
}

// TestValidateResponseOverrideAcceptsOnlySubtractingCorrections pins the save
// path: a correction may only subtract, and a document may carry the correction
// without shaping operations.
func TestValidateResponseOverrideAcceptsOnlySubtractingCorrections(t *testing.T) {
	if err := ValidateResponseOverride(`{"usage_correction": {"prompt_tokens_delta": -1}}`); err != nil {
		t.Fatalf("a correction-only document must be accepted: %v", err)
	}
	if err := ValidateResponseOverride(`{"usage_correction": {"prompt_tokens_delta": 1}}`); err == nil {
		t.Fatal("a positive delta must be rejected: the rule may only subtract upstream-injected tokens")
	}
	if err := ValidateResponseOverride(`{"usage_correction": {"prompt_tokens_delta": -1, "conditions": {"messages.0.role": "user"}}}`); err != nil {
		t.Fatalf("the map form of conditions must be accepted: %v", err)
	}
	if err := ValidateResponseOverride(`{"usage_correction": {"prompt_tokens_delta": -1}, "operations": [{"mode": "delete", "path": "usage_extra"}]}`); err != nil {
		t.Fatalf("a correction beside shaping operations must be accepted: %v", err)
	}
}

// TestResponseUsageCorrectionFallsBackToTheParsedRequest pins the paths that
// settle usage without a stored body: the parsed request still feeds the
// conditions.
func TestResponseUsageCorrectionFallsBackToTheParsedRequest(t *testing.T) {
	const document = `{"usage_correction": {"prompt_tokens_delta": -1,
		"conditions": [{"path": "messages.0.role", "mode": "full", "value": "user"}]}}`
	info := usageCorrectionInfo(document)
	info.Request = &dto.GeneralOpenAIRequest{
		Model:    "kimi-k3.1-pro",
		Messages: []dto.Message{{Role: "user", Content: "Hi"}},
	}
	usage := &dto.Usage{PromptTokens: 88, CompletionTokens: 10, TotalTokens: 98}
	ApplyResponseUsageCorrection(nil, info, usage)
	if usage.PromptTokens != 87 {
		t.Fatalf("prompt tokens = %d, want 87", usage.PromptTokens)
	}
}
