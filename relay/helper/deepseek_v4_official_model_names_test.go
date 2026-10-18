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

// The official-fit Validate gate accepts dated snapshot ids built from an
// official id: the platform publishes upstream snapshots as
// "<official-id>-<suffix>" (e.g. deepseek-v4.1-flash-1017) and the channel
// model_mapping rewrites them to the vendor id at dispatch time, so official-fit
// accounts must be able to call them. Unknown family members still keep the
// official 400.
func TestIsDeepSeekV4FitAcceptedModelName(t *testing.T) {
	accepted := []string{
		"deepseek-v4-pro",
		"deepseek-v4.1-flash",
		"deepseek-v4.1-flash-1017",
		"deepseek-v4-flash-1017",
		"deepseek-v4-pro-20261017",
		" DeepSeek-V4.1-Flash-1017 ",
	}
	for _, model := range accepted {
		assert.True(t, IsDeepSeekV4FitAcceptedModelName(model), model)
	}

	rejected := []string{
		"deepseek-v4.1-pro",
		"deepseek-v4-notexist",
		"deepseek-v4.1-flash-vision-exp",
		"deepseek-v4.1-flash-",
		"deepseek-v4.1-flash-abc",
		"",
		"deepseek-flash",
	}
	for _, model := range rejected {
		assert.False(t, IsDeepSeekV4FitAcceptedModelName(model), model)
	}
}

// The dated-snapshot spelling rule is family-agnostic: every official-fit
// family accepts its own official ids plus a numeric suffix, so a snapshot the
// platform chooses to publish under a dated name is callable through the same
// gate. Non-dated variants and ids outside the registry stay unknown.
func TestIsOfficialFitDatedSnapshotModelName(t *testing.T) {
	accepted := []string{
		"deepseek-v4.1-flash-1017",
		"deepseek-v4-pro-20261017",
		"kimi-k3-20250101",
		"glm-5.3-20250101",
		"glm-5.3-flash-20261017",
		" DeepSeek-V4.1-Flash-1017 ",
	}
	for _, model := range accepted {
		assert.True(t, IsOfficialFitDatedSnapshotModelName(model), model)
	}

	rejected := []string{
		"deepseek-v4.1-flash",
		"kimi-k3",
		"glm-5.3",
		"deepseek-v4.1-flash-vision-exp",
		"deepseek-v4.1-flash-abc",
		"deepseek-v4.1-flash-",
		"deepseek-flash-1017",
		"kimi-k3.5-20250101",
		"",
	}
	for _, model := range rejected {
		assert.False(t, IsOfficialFitDatedSnapshotModelName(model), model)
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
