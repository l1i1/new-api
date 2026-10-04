package fitpolicy

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// TestDefaultPolicyIsTheBuiltinRulesLive is the M3 gate at the document level:
// the document a node installs when no option has been written must be the
// shipped rules, and it must be live.
//
// A shadow default is the defect this asserts against, not a detail: since the
// compiled-in predicates were retired a shadow document attaches no requirement,
// so pinning stops entirely while every log line still says a policy is
// installed. The equivalence test validates the rules of BuiltinPolicy; this
// test is what ties that validated rule set to what production actually runs.
func TestDefaultPolicyIsTheBuiltinRulesLive(t *testing.T) {
	live := DefaultPolicy()
	reference := BuiltinPolicy()

	if live.Shadow {
		t.Fatal("the production default must be live, or the official-fit pin is inert by default")
	}
	if !live.Enabled {
		t.Fatal("the production default must be enabled")
	}
	if live.Version != reference.Version {
		t.Fatalf("version = %d, want the shipped version %d", live.Version, reference.Version)
	}
	if got, want := len(live.Families), len(reference.Families); got != want {
		t.Fatalf("families = %d, want the shipped %d", got, want)
	}
	for i := range reference.Families {
		if canonicalFamily(live.Families[i]) != canonicalFamily(reference.Families[i]) {
			t.Fatalf("family %q differs from the shipped rules", reference.Families[i].ID)
		}
	}

	// The two documents differ in exactly one field, so "the shipped rules" has
	// one definition and the flag cannot drift into a second rule set.
	if diff := DivergenceFromBuiltin(live); diff != "" {
		t.Fatalf("the production default must not diverge from itself, got %q", diff)
	}
	if diff := DivergenceFromBuiltin(reference); diff != "shadow false→true" {
		t.Fatalf("the reference document must differ from the default only in the shadow flag, got %q", diff)
	}
}

// canonicalFamily renders one family as a comparable string for the test above.
func canonicalFamily(family FamilyPolicy) string {
	return canonicalRules(family.Rules) + "#" + canonicalBehaviors(family.Behaviors) +
		"#" + family.UnknownMarkPolicyOrDefault() + "#" + family.EmptyMatchPolicyOrDefault()
}

// TestDefaultPolicyDecidesShippedShapes keeps the seed honest: with the default
// live, a shape the shipped contract narrows must carry an opinion, and one it
// leaves to the priority channel must not.
//
// deepseek-v4 and glm-5.3 keep their original predicate shapes. kimi-k3 moved
// to per-shape narrowing on 2026-10-03, so ordinary thinking is deliberately NO
// longer pinned for that family — the family-wide pin took all of its traffic
// off the priority channel, which is the defect the per-shape rules replaced.
func TestDefaultPolicyDecidesLikeTheShippedPredicates(t *testing.T) {
	snapshot, err := Compile(DefaultPolicy())
	if err != nil {
		t.Fatalf("the production default must compile: %v", err)
	}
	thinkingOn := []byte(`{"type":"enabled"}`)
	thinkingOff := []byte(`{"type":"disabled"}`)
	userOnly := []dto.Message{{Role: "user"}}

	cases := []struct {
		name  string
		model string
		view  RequestView
		want  bool
	}{
		{
			name: "ds thinking on pins", model: "deepseek-v4-flash", want: true,
			view: RequestView{Model: "deepseek-v4-flash", Thinking: thinkingOn, Messages: userOnly},
		},
		{
			name: "ds thinking off does not pin", model: "deepseek-v4-flash", want: false,
			view: RequestView{Model: "deepseek-v4-flash", Thinking: thinkingOff, Messages: userOnly},
		},
		{
			name: "k3 ordinary thinking stays on the priority channel", model: "kimi-k3", want: false,
			view: RequestView{Model: "kimi-k3", Thinking: thinkingOn, ToolChoice: []byte(`"auto"`), Messages: userOnly},
		},
		{
			name: "k3 thinking off narrows", model: "kimi-k3", want: true,
			view: RequestView{Model: "kimi-k3", Thinking: thinkingOff, Messages: userOnly},
		},
		{
			name: "glm whole family pins", model: "glm-5.3", want: true,
			view: RequestView{Model: "glm-5.3", Messages: userOnly},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := snapshot.Decide(testCase.model, true, testCase.view).HasOpinion()
			if got != testCase.want {
				t.Fatalf("live default decided %v, want %v", got, testCase.want)
			}
		})
	}
}

// TestDivergenceFromBuiltinNamesWhatMoved is the visibility half of M3: an
// operator document may deviate, but the deviation has to be legible in one log
// line rather than inferred from routing.
func TestDivergenceFromBuiltinNamesWhatMoved(t *testing.T) {
	if diff := DivergenceFromBuiltin(DefaultPolicy()); diff != "" {
		t.Fatalf("the default must not diverge from itself, got %q", diff)
	}

	// The exact defect the seed exists for: a shadow document in production.
	shadow := DefaultPolicy()
	shadow.Shadow = true
	if diff := DivergenceFromBuiltin(shadow); !strings.Contains(diff, "shadow false→true") {
		t.Fatalf("a shadow document must be reported as such, got %q", diff)
	}

	// The documented rollback lever.
	disabled := DefaultPolicy()
	disabled.Enabled = false
	if diff := DivergenceFromBuiltin(disabled); !strings.Contains(diff, "enabled true→false") {
		t.Fatalf("a disabled document must be reported as such, got %q", diff)
	}

	// A family dropped while its consumers stay is the silent case: nothing
	// errors, the shape simply stops being pinned.
	missingFamily := DefaultPolicy()
	kept := missingFamily.Families[:0:0]
	for _, family := range missingFamily.Families {
		if family.ID == familyKimiK3 {
			continue
		}
		kept = append(kept, family)
	}
	missingFamily.Families = kept
	if diff := DivergenceFromBuiltin(missingFamily); !strings.Contains(diff, "family kimi-k3 removed") {
		t.Fatalf("a dropped family must be named, got %q", diff)
	}

	// Rule-level moves inside a family.
	ruleChanged := DefaultPolicy()
	for i := range ruleChanged.Families {
		if ruleChanged.Families[i].ID != familyDeepSeekV4 {
			continue
		}
		ruleChanged.Families[i].Rules[0].When = "WholeFamily()"
	}
	if diff := DivergenceFromBuiltin(ruleChanged); !strings.Contains(diff, "family deepseek-v4: rules ds-logprobs changed") {
		t.Fatalf("a changed rule must be named, got %q", diff)
	}

	ruleRemoved := DefaultPolicy()
	droppedRule := ""
	for i := range ruleRemoved.Families {
		if ruleRemoved.Families[i].ID != familyKimiK3 {
			continue
		}
		droppedRule = ruleRemoved.Families[i].Rules[0].ID
		ruleRemoved.Families[i].Rules = nil
	}
	if diff := DivergenceFromBuiltin(ruleRemoved); !strings.Contains(diff, droppedRule+" removed") {
		t.Fatalf("a removed rule must be named, got %q", diff)
	}

	// A switch, not a rule: the unknown-mark turn changes how absence is read.
	permissive := DefaultPolicy()
	for i := range permissive.Families {
		permissive.Families[i].UnknownMarkPolicy = UnknownMarkPermissive
	}
	if diff := DivergenceFromBuiltin(permissive); !strings.Contains(diff, "unknown_mark_policy conservative→permissive") {
		t.Fatalf("a changed switch must be named, got %q", diff)
	}

	// A pure reordering of the same rules must not read as "rules" with an
	// empty explanation.
	reordered := DefaultPolicy()
	for i := range reordered.Families {
		if reordered.Families[i].ID != familyDeepSeekV4 {
			continue
		}
		rules := reordered.Families[i].Rules
		reordered.Families[i].Rules = []Rule{rules[1], rules[0], rules[2]}
	}
	if diff := DivergenceFromBuiltin(reordered); !strings.Contains(diff, "family deepseek-v4: rule order changed") {
		t.Fatalf("a reordered rule list must be reported as such, got %q", diff)
	}
}
