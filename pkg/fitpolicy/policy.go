package fitpolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Policy is the versioned rule set stored at options["official_fit.policy"].
//
// The shape is intentionally small: a family owns a list of rules, each rule
// names the behaviours a matching request requires. Which channel can serve
// those behaviours is a separate concern (the channel capability marks), so the
// policy never mentions channels.
type Policy struct {
	Version  int            `json:"version"`
	Enabled  bool           `json:"enabled"`
	Shadow   bool           `json:"shadow"`
	Families []FamilyPolicy `json:"families"`
}

// FamilyPolicy is one family's rules.
type FamilyPolicy struct {
	// ID must be a canonical family id registered in officialfit. The policy
	// cannot invent a family: family knowledge (model prefixes, official
	// channel type, wire shape) stays in code.
	ID string `json:"id"`
	// Rules are evaluated in order; the union of every matching rule's Require
	// list is the request's required-behaviour set.
	Rules []Rule `json:"rules"`
	// Behaviors declares the class of every behaviour a rule may require.
	// A rule referencing an undeclared behaviour is rejected at write time.
	Behaviors map[string]Behavior `json:"behaviors"`
	// UnknownMarkPolicy decides how a channel with no mark for a required
	// behaviour is treated: "conservative" treats the missing mark as
	// "unsupported" (a mark is a promise), "permissive" treats it as unknown.
	UnknownMarkPolicy string `json:"unknown_mark_policy"`
	// EmptyMatchPolicy decides what happens when requirement narrowing leaves no
	// candidate. v1 only allows legacy_hard_pin_then_existing_error.
	EmptyMatchPolicy string `json:"empty_match_policy"`
}

// Rule requires Behaviours when When evaluates true.
type Rule struct {
	ID      string   `json:"id"`
	When    string   `json:"when"`
	Require []string `json:"require"`
}

// Behavior classifies what a required behaviour means when it cannot be met.
type Behavior struct {
	// Class is one of:
	//   verdict    - correctness: failing it is worse than failing the request;
	//   capability - another channel can satisfy it, so switching is right;
	//   courtesy   - degradable, annotate instead of failing.
	Class string `json:"class"`
}

const (
	// ClassVerdict marks a correctness behaviour.
	ClassVerdict = "verdict"
	// ClassCapability marks a behaviour a different channel can satisfy.
	ClassCapability = "capability"
	// ClassCourtesy marks a degradable behaviour.
	ClassCourtesy = "courtesy"
)

const (
	// UnknownMarkConservative treats a missing mark as "not supported". A mark
	// is a promise, so an unmarked channel is not assumed to fit.
	UnknownMarkConservative = "conservative"
	// UnknownMarkPermissive treats a missing mark as unknown rather than false.
	UnknownMarkPermissive = "permissive"
)

const (
	// EmptyMatchLegacyHardPin falls back to today's official-behaviour candidate
	// set and, when that is empty too, to the existing selection error. It never
	// falls back to the ordinary priority pool.
	EmptyMatchLegacyHardPin = "legacy_hard_pin_then_existing_error"
)

// MaxPolicyFamilies bounds a policy so a malformed write cannot make the
// per-request evaluation unbounded.
const MaxPolicyFamilies = 16

// MaxRulesPerFamily bounds the rule count per family.
const MaxRulesPerFamily = 64

// ErrPolicyInvalid wraps every write-time rejection so callers can return it to
// the author without leaking internals.
var ErrPolicyInvalid = errors.New("fitpolicy: invalid policy")

// ParsePolicy decodes and statically validates a policy document. It does not
// compile expressions; call Compile for that. Both run before the database
// commit, so a rejected policy never reaches the in-memory snapshot.
func ParsePolicy(raw []byte) (Policy, error) {
	var policy Policy
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("%w: %v", ErrPolicyInvalid, err)
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

// Validate checks the static shape: version, family ids, rule ids, behaviour
// references and the two policy enums.
func (p Policy) Validate() error {
	if p.Version <= 0 {
		return fmt.Errorf("%w: version must be a positive integer", ErrPolicyInvalid)
	}
	if len(p.Families) > MaxPolicyFamilies {
		return fmt.Errorf("%w: at most %d families", ErrPolicyInvalid, MaxPolicyFamilies)
	}
	seenFamily := make(map[string]struct{}, len(p.Families))
	for _, family := range p.Families {
		if err := family.validate(); err != nil {
			return err
		}
		if _, duplicate := seenFamily[family.ID]; duplicate {
			return fmt.Errorf("%w: duplicate family %q", ErrPolicyInvalid, family.ID)
		}
		seenFamily[family.ID] = struct{}{}
	}
	return nil
}

func (f FamilyPolicy) validate() error {
	id := strings.TrimSpace(f.ID)
	if id == "" {
		return fmt.Errorf("%w: family id is required", ErrPolicyInvalid)
	}
	if !IsRegisteredFamily(id) {
		return fmt.Errorf("%w: family %q is not registered", ErrPolicyInvalid, id)
	}
	if switchValue := strings.TrimSpace(f.UnknownMarkPolicy); switchValue != "" {
		if switchValue != UnknownMarkConservative && switchValue != UnknownMarkPermissive {
			return fmt.Errorf("%w: family %q unknown_mark_policy %q is not supported", ErrPolicyInvalid, id, switchValue)
		}
	}
	if switchValue := strings.TrimSpace(f.EmptyMatchPolicy); switchValue != "" {
		if switchValue != EmptyMatchLegacyHardPin {
			return fmt.Errorf("%w: family %q empty_match_policy %q is not supported in v1", ErrPolicyInvalid, id, switchValue)
		}
	}
	if len(f.Rules) > MaxRulesPerFamily {
		return fmt.Errorf("%w: family %q has more than %d rules", ErrPolicyInvalid, id, MaxRulesPerFamily)
	}
	behaviors := make(map[string]struct{}, len(f.Behaviors))
	for name, behavior := range f.Behaviors {
		behaviorName := strings.TrimSpace(name)
		if behaviorName == "" {
			return fmt.Errorf("%w: family %q has an empty behaviour name", ErrPolicyInvalid, id)
		}
		switch strings.TrimSpace(behavior.Class) {
		case ClassVerdict, ClassCapability, ClassCourtesy:
		default:
			return fmt.Errorf("%w: family %q behaviour %q has invalid class %q", ErrPolicyInvalid, id, behaviorName, behavior.Class)
		}
		behaviors[behaviorName] = struct{}{}
	}
	seenRule := make(map[string]struct{}, len(f.Rules))
	for _, rule := range f.Rules {
		ruleID := strings.TrimSpace(rule.ID)
		if ruleID == "" {
			return fmt.Errorf("%w: family %q has a rule without an id", ErrPolicyInvalid, id)
		}
		if _, duplicate := seenRule[ruleID]; duplicate {
			return fmt.Errorf("%w: family %q has duplicate rule %q", ErrPolicyInvalid, id, ruleID)
		}
		seenRule[ruleID] = struct{}{}
		if strings.TrimSpace(rule.When) == "" {
			return fmt.Errorf("%w: family %q rule %q has an empty when", ErrPolicyInvalid, id, ruleID)
		}
		if len(rule.Require) == 0 {
			return fmt.Errorf("%w: family %q rule %q requires nothing", ErrPolicyInvalid, id, ruleID)
		}
		for _, required := range rule.Require {
			name := strings.TrimSpace(required)
			if _, declared := behaviors[name]; !declared {
				return fmt.Errorf("%w: family %q rule %q requires undeclared behaviour %q", ErrPolicyInvalid, id, ruleID, required)
			}
		}
	}
	return nil
}

// UnknownMarkPolicyOrDefault returns the family's policy or the v1 default.
func (f FamilyPolicy) UnknownMarkPolicyOrDefault() string {
	if value := strings.TrimSpace(f.UnknownMarkPolicy); value != "" {
		return value
	}
	return UnknownMarkConservative
}

// EmptyMatchPolicyOrDefault returns the family's policy or the v1 default.
func (f FamilyPolicy) EmptyMatchPolicyOrDefault() string {
	if value := strings.TrimSpace(f.EmptyMatchPolicy); value != "" {
		return value
	}
	return EmptyMatchLegacyHardPin
}
