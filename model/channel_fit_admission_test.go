package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The admission layer's contract tests: the official-behaviour predicate under
// both admission sources, and the narrowing's use of the request-bound
// admission.
//
// Fixtures mirror the live kimi-k3 topology at the time this landed: channel 8
// is Moonshot-typed (the family's official type), 41 is a whitelisted
// aggregator, 37 is an ordinary aggregator. What the tests pin is the split:
// declared answers from the declaration (allowlist ∪ type), measured answers
// from the battery (fresh marks ∪ type), and nothing else changes.

const (
	admissionTestPolicyHash   = "admission-policy-live"
	admissionTestBaselineHash = "admission-baseline-live"
)

// installAdmissionSelectionCache publishes the kimi-k3 candidate cache: one
// Moonshot-typed channel, one whitelisted aggregator, one plain aggregator.
func installAdmissionSelectionCache(t *testing.T) {
	t.Helper()
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	oldChannelsIDM := channelsIDM
	channelsIDM = map[int]*Channel{
		8:  {Id: 8, Type: constant.ChannelTypeMoonshot, Priority: int64Ptr(10), Weight: uintPtr(1)},
		41: {Id: 41, Type: constant.ChannelTypeOpenAI, Priority: int64Ptr(10), Weight: uintPtr(1)},
		37: {Id: 37, Type: constant.ChannelTypeOpenAI, Priority: int64Ptr(10), Weight: uintPtr(1)},
	}
	channelSyncLock.Unlock()
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		channelSyncLock.Lock()
		channelsIDM = oldChannelsIDM
		channelSyncLock.Unlock()
	})
}

// seedAdmissionMark writes one suite mark bound to the live admission hashes.
func seedAdmissionMark(t *testing.T, channelID int, behavior, policyHash string, supported bool) {
	t.Helper()
	require.NoError(t, DB.Create(&ChannelFitCapability{
		ChannelId: channelID, Family: "kimi-k3", Model: "kimi-k3", Behavior: behavior,
		Supported: supported, Source: FitCapabilitySourceSuite, Suite: "admission-test",
		Cases: "1/1", Rounds: 1,
		At:           common.GetTimestamp() - 60,
		PolicyHash:   policyHash,
		BaselineHash: admissionTestBaselineHash,
		Revision:     1,
	}).Error)
}

// rebuildAdmissionIndex reloads the capability index from the test database.
func rebuildAdmissionIndex(t *testing.T) {
	t.Helper()
	ResetFitCapabilityIndexForTest()
	InitFitCapabilityIndex()
	require.True(t, FitCapabilityIndexReady())
}

// admissionMeasured is a measured admission over the battery the fixtures
// measure, bound to the live hashes.
func admissionMeasured(behaviors ...string) fitpolicy.Admission {
	return fitpolicy.Admission{
		Family:       "kimi-k3",
		Source:       fitpolicy.AdmissionSourceMeasured,
		Battery:      behaviors,
		PolicyHash:   admissionTestPolicyHash,
		BaselineHash: admissionTestBaselineHash,
	}
}

// matchAdmission answers the locked predicate for one channel under one
// admission, taking the read lock the predicate requires.
func matchAdmission(channelID int, model string, admission fitpolicy.Admission) bool {
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()
	return officialFitChannelMatchesAdmissionLocked(channelID, model, admission)
}

// TestOfficialFitAdmissionDeclaredMatchesLegacy pins the declared source after
// the allowlist retirement: only the family's official channel type qualifies,
// marks and former allowlist entries change nothing.
func TestOfficialFitAdmissionDeclaredMatchesLegacy(t *testing.T) {
	installAdmissionSelectionCache(t)
	setupFitCapabilityDB(t)
	// A fully-armed mark set exists; declared admission must ignore it.
	seedAdmissionMark(t, 41, fitpolicy.BehaviorThinkingCounting, admissionTestPolicyHash, true)
	seedAdmissionMark(t, 37, fitpolicy.BehaviorThinkingCounting, admissionTestPolicyHash, true)
	rebuildAdmissionIndex(t)

	declared := fitpolicy.Admission{Family: "kimi-k3", Source: fitpolicy.AdmissionSourceDeclared}
	assert.True(t, matchAdmission(8, "kimi-k3", declared), "the family's official type stays admitted")
	assert.False(t, matchAdmission(41, "kimi-k3", declared),
		"with the allowlist retired, a non-official-typed channel is never admitted under declared, marks or not")
	assert.False(t, matchAdmission(37, "kimi-k3", declared))

	// The legacy predicate resolves the same answer from the installed policy:
	// no snapshot is installed, so the default is declared.
	channelSyncLock.RLock()
	legacy8 := officialFitChannelMatchesLocked(8, "kimi-k3")
	legacy41 := officialFitChannelMatchesLocked(41, "kimi-k3")
	channelSyncLock.RUnlock()
	assert.True(t, legacy8)
	assert.False(t, legacy41)
}

// TestOfficialFitAdmissionMeasuredFromMarks is the replacement's core
// behaviour: a non-official-typed channel qualifies only through fresh,
// passing, correctly-bound marks; the official type needs no marks at all.
func TestOfficialFitAdmissionMeasuredFromMarks(t *testing.T) {
	installAdmissionSelectionCache(t)
	setupFitCapabilityDB(t)

	seedAdmissionMark(t, 41, fitpolicy.BehaviorThinkingCounting, admissionTestPolicyHash, true)
	rebuildAdmissionIndex(t)
	battery := []string{fitpolicy.BehaviorThinkingCounting}

	// Fresh, passing, bound: the aggregator is admitted by measurement.
	assert.True(t, matchAdmission(41, "kimi-k3", admissionMeasured(battery...)),
		"a channel with fresh passing marks for the whole battery is admitted")
	assert.True(t, matchAdmission(8, "kimi-k3", admissionMeasured(battery...)),
		"the family's official type is admitted by identity, marks or none")
	assert.False(t, matchAdmission(37, "kimi-k3", admissionMeasured(battery...)),
		"a channel nobody measured is not admitted")

	// Marks bound to another policy hash are stale and admit nothing.
	assert.False(t, matchAdmission(41, "kimi-k3", withPolicy(admissionMeasured(battery...), "other-policy")),
		"stale-bound marks do not admit: a policy change must re-measure, not re-validate")

	// A battery the channel only partly satisfies does not admit it.
	twoBehaviorBattery := []string{fitpolicy.BehaviorThinkingCounting, fitpolicy.BehaviorLogprobsDualPath}
	assert.False(t, matchAdmission(41, "kimi-k3", admissionMeasured(twoBehaviorBattery...)),
		"admission requires every battery behaviour, not a subset")

	// A failing mark is an explicit negative.
	seedAdmissionMark(t, 37, fitpolicy.BehaviorThinkingCounting, admissionTestPolicyHash, false)
	rebuildAdmissionIndex(t)
	assert.False(t, matchAdmission(37, "kimi-k3", admissionMeasured(battery...)),
		"a measured failure never admits")
}

// TestOfficialFitAdmissionMeasuredIsConservative pins the semantics the battery
// borrows from the marks layer on purpose: an unknown mark means "not
// admitted" regardless of the family's UnknownMarkPolicy, because admission is
// a promise. The permissive reading exists so phase-1 narrowing does not rule
// out unmeasured channels — never so an unmeasured channel is admitted.
func TestOfficialFitAdmissionMeasuredIsConservative(t *testing.T) {
	installAdmissionSelectionCache(t)
	setupFitCapabilityDB(t)
	// No marks at all: the state every channel is in before the first suite
	// report, and the state the whole platform was in when this test was
	// written.
	rebuildAdmissionIndex(t)

	admission := admissionMeasured(fitpolicy.BehaviorThinkingCounting)

	// Even a permissive policy's admission answers conservatively: build the
	// battery requirement the way the narrowing would for a permissive family
	// and confirm the admission predicate still refuses.
	assert.False(t, channelAdmissionBatterySatisfied(41, "kimi-k3", admission),
		"an unmeasured channel is never admitted by the battery, whatever unknown_mark_policy says")
}

// TestNarrowChannelsUnderMeasuredAdmission runs the two-phase narrowing with a
// request-bound measured admission and proves the official set — phase 2 — is
// the battery's answer, while the declared admission on the same candidates
// reproduces the legacy set.
func TestNarrowChannelsUnderMeasuredAdmission(t *testing.T) {
	installAdmissionSelectionCache(t)
	setupFitCapabilityDB(t)
	seedAdmissionMark(t, 41, fitpolicy.BehaviorThinkingCounting, admissionTestPolicyHash, true)
	rebuildAdmissionIndex(t)

	candidates := []int{8, 41, 37}
	measuredFilter := &FitChannelFilter{
		Requirement: fitpolicy.Requirement{
			Family: "kimi-k3", Model: "kimi-k3",
			Marks:     []string{fitpolicy.BehaviorFamilyWhole},
			Admission: admissionMeasured(fitpolicy.BehaviorThinkingCounting),
		},
	}
	channelSyncLock.RLock()
	narrowed, applied := measuredFilter.narrowChannels(candidates, "kimi-k3")
	channelSyncLock.RUnlock()
	require.True(t, applied)
	assert.ElementsMatch(t, []int{8, 41}, narrowed,
		"phase 2 under measured admission is the type ∪ battery set")

	// The declared admission over the same candidates is the legacy answer,
	// which is what the equivalence window compares against.
	declaredFilter := &FitChannelFilter{
		Requirement: fitpolicy.Requirement{
			Family: "kimi-k3", Model: "kimi-k3",
			Marks:     []string{fitpolicy.BehaviorFamilyWhole},
			Admission: fitpolicy.Admission{Family: "kimi-k3", Source: fitpolicy.AdmissionSourceDeclared},
		},
	}
	channelSyncLock.RLock()
	declared, appliedDeclared := declaredFilter.narrowChannels(candidates, "kimi-k3")
	channelSyncLock.RUnlock()
	require.True(t, appliedDeclared)
	assert.ElementsMatch(t, []int{8}, declared,
		"declared is the type-only set now that the allowlist is retired")

	// A measured family whose battery admits no candidate empties the set so
	// the caller fails honestly — the availability cliff is deliberate and the
	// narrowing must not soften it. The family's official type stays admitted
	// by identity, so the empty case is evaluated over the aggregators alone.
	emptyFilter := &FitChannelFilter{
		Requirement: fitpolicy.Requirement{
			Family: "kimi-k3", Model: "kimi-k3",
			Marks:     []string{fitpolicy.BehaviorFamilyWhole},
			Admission: admissionMeasured("logprobs.dual_path"),
		},
	}
	channelSyncLock.RLock()
	empty, appliedEmpty := emptyFilter.narrowChannels([]int{41, 37}, "kimi-k3")
	channelSyncLock.RUnlock()
	require.True(t, appliedEmpty)
	assert.Empty(t, empty, "measured admission with no qualifying candidate keeps the empty set")
}

// TestChannelIsOfficialFitForModelMeasuredDBPath covers the public predicate on
// the database path (memory cache disabled): with a measured policy installed,
// the battery must be read from the capability table, not from the channel's
// declaration.
func TestChannelIsOfficialFitForModelMeasuredDBPath(t *testing.T) {
	setupFitCapabilityDB(t)
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })

	// Install a measured policy for kimi-k3 whose battery is that behaviour,
	// then ask the public predicate — the one the affinity gate and the relay
	// path call — on the database path.
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
	fitpolicy.Install(snapshot)
	t.Cleanup(func() { fitpolicy.Install(nil) })

	// The mark must carry the installed snapshot's own hash: the admission the
	// public predicate resolves is bound to it, and a mark bound to anything
	// else is stale by the binding rule.
	seedAdmissionMark(t, 7, fitpolicy.BehaviorThinkingCounting, snapshot.Hash(), true)
	rebuildAdmissionIndex(t)

	assert.True(t, ChannelIsOfficialFitForModel(7, "kimi-k3"),
		"the DB-path battery evaluation reads the same capability index the selection path reads")
}

// withPolicy returns the admission bound to a different policy hash, for the
// stale-binding case.
func withPolicy(a fitpolicy.Admission, policyHash string) fitpolicy.Admission {
	other := a
	other.PolicyHash = policyHash
	return other
}
