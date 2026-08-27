package dto

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOfficialFitProfileFor(t *testing.T) {
	setting := &UserSetting{OfficialFit: &OfficialFitConfig{Profile: map[string]OfficialFitProfile{
		"deepseek-v4-": {Validate: true},
		"kimi-k3":      {Errors: true},
		"*":            {Shape: true},
	}}}

	cases := []struct {
		model string
		want  OfficialFitProfile
		ok    bool
	}{
		{"deepseek-v4-flash", OfficialFitProfile{Validate: true}, true},
		{"DEEPSEEK-V4-Pro ", OfficialFitProfile{Validate: true}, true},
		{"kimi-k3", OfficialFitProfile{Errors: true}, true},
		{"kimi-k3x", OfficialFitProfile{Errors: true}, true}, // prefix match (exact not required)
		{"qwen3.7-max", OfficialFitProfile{Shape: true}, true},
		{"", OfficialFitProfile{}, false},
	}
	for _, tc := range cases {
		got, ok := setting.OfficialFitProfileFor(tc.model)
		assert.Equal(t, tc.ok, ok, tc.model)
		assert.Equal(t, tc.want, got, tc.model)
	}

	// Nil / empty configuration never matches.
	var nilSetting *UserSetting
	_, ok := nilSetting.OfficialFitProfileFor("deepseek-v4-flash")
	assert.False(t, ok)
	empty := &UserSetting{}
	_, ok = empty.OfficialFitProfileFor("deepseek-v4-flash")
	assert.False(t, ok)
	_, ok = (&UserSetting{OfficialFit: &OfficialFitConfig{}}).OfficialFitProfileFor("deepseek-v4-flash")
	assert.False(t, ok)
}

func TestOfficialFitProfileMostSpecificPrefixWins(t *testing.T) {
	setting := &UserSetting{OfficialFit: &OfficialFitConfig{Profile: map[string]OfficialFitProfile{
		"deepseek-": {Validate: true},
		"deepseek-v4-": {Shape: true, Route: true},
	}}}
	got, ok := setting.OfficialFitProfileFor("deepseek-v4-flash")
	assert.True(t, ok)
	assert.True(t, got.Shape)
	assert.True(t, got.Route)
	assert.False(t, got.Validate)
}
