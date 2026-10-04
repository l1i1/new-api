package fitpolicy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/officialfit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// admissionFamily is a valid family skeleton to hang admission fields on.
func admissionFamily() FamilyPolicy {
	return FamilyPolicy{
		ID: "kimi-k3",
		Rules: []Rule{{
			ID: "k3-whole-family", When: "WholeFamily()", Require: []string{BehaviorFamilyWhole},
		}},
		Behaviors: map[string]Behavior{
			BehaviorFamilyWhole:      {Class: ClassVerdict},
			BehaviorThinkingCounting: {Class: ClassVerdict},
		},
	}
}

func TestAdmissionValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*FamilyPolicy)
		wantErr string
	}{
		{
			name: "declared source with a battery is the shadow window's configuration",
			mutate: func(f *FamilyPolicy) {
				f.AdmissionSource = AdmissionSourceDeclared
				f.AdmissionBattery = []string{BehaviorThinkingCounting}
			},
			wantErr: "",
		},
		{
			name: "measured source with a battery is valid",
			mutate: func(f *FamilyPolicy) {
				f.AdmissionSource = AdmissionSourceMeasured
				f.AdmissionBattery = []string{BehaviorThinkingCounting}
			},
			wantErr: "",
		},
		{
			name:    "measured without a battery cannot admit anything",
			mutate:  func(f *FamilyPolicy) { f.AdmissionSource = AdmissionSourceMeasured },
			wantErr: "without an admission battery",
		},
		{
			name:    "unknown source value",
			mutate:  func(f *FamilyPolicy) { f.AdmissionSource = "inherited" },
			wantErr: "admission_source",
		},
		{
			name:    "battery entry outside the declared vocabulary",
			mutate:  func(f *FamilyPolicy) { f.AdmissionBattery = []string{BehaviorImageParts} },
			wantErr: "undeclared behaviour",
		},
		{
			name:    "battery entry the rules never declared at all",
			mutate:  func(f *FamilyPolicy) { f.AdmissionBattery = []string{"no.such.behaviour"} },
			wantErr: "undeclared behaviour",
		},
		{
			name: "duplicate battery entry",
			mutate: func(f *FamilyPolicy) {
				f.AdmissionBattery = []string{BehaviorThinkingCounting, " " + BehaviorThinkingCounting}
			},
			wantErr: "repeats behaviour",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			family := admissionFamily()
			if testCase.mutate != nil {
				testCase.mutate(&family)
			}
			policy := Policy{Version: 1, Enabled: true, Families: []FamilyPolicy{family}}
			err := policy.Validate()
			if testCase.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.wantErr)
		})
	}
}

func TestAdmissionBatteryBound(t *testing.T) {
	family := admissionFamily()
	family.AdmissionBattery = make([]string, MaxAdmissionBatteryBehaviors+1)
	for i := range family.AdmissionBattery {
		family.AdmissionBattery[i] = BehaviorFamilyWhole
	}
	policy := Policy{Version: 1, Enabled: true, Families: []FamilyPolicy{family}}
	err := policy.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "admission battery")
}

func TestAdmissionSourceOrDefault(t *testing.T) {
	assert.Equal(t, AdmissionSourceDeclared, Admission{}.SourceOrDefault(),
		"a zero admission is declared, which is the behaviour that predates the field")
	assert.Equal(t, AdmissionSourceDeclared, Admission{Source: AdmissionSourceDeclared}.SourceOrDefault())
	assert.Equal(t, AdmissionSourceMeasured, Admission{Source: AdmissionSourceMeasured}.SourceOrDefault())
	assert.False(t, Admission{}.Measured())
	assert.True(t, Admission{Source: AdmissionSourceMeasured}.Measured())

	declared := Admission{Source: AdmissionSourceDeclared, Battery: []string{"a"}}
	assert.Equal(t, AdmissionSourceMeasured, declared.WithSourceMeasured().SourceOrDefault())
	assert.Equal(t, AdmissionSourceDeclared, declared.SourceOrDefault(), "WithSourceMeasured must not mutate the receiver")
}

// TestDefaultPolicyDeclaresAdmissionOnlyWhereMeasured pins the shape of the
// shipped default: a family that answers official behaviour from its channel
// type alone carries no admission fields at all (so its JSON — and therefore
// the hashes of documents that only change other families — stays as small as
// it was before the fields existed), while kimi-k3, which qualifies channels by
// measurement, names both the source and the battery it measures.
func TestDefaultPolicyDeclaresAdmissionOnlyWhereMeasured(t *testing.T) {
	document := DefaultPolicy()
	encoded, err := json.Marshal(document)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"admission_source":"measured"`)
	assert.Contains(t, string(encoded), `"admission_battery"`)

	for _, family := range document.Families {
		if family.ID == familyKimiK3 {
			assert.Equal(t, AdmissionSourceMeasured, family.AdmissionSource)
			assert.Len(t, family.AdmissionBattery, 5, "every kimi-k3 shape rule must be measured")
			continue
		}
		assert.Empty(t, family.AdmissionSource, "family %s answers from its type alone", family.ID)
		assert.Empty(t, family.AdmissionBattery, "family %s answers from its type alone", family.ID)
	}
	// The fields are omitempty in both directions: a document that round-trips
	// without them parses to the same zero values.
	var round FamilyPolicy
	require.NoError(t, json.Unmarshal([]byte(`{"id":"kimi-k3"}`), &round))
	assert.Empty(t, round.AdmissionSource)
	assert.Empty(t, round.AdmissionBattery)
}

func TestDecideCarriesAdmission(t *testing.T) {
	family := admissionFamily()
	family.AdmissionSource = AdmissionSourceMeasured
	family.AdmissionBattery = []string{BehaviorFamilyWhole, " " + BehaviorThinkingCounting}
	snapshot, err := Compile(Policy{Version: 1, Enabled: true, Families: []FamilyPolicy{family}})
	require.NoError(t, err)

	requirement := snapshot.Decide("kimi-k3", true, RequestView{Model: "kimi-k3"})
	require.True(t, requirement.HasOpinion())
	assert.Equal(t, AdmissionSourceMeasured, requirement.Admission.SourceOrDefault())
	assert.True(t, requirement.Admission.Measured())
	// The battery is normalized (trimmed) and copied, not aliased: editing the
	// requirement's battery cannot rewrite the snapshot's.
	assert.Equal(t, []string{BehaviorFamilyWhole, BehaviorThinkingCounting}, requirement.Admission.Battery)
	requirement.Admission.Battery[0] = "mutated"
	assert.Equal(t, []string{BehaviorFamilyWhole, BehaviorThinkingCounting}, snapshot.AdmissionBatteryFor(t, "kimi-k3"))
	assert.Equal(t, snapshot.Hash(), requirement.Admission.PolicyHash,
		"the admission's policy binding is the snapshot that produced the decision")
}

func TestSnapshotAdmissionFor(t *testing.T) {
	family := admissionFamily()
	family.AdmissionSource = AdmissionSourceMeasured
	family.AdmissionBattery = []string{BehaviorThinkingCounting}
	snapshot, err := Compile(Policy{Version: 1, Enabled: true, Families: []FamilyPolicy{family}})
	require.NoError(t, err)

	admission, ok := snapshot.AdmissionFor("kimi-k3")
	require.True(t, ok)
	assert.True(t, admission.Measured())
	assert.Equal(t, []string{BehaviorThinkingCounting}, admission.Battery)
	assert.Equal(t, snapshot.Hash(), admission.PolicyHash)

	// A model of a family the document does not carry resolves to nothing, so
	// callers fall back to declared.
	_, ok = snapshot.AdmissionFor("deepseek-v4-pro")
	assert.False(t, ok, "a family absent from the document has no admission to resolve")

	// A model outside every family resolves to nothing.
	_, ok = snapshot.AdmissionFor("gpt-4o")
	assert.False(t, ok)

	// A nil snapshot is the pre-install state.
	_, ok = (*Snapshot)(nil).AdmissionFor("kimi-k3")
	assert.False(t, ok)
}

func TestAdmissionDivergence(t *testing.T) {
	// Compared family-to-family rather than against the builtin: the question is
	// whether an admission change is legible, not what the shipped kimi-k3
	// rules happen to be this month.
	declared := admissionFamily()
	measured := admissionFamily()
	measured.AdmissionSource = AdmissionSourceMeasured
	measured.AdmissionBattery = []string{BehaviorThinkingCounting}

	diff := familyDivergence("kimi-k3", declared, measured)
	require.NotEmpty(t, diff)
	assert.Contains(t, diff, "admission_source declared→measured")
	assert.Contains(t, diff, "admission_battery")
	assert.Contains(t, diff, BehaviorThinkingCounting+" added")

	// A battery reorder is not a divergence: the order of a battery is not
	// part of its meaning.
	reordered := admissionFamily()
	reordered.AdmissionBattery = []string{BehaviorFamilyWhole, BehaviorThinkingCounting}
	ordered := admissionFamily()
	ordered.AdmissionBattery = []string{BehaviorThinkingCounting, BehaviorFamilyWhole}
	assert.Empty(t, familyDivergence("kimi-k3", ordered, reordered))
}

// AdmissionBatteryFor is the test-side accessor for the compiled battery.
func (s *Snapshot) AdmissionBatteryFor(t *testing.T, model string) []string {
	t.Helper()
	familyID := officialfit.FamilyOf(model)
	family, ok := s.families[familyID]
	require.True(t, ok)
	return family.admissionBattery
}

// TestParsePolicyRejectsUnknownAdmissionFields documents the rollout contract:
// a document carrying admission fields cannot be parsed by a node that predates
// them, so the fields only reach the option after the whole fleet runs a build
// that knows them. This test pins the DisallowUnknownFields behaviour the
// rollout order depends on.
func TestParsePolicyRejectsUnknownAdmissionFields(t *testing.T) {
	raw := `{"version":1,"enabled":true,"families":[{"id":"kimi-k3","admission_future_field":true}]}`
	_, err := ParsePolicy([]byte(raw))
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "admission_future_field") || strings.Contains(err.Error(), "unknown field"),
		"an unknown field must be named in the rejection: %v", err)
}
