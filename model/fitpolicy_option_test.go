package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
)

func builtinFitPolicyJSON(t *testing.T) string {
	t.Helper()
	encoded, err := common.Marshal(fitpolicy.BuiltinPolicy())
	if err != nil {
		t.Fatalf("marshal builtin policy: %v", err)
	}
	return string(encoded)
}

func TestValidateFitPolicyOption(t *testing.T) {
	if err := ValidateFitPolicyOption(""); err != nil {
		t.Fatalf("an empty value removes the option and must be accepted: %v", err)
	}
	if err := ValidateFitPolicyOption("   "); err != nil {
		t.Fatalf("a blank value must be treated as removal: %v", err)
	}
	if err := ValidateFitPolicyOption(builtinFitPolicyJSON(t)); err != nil {
		t.Fatalf("the built-in policy must validate: %v", err)
	}

	for name, value := range map[string]string{
		"unknown family":        `{"version":1,"enabled":true,"families":[{"id":"nope","rules":[]}]}`,
		"unknown function":      `{"version":1,"enabled":true,"families":[{"id":"kimi-k3","rules":[{"id":"r","when":"Nope()","require":["b"]}],"behaviors":{"b":{"class":"verdict"}}}]}`,
		"undeclared behaviour":  `{"version":1,"enabled":true,"families":[{"id":"kimi-k3","rules":[{"id":"r","when":"WholeFamily()","require":["b"]}],"behaviors":{}}]}`,
		"malformed json":        `{"version":`,
		"unknown top-level key": `{"version":1,"enabled":true,"families":[],"typo":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateFitPolicyOption(value); err == nil {
				t.Fatal("this policy must be rejected before it reaches the database")
			}
		})
	}
}

func TestRefreshFitPolicySnapshotFollowsOptionMap(t *testing.T) {
	previous := fitpolicy.Current()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		delete(common.OptionMap, fitpolicy.OptionKey)
		common.OptionMapRWMutex.Unlock()
		fitpolicy.Install(previous)
	})

	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	delete(common.OptionMap, fitpolicy.OptionKey)
	common.OptionMapRWMutex.Unlock()

	if err := refreshFitPolicySnapshot(); err != nil {
		t.Fatalf("an absent option is not an error: %v", err)
	}
	if fitpolicy.Current() != nil {
		t.Fatal("an absent option must leave the layer with no opinion")
	}

	common.OptionMapRWMutex.Lock()
	common.OptionMap[fitpolicy.OptionKey] = builtinFitPolicyJSON(t)
	common.OptionMapRWMutex.Unlock()

	if err := refreshFitPolicySnapshot(); err != nil {
		t.Fatalf("refresh from the option map: %v", err)
	}
	installed := fitpolicy.Current()
	if installed == nil || !installed.Enabled() {
		t.Fatal("the policy present in the option map must be installed")
	}
	if installed.Version() != fitpolicy.BuiltinPolicy().Version {
		t.Fatalf("installed version = %d", installed.Version())
	}

	// A broken document must keep the last-known-good snapshot in place rather
	// than dropping back to no opinion.
	common.OptionMapRWMutex.Lock()
	common.OptionMap[fitpolicy.OptionKey] = `{"version":1,"enabled":true,"families":[{"id":"nope","rules":[]}]}`
	common.OptionMapRWMutex.Unlock()

	if err := refreshFitPolicySnapshot(); err == nil {
		t.Fatal("a broken policy must be reported")
	}
	if fitpolicy.Current() != installed {
		t.Fatal("a broken policy must keep the last-known-good snapshot")
	}
}
