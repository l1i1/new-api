package common

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// kimiK3EffortOverride is the document applied to channels that honour
// reasoning_effort but ignore thinking.effort (ch48 and ch75 measured: they answer
// the default while the official endpoint answers the requested level). The
// official contract makes reasoning_effort override thinking.effort, so copying
// the value across is equivalent rather than a downgrade.
func kimiK3EffortOverride() map[string]any {
	var doc map[string]any
	require.NoError(nil, json.Unmarshal([]byte(kimiK3EffortOverrideJSON), &doc))
	return doc
}

// The production documents these merge with, recovered from the gateway migration
// fixtures that carry them verbatim (developerRoleRequest in
// internal/upstream/openaichat/override_test.go and otherParamOverride in
// internal/relay/override_wiring_test.go). The effort mapping is appended, never
// substituted: an override is a whole-field replace, so writing only the new
// document silently drops whatever the channel already did.
const kimiK3Ch75Original = `{"path":"messages.*.role","mode":"replace","from":"developer","to":"system"}`
const kimiK3Ch48Original = `{"path":"reasoning_effort","mode":"set","value":"minimal","conditions":[{"path":"reasoning_effort","mode":"full","value":"none"}]}`

const kimiK3EffortOverrideJSON = `{"operations":[
 {"mode":"copy","from":"thinking.effort","to":"reasoning_effort","conditions":[{"path":"thinking.effort","mode":"full","value":"low","pass_missing_key":false}]},
 {"mode":"copy","from":"thinking.effort","to":"reasoning_effort","conditions":[{"path":"thinking.effort","mode":"full","value":"high","pass_missing_key":false}]},
 {"mode":"copy","from":"thinking.effort","to":"reasoning_effort","conditions":[{"path":"thinking.effort","mode":"full","value":"max","pass_missing_key":false}]}
]}`

// The mapping has to survive a body that carries no thinking block at all, and it
// must not invent an effort for a request that never asked for one.
func TestKimiK3EffortOverrideMapsEffortAndLeavesOthersAlone(t *testing.T) {
	doc := kimiK3EffortOverride()

	for _, level := range []string{"low", "high", "max"} {
		out, err := ApplyParamOverride(
			[]byte(`{"model":"kimi-k3","thinking":{"type":"enabled","keep":"all","effort":"`+level+`"}}`),
			doc, nil)
		require.NoError(t, err)
		var parsed map[string]any
		require.NoError(t, json.Unmarshal(out, &parsed))
		require.Equal(t, level, parsed["reasoning_effort"],
			"thinking.effort=%s must be mirrored into reasoning_effort, the field these channels read", level)
		thinking := parsed["thinking"].(map[string]any)
		require.Equal(t, level, thinking["effort"], "the original field is copied, not moved")
	}

	// No effort requested: the override must add nothing, so the channel keeps its
	// own default (which is the official default too).
	out, err := ApplyParamOverride([]byte(`{"model":"kimi-k3","thinking":{"type":"enabled","keep":"all"}}`), doc, nil)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.NotContains(t, parsed, "reasoning_effort",
		"a request that never asked for an effort must not have one invented for it")

	// A body with no thinking block is untouched.
	out, err = ApplyParamOverride([]byte(`{"model":"kimi-k3","messages":[]}`), doc, nil)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.NotContains(t, parsed, "reasoning_effort")

	// The channels' own documents are untouched by this mapping: ch75 keeps its
	// developer->system rewrite and ch48 its none->minimal rewrite, each verified
	// against the engine separately by TestReasoningEffortConditionMissingKeySemantics.

	// An effort value outside the documented set is left alone rather than
	// guessed at.
	out, err = ApplyParamOverride([]byte(`{"model":"kimi-k3","thinking":{"type":"enabled","effort":"turbo"}}`), kimiK3EffortOverride(), nil)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.NotContains(t, parsed, "reasoning_effort")
}
