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
	"github.com/tidwall/gjson"
	"gorm.io/gorm"
)

// The official endpoint normalises three of the values this surface accepts
// medium -> high and an absent or unrecognised value -> the declared default
// max (probe 2026-10-13), while the resellers the pool falls back to answer the
// weakest tier for all three with a 200. The edge has to send the official tier
// itself, and the log must keep reporting what the caller asked for.
func TestKimiK3EffortDialectCanonicalisesForAggregator(t *testing.T) {
	for _, tc := range []struct {
		name   string
		effort string
		want   string
	}{
		{"omitted runs at the official default", "", kimiK3OfficialDefaultEffort},
		{"medium runs at high", "medium", "high"},
		{"unrecognised runs at the official default", "banana", kimiK3OfficialDefaultEffort},
		{"xhigh is not on the official axis", "xhigh", kimiK3OfficialDefaultEffort},
		{"low is already official", "low", "low"},
		{"high is already official", "high", "high"},
		{"max is already official", "max", "max"},
		{"the official axis is case-insensitive", "MAX", "max"},
		{"minimal stays the aggregator disable marker", "minimal", "minimal"},
		{"none stays the disabled class", "none", "none"},
	} {
		info, req := kimiDialectInfo(1, tc.effort, "")
		applyKimiK3EffortDialect(info, req)
		if req.ReasoningEffort != tc.want {
			t.Fatalf("%s: effort = %q, want %q", tc.name, req.ReasoningEffort, tc.want)
		}
		if info.ReasoningEffort != tc.effort {
			t.Fatalf("%s: the log must keep the caller's own value %q, got %q", tc.name, tc.effort, info.ReasoningEffort)
		}
	}
}

// A caller that uses the native spelling carries the tier on the thinking
// object; the projection has to reach that spelling too, and must not leave the
// original value next to the canonical one.
func TestKimiK3EffortDialectRewritesNativeThinkingEffort(t *testing.T) {
	info, req := kimiDialectInfo(1, "", "")
	req.THINKING = []byte(`{"type":"enabled","effort":"medium"}`)
	applyKimiK3EffortDialect(info, req)
	if req.ReasoningEffort != "high" {
		t.Fatalf("native medium must reach the upstream as high, got %q", req.ReasoningEffort)
	}
	if got := gjson.GetBytes(req.THINKING, "effort").String(); got != "high" {
		t.Fatalf("thinking.effort = %q, want high", got)
	}
	if got := gjson.GetBytes(req.THINKING, "type").String(); got != "enabled" {
		t.Fatalf("the rest of the thinking object must survive, type = %q", got)
	}

	// The OpenRouter-style nested spelling is the third place a tier can hide.
	info, req = kimiDialectInfo(1, "", "")
	req.Reasoning = []byte(`{"effort":"banana","enabled":true}`)
	applyKimiK3EffortDialect(info, req)
	if req.ReasoningEffort != kimiK3OfficialDefaultEffort {
		t.Fatalf("nested unrecognised value must run at the official default, got %q", req.ReasoningEffort)
	}
	if got := gjson.GetBytes(req.Reasoning, "effort").String(); got != kimiK3OfficialDefaultEffort {
		t.Fatalf("reasoning.effort = %q, want %q", got, kimiK3OfficialDefaultEffort)
	}
}

// A request that asked to disable thinking belongs to the other helper, and the
// disable marker it produced must survive this pass: rewriting it to the
// default tier would turn a disabled request into a maximum-effort one.
func TestKimiK3EffortDialectKeepsTheDisabledContract(t *testing.T) {
	for _, tc := range []struct {
		name     string
		effort   string
		thinking string
	}{
		{"effort none", "none", ""},
		{"thinking disabled", "", "disabled"},
	} {
		info, req := kimiDialectInfo(1, tc.effort, tc.thinking)
		applyKimiK3DisabledThinkingDialect(info, req)
		applyKimiK3EffortDialect(info, req)
		if req.ReasoningEffort != kimiK3AggregatorDisabledEffort {
			t.Fatalf("%s: effort = %q, want the aggregator disable marker %q",
				tc.name, req.ReasoningEffort, kimiK3AggregatorDisabledEffort)
		}
	}
}

// The Moonshot channel type, official-behaving channels and other families keep
// their own handling: they already speak the official axis, and translating
// their requests would break the channel contract this dialect exists to
// preserve.
func TestKimiK3EffortDialectLeavesOfficialAndOtherFamiliesAlone(t *testing.T) {
	cases := map[string]func() (*relaycommon.RelayInfo, *dto.GeneralOpenAIRequest){
		"official moonshot channel": func() (*relaycommon.RelayInfo, *dto.GeneralOpenAIRequest) {
			return kimiDialectInfo(constant.ChannelTypeMoonshot, "", "")
		},
		"openrouter keeps its own dialect": func() (*relaycommon.RelayInfo, *dto.GeneralOpenAIRequest) {
			return kimiDialectInfo(constant.ChannelTypeOpenRouter, "", "")
		},
		"another family": func() (*relaycommon.RelayInfo, *dto.GeneralOpenAIRequest) {
			info, req := kimiDialectInfo(1, "medium", "")
			req.Model = "glm-5.3"
			info.OriginModelName = "glm-5.3"
			info.UpstreamModelName = "glm-5.3"
			return info, req
		},
	}
	for name, build := range cases {
		info, req := build()
		before := req.ReasoningEffort
		applyKimiK3EffortDialect(info, req)
		if req.ReasoningEffort != before {
			t.Fatalf("%s: effort must not be rewritten (%q -> %q)", name, before, req.ReasoningEffort)
		}
	}
}

// The exemption that matters in production is the channel row, not the channel
// type constant: the official-behaving predicate has to answer for the channel
// that is about to receive the body.
func TestKimiK3EffortDialectKeepsOfficialBehaviorChannel(t *testing.T) {
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
		name    string
		channel int
		want    string
	}{
		{"official channel keeps the caller's empty tier", 8, ""},
		{"aggregator receives the official default", 37, kimiK3OfficialDefaultEffort},
	} {
		info, req := kimiDialectInfo(1, "", "")
		info.ChannelId = tc.channel
		applyKimiK3EffortDialect(info, req)
		if req.ReasoningEffort != tc.want {
			t.Fatalf("%s: effort = %q, want %q", tc.name, req.ReasoningEffort, tc.want)
		}
	}
}

// The body is what goes upstream, so the assertion that matters is on the
// marshalled request rather than the struct.
func TestKimiK3EffortDialectReachesTheBody(t *testing.T) {
	info, req := kimiDialectInfo(1, "", "")
	applyKimiK3EffortDialect(info, req)
	body, err := common.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if !strings.Contains(string(body), `"reasoning_effort":"`+kimiK3OfficialDefaultEffort+`"`) {
		t.Fatalf("the default tier must travel in the body: %s", body)
	}
}
