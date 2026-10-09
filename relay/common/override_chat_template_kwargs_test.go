package common

import (
	"encoding/json"
	"testing"
)

// The channel param_override actually applied to ch77/ch49 on 2026-10-09.
//
// Context (why delete-only, and why NOT a translation):
//
//	ch77 (DEF_YJDY-JYUAN_copy, base http://43.248.188.130:3000, prio 150) and
//	ch49 share one upstream that answers "400 未知请求字段：chat_template_kwargs"
//	whenever a caller sends that vLLM-style field; ch77 took 21 such 400s in its
//	first 18 minutes because it outranks ch49. Nothing outside the DTO reads
//	ChatTemplateKwargs, so the field is forwarded verbatim.
//
//	The tempting fix - translate chat_template_kwargs.enable_thinking=false into
//	reasoning_effort=minimal, the value the aggregator dialect uses elsewhere -
//	was REJECTED on live evidence: on that upstream `minimal` does not collapse
//	reasoning at all (1059 reasoning chars vs 665 for the same question with no
//	effort, and 1270 for high, while the official channel returns 0 for
//	reasoning_effort=none). Mapping the caller's "do not think" onto a value that
//	still thinks would have turned an honest 400 into a silent no-op the caller
//	is billed for. The official endpoint ignores the field too (200 with 936
//	reasoning chars for enable_thinking=false), so removing it makes these
//	channels behave like the official one instead of inventing a promise.
//
//	2026-10-09 later the same day: the same upstream also answered
//	"400 未知请求字段：prompt_cache_key" (last seen 14:11), so that field is
//	deleted too. Worth recording WHY the fix lives here and not with them:
//	new-api's own relay path ignores unknown fields - common.DecodeJson uses the
//	default decoder, and the only DisallowUnknownFields() callers in the tree are
//	pkg/fitpolicy (our policy documents) and scripts/fit-probe (our probe tool) -
//	so the rejection comes from a layer past their gateway, and "upgrade new-api"
//	would not have changed it. Stripping on our side is the part we control;
//	prompt caching is not a thing that upstream offers, so the only loss is a
//	cache hint that never took effect.
//
// What this test pins: none of the three rejected fields reaches an upstream,
// the reasoning_effort copies still run, and nothing else in the request moves.
func yjdyChatTemplateKwargsOverride() map[string]any {
	return map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "copy", "from": "thinking.effort", "to": "reasoning_effort",
				"conditions": []any{map[string]any{
					"path": "thinking.effort", "mode": "full", "value": "low", "pass_missing_key": false,
				}},
			},
			map[string]any{
				"mode": "copy", "from": "thinking.effort", "to": "reasoning_effort",
				"conditions": []any{map[string]any{
					"path": "thinking.effort", "mode": "full", "value": "high", "pass_missing_key": false,
				}},
			},
			map[string]any{
				"mode": "copy", "from": "thinking.effort", "to": "reasoning_effort",
				"conditions": []any{map[string]any{
					"path": "thinking.effort", "mode": "full", "value": "max", "pass_missing_key": false,
				}},
			},
			map[string]any{"mode": "delete", "path": "thinking"},
			map[string]any{"mode": "delete", "path": "chat_template_kwargs"},
			map[string]any{"mode": "delete", "path": "prompt_cache_key"},
		},
	}
}

func TestYjdyChatTemplateKwargsIsStrippedAndNothingElseMoves(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantEffort string // "" = absent
	}{
		{
			"enable_thinking false: field stripped, no effort invented",
			`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":false}}`,
			"",
		},
		{
			"enable_thinking true: field stripped, no effort invented",
			`{"model":"kimi-k3","chat_template_kwargs":{"enable_thinking":true}}`,
			"",
		},
		{
			"unrelated kwargs keys: field stripped",
			`{"model":"kimi-k3","chat_template_kwargs":{"foo":"bar"}}`,
			"",
		},
		{
			"thinking.effort high still copies (pre-existing op intact)",
			`{"model":"kimi-k3","thinking":{"effort":"high"},"chat_template_kwargs":{"enable_thinking":false}}`,
			"high",
		},
		{
			"caller's own reasoning_effort survives",
			`{"model":"kimi-k3","reasoning_effort":"max","chat_template_kwargs":{"enable_thinking":false}}`,
			"max",
		},
		{
			"prompt_cache_key alone: stripped",
			`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"prompt_cache_key":"conv-42"}`,
			"",
		},
		{
			"all three rejected fields together: all stripped, effort still copied",
			`{"model":"kimi-k3","stream":true,"max_tokens":512,"messages":[{"role":"user","content":"hi"}],"prompt_cache_key":"conv-42","chat_template_kwargs":{"enable_thinking":false},"thinking":{"effort":"max","keep":"all"}}`,
			"max",
		},
		{
			// minimal is deliberately not a copy condition: that upstream does not
			// collapse reasoning for it, so translating the disable intent into it
			// would be a billed no-op. The thinking object is removed regardless.
			"thinking.effort minimal: no effort invented, thinking still stripped",
			`{"model":"kimi-k3","prompt_cache_key":"conv-42","thinking":{"effort":"minimal"}}`,
			"",
		},
	}
	for _, tc := range cases {
		out, err := ApplyParamOverride([]byte(tc.in), yjdyChatTemplateKwargsOverride(), nil)
		if err != nil {
			t.Fatalf("%s: ApplyParamOverride: %v", tc.name, err)
		}
		var got, in map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%s: output is not JSON: %v", tc.name, err)
		}
		if err := json.Unmarshal([]byte(tc.in), &in); err != nil {
			t.Fatalf("%s: input is not JSON: %v", tc.name, err)
		}
		for _, field := range []string{"chat_template_kwargs", "thinking", "prompt_cache_key"} {
			if _, present := got[field]; present {
				t.Fatalf("%s: %s still present (body: %s)", tc.name, field, string(out))
			}
		}
		effort, _ := got["reasoning_effort"].(string)
		if effort != tc.wantEffort {
			t.Fatalf("%s: reasoning_effort = %q, want %q (body: %s)",
				tc.name, effort, tc.wantEffort, string(out))
		}
		// Everything the caller sent that is not one of the stripped keys must
		// come through untouched - a delete op that ate neighbouring fields would
		// otherwise silently change the request.
		for k, want := range in {
			switch k {
			case "chat_template_kwargs", "thinking", "prompt_cache_key":
				continue
			}
			gotVal, present := got[k]
			if !present {
				t.Fatalf("%s: field %q disappeared (body: %s)", tc.name, k, string(out))
			}
			wantJSON, _ := json.Marshal(want)
			gotJSON, _ := json.Marshal(gotVal)
			if string(wantJSON) != string(gotJSON) {
				t.Fatalf("%s: field %q changed: %s -> %s (body: %s)",
					tc.name, k, wantJSON, gotJSON, string(out))
			}
		}
	}
}
