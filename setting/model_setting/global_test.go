package model_setting

import (
	"bytes"
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldPreserveThinkingSuffixExactAndRegex(t *testing.T) {
	settings := GetGlobalSettings()
	original := append([]string(nil), settings.ThinkingModelBlacklist...)
	t.Cleanup(func() { settings.ThinkingModelBlacklist = original })

	assert.True(t, ShouldPreserveThinkingSuffix("kimi-k2-thinking"))
	assert.True(t, ShouldPreserveThinkingSuffix("moonshotai/kimi-k2-thinking"))
	assert.False(t, ShouldPreserveThinkingSuffix("m@sha256:abc"))

	settings.ThinkingModelBlacklist = []string{
		"kimi-k2-thinking",
		"re:[",
		"re:",
		"re:.*@sha256:.*",
	}

	var logged bytes.Buffer
	previous := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logged
	t.Cleanup(func() { gin.DefaultErrorWriter = previous })

	assert.True(t, ShouldPreserveThinkingSuffix("kimi-k2-thinking"))
	assert.True(t, ShouldPreserveThinkingSuffix("m@sha256:abc"))
	assert.False(t, ShouldPreserveThinkingSuffix("m@sha256"))
	assert.False(t, ShouldPreserveThinkingSuffix("qwen3-max@thinking:on"))
	require.Contains(t, logged.String(), `invalid thinking_model_blacklist regex "re:["`)
	require.Contains(t, logged.String(), `invalid thinking_model_blacklist regex "re:"`)

	settings.ThinkingModelBlacklist = []string{"re:^beta@"}
	assert.False(t, ShouldPreserveThinkingSuffix("m@sha256:abc"))
	assert.True(t, ShouldPreserveThinkingSuffix("beta@sha256:abc"))
	assert.False(t, ShouldPreserveThinkingSuffix("alpha@sha256:abc"))
}

func TestResolveModelAlias(t *testing.T) {
	settings := GetGlobalSettings()
	original := settings.ModelAliasMap
	t.Cleanup(func() { settings.ModelAliasMap = original })
	settings.ModelAliasMap = nil

	_, ok := ResolveModelAlias("claude-haiku-4-5-20251001")
	assert.False(t, ok, "no aliases configured means no resolution")

	settings.ModelAliasMap = map[string]string{
		"claude-haiku-4-5-20251001": "claude-haiku-4-5",
		"padded-target":             "  claude-haiku-4-5  ",
		"blank-target":              "   ",
		"self":                      "self",
	}

	target, ok := ResolveModelAlias("claude-haiku-4-5-20251001")
	assert.True(t, ok)
	assert.Equal(t, "claude-haiku-4-5", target)

	// The canonical id itself, unknown ids, and empty input resolve to nothing.
	_, ok = ResolveModelAlias("claude-haiku-4-5")
	assert.False(t, ok)
	_, ok = ResolveModelAlias("claude-sonnet-4-5-20250929")
	assert.False(t, ok)
	_, ok = ResolveModelAlias("")
	assert.False(t, ok)

	// Whitespace around the requested id and around the configured target is
	// trimmed.
	target, ok = ResolveModelAlias("  claude-haiku-4-5-20251001  ")
	assert.True(t, ok)
	assert.Equal(t, "claude-haiku-4-5", target)
	target, ok = ResolveModelAlias("padded-target")
	assert.True(t, ok)
	assert.Equal(t, "claude-haiku-4-5", target)

	// Blank targets and self-mappings are ignored instead of looping.
	_, ok = ResolveModelAlias("blank-target")
	assert.False(t, ok)
	_, ok = ResolveModelAlias("self")
	assert.False(t, ok)
}

// The option loader matches struct fields by their exact json tag, so the
// exported key must equal the option key an operator writes. A tag carrying
// ,omitempty silently exports "model_alias_map,omitempty" and every written
// value lands on the floor — this pins both directions of that round trip.
func TestModelAliasOptionRoundTrip(t *testing.T) {
	settings := GetGlobalSettings()
	original := settings.ModelAliasMap
	t.Cleanup(func() { settings.ModelAliasMap = original })
	settings.ModelAliasMap = nil

	exported, err := config.ConfigToMap(settings)
	require.NoError(t, err)
	require.Contains(t, exported, "model_alias_map")
	require.NotContains(t, exported, "model_alias_map,omitempty")

	require.NoError(t, config.UpdateConfigFromMap(settings, map[string]string{
		"model_alias_map": `{"claude-haiku-4-5-20251001":"claude-haiku-4-5"}`,
	}))
	target, ok := ResolveModelAlias("claude-haiku-4-5-20251001")
	assert.True(t, ok)
	assert.Equal(t, "claude-haiku-4-5", target)

	// An unknown key must leave the configured map untouched.
	require.NoError(t, config.UpdateConfigFromMap(settings, map[string]string{
		"model_alias_map,omitempty": `{}`,
	}))
	target, ok = ResolveModelAlias("claude-haiku-4-5-20251001")
	assert.True(t, ok)
	assert.Equal(t, "claude-haiku-4-5", target)
}
