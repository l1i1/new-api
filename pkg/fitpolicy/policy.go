package fitpolicy

import (
	"bytes"
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
	Version int  `json:"version"`
	Enabled bool `json:"enabled"`
	Shadow  bool `json:"shadow"`
	// Baseline identifies the official behaviour reference the measurements were
	// taken against. Bumping it invalidates every suite result bound to the old
	// value, which is how a change in the official upstream stops old
	// measurements from continuing to vouch for a channel. It is free-form and
	// optional: an empty value simply skips the check.
	Baseline string         `json:"baseline,omitempty"`
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
	//
	// The map is required by the schema and carried into the decision, but v1
	// reads only its keys: a class is accepted, validated and reported, and no
	// consumer acts on it yet. It exists so the classification is authored and
	// reviewed now rather than invented later, and so a future consumer can read
	// it out of a document that already carries it.
	Behaviors map[string]Behavior `json:"behaviors"`
	// UnknownMarkPolicy decides how a channel with no mark for a required
	// behaviour is treated: "conservative" treats the missing mark as
	// "unsupported" (a mark is a promise), "permissive" treats it as unknown.
	//
	// Both values are in force. Under "permissive" a channel with no measurement
	// at all is not ruled out, so the narrowing's PermittedByMarks must not be
	// read as "verified" for that policy.
	UnknownMarkPolicy string `json:"unknown_mark_policy"`
	// AdmissionSource selects how a channel qualifies as official-behaving for
	// this family. "declared" (the default, and the only value before this
	// field existed) answers from the channel's own declaration: the family's
	// official channel type or its official_fit_models allowlist. "measured"
	// replaces the allowlist with the admission battery below — a channel
	// qualifies only by carrying fresh, passing marks for every battery
	// behaviour, so admission stops being an operator's memory and becomes the
	// suite's output. The family's official channel type remains admitted by
	// identity under both values: it is the baseline the measurements are taken
	// against, not a thing the battery needs to prove.
	//
	// The switch is per family on purpose: kimi-k3 can move to measured while
	// deepseek-v4 stays declared, and the rollback is a document edit away
	// (epoch reload, seconds) rather than a release.
	AdmissionSource string `json:"admission_source,omitempty"`
	// AdmissionBattery is the set of behaviours that constitute official
	// equivalence for this family. It is only read when AdmissionSource is
	// "measured"; when the source is "declared" it is carried so the selection
	// path can log the admission shadow — what the measured set would have been
	// — which is the data the equivalence window runs on.
	//
	// Every entry must be declared in Behaviors: the battery draws from the same
	// vocabulary the rules do, so a typo cannot quietly shrink the battery. The
	// battery is always evaluated conservatively (an unknown or stale mark means
	// "not admitted"), regardless of UnknownMarkPolicy: admission is a promise,
	// and the permissive reading exists to keep phase-1 narrowing from ruling
	// out channels nobody has measured — never to admit an unmeasured channel.
	AdmissionBattery []string `json:"admission_battery,omitempty"`
	// EmptyMatchPolicy decides what happens when requirement narrowing leaves no
	// candidate.
	//
	// v1 only allows legacy_hard_pin_then_existing_error, which is also the only
	// behaviour implemented (see pkg/fitpolicy/narrow.go): phase 2 falls back to
	// the official set and an empty official set keeps the caller's existing
	// selection error. The field is validated so a document cannot ask for a
	// fallback the code does not have; it is not yet read at decision time
	// because there is exactly one legal value.
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
	//
	// All three are accepted and validated; every behaviour the shipped document
	// declares is verdict. Be aware that no consumer branches on the class yet:
	// writing "capability" or "courtesy" into a document does not, by itself,
	// change how a failure is handled — the narrowing treats every required
	// behaviour the same way. See docs/fitpolicy-tech-spec.md §6.1.
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

const (
	// AdmissionSourceDeclared answers official behaviour from the channel's own
	// declaration: the family's official channel type or the
	// official_fit_models allowlist. It is the default, the only behaviour
	// before the field existed, and the rollback target for the other value.
	AdmissionSourceDeclared = "declared"
	// AdmissionSourceMeasured answers official behaviour from the admission
	// battery: fresh passing marks for every battery behaviour, plus the
	// family's official channel type by identity.
	AdmissionSourceMeasured = "measured"
)

// MaxAdmissionBatteryBehaviors bounds a family's battery. It matches
// MaxRulesPerFamily: a battery is a list of behaviour identifiers the way a
// rule list is, and both are hand-authored and reviewed.
const MaxAdmissionBatteryBehaviors = 64

// MaxPolicyFamilies bounds a policy so a malformed write cannot make the
// per-request evaluation unbounded.
const MaxPolicyFamilies = 16

// MaxRulesPerFamily bounds the rule count per family.
const MaxRulesPerFamily = 64

// MaxPolicyDocumentBytes bounds a policy document at the one entry every path
// shares.
//
// The structural limits above are checked *after* the document has been decoded,
// so on their own they do not bound the work a write can cause: a body with a
// million families is fully decoded — and, on the validation endpoint, compiled
// into expr programs — before the first family is counted. A byte bound in front
// of the decoder is what actually caps that, and it belongs here rather than in
// the HTTP handler, so that the root-only option write, the loader and any later
// caller inherit it instead of only the one endpoint that was audited.
//
// 1 MiB is not a practical restriction on the content: the shipped default is a
// few kilobytes, and the structural limits can express at most 16 families of 64
// hand-written rules, which is two orders of magnitude below this. It only stops
// a body that is not a rule set at all.
const MaxPolicyDocumentBytes = 1 << 20

// ErrPolicyInvalid wraps every write-time rejection so callers can return it to
// the author without leaking internals.
var ErrPolicyInvalid = errors.New("fitpolicy: invalid policy")

// ParsePolicy decodes and statically validates a policy document. It does not
// compile expressions; call Compile for that. Both run before the database
// commit, so a rejected policy never reaches the in-memory snapshot.
func ParsePolicy(raw []byte) (Policy, error) {
	if len(raw) > MaxPolicyDocumentBytes {
		return Policy{}, fmt.Errorf("%w: the document is %d bytes, over the %d byte limit",
			ErrPolicyInvalid, len(raw), MaxPolicyDocumentBytes)
	}
	var policy Policy
	// bytes.NewReader over the caller's slice: strings.NewReader(string(raw))
	// copied the whole document a second time before the decoder saw it, which is
	// exactly the amplification a size bound is meant to keep small.
	decoder := json.NewDecoder(bytes.NewReader(raw))
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
	if err := f.validateAdmission(id, behaviors); err != nil {
		return err
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

// validateAdmission checks the admission pair. The battery is validated under
// both sources — a declared family with a battery is the shadow-admission
// window's configuration, and its entries must obey the same rules then.
func (f FamilyPolicy) validateAdmission(id string, behaviors map[string]struct{}) error {
	if source := strings.TrimSpace(f.AdmissionSource); source != "" {
		if source != AdmissionSourceDeclared && source != AdmissionSourceMeasured {
			return fmt.Errorf("%w: family %q admission_source %q is not supported", ErrPolicyInvalid, id, source)
		}
	}
	if len(f.AdmissionBattery) > MaxAdmissionBatteryBehaviors {
		return fmt.Errorf("%w: family %q has more than %d admission battery behaviours", ErrPolicyInvalid, id, MaxAdmissionBatteryBehaviors)
	}
	seen := make(map[string]struct{}, len(f.AdmissionBattery))
	for _, entry := range f.AdmissionBattery {
		name := strings.TrimSpace(entry)
		if name == "" {
			return fmt.Errorf("%w: family %q has an empty admission battery behaviour", ErrPolicyInvalid, id)
		}
		if _, declared := behaviors[name]; !declared {
			return fmt.Errorf("%w: family %q admission battery lists undeclared behaviour %q", ErrPolicyInvalid, id, entry)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("%w: family %q admission battery repeats behaviour %q", ErrPolicyInvalid, id, entry)
		}
		seen[name] = struct{}{}
	}
	if f.AdmissionSourceOrDefault() == AdmissionSourceMeasured && len(f.AdmissionBattery) == 0 {
		return fmt.Errorf("%w: family %q sets admission_source to measured without an admission battery", ErrPolicyInvalid, id)
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

// AdmissionSourceOrDefault returns the family's admission source or the
// default, which is the behaviour that predates the field: admission from the
// channel's own declaration.
func (f FamilyPolicy) AdmissionSourceOrDefault() string {
	if value := strings.TrimSpace(f.AdmissionSource); value != "" {
		return value
	}
	return AdmissionSourceDeclared
}

// AdmissionBatteryOrDefault returns the family's battery, normalized: trimmed,
// in declaration order, and nil when absent. Normalizing here rather than at
// each consumer keeps the compiled snapshot and the divergence report reading
// the same list.
func (f FamilyPolicy) AdmissionBatteryOrDefault() []string {
	if len(f.AdmissionBattery) == 0 {
		return nil
	}
	battery := make([]string, 0, len(f.AdmissionBattery))
	for _, entry := range f.AdmissionBattery {
		if name := strings.TrimSpace(entry); name != "" {
			battery = append(battery, name)
		}
	}
	if len(battery) == 0 {
		return nil
	}
	return battery
}
