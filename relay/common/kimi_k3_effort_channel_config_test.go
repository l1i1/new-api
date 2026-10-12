package common

import (
	"encoding/json"
	"fmt"
	"testing"
)

// The self-contained operation block the production rollout appends to every
// non-official Kimi K3 channel. It is deliberately independent of whatever the
// channel already carries so that a channel with no param_override at all (ch76
// and friends) ends up with the same behaviour as one that already copies the
// native thinking.effort axis. Every operation is guarded on the model name:
// these channels serve other families too.
const k3EffortOps = `[
 {"mode":"copy","from":"thinking.effort","to":"reasoning_effort","logic":"AND","conditions":[{"path":"model","mode":"prefix","value":"kimi-k3"},{"path":"thinking.effort","mode":"full","value":"low","pass_missing_key":false}]},
 {"mode":"copy","from":"thinking.effort","to":"reasoning_effort","logic":"AND","conditions":[{"path":"model","mode":"prefix","value":"kimi-k3"},{"path":"thinking.effort","mode":"full","value":"high","pass_missing_key":false}]},
 {"mode":"copy","from":"thinking.effort","to":"reasoning_effort","logic":"AND","conditions":[{"path":"model","mode":"prefix","value":"kimi-k3"},{"path":"thinking.effort","mode":"full","value":"max","pass_missing_key":false}]},
 {"path":"reasoning_effort","mode":"set","value":"high","logic":"AND","conditions":[{"path":"model","mode":"prefix","value":"kimi-k3"},{"path":"thinking.effort","mode":"full","value":"medium","pass_missing_key":false}]},
 {"path":"reasoning_effort","mode":"set","value":"high","logic":"AND","conditions":[{"path":"model","mode":"prefix","value":"kimi-k3"},{"path":"reasoning_effort","mode":"full","value":"medium"}]},
 {"path":"thinking.effort","mode":"set","value":"high","logic":"AND","conditions":[{"path":"model","mode":"prefix","value":"kimi-k3"},{"path":"thinking.effort","mode":"full","value":"medium","pass_missing_key":false}]},
 {"path":"reasoning_effort","mode":"set","value":"max","logic":"AND","conditions":[{"path":"model","mode":"prefix","value":"kimi-k3"},{"path":"reasoning_effort","mode":"full","value":"low","invert":true},{"path":"reasoning_effort","mode":"full","value":"high","invert":true},{"path":"reasoning_effort","mode":"full","value":"max","invert":true},{"path":"reasoning_effort","mode":"full","value":"none","invert":true},{"path":"reasoning_effort","mode":"full","value":"minimal","invert":true}]},
 {"path":"reasoning_effort","mode":"set","value":"max","logic":"AND","conditions":[{"path":"model","mode":"prefix","value":"kimi-k3"},{"path":"reasoning_effort","mode":"full","value":"__k3_absent_sentinel__","pass_missing_key":true}]}
]`

// Channel 48's live document: the block has to compose with the operations a
// channel already carries, not replace them.
const k3Ch48ExistingOps = `[
 {"path":"reasoning_effort","mode":"set","value":"minimal","conditions":[{"path":"reasoning_effort","mode":"full","value":"none"}]},
 {"mode":"copy","from":"thinking.effort","to":"reasoning_effort","conditions":[{"path":"thinking.effort","mode":"full","value":"low","pass_missing_key":false}]},
 {"mode":"copy","from":"thinking.effort","to":"reasoning_effort","conditions":[{"path":"thinking.effort","mode":"full","value":"high","pass_missing_key":false}]},
 {"mode":"copy","from":"thinking.effort","to":"reasoning_effort","conditions":[{"path":"thinking.effort","mode":"full","value":"max","pass_missing_key":false}]}
]`

func k3Document(t *testing.T, parts ...string) map[string]any {
	t.Helper()
	ops := make([]any, 0)
	for _, part := range parts {
		var parsed []any
		if err := json.Unmarshal([]byte(part), &parsed); err != nil {
			t.Fatalf("parse operations: %v", err)
		}
		ops = append(ops, parsed...)
	}
	raw, err := json.Marshal(map[string]any{"operations": ops})
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("reparse document: %v", err)
	}
	return doc
}

// The rollout document must produce the official tier on any non-official
// channel: one that already carries copy operations, and one that carries none.
func TestK3EffortOverrideMatrix(t *testing.T) {
	documents := []struct {
		name string
		doc  map[string]any
		// deletesThinking: the synthetic channel-49 shape drops the thinking
		// object after the block, so the object itself cannot be asserted on.
		deletesThinking bool
		// noneBecomesMinimal: channel 48 already rewrites none onto the
		// aggregator's disable marker, and the block must not undo that.
		noneBecomesMinimal bool
	}{
		{"barren channel (block only)", k3Document(t, k3EffortOps), false, false},
		{"channel 48 (existing + block)", k3Document(t, k3Ch48ExistingOps, k3EffortOps), false, true},
		{"block before an existing thinking delete", k3Document(t, k3EffortOps, `[{"mode":"delete","path":"thinking"}]`), true, false},
	}
	cases := []struct {
		name string
		body string
		want string
	}{
		{"omitted runs at the official default", `{"model":"kimi-k3"}`, "max"},
		{"medium runs at high", `{"model":"kimi-k3","reasoning_effort":"medium"}`, "high"},
		{"banana runs at the default", `{"model":"kimi-k3","reasoning_effort":"banana"}`, "max"},
		{"xhigh runs at the default", `{"model":"kimi-k3","reasoning_effort":"xhigh"}`, "max"},
		{"low stays low", `{"model":"kimi-k3","reasoning_effort":"low"}`, "low"},
		{"high stays high", `{"model":"kimi-k3","reasoning_effort":"high"}`, "high"},
		{"max stays max", `{"model":"kimi-k3","reasoning_effort":"max"}`, "max"},
		{"minimal stays the disable marker", `{"model":"kimi-k3","reasoning_effort":"minimal"}`, "minimal"},
		{"none stays the disable marker", `{"model":"kimi-k3","reasoning_effort":"none"}`, "none"},
		{"native low copies", `{"model":"kimi-k3","thinking":{"type":"enabled","effort":"low"}}`, "low"},
		{"native max copies", `{"model":"kimi-k3","thinking":{"type":"enabled","effort":"max"}}`, "max"},
		{"native medium reaches high", `{"model":"kimi-k3","thinking":{"type":"enabled","effort":"medium"}}`, "high"},
		{"another family keeps its tier", `{"model":"glm-5.3","reasoning_effort":"medium"}`, "medium"},
		{"another kimi line keeps its tier", `{"model":"kimi-k2-thinking","reasoning_effort":"medium"}`, "medium"},
	}
	for _, d := range documents {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/%s", d.name, tc.name), func(t *testing.T) {
				want := tc.want
				if tc.name == "none stays the disable marker" && d.noneBecomesMinimal {
					want = "minimal"
				}
				info := &RelayInfo{ChannelMeta: &ChannelMeta{ParamOverride: d.doc, UpstreamModelName: "kimi-k3"}}
				out, err := ApplyParamOverrideWithRelayInfo([]byte(tc.body), info)
				if err != nil {
					t.Fatalf("apply: %v", err)
				}
				var got struct {
					ReasoningEffort string `json:"reasoning_effort"`
					Thinking        struct {
						Effort string `json:"effort"`
					} `json:"thinking"`
				}
				if err := json.Unmarshal(out, &got); err != nil {
					t.Fatalf("decode %s: %v", out, err)
				}
				if got.ReasoningEffort != want {
					t.Fatalf("reasoning_effort = %q, want %q (body %s)", got.ReasoningEffort, want, out)
				}
				if d.deletesThinking {
					return
				}
				if tc.name == "native medium reaches high" && got.Thinking.Effort != "high" {
					t.Fatalf("thinking.effort = %q, want high (body %s)", got.Thinking.Effort, out)
				}
				if tc.name == "native low copies" && got.Thinking.Effort != "low" {
					t.Fatalf("the thinking object must survive with its caller value, got %q", got.Thinking.Effort)
				}
			})
		}
	}
}
