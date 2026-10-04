package fitpolicy

// BuiltinPolicy returns the shipped default document — the rules a node runs
// when no policy option has been written (see DefaultPolicy).
//
// It exists so step A can run in shadow and prove equivalence: with this policy
// installed, "the request requires at least one behaviour" must be true if and
// only if the shipped Go predicate pins the request. The composition lives here
// as data; the primitives it calls stay in Go (see request.go).
//
// Shadow is on in this document because that is what the equivalence window
// installed, and the shadow safety gate measures exactly this value. Production
// does not run it as-is: DefaultPolicy below is the same rules with shadow off,
// which is the document a node installs when the option has never been written.
// The distinction matters because since the compiled-in predicates were retired
// a shadow document decides and acts on nothing — installing it by default would
// leave the whole official-fit pin inert. The rules, not the flag, are what the
// equivalence test measures.
//
// Family-specific notes, all carried over from middleware/distributor.go:
//   - DeepSeek V4 pins logprobs, image parts and thinking-output requests; the
//     one class the pool serves faithfully is explicit thinking-off.
//   - kimi-k3 narrows per shape. The family moved to measured admission on
//     2026-10-03: a channel qualifies by passing the five behaviour probes
//     below, so a request is only pinned when its own shape is one the
//     priority channel measurably cannot reproduce. Pinning the whole family
//     would take ordinary traffic off the priority channel, which is exactly
//     what the per-shape rules exist to avoid.
//   - glm-5.3 has no measured selective predicate yet, so the whole family
//     pins.
func BuiltinPolicy() Policy {
	return Policy{
		Version: 1,
		Enabled: true,
		Shadow:  true,
		Families: []FamilyPolicy{
			{
				ID: familyDeepSeekV4,
				Rules: []Rule{
					{ID: "ds-logprobs", When: "LogprobsRequested()", Require: []string{BehaviorLogprobsDualPath}},
					{ID: "ds-image-part", When: "HasImagePart()", Require: []string{BehaviorImageParts}},
					{ID: "ds-thinking-expected", When: "!ThinkingDisabled()", Require: []string{BehaviorThinkingCounting}},
				},
				Behaviors: map[string]Behavior{
					BehaviorLogprobsDualPath: {Class: ClassVerdict},
					BehaviorImageParts:       {Class: ClassVerdict},
					BehaviorThinkingCounting: {Class: ClassVerdict},
				},
				UnknownMarkPolicy: UnknownMarkConservative,
				EmptyMatchPolicy:  EmptyMatchLegacyHardPin,
			},
			{
				ID: familyKimiK3,
				Rules: []Rule{
					{ID: "k3-thinking-off", When: `ThinkingDisabled() || ReasoningEffortIs("none") || ReasoningEffortIs("minimal")`, Require: []string{BehaviorThinkingCounting}},
					{ID: "k3-tool-choice", When: "ToolChoiceForcesOfficial()", Require: []string{BehaviorToolsChoiceSemantics}},
					{ID: "k3-response-format", When: "ResponseFormatNotText()", Require: []string{BehaviorResponseFormatJSON}},
					{ID: "k3-history", When: "!HistoryBeginsWithUserTurn()", Require: []string{BehaviorHistoryAssistantFirst}},
					{ID: "k3-dynamic-tools", When: "MessagesCarryDynamicTools()", Require: []string{BehaviorToolsDynamicNames}},
				},
				Behaviors: map[string]Behavior{
					BehaviorThinkingCounting:      {Class: ClassVerdict},
					BehaviorToolsChoiceSemantics:  {Class: ClassVerdict},
					BehaviorResponseFormatJSON:    {Class: ClassVerdict},
					BehaviorHistoryAssistantFirst: {Class: ClassVerdict},
					BehaviorToolsDynamicNames:     {Class: ClassVerdict},
				},
				UnknownMarkPolicy: UnknownMarkConservative,
				EmptyMatchPolicy:  EmptyMatchLegacyHardPin,
				AdmissionSource:   AdmissionSourceMeasured,
				AdmissionBattery: []string{
					BehaviorThinkingCounting,
					BehaviorToolsChoiceSemantics,
					BehaviorResponseFormatJSON,
					BehaviorHistoryAssistantFirst,
					BehaviorToolsDynamicNames,
				},
			},
			{
				ID: familyGlm53,
				Rules: []Rule{
					{ID: "glm-whole-family", When: "WholeFamily()", Require: []string{BehaviorFamilyWhole}},
				},
				Behaviors: map[string]Behavior{
					BehaviorFamilyWhole: {Class: ClassVerdict},
				},
				UnknownMarkPolicy: UnknownMarkConservative,
				EmptyMatchPolicy:  EmptyMatchLegacyHardPin,
			},
		},
	}
}

// DefaultPolicy is the document a node runs when the policy option has never
// been written: the shipped rules, live.
//
// The option-used-to-be-absent path used to install nothing at all, which since
// the compiled-in predicates were retired means "no opinion for every request" —
// no shape is pinned, and the four consumers of the pin (channel selection,
// affinity admission, affinity recording, and the retry guard in
// service.officialFitPinKeepsVerdict) all take the unpinned branch. Seeding the
// shipped document is what keeps the release no worse than the predicate era,
// and it is the reason the equivalence test still describes production rather
// than a document nobody installs.
//
// This is a default, not a lock: a written document — including one with
// enabled=false, which is the documented rollback — always wins.
func DefaultPolicy() Policy {
	policy := BuiltinPolicy()
	policy.Shadow = false
	return policy
}

// Canonical family ids, mirrored from officialfit so this package's built-in
// policy cannot drift from the registry. They are duplicated as constants only
// for the built-in policy; a user-supplied policy is validated against the
// registry instead.
const (
	familyDeepSeekV4 = "deepseek-v4"
	familyKimiK3     = "kimi-k3"
	familyGlm53      = "glm-5.3"
)

// Behaviour names used by the built-in policy. They are stable identifiers: a
// channel capability mark and a CDP suite report both key off these strings, so
// renaming one is a data migration, not a refactor.
const (
	BehaviorThinkingCounting      = "usage.thinking_counting"
	BehaviorToolsChoiceSemantics  = "tools.choice_semantics"
	BehaviorResponseFormatJSON    = "response_format.json"
	BehaviorHistoryAssistantFirst = "history.assistant_first"
	BehaviorToolsDynamicNames     = "tools.dynamic_names"
	BehaviorLogprobsDualPath      = "logprobs.dual_path"
	BehaviorImageParts            = "image.parts"
	BehaviorFamilyWhole           = "family.whole"
)
