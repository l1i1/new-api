package fitpolicy

// Two-phase narrowing.
//
// A policy opinion does not pick a channel; it narrows the candidates the
// selector already computed. The order is fixed and deliberately conservative:
//
//	phase 1  candidates ∩ official-behaviour ∩ satisfied-marks   (preferred)
//	phase 2  candidates ∩ official-behaviour                     (today's result)
//	phase 3  nothing official remains                            (existing error)
//
// Phase 1 is empty in step A because no channel capability data exists yet, so
// the outcome is phase 2 — byte-for-byte today's hard pin. That is what makes
// this wiring safe to land before the capability table does: the marked branch
// can only ever *remove* candidates once real marks exist, and every removal is
// backed by a measured suite result.
//
// The narrowing never falls through to the ordinary priority pool. A request
// whose shape diverges from the official endpoint must either reach a channel
// that reproduces official behaviour or fail honestly; silently serving it from
// an unverified aggregator is the exact failure this layer exists to prevent.

// Narrowing is the outcome of applying a requirement to a candidate set.
type Narrowing struct {
	// Applied reports whether the requirement constrained the candidates. When
	// it is false the caller must keep its existing behaviour.
	Applied bool
	// Channels is the permitted candidate set when Applied is true. An empty
	// set means no candidate satisfies the requirement, which the caller must
	// surface through its existing selection error — never by widening the
	// candidate set.
	Channels []int
	// PermittedByMarks reports whether phase 1 produced the result (at least one
	// candidate was not ruled out by the required behaviours). It distinguishes
	// "narrowed by the marks" from "fell back to the official set" for
	// observability.
	//
	// The name avoids claiming verification on purpose. A conservative policy
	// reads an absent mark as "not supported", so phase 1 then means "every
	// mark was found and is in a satisfying state"; a permissive policy reads it
	// as "unknown", so phase 1 can keep a channel for which no measurement
	// exists at all. The boolean cannot tell those two apart and must not be
	// reported as if it could.
	PermittedByMarks bool
	// Satisfied carries the candidates that satisfied every required behaviour,
	// which is the work phase 1 already did. It travels in the result so the
	// caller can report which candidate carried the marks without asking the
	// same question — and running the same lookups — a second time.
	//
	// nil means the marks were never evaluated (no marks, or no predicate);
	// non-nil means they were, and an empty non-nil slice means every candidate
	// was ruled out. That distinction is how a caller tells "no measurement
	// exists" apart from "the measurement exists and failed".
	Satisfied []int
}

// Matched reports whether one candidate was among those that satisfied every
// required behaviour in this narrowing. It answers from the outcome instead of
// re-running the lookup, so a caller can attribute a result without paying for
// the marks a second time.
func (n Narrowing) Matched(channelID int) bool {
	for _, candidate := range n.Satisfied {
		if candidate == channelID {
			return true
		}
	}
	return false
}

// Narrow applies the two-phase narrowing.
//
// isOfficial reports whether a candidate is an official-behaviour channel
// (the family's official channel type; the official_fit_models allowlist is
// retired, so a declared family is its official type alone).
// satisfiesMarks reports whether a candidate satisfies every required
// behaviour; nil means "no capability data exists", which keeps phase 1 empty
// and the result equal to today's official pin.
//
// Both predicates receive a channel id; the caller owns the cache lookups.
func (r Requirement) Narrow(candidates []int, isOfficial func(int) bool, satisfiesMarks func(int) bool) Narrowing {
	if !r.HasOpinion() || r.Shadow || isOfficial == nil {
		return Narrowing{}
	}
	official := make([]int, 0, len(candidates))
	for _, candidate := range candidates {
		if isOfficial(candidate) {
			official = append(official, candidate)
		}
	}
	if len(official) == 0 {
		// Phase 3: keep the empty set so the caller reports its existing error.
		return Narrowing{Applied: true}
	}
	if satisfiesMarks != nil && len(r.Marks) > 0 {
		marked := make([]int, 0, len(official))
		for _, candidate := range official {
			if satisfiesMarks(candidate) {
				marked = append(marked, candidate)
			}
		}
		if len(marked) > 0 {
			return Narrowing{Applied: true, Channels: marked, PermittedByMarks: true, Satisfied: marked}
		}
		// Phase 2: no verified candidate, so use the official set unchanged. This
		// is where step A always lands. The empty Satisfied set is carried on
		// purpose: it says "the marks were evaluated and none carried them",
		// which is what separates a failed mark from data that never existed.
		return Narrowing{Applied: true, Channels: official, Satisfied: marked}
	}
	// Phase 2: no verified candidate, so use the official set unchanged. This is
	// where step A always lands.
	return Narrowing{Applied: true, Channels: official}
}
