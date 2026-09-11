package helper

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The accepted model-id set is probed against the live endpoint, not read from
// /v1/models: the public list omits ids the endpoint still serves. The v4.1
// flash line joined the set on 2026-09-11 (probed live); v4.1-pro is rejected
// upstream and must stay off the list.
func TestIsDeepSeekV4OfficialModelName(t *testing.T) {
	accepted := []string{
		"deepseek-v4-pro",
		"deepseek-v4-flash",
		"deepseek-v4-flash-vision-exp",
		"deepseek-v4.1-flash",
		" DeepSeek-V4.1-Flash ",
	}
	for _, model := range accepted {
		assert.True(t, IsDeepSeekV4OfficialModelName(model), model)
	}

	rejected := []string{
		"deepseek-v4.1-pro",
		"deepseek-v4-notexist",
		"deepseek-v4.1-flash-vision-exp",
		"deepseek-flash",
	}
	for _, model := range rejected {
		assert.False(t, IsDeepSeekV4OfficialModelName(model), model)
	}
}

// The unknown-model error must be byte-identical to the live official text.
// The accepted-id set is probed separately; the message's own model list is
// the endpoint's wording and deliberately not used as the accepted set.
func TestDeepSeekV4UnknownModelMessageMatchesOfficialText(t *testing.T) {
	got := DeepSeekV4UnknownModelMessage("deepseek-v4-notexist")
	want := "The supported API model names are deepseek-flash, deepseek-v4-pro, but you passed deepseek-v4-notexist."
	assert.Equal(t, want, got)
}
