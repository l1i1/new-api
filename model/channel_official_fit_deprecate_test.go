package model

import (
	"testing"

	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The deprecation's write gate (spec §19.3 step 5): a family running measured
// admission ignores the official_fit_models allowlist, so writing a NEW entry
// for one of its models must be rejected with an error that names the fix.
// Entries carried over unchanged keep passing — flipping a family to measured
// must not turn an unrelated edit of an existing channel into a save failure.

// installMeasuredKimiPolicy installs a snapshot whose kimi-k3 family runs
// measured admission, restoring the previous snapshot afterwards.
func installMeasuredKimiPolicy(t *testing.T) {
	t.Helper()
	snapshot, err := fitpolicy.Compile(fitpolicy.Policy{
		Version: 1, Enabled: true,
		Families: []fitpolicy.FamilyPolicy{{
			ID: "kimi-k3",
			Rules: []fitpolicy.Rule{{
				ID: "k3-whole-family", When: "WholeFamily()", Require: []string{fitpolicy.BehaviorFamilyWhole},
			}},
			Behaviors: map[string]fitpolicy.Behavior{
				fitpolicy.BehaviorFamilyWhole:      {Class: fitpolicy.ClassVerdict},
				fitpolicy.BehaviorThinkingCounting: {Class: fitpolicy.ClassVerdict},
			},
			AdmissionSource:  fitpolicy.AdmissionSourceMeasured,
			AdmissionBattery: []string{fitpolicy.BehaviorThinkingCounting},
		}},
	})
	require.NoError(t, err)
	previous := fitpolicy.Current()
	fitpolicy.Install(snapshot)
	t.Cleanup(func() { fitpolicy.Install(previous) })
}

func channelWithOfficialFit(models string) *Channel {
	return &Channel{Id: 41, Type: 1, OtherSettings: `{"official_fit_models":["` + models + `"]}`}
}

func TestValidateOfficialFitModelsMeasured(t *testing.T) {
	installMeasuredKimiPolicy(t)

	tests := []struct {
		name     string
		channel  *Channel
		previous *Channel
		wantErr  bool
	}{
		{
			name:     "a new entry for a measured family is rejected",
			channel:  channelWithOfficialFit("kimi-k3"),
			previous: nil,
			wantErr:  true,
		},
		{
			name:     "an entry carried over unchanged keeps passing",
			channel:  channelWithOfficialFit("kimi-k3"),
			previous: channelWithOfficialFit("kimi-k3"),
			wantErr:  false,
		},
		{
			name:     "adding a second model to a carried entry is still a new entry",
			channel:  channelWithOfficialFit("kimi-k3,gpt-4o"),
			previous: channelWithOfficialFit("kimi-k3"),
			wantErr:  true,
		},
		{
			name:     "an entry for a declared family is untouched",
			channel:  channelWithOfficialFit("deepseek-v4-flash"),
			previous: nil,
			wantErr:  false,
		},
		{
			name:     "a channel without the field is untouched",
			channel:  &Channel{Id: 41, Type: 1},
			previous: nil,
			wantErr:  false,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.channel.ValidateOfficialFitModelsMeasured(testCase.previous)
			if testCase.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "admits channels by measurement")
				assert.Contains(t, err.Error(), "kimi-k3")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestValidateOfficialFitModelsMeasuredWithoutPolicy pins the pre-flip
// behaviour: with no snapshot installed the allowlist is the admission source
// everywhere, and the check must not fire.
func TestValidateOfficialFitModelsMeasuredWithoutPolicy(t *testing.T) {
	previous := fitpolicy.Current()
	fitpolicy.Install(nil)
	t.Cleanup(func() { fitpolicy.Install(previous) })

	require.NoError(t, channelWithOfficialFit("kimi-k3").ValidateOfficialFitModelsMeasured(nil))
}

// TestValidateOfficialFitModelsMeasuredMalformedSettings pins the failure
// posture: a malformed settings document is ValidateSettings's diagnosis, not
// this check's, so the check stays silent and lets the structural validation
// reject the save.
func TestValidateOfficialFitModelsMeasuredMalformedSettings(t *testing.T) {
	installMeasuredKimiPolicy(t)

	channel := &Channel{Id: 41, Type: 1, OtherSettings: `{not json`}
	require.NoError(t, channel.ValidateOfficialFitModelsMeasured(nil))
}
