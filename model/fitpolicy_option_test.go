package model

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/gin-gonic/gin"
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
	// An absent option installs the shipped default rather than nothing. That is
	// the whole point: with the compiled-in predicates retired, "nothing" means
	// no shape is pinned and every consumer of the pin takes the unpinned branch.
	// A default that is installed but shadow would be just as inert, so both
	// halves are asserted here.
	seeded := fitpolicy.Current()
	if seeded == nil {
		t.Fatal("an absent option must install the shipped default policy")
	}
	wantDefault, err := fitpolicy.Compile(fitpolicy.DefaultPolicy())
	if err != nil {
		t.Fatalf("compile shipped default: %v", err)
	}
	if seeded.Hash() != wantDefault.Hash() {
		t.Fatalf("an absent option must install exactly the shipped default: hash %s, want %s", seeded.Hash(), wantDefault.Hash())
	}
	if seeded.Shadow() {
		t.Fatal("the shipped default must be live, or an unconfigured node pins nothing")
	}
	if reference, err := fitpolicy.Compile(fitpolicy.BuiltinPolicy()); err == nil && seeded.Hash() == reference.Hash() {
		t.Fatal("the shipped default must not be the shadow reference document")
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
	if installed.Hash() == wantDefault.Hash() {
		t.Fatal("a written document must replace the shipped default, not be ignored")
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

// clearFitPolicyOptionEnv isolates a test from the process-wide option map and
// snapshot, and restores both.
func clearFitPolicyOptionEnv(t *testing.T) {
	t.Helper()
	previous := fitpolicy.Current()
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	previousRaw, hadOption := common.OptionMap[fitpolicy.OptionKey]
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		if hadOption {
			common.OptionMap[fitpolicy.OptionKey] = previousRaw
		} else {
			delete(common.OptionMap, fitpolicy.OptionKey)
		}
		common.OptionMapRWMutex.Unlock()
		fitpolicy.Install(previous)
	})
}

// TestClearedFitPolicyOptionDisarmsTheLayerUnlikeAnAbsentOne pins the two
// meanings an unwritten option must not be collapsed into. Clearing the value is
// the documented way to take the layer out and must keep working; never writing
// it must install the shipped default, because "nothing installed" means no
// shape is pinned at all now that the compiled-in predicates are retired.
func TestClearedFitPolicyOptionDisarmsTheLayerUnlikeAnAbsentOne(t *testing.T) {
	clearFitPolicyOptionEnv(t)

	common.OptionMapRWMutex.Lock()
	common.OptionMap[fitpolicy.OptionKey] = ""
	common.OptionMapRWMutex.Unlock()
	if err := refreshFitPolicySnapshot(); err != nil {
		t.Fatalf("a cleared option is not an error: %v", err)
	}
	if fitpolicy.Current() != nil {
		t.Fatal("a cleared option must drop back to no opinion, or the rollback lever re-arms itself")
	}

	common.OptionMapRWMutex.Lock()
	delete(common.OptionMap, fitpolicy.OptionKey)
	common.OptionMapRWMutex.Unlock()
	if err := refreshFitPolicySnapshot(); err != nil {
		t.Fatalf("an absent option is not an error: %v", err)
	}
	seeded := fitpolicy.Current()
	if seeded == nil || seeded.Shadow() {
		t.Fatal("an absent option must install the shipped default live")
	}
}

// TestFitPolicySeedAndDriftAreLogged is the production half of M3: which
// document is live decides whether anything is pinned, and every way of getting
// it wrong is silent. The log is the only signal, so both the seed and a
// deviation from the shipped rules have to appear there.
func TestFitPolicySeedAndDriftAreLogged(t *testing.T) {
	clearFitPolicyOptionEnv(t)

	// The notices are budgeted because this path runs on every sync; a fresh
	// budget keeps the assertions independent of how many other tests ran first.
	previousSeed := fitPolicySeededLog
	previousDrift := fitPolicyDivergenceLog
	previousCleared := fitPolicyDisabledLog
	fitPolicySeededLog = common.NewLogBudget(1, time.Hour)
	// Two divergent documents are asserted below, so the budget needs two lines:
	// the limiter itself is covered by common/log_budget_test.go.
	fitPolicyDivergenceLog = common.NewLogBudget(2, time.Hour)
	fitPolicyDisabledLog = common.NewLogBudget(1, time.Hour)
	t.Cleanup(func() {
		fitPolicySeededLog = previousSeed
		fitPolicyDivergenceLog = previousDrift
		fitPolicyDisabledLog = previousCleared
	})

	var logs strings.Builder
	previousWriter := gin.DefaultWriter
	gin.DefaultWriter = &logs
	t.Cleanup(func() { gin.DefaultWriter = previousWriter })

	common.OptionMapRWMutex.Lock()
	delete(common.OptionMap, fitpolicy.OptionKey)
	common.OptionMapRWMutex.Unlock()
	if err := refreshFitPolicySnapshot(); err != nil {
		t.Fatalf("refresh with an absent option: %v", err)
	}
	if !strings.Contains(logs.String(), "has never been written") {
		t.Fatalf("the seed must be visible, got %q", logs.String())
	}

	// A document identical to the shipped default must not produce a drift line:
	// a warning that always fires is a warning nobody reads.
	logs.Reset()
	defaultDocument, err := common.Marshal(fitpolicy.DefaultPolicy())
	if err != nil {
		t.Fatalf("marshal the shipped default: %v", err)
	}
	common.OptionMapRWMutex.Lock()
	common.OptionMap[fitpolicy.OptionKey] = string(defaultDocument)
	common.OptionMapRWMutex.Unlock()
	if err := refreshFitPolicySnapshot(); err != nil {
		t.Fatalf("refresh with the shipped default document: %v", err)
	}
	if strings.Contains(logs.String(), "differs from the shipped default") {
		t.Fatalf("an identical document must not be reported as drift: %q", logs.String())
	}

	// The shadow reference document is a real deviation when it is what an
	// operator installs, and it is the deviation that silently turns the pin off.
	logs.Reset()
	common.OptionMapRWMutex.Lock()
	common.OptionMap[fitpolicy.OptionKey] = builtinFitPolicyJSON(t)
	common.OptionMapRWMutex.Unlock()
	if err := refreshFitPolicySnapshot(); err != nil {
		t.Fatalf("refresh with the reference document: %v", err)
	}
	if !strings.Contains(logs.String(), "shadow false→true") {
		t.Fatalf("installing the shadow reference as the live document must be reported, got %q", logs.String())
	}

	// An operator document may deviate; the deviation must be named.
	logs.Reset()
	common.OptionMapRWMutex.Lock()
	common.OptionMap[fitpolicy.OptionKey] = `{"version":7,"enabled":true,"shadow":true,"families":[]}`
	common.OptionMapRWMutex.Unlock()
	if err := refreshFitPolicySnapshot(); err != nil {
		t.Fatalf("refresh with an operator document: %v", err)
	}
	line := logs.String()
	for _, want := range []string{"differs from the shipped default", "version 1→7", "shadow false→true", "family kimi-k3 removed"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the drift line must contain %q, got %q", want, line)
		}
	}
}
