package openai

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

func kimiDialectInfo(channelType int, effort, thinking string) (*relaycommon.RelayInfo, *dto.GeneralOpenAIRequest) {
	req := &dto.GeneralOpenAIRequest{Model: "kimi-k3"}
	if effort != "" {
		req.ReasoningEffort = effort
	}
	if thinking != "" {
		req.THINKING = []byte(`{"type":"` + thinking + `"}`)
	}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: channelType, ApiType: constant.APITypeOpenAI}}
	info.Request = req
	info.ReasoningEffort = effort
	info.OriginModelName = req.Model
	return info, req
}

// The caller's disable-thinking intent must reach aggregator channels in a
// dialect they honour, and the response must NOT be stripped: the gateway cannot
// tell at delta time whether the upstream obeyed, and hiding content that may
// already be billed is exactly what produced the 2026-10-02
// "reasoning_tokens without reasoning_content" reports.
func TestKimiK3DisabledThinkingTranslatedForAggregator(t *testing.T) {
	for _, tc := range []struct {
		name     string
		effort   string
		thinking string
	}{
		{"effort none", "none", ""},
		{"thinking disabled", "", "disabled"},
		{"both", "none", "disabled"},
	} {
		info, req := kimiDialectInfo(1, tc.effort, tc.thinking) // type 1 = generic OpenAI-compatible aggregator
		applyKimiK3DisabledThinkingDialect(info, req)
		if req.ReasoningEffort != kimiK3AggregatorDisabledEffort {
			t.Fatalf("%s: effort = %q, want %q", tc.name, req.ReasoningEffort, kimiK3AggregatorDisabledEffort)
		}
		if shouldSuppressReasoningContent(info) {
			t.Fatalf("%s: Kimi K3 responses must be delivered, not stripped", tc.name)
		}
	}
}

func TestKimiK3DialectLeavesOtherRequestsAlone(t *testing.T) {
	cases := map[string]func() (*relaycommon.RelayInfo, *dto.GeneralOpenAIRequest){
		"official moonshot channel": func() (*relaycommon.RelayInfo, *dto.GeneralOpenAIRequest) {
			return kimiDialectInfo(constant.ChannelTypeMoonshot, "none", "")
		},
		"max effort": func() (*relaycommon.RelayInfo, *dto.GeneralOpenAIRequest) {
			return kimiDialectInfo(1, "max", "")
		},
		"thinking enabled": func() (*relaycommon.RelayInfo, *dto.GeneralOpenAIRequest) {
			return kimiDialectInfo(1, "", "enabled")
		},
		"no signal": func() (*relaycommon.RelayInfo, *dto.GeneralOpenAIRequest) {
			return kimiDialectInfo(1, "", "")
		},
	}
	for name, build := range cases {
		info, req := build()
		before := req.ReasoningEffort
		applyKimiK3DisabledThinkingDialect(info, req)
		if req.ReasoningEffort != before {
			t.Fatalf("%s: effort must not be rewritten (%q -> %q)", name, before, req.ReasoningEffort)
		}
	}

	// A different family keeps its own handling: DeepSeek V4 has its own dialect
	// helper and its own response contract.
	info, req := kimiDialectInfo(1, "none", "")
	req.Model = "deepseek-v4-flash"
	info.OriginModelName = "deepseek-v4-flash"
	info.UpstreamModelName = "deepseek-v4-flash"
	applyKimiK3DisabledThinkingDialect(info, req)
	if req.ReasoningEffort != "none" {
		t.Fatalf("non-Kimi model must not be rewritten: effort=%q", req.ReasoningEffort)
	}
	if !shouldSuppressReasoningContent(info) {
		t.Fatal("DeepSeek V4 keeps its existing disabled-thinking response contract")
	}
}

// A plain max-effort Kimi K3 request is never rewritten and never suppressed.
func TestKimiK3MaxEffortUntouched(t *testing.T) {
	info, req := kimiDialectInfo(1, "max", "")
	applyKimiK3DisabledThinkingDialect(info, req)
	if req.ReasoningEffort != "max" {
		t.Fatalf("effort = %q, want max", req.ReasoningEffort)
	}
	if shouldSuppressReasoningContent(info) {
		t.Fatal("max effort must deliver reasoning content")
	}
}
