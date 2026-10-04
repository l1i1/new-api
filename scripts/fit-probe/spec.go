package main

// Probe dimensions and the doc-derived official expectation table.
//
// The dimension list is NOT invented here: it is the set of behaviours the live
// fit policy can require. The authoritative source is the compiled built-in
// policy in pkg/fitpolicy (pkg/fitpolicy/builtin.go); loadPolicy() reads the
// live document from the admin API when it is reachable and falls back to
// fitpolicy.BuiltinPolicy(). Every expectation carries file:line evidence; a
// dimension with no evidence is marked "undefined" and can never produce a
// consistency verdict (see classify in probe.go).

import (
	"strings"

	"github.com/QuantumNous/new-api/pkg/fitpolicy"
)

// Expected official verdicts for the doc-derived baseline.
const (
	expectSupported   = "supported"
	expectUnsupported = "unsupported"
	expectUndefined   = "undefined"
)

// Discriminators describe what a minimal probe can actually compare.
const (
	discPromptTokens = "prompt_tokens" // equal prompt_tokens on the identical request
	discStructure    = "structure"     // a structural field (logprobs/tool_calls/...)
	discAcceptance   = "acceptance"    // only "the shape was accepted"
)

// Expectation is one row of the doc-derived official baseline table.
type Expectation struct {
	Family   string `json:"family"`
	Behavior string `json:"behavior"`
	// Official is what the repository's own material says the official endpoint
	// does for the minimal probe of this dimension.
	Official string `json:"official_expectation"`
	// Discriminators are the signals a probe can compare. A dimension whose only
	// discriminator is prompt_tokens cannot be decided without a measured
	// official baseline.
	Discriminators []string `json:"discriminators"`
	// BasisKind is "doc" for an explicit statement and "mechanism" for an
	// inference from the documented purpose of the official-fit pin. It is
	// reported so a reader can weigh the evidence.
	BasisKind string `json:"basis_kind"`
	// Basis is file:line evidence inside this repository.
	Basis []string `json:"basis"`
}

// officialExpectations is the decidable expectation table.
//
// Every entry below is traceable to the repository. Nothing is filled in from
// general knowledge: pkg/fitpolicy/builtin.go names the behaviour and the rule
// that requires it, docs/official-fit-mode.md records the live-probed official
// behaviour and the measured divergence of the aggregator pool, and
// relay/helper/valid_request.go records the request contract the official
// endpoint was calibrated against.
var officialExpectations = []Expectation{
	{
		Family:         "deepseek-v4",
		Behavior:       fitpolicy.BehaviorLogprobsDualPath,
		Official:       expectSupported,
		Discriminators: []string{discStructure, discPromptTokens},
		BasisKind:      "doc",
		Basis: []string{
			"pkg/fitpolicy/builtin.go:37",
			"docs/official-fit-mode.md:57",
			"docs/official-fit-mode.md:31",
			"relay/helper/valid_request.go:411",
		},
	},
	{
		Family:         "deepseek-v4",
		Behavior:       fitpolicy.BehaviorImageParts,
		Official:       expectSupported,
		Discriminators: []string{discAcceptance},
		BasisKind:      "doc",
		Basis: []string{
			"pkg/fitpolicy/builtin.go:38",
			"docs/official-fit-mode.md:58",
			"relay/helper/valid_request.go:742",
		},
	},
	{
		Family:         "deepseek-v4",
		Behavior:       fitpolicy.BehaviorThinkingCounting,
		Official:       expectSupported,
		Discriminators: []string{discStructure, discPromptTokens},
		BasisKind:      "doc",
		Basis: []string{
			"pkg/fitpolicy/builtin.go:39",
			"docs/official-fit-mode.md:59",
			"pkg/fitpolicy/request.go:53",
		},
	},
	{
		Family:         "kimi-k3",
		Behavior:       fitpolicy.BehaviorThinkingCounting,
		Official:       expectSupported,
		Discriminators: []string{discPromptTokens},
		BasisKind:      "doc",
		Basis: []string{
			"pkg/fitpolicy/builtin.go:52",
			"docs/official-fit-mode.md:66",
		},
	},
	{
		Family:         "kimi-k3",
		Behavior:       fitpolicy.BehaviorToolsChoiceSemantics,
		Official:       expectSupported,
		Discriminators: []string{discStructure, discPromptTokens},
		BasisKind:      "doc",
		Basis: []string{
			"pkg/fitpolicy/builtin.go:53",
			"docs/official-fit-mode.md:68",
			"relay/helper/valid_request.go:1234",
		},
	},
	{
		Family:         "kimi-k3",
		Behavior:       fitpolicy.BehaviorResponseFormatJSON,
		Official:       expectSupported,
		Discriminators: []string{discStructure, discPromptTokens},
		BasisKind:      "doc",
		Basis: []string{
			"pkg/fitpolicy/builtin.go:54",
			"docs/official-fit-mode.md:69",
			"relay/helper/valid_request.go:1100",
		},
	},
	{
		Family:         "kimi-k3",
		Behavior:       fitpolicy.BehaviorHistoryAssistantFirst,
		Official:       expectSupported,
		Discriminators: []string{discPromptTokens},
		BasisKind:      "doc",
		Basis: []string{
			"pkg/fitpolicy/builtin.go:55",
			"docs/official-fit-mode.md:70",
		},
	},
	{
		Family:         "kimi-k3",
		Behavior:       fitpolicy.BehaviorToolsDynamicNames,
		Official:       expectSupported,
		Discriminators: []string{discPromptTokens},
		BasisKind:      "doc",
		Basis: []string{
			"pkg/fitpolicy/builtin.go:56",
			"docs/official-fit-mode.md:71",
			"relay/helper/valid_request.go:1296",
		},
	},
	{
		Family:         "glm-5.3",
		Behavior:       fitpolicy.BehaviorFamilyWhole,
		Official:       expectSupported,
		Discriminators: []string{discAcceptance},
		BasisKind:      "mechanism",
		Basis: []string{
			"pkg/fitpolicy/builtin.go:71",
			"docs/official-fit-mode.md:74",
			"officialfit/officialfit.go:100",
		},
	},
}

// expectationKey indexes the table.
func expectationKey(family, behavior string) string {
	return strings.ToLower(strings.TrimSpace(family)) + "\x00" + strings.ToLower(strings.TrimSpace(behavior))
}

func expectationIndex() map[string]Expectation {
	index := make(map[string]Expectation, len(officialExpectations))
	for _, entry := range officialExpectations {
		index[expectationKey(entry.Family, entry.Behavior)] = entry
	}
	return index
}

// requiredBehaviors returns, per family id, the union of every rule's require
// list in the given policy. This is exactly what the Decide path can demand.
func requiredBehaviors(policy fitpolicy.Policy) map[string][]string {
	out := make(map[string][]string, len(policy.Families))
	for _, family := range policy.Families {
		seen := map[string]struct{}{}
		ordered := make([]string, 0)
		for _, rule := range family.Rules {
			for _, behavior := range rule.Require {
				name := strings.ToLower(strings.TrimSpace(behavior))
				if name == "" {
					continue
				}
				if _, duplicate := seen[name]; duplicate {
					continue
				}
				seen[name] = struct{}{}
				ordered = append(ordered, name)
			}
		}
		out[family.ID] = ordered
	}
	return out
}
