package fitpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// evalEnv is the compile-time and run-time environment for a rule expression.
//
// It is a struct (not a map) on purpose: expr type-checks method calls against
// the declared type, so a typo like `thinkingDisable()` or a non-boolean rule
// body is rejected when the policy is written instead of silently evaluating
// false on a live request.
type evalEnv struct {
	View RequestView
}

func (e *evalEnv) ThinkingDisabled() bool              { return e.View.ThinkingDisabled() }
func (e *evalEnv) ReasoningEffortIs(value string) bool { return e.View.ReasoningEffortIs(value) }
func (e *evalEnv) LogprobsRequested() bool             { return e.View.LogprobsRequested() }
func (e *evalEnv) HasImagePart() bool                  { return e.View.HasImagePart() }
func (e *evalEnv) HistoryBeginsWithUserTurn() bool     { return e.View.HistoryBeginsWithUserTurn() }
func (e *evalEnv) MessagesCarryDynamicTools() bool     { return e.View.MessagesCarryDynamicTools() }
func (e *evalEnv) ResponseFormatNotText() bool         { return e.View.ResponseFormatNotText() }
func (e *evalEnv) ToolChoiceForcesOfficial() bool      { return e.View.ToolChoiceForcesOfficial() }
func (e *evalEnv) WholeFamily() bool                   { return e.View.WholeFamily() }

// compiledRule is one rule with its program already built.
type compiledRule struct {
	id      string
	prog    *vm.Program
	require []string
}

// compiledFamily is a family's rules ready to evaluate.
type compiledFamily struct {
	id                string
	rules             []compiledRule
	behaviors         map[string]string // behaviour name -> class
	unknownMarkPolicy string
	emptyMatchPolicy  string
}

// Snapshot is an immutable compiled policy. A request reads one snapshot for
// its whole lifetime, so a concurrent hot reload cannot change the rules
// mid-request.
type Snapshot struct {
	version  int
	enabled  bool
	shadow   bool
	baseline string
	families map[string]*compiledFamily
	hash     string
}

// Version reports the policy version this snapshot was compiled from.
func (s *Snapshot) Version() int {
	if s == nil {
		return 0
	}
	return s.version
}

// Shadow reports whether the policy is in observe-only mode.
func (s *Snapshot) Shadow() bool {
	return s == nil || s.shadow
}

// Enabled reports whether the policy is active at all.
func (s *Snapshot) Enabled() bool {
	return s != nil && s.enabled
}

// Hash is the content hash of the policy this snapshot was compiled from, used
// to bind capability reports and shadow traces to the policy that produced
// them.
func (s *Snapshot) Hash() string {
	if s == nil {
		return ""
	}
	return s.hash
}

// Baseline reports the official behaviour reference this snapshot's
// measurements must have been taken against. An empty value means the check is
// not applied.
func (s *Snapshot) Baseline() string {
	if s == nil {
		return ""
	}
	return s.baseline
}

// Compile validates a policy and builds its expression programs. It is the same
// call the write path uses before the database commit, so an uncompilable rule
// is rejected by the author rather than at request time.
func Compile(policy Policy) (*Snapshot, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	snapshot := &Snapshot{
		version:  policy.Version,
		enabled:  policy.Enabled,
		shadow:   policy.Shadow,
		baseline: strings.TrimSpace(policy.Baseline),
		families: make(map[string]*compiledFamily, len(policy.Families)),
	}
	for _, family := range policy.Families {
		compiled, err := compileFamily(family)
		if err != nil {
			return nil, err
		}
		snapshot.families[compiled.id] = compiled
	}
	snapshot.hash = hashPolicy(policy)
	return snapshot, nil
}

func compileFamily(family FamilyPolicy) (*compiledFamily, error) {
	compiled := &compiledFamily{
		id:                family.ID,
		behaviors:         make(map[string]string, len(family.Behaviors)),
		unknownMarkPolicy: family.UnknownMarkPolicyOrDefault(),
		emptyMatchPolicy:  family.EmptyMatchPolicyOrDefault(),
	}
	for name, behavior := range family.Behaviors {
		compiled.behaviors[name] = behavior.Class
	}
	compiled.rules = make([]compiledRule, 0, len(family.Rules))
	for _, rule := range family.Rules {
		program, err := expr.Compile(rule.When, expr.Env(&evalEnv{}), expr.AsBool())
		if err != nil {
			return nil, fmt.Errorf("%w: family %q rule %q: %v", ErrPolicyInvalid, family.ID, rule.ID, err)
		}
		compiled.rules = append(compiled.rules, compiledRule{
			id:      rule.ID,
			prog:    program,
			require: append([]string(nil), rule.Require...),
		})
	}
	return compiled, nil
}

// hashPolicy hashes the JSON encoding of the policy. encoding/json emits struct
// fields in declaration order and map keys sorted, so equal policies hash
// equally without a hand-written canonical form.
func hashPolicy(policy Policy) string {
	encoded, err := json.Marshal(policy)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
