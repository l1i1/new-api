package operation_setting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDefaultChannelAffinitySettingShape locks the default rule set shipped
// with the binary. Rules are matched in order, so both the count and the
// relative order are part of the contract: the CLI trace rules must stay
// ahead of the catch-all fallback rules.
func TestDefaultChannelAffinitySettingShape(t *testing.T) {
	setting := GetChannelAffinitySetting()
	require.NotNil(t, setting)
	require.True(t, setting.Enabled)
	require.True(t, setting.SwitchOnSuccess)
	require.False(t, setting.KeepOnChannelDisabled)
	require.Greater(t, setting.MaxEntries, 0)
	require.Greater(t, setting.DefaultTTLSeconds, 0)

	require.Len(t, setting.Rules, 6)

	wantOrder := []string{
		"codex cli trace",
		"claude cli trace",
		"chat completion affinity",
		"responses trace",
		"messages trace",
		"gemini native affinity",
	}
	for i, want := range wantOrder {
		require.Equal(t, want, setting.Rules[i].Name, "rule order mismatch at index %d", i)
	}
}

func TestDefaultCodexRuleUsesTrimmedHeaderSet(t *testing.T) {
	setting := GetChannelAffinitySetting()
	require.NotNil(t, setting)

	var codexRule *ChannelAffinityRule
	for i := range setting.Rules {
		if setting.Rules[i].Name == "codex cli trace" {
			codexRule = &setting.Rules[i]
			break
		}
	}
	require.NotNil(t, codexRule)
	require.True(t, codexRule.SkipRetryOnFailure)

	tpl := codexRule.ParamOverrideTemplate
	require.NotNil(t, tpl)
	opsAny, ok := tpl["operations"]
	require.True(t, ok)
	ops, ok := opsAny.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, ops, 1)

	op := ops[0]
	require.Equal(t, "pass_headers", op["mode"])
	require.Equal(t, true, op["keep_origin"])

	valueAny, ok := op["value"].([]string)
	require.True(t, ok)
	require.Equal(t, codexCliPassThroughHeadersActive, valueAny)

	// The full reference list is kept for upstream parity but must not leak
	// into the shipped default template.
	require.NotEqual(t, codexCliPassThroughHeaders, valueAny)
}

func TestDefaultClaudeRuleSkipsRetryOnFailure(t *testing.T) {
	setting := GetChannelAffinitySetting()
	require.NotNil(t, setting)

	var claudeRule *ChannelAffinityRule
	for i := range setting.Rules {
		if setting.Rules[i].Name == "claude cli trace" {
			claudeRule = &setting.Rules[i]
			break
		}
	}
	require.NotNil(t, claudeRule)
	require.True(t, claudeRule.SkipRetryOnFailure)
}

func TestDefaultChatRuleKeySourceFallbackOrder(t *testing.T) {
	setting := GetChannelAffinitySetting()
	require.NotNil(t, setting)

	var chatRule *ChannelAffinityRule
	for i := range setting.Rules {
		if setting.Rules[i].Name == "chat completion affinity" {
			chatRule = &setting.Rules[i]
			break
		}
	}
	require.NotNil(t, chatRule)

	require.True(t, chatRule.IncludeModelName)
	require.False(t, chatRule.SkipRetryOnFailure)

	wantSources := []ChannelAffinityKeySource{
		{Type: "gjson", Path: "metadata.user_id"},
		{Type: "gjson", Path: "user"},
		{Type: "context_int", Key: "token_id"},
		{Type: "context_int", Key: "id"},
	}
	require.Equal(t, wantSources, chatRule.KeySources)
}
