package openai

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
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
		// The native axis must not travel next to the translated effort: an
		// aggregator that validates the pair answers 400 (channel 14, live
		// 2026-10-05). The body is what goes upstream, so assert on the body.
		body, err := common.Marshal(req)
		if err != nil {
			t.Fatalf("%s: marshal request: %v", tc.name, err)
		}
		if strings.Contains(string(body), `"thinking"`) {
			t.Fatalf("%s: translated request still carries a thinking object: %s", tc.name, body)
		}
		if shouldSuppressReasoningContent(info) {
			t.Fatalf("%s: Kimi K3 responses must be delivered, not stripped", tc.name)
		}
	}
}

// The exported dialect contract for other channels: an official-behaving
// channel keeps the caller's own axis, and a request that did not ask for
// disabled thinking keeps whatever thinking object it sent.
func TestKimiK3DialectKeepsThinkingWhereItIsNotContradictory(t *testing.T) {
	info, req := kimiDialectInfo(1, "", "enabled")
	applyKimiK3DisabledThinkingDialect(info, req)
	if len(req.THINKING) == 0 {
		t.Fatal("a thinking-enabled request must keep its thinking object")
	}
	if req.ReasoningEffort != "" {
		t.Fatalf("effort = %q, want empty", req.ReasoningEffort)
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

// An official-behaving channel promises the official control axes natively,
// so the disable intent must reach it unchanged. Translating it into the
// aggregator dialect breaks such channels: the K3 reseller that used to sit in
// the official_fit_models allowlist answered 400 "supported values are 'low',
// 'high', 'max'" for reasoning_effort=minimal (live 2026-10-02) while accepting
// the official axes the caller actually sent. The allowlist is retired; the
// official-behaving predicate now answers from the family's official channel
// type (exercised here through the DB path) plus the admission battery for
// measured families (covered by the model package's admission tests).
func TestKimiK3DisabledThinkingKeptForOfficialBehaviorChannel(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	previousDB := model.DB
	previousCache := common.MemoryCacheEnabled
	model.DB = db
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousCache
	})
	if err := db.AutoMigrate(&model.Channel{}); err != nil {
		t.Fatalf("migrate channels: %v", err)
	}
	if err := db.Create(&model.Channel{Id: 8, Type: 25, Name: "moonshot-official", Models: "kimi-k3"}).Error; err != nil {
		t.Fatalf("seed official channel: %v", err)
	}
	if err := db.Create(&model.Channel{Id: 37, Type: 1, Name: "aggregator", Models: "kimi-k3"}).Error; err != nil {
		t.Fatalf("seed aggregator: %v", err)
	}

	for _, tc := range []struct {
		name     string
		effort   string
		thinking string
		channel  int
		want     string
	}{
		{"official channel keeps effort none", "none", "", 8, "none"},
		{"official channel keeps thinking disabled", "", "disabled", 8, ""},
		{"plain aggregator still translates", "none", "", 37, kimiK3AggregatorDisabledEffort},
	} {
		info, req := kimiDialectInfo(1, tc.effort, tc.thinking)
		info.ChannelId = tc.channel
		applyKimiK3DisabledThinkingDialect(info, req)
		if tc.thinking == "" && req.ReasoningEffort != tc.want {
			t.Fatalf("%s: effort = %q, want %q", tc.name, req.ReasoningEffort, tc.want)
		}
		if tc.thinking != "" && req.ReasoningEffort != "" {
			// A thinking-object request carries no effort; the official axis
			// stays on the thinking object, and no effort may be injected.
			t.Fatalf("%s: effort = %q, want empty", tc.name, req.ReasoningEffort)
		}
		// The official-behaving channel is why the field can be dropped safely
		// everywhere else: the caller's own axis has to survive here untouched.
		if tc.channel == 8 && tc.thinking != "" && len(req.THINKING) == 0 {
			t.Fatalf("%s: official-behaving channel lost the caller's thinking object", tc.name)
		}
		if tc.channel == 37 && len(req.THINKING) != 0 {
			t.Fatalf("%s: translated aggregator request still carries the native axis", tc.name)
		}
	}
}
