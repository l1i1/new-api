package common

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// ch48's production document sets reasoning_effort to "minimal" when it is "none",
// but measured behaviour says the condition also fires when the field is absent -
// pinning "minimal" on requests that never asked for it. This isolates the
// missing-key semantics of the condition parser, which is what any guard has to
// rest on.
func TestReasoningEffortConditionMissingKeySemantics(t *testing.T) {
	body := []byte(`{"model":"kimi-k3"}`)
	cases := []struct {
		name string
		cond map[string]any
		want string // "" means no reasoning_effort may appear
	}{
		// Measured: the default behaves like false - a missing field does not pass the
		// condition, so the channel's document does not pin a value nobody asked for.
		{"default (unset)", map[string]any{"path": "reasoning_effort", "mode": "full", "value": "none"}, ""},
		{"pass_missing_key false", map[string]any{"path": "reasoning_effort", "mode": "full", "value": "none", "pass_missing_key": false}, ""},
		{"pass_missing_key true", map[string]any{"path": "reasoning_effort", "mode": "full", "value": "none", "pass_missing_key": true}, "minimal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := map[string]any{"operations": []any{map[string]any{
				"path": "reasoning_effort", "mode": "set", "value": "minimal",
				"conditions": []any{tc.cond},
			}}}
			out, err := ApplyParamOverride(body, doc, nil)
			require.NoError(t, err)
			var parsed map[string]any
			require.NoError(t, json.Unmarshal(out, &parsed))
			if tc.want == "" {
				require.NotContains(t, parsed, "reasoning_effort", "a missing field must not be pinned")
				return
			}
			require.Equal(t, tc.want, parsed["reasoning_effort"])
		})
	}

	// And the guard must not break the intended case: none -> minimal.
	doc := map[string]any{"operations": []any{map[string]any{
		"path": "reasoning_effort", "mode": "set", "value": "minimal",
		"conditions": []any{map[string]any{"path": "reasoning_effort", "mode": "full", "value": "none", "pass_missing_key": false}},
	}}}
	out, err := ApplyParamOverride([]byte(`{"model":"kimi-k3","reasoning_effort":"none"}`), doc, nil)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.Equal(t, "minimal", parsed["reasoning_effort"], "the intended rewrite must still happen")
}
