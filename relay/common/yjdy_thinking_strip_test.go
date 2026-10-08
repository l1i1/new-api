package common

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// ch49's upstream does not implement the thinking extension at all: it answers
// "unknown field: *.effort" to any request carrying it (measured directly against the
// upstream, and confirmed independently as "the server does not know the thinking
// field"), while the same request with a flat reasoning_effort and no thinking field
// returns 200. kimi-k3 is an always-thinking model there, so dropping the extension
// loses nothing the channel could have honoured.
//
// The order matters and is the reason this is pinned: the effort has to be copied out
// of thinking.effort before thinking is deleted, or the source is gone.
func yjdyThinkingStripDocument() map[string]any {
	ops := []any{}
	for _, level := range []string{"low", "high", "max"} {
		ops = append(ops, map[string]any{
			"mode": "copy", "from": "thinking.effort", "to": "reasoning_effort",
			"conditions": []any{map[string]any{
				"path": "thinking.effort", "mode": "full", "value": level, "pass_missing_key": false,
			}},
		})
	}
	ops = append(ops, map[string]any{"mode": "delete", "path": "thinking"})
	return map[string]any{"operations": ops}
}

func TestYJDYThinkingStripKeepsEffortAndDropsTheExtension(t *testing.T) {
	doc := yjdyThinkingStripDocument()
	out, err := ApplyParamOverride([]byte(`{"model":"kimi-k3","thinking":{"type":"enabled","keep":"all","effort":"low"}}`), doc, nil)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.Equal(t, "low", parsed["reasoning_effort"], "the effort survives as a flat field - the only spelling this channel reads")
	require.NotContains(t, parsed, "thinking", "the extension itself must be gone, or the upstream answers 400")

	// A request that carries thinking but no effort still has to lose the field.
	out, err = ApplyParamOverride([]byte(`{"model":"kimi-k3","thinking":{"type":"disabled"}}`), doc, nil)
	require.NoError(t, err)
	parsed = nil
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.NotContains(t, parsed, "thinking")
	require.NotContains(t, parsed, "reasoning_effort", "no effort was asked for, so none is invented")

	// A request with no thinking block is only otherwise untouched.
	out, err = ApplyParamOverride([]byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"x"}]}`), doc, nil)
	require.NoError(t, err)
	parsed = nil
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.NotContains(t, parsed, "thinking")
	require.Equal(t, "kimi-k3", parsed["model"], "the rest of the body is preserved")
}
