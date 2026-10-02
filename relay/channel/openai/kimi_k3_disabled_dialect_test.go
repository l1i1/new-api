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
	return info, req
}

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
		if !info.ReasoningDisabledByClient {
			t.Fatalf("%s: caller intent must be remembered for the response contract", tc.name)
		}
		if !shouldSuppressReasoningContent(info) {
			t.Fatalf("%s: the caller asked for disabled thinking, content must still be suppressed", tc.name)
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
		if info.ReasoningDisabledByClient {
			t.Fatalf("%s: intent flag must stay false", name)
		}
	}

	// A different family keeps its own handling (DeepSeek V4 has its own helper).
	info, req := kimiDialectInfo(1, "none", "")
	req.Model = "deepseek-v4-flash"
	info.UpstreamModelName = "deepseek-v4-flash"
	applyKimiK3DisabledThinkingDialect(info, req)
	if req.ReasoningEffort != "none" || info.ReasoningDisabledByClient {
		t.Fatalf("non-Kimi model must not be rewritten: effort=%q flag=%v", req.ReasoningEffort, info.ReasoningDisabledByClient)
	}
}

func TestSuppressReasoningUsageZeroesOnlyReasoning(t *testing.T) {
	usage := &dto.Usage{CompletionTokens: 500, TotalTokens: 600}
	usage.CompletionTokenDetails.ReasoningTokens = 480
	suppressReasoningUsage(usage)
	if usage.CompletionTokenDetails.ReasoningTokens != 0 {
		t.Fatalf("reasoning tokens must be cleared, got %d", usage.CompletionTokenDetails.ReasoningTokens)
	}
	if usage.CompletionTokens != 500 || usage.TotalTokens != 600 {
		t.Fatalf("visible token accounting must survive: %+v", usage)
	}
	suppressReasoningUsage(nil) // must not panic
}
