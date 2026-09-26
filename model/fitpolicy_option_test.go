package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
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

// TestUpdateOptionRejectsInvalidFitPolicyBeforeCommit exercises the real write
// path rather than the validator directly: an uncompilable policy must be
// rejected before the database transaction, so a broken rule set can never be
// persisted and left for the next node to discover.
func TestUpdateOptionRejectsInvalidFitPolicyBeforeCommit(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&Option{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMapRWMutex.Lock()
		delete(common.OptionMap, fitpolicy.OptionKey)
		common.OptionMapRWMutex.Unlock()
		fitpolicy.Install(nil)
	})

	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	common.OptionMap[fitpolicy.OptionKey] = builtinFitPolicyJSON(t)
	common.OptionMapRWMutex.Unlock()
	if err := refreshFitPolicySnapshot(); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	before := fitpolicy.Current()

	broken := `{"version":1,"enabled":true,"families":[{"id":"not-a-family","rules":[]}]}`
	if err := UpdateOption(fitpolicy.OptionKey, broken); err == nil {
		t.Fatal("UpdateOption must reject a policy that cannot compile")
	}

	var count int64
	if err := DB.Model(&Option{}).Where("key = ?", fitpolicy.OptionKey).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatal("a rejected policy must not reach the database")
	}
	if fitpolicy.Current() != before {
		t.Fatal("a rejected policy must leave the live snapshot untouched")
	}
}

// TestUpdateOptionInstallsFitPolicySnapshotOnTheWriter covers the other half of
// gap 2: the node performing the write must pick the new policy up immediately
// instead of waiting for a Redis epoch round-trip or the periodic sync.
func TestUpdateOptionInstallsFitPolicySnapshotOnTheWriter(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&Option{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMapRWMutex.Lock()
		delete(common.OptionMap, fitpolicy.OptionKey)
		common.OptionMapRWMutex.Unlock()
		fitpolicy.Install(nil)
	})
	fitpolicy.Install(nil)

	document := `{"version":42,"enabled":true,"shadow":true,"families":[]}`
	if err := UpdateOption(fitpolicy.OptionKey, document); err != nil {
		t.Fatalf("UpdateOption: %v", err)
	}
	installed := fitpolicy.Current()
	if installed == nil {
		t.Fatal("the writing node must install the new snapshot without waiting for the epoch")
	}
	if installed.Version() != 42 {
		t.Fatalf("installed version = %d, want 42", installed.Version())
	}
}
