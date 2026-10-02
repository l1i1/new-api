package common

import (
	"encoding/json"
	"testing"
)

// Documents the recommended no-code configuration for the kimi-k3
// disable-thinking gap: a CONDITIONAL channel param_override that rewrites the
// caller's disable intent into the dialect the aggregator honours
// (reasoning_effort=minimal, live-probed 2026-10-02: reasoning tokens collapse
// from ~1000-2000 to ~5), while leaving every other request untouched.
//
// The two rules cover both control axes the official Moonshot endpoint accepts.
func kimiK3DisableOverride() map[string]any {
	return map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "reasoning_effort",
				"mode":  "set",
				"value": "minimal",
				"conditions": []any{
					map[string]any{"path": "reasoning_effort", "mode": "full", "value": "none"},
				},
			},
			map[string]any{
				"path":  "reasoning_effort",
				"mode":  "set",
				"value": "minimal",
				"conditions": []any{
					map[string]any{"path": "thinking.type", "mode": "full", "value": "disabled"},
				},
			},
		},
	}
}

func TestKimiK3DisableOverrideIsConditional(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // expected reasoning_effort after the override ("" = absent)
	}{
		{"caller asked none", `{"model":"kimi-k3","reasoning_effort":"none"}`, "minimal"},
		// "full" matching is case-sensitive: the config path does NOT catch
		// "NONE"/"None". The gateway's own suppression check is case-insensitive
		// (strings.EqualFold), so such a request would still be stripped; this is
		// exactly the corner the code-side dialect covers.
		{"caller asked none upper (config misses it)", `{"model":"kimi-k3","reasoning_effort":"NONE"}`, "NONE"},
		{"caller sent thinking disabled", `{"model":"kimi-k3","thinking":{"type":"disabled"}}`, "minimal"},
		{"caller asked max (must stay max)", `{"model":"kimi-k3","reasoning_effort":"max"}`, "max"},
		{"caller sent thinking enabled", `{"model":"kimi-k3","thinking":{"type":"enabled"}}`, ""},
		{"no signal at all", `{"model":"kimi-k3"}`, ""},
		{"other family untouched", `{"model":"kimi-k3-other","reasoning_effort":"none"}`, "minimal"},
	}
	for _, tc := range cases {
		out, err := ApplyParamOverride([]byte(tc.in), kimiK3DisableOverride(), nil)
		if err != nil {
			t.Fatalf("%s: ApplyParamOverride: %v", tc.name, err)
		}
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%s: output is not JSON: %v", tc.name, err)
		}
		effort, _ := got["reasoning_effort"].(string)
		if effort != tc.want {
			t.Fatalf("%s: reasoning_effort = %q, want %q (body: %s)", tc.name, effort, tc.want, string(out))
		}
	}
}
