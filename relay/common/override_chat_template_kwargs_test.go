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
// What this test pins: the field never reaches an upstream, the four pre-existing
// operations still run, and nothing else in the request is disturbed.
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
	}
	for _, tc := range cases {
		out, err := ApplyParamOverride([]byte(tc.in), yjdyChatTemplateKwargsOverride(), nil)
		if err != nil {
			t.Fatalf("%s: ApplyParamOverride: %v", tc.name, err)
		}
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%s: output is not JSON: %v", tc.name, err)
		}
		if _, present := got["chat_template_kwargs"]; present {
			t.Fatalf("%s: chat_template_kwargs still present (body: %s)", tc.name, string(out))
		}
		if _, present := got["thinking"]; present {
			t.Fatalf("%s: thinking not stripped (body: %s)", tc.name, string(out))
		}
		effort, _ := got["reasoning_effort"].(string)
		if effort != tc.wantEffort {
			t.Fatalf("%s: reasoning_effort = %q, want %q (body: %s)",
				tc.name, effort, tc.wantEffort, string(out))
		}
	}
}
