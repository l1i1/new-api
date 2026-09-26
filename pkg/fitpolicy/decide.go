package fitpolicy

import (
	"fmt"

	"github.com/QuantumNous/new-api/officialfit"
	"github.com/expr-lang/expr/vm"
)

// Requirement is the immutable answer to "which behaviours does this request
// require". It is produced once per request and read by every selection
// attempt, so a hot reload cannot change what the in-flight request is
// demanding.
type Requirement struct {
	// Family is the canonical family id the request matched.
	Family string
	// Model is the model id the decision was made for.
	Model string
	// Marks are the required behaviours, in rule order and deduplicated.
	Marks []string
	// Behaviors maps each required mark to its class.
	Behaviors map[string]string
	// PolicyVersion and PolicyHash identify the snapshot that produced this
	// decision, so a shadow trace or capability report can be tied back to it.
	PolicyVersion int
	PolicyHash    string
	// EmptyMatchPolicy and UnknownMarkPolicy are carried so the consumer does
	// not have to re-read the snapshot (which may have been replaced).
	EmptyMatchPolicy  string
	UnknownMarkPolicy string
	// Shadow records that the producing policy was observe-only. The consumer
	// must not narrow candidates when this is true.
	Shadow bool
}

// NoOpinion reports that the policy has nothing to say about this request, so
// the caller must keep today's behaviour.
func (r Requirement) HasOpinion() bool {
	return len(r.Marks) > 0
}

// Decide evaluates the snapshot against one request.
//
// routeEnabled is the user's official_fit Route dimension; the caller passes it
// because the policy never reads user settings. A request the policy has no
// opinion about returns the zero Requirement, which keeps the legacy path in
// charge.
//
// Evaluation failures return no opinion rather than an error: a broken rule
// must degrade to today's routing, never fail a request.
func (s *Snapshot) Decide(model string, routeEnabled bool, view RequestView) Requirement {
	if !s.Enabled() || !routeEnabled {
		return Requirement{}
	}
	familyID := officialfit.FamilyOf(model)
	if familyID == "" {
		return Requirement{}
	}
	family, known := s.families[familyID]
	if !known {
		return Requirement{}
	}
	marks, ok := family.evaluate(view)
	if !ok || len(marks) == 0 {
		return Requirement{}
	}
	behaviors := make(map[string]string, len(marks))
	for _, mark := range marks {
		behaviors[mark] = family.behaviors[mark]
	}
	return Requirement{
		Family:            familyID,
		Model:             model,
		Marks:             marks,
		Behaviors:         behaviors,
		PolicyVersion:     s.version,
		PolicyHash:        s.hash,
		EmptyMatchPolicy:  family.emptyMatchPolicy,
		UnknownMarkPolicy: family.unknownMarkPolicy,
		Shadow:            s.shadow,
	}
}

// evaluate runs the family's rules and returns the union of the required
// behaviours. ok is false when any rule failed to evaluate, which the caller
// treats as "no opinion" so the legacy path stays in charge.
func (f *compiledFamily) evaluate(view RequestView) ([]string, bool) {
	if len(f.rules) == 0 {
		return nil, true
	}
	env := &evalEnv{View: view}
	var marks []string
	seen := make(map[string]struct{}, 8)
	for i := range f.rules {
		matched, err := runRule(f.rules[i].prog, env)
		if err != nil {
			return nil, false
		}
		if !matched {
			continue
		}
		for _, mark := range f.rules[i].require {
			if _, duplicate := seen[mark]; duplicate {
				continue
			}
			seen[mark] = struct{}{}
			marks = append(marks, mark)
		}
	}
	return marks, true
}

// runRule executes one compiled rule, converting a panic into an evaluation
// failure so a malformed policy can never take down a request.
func runRule(program *vm.Program, env *evalEnv) (matched bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			matched = false
			err = fmt.Errorf("fitpolicy: rule evaluation panic: %v", recovered)
		}
	}()
	output, runErr := vm.Run(program, env)
	if runErr != nil {
		return false, runErr
	}
	value, isBool := output.(bool)
	if !isBool {
		return false, fmt.Errorf("fitpolicy: rule returned %T, expected bool", output)
	}
	return value, nil
}
