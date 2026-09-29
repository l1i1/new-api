package fitpolicy

import (
	"fmt"
	"sort"
	"strings"
)

// Document drift reporting.
//
// The policy being data is the point of this layer: an operator document is
// allowed to differ from the shipped default, and it is expected to — the rules
// are edited without a release. What is not allowed is for that difference to be
// invisible. A document that quietly pins nothing (shadow left on, enabled set
// to false by an old rollback, a family dropped while its consumers stay) looks
// exactly like a healthy policy from the outside, because the selection path
// answers "no opinion" for both. This function turns that silence into a log
// line naming what changed.
//
// It is deliberately coarse: it names fields, families and rule ids, not a
// character-level diff. The reader's question is "did the live document move
// away from the shipped rules, and in which family", not "which byte changed".

// DivergenceFromBuiltin describes how policy differs from the shipped default
// document (DefaultPolicy). It returns "" when the two are identical.
func DivergenceFromBuiltin(policy Policy) string {
	reference := DefaultPolicy()
	var diffs []string

	if policy.Version != reference.Version {
		diffs = append(diffs, fmt.Sprintf("version %d→%d", reference.Version, policy.Version))
	}
	if policy.Enabled != reference.Enabled {
		diffs = append(diffs, fmt.Sprintf("enabled %t→%t", reference.Enabled, policy.Enabled))
	}
	if policy.Shadow != reference.Shadow {
		diffs = append(diffs, fmt.Sprintf("shadow %t→%t", reference.Shadow, policy.Shadow))
	}
	if strings.TrimSpace(policy.Baseline) != strings.TrimSpace(reference.Baseline) {
		diffs = append(diffs, fmt.Sprintf("baseline %q→%q", strings.TrimSpace(reference.Baseline), strings.TrimSpace(policy.Baseline)))
	}

	referenceFamilies := familiesByID(reference.Families)
	liveFamilies := familiesByID(policy.Families)

	removed := make([]string, 0, len(referenceFamilies))
	for id := range referenceFamilies {
		if _, ok := liveFamilies[id]; !ok {
			removed = append(removed, id)
		}
	}
	added := make([]string, 0, len(liveFamilies))
	for id := range liveFamilies {
		if _, ok := referenceFamilies[id]; !ok {
			added = append(added, id)
		}
	}
	sort.Strings(removed)
	sort.Strings(added)
	for _, id := range removed {
		diffs = append(diffs, "family "+id+" removed")
	}
	for _, id := range added {
		diffs = append(diffs, "family "+id+" added")
	}

	shared := make([]string, 0, len(referenceFamilies))
	for id := range referenceFamilies {
		if _, ok := liveFamilies[id]; ok {
			shared = append(shared, id)
		}
	}
	sort.Strings(shared)
	for _, id := range shared {
		if diff := familyDivergence(id, referenceFamilies[id], liveFamilies[id]); diff != "" {
			diffs = append(diffs, diff)
		}
	}

	return strings.Join(diffs, "; ")
}

// familyDivergence describes how one family's rules and switches moved, in the
// order a reader checks them: rules first (they are what pins traffic), then
// behaviours, then the two policies that decide how a missing mark and an empty
// candidate set are treated.
func familyDivergence(id string, reference, live FamilyPolicy) string {
	var parts []string
	if canonicalRules(reference.Rules) != canonicalRules(live.Rules) {
		if diff := rulesDiff(reference.Rules, live.Rules); diff != "" {
			parts = append(parts, "rules "+diff)
		} else {
			parts = append(parts, "rule order changed")
		}
	}
	if canonicalBehaviors(reference.Behaviors) != canonicalBehaviors(live.Behaviors) {
		parts = append(parts, "behaviours "+behaviorsDiff(reference.Behaviors, live.Behaviors))
	}
	if reference.UnknownMarkPolicyOrDefault() != live.UnknownMarkPolicyOrDefault() {
		parts = append(parts, fmt.Sprintf("unknown_mark_policy %s→%s",
			reference.UnknownMarkPolicyOrDefault(), live.UnknownMarkPolicyOrDefault()))
	}
	if reference.EmptyMatchPolicyOrDefault() != live.EmptyMatchPolicyOrDefault() {
		parts = append(parts, fmt.Sprintf("empty_match_policy %s→%s",
			reference.EmptyMatchPolicyOrDefault(), live.EmptyMatchPolicyOrDefault()))
	}
	if len(parts) == 0 {
		return ""
	}
	return "family " + id + ": " + strings.Join(parts, ", ")
}

func familiesByID(families []FamilyPolicy) map[string]FamilyPolicy {
	byID := make(map[string]FamilyPolicy, len(families))
	for _, family := range families {
		byID[strings.TrimSpace(family.ID)] = family
	}
	return byID
}

// canonicalRules renders the rule list in declaration order. Order is kept
// because it decides the order of the marks a request carries, which the shadow
// trace prints.
func canonicalRules(rules []Rule) string {
	rendered := make([]string, 0, len(rules))
	for _, rule := range rules {
		rendered = append(rendered, canonicalRule(rule))
	}
	return strings.Join(rendered, ";")
}

func canonicalRule(rule Rule) string {
	required := append([]string(nil), rule.Require...)
	for i := range required {
		required[i] = strings.TrimSpace(required[i])
	}
	return strings.TrimSpace(rule.ID) + "|" + strings.TrimSpace(rule.When) + "|" + strings.Join(required, ",")
}

func canonicalBehaviors(behaviors map[string]Behavior) string {
	rendered := make([]string, 0, len(behaviors))
	for name, behavior := range behaviors {
		rendered = append(rendered, strings.TrimSpace(name)+"="+strings.TrimSpace(behavior.Class))
	}
	sort.Strings(rendered)
	return strings.Join(rendered, ",")
}

func rulesDiff(reference, live []Rule) string {
	referenceRules := make(map[string]string, len(reference))
	for _, rule := range reference {
		referenceRules[strings.TrimSpace(rule.ID)] = canonicalRule(rule)
	}
	liveRules := make(map[string]string, len(live))
	for _, rule := range live {
		liveRules[strings.TrimSpace(rule.ID)] = canonicalRule(rule)
	}
	var parts []string
	for id, rendered := range referenceRules {
		switch current, ok := liveRules[id]; {
		case !ok:
			parts = append(parts, id+" removed")
		case current != rendered:
			parts = append(parts, id+" changed")
		}
	}
	for id := range liveRules {
		if _, ok := referenceRules[id]; !ok {
			parts = append(parts, id+" added")
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func behaviorsDiff(reference, live map[string]Behavior) string {
	referenceClasses := normalizedBehaviorClasses(reference)
	liveClasses := normalizedBehaviorClasses(live)
	var parts []string
	for name, class := range referenceClasses {
		switch current, ok := liveClasses[name]; {
		case !ok:
			parts = append(parts, name+" removed")
		case current != class:
			parts = append(parts, name+" "+class+"→"+current)
		}
	}
	for name := range liveClasses {
		if _, ok := referenceClasses[name]; !ok {
			parts = append(parts, name+" added")
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func normalizedBehaviorClasses(behaviors map[string]Behavior) map[string]string {
	classes := make(map[string]string, len(behaviors))
	for name, behavior := range behaviors {
		classes[strings.TrimSpace(name)] = strings.TrimSpace(behavior.Class)
	}
	return classes
}
