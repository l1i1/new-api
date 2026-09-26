package fitpolicy

// BuiltinPolicy returns the policy that reproduces today's shipped pin
// predicates exactly.
//
// It exists so step A can run in shadow and prove equivalence: with this policy
// installed, "the request requires at least one behaviour" must be true if and
// only if the shipped Go predicate pins the request. The composition lives here
// as data; the primitives it calls stay in Go (see request.go).
//
// Family-specific notes, all carried over from middleware/distributor.go:
//   - DeepSeek V4 pins logprobs, image parts and thinking-output requests; the
//     one class the pool serves faithfully is explicit thinking-off.
//   - kimi-k3 pins thinking-OFF (the pool reports the thinking-on prompt count
//     for it), a forcing tool_choice, a non-text response_format, a history
//     that does not begin with a user turn, and dynamic tools.
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
					{ID: "k3-thinking-off", When: "ThinkingDisabled()", Require: []string{BehaviorThinkingCounting}},
					{ID: "k3-tool-choice", When: "ToolChoiceForcesOfficial()", Require: []string{BehaviorToolsChoiceSemantics}},
					{ID: "k3-response-format", When: "ResponseFormatNotText()", Require: []string{BehaviorResponseFormatJSON}},
					{ID: "k3-history-not-user", When: "!HistoryBeginsWithUserTurn()", Require: []string{BehaviorHistoryAssistantFirst}},
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
