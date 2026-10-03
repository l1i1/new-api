package fitpolicy

// Narrowing order.
//
// A policy opinion does not pick a channel; it narrows the candidates the
// selector already computed. The order (2026-10-03, per the operator's
// statement of the feature's goal — "when official fitting is achievable,
// match channels by priority; if the channel does not support the specific
// feature, step to the next one by priority"):
//
//	phase 1  candidates ∩ satisfied-marks                          (preferred)
//	phase 2  candidates ∩ official-behaviour                        (fallback)
//	phase 3  neither leaves anything                               (existing error)
//
// Phase 1 judges every candidate by the behaviours THIS request requires, not
// only the officials: the caller's selector then decides by priority among
// them, so the highest-priority channel that measurably handles this request
// wins and the next one steps in when it does not. Phase 2 covers the case no
// candidate carries the required behaviours: the family's official-behaviour
// set (official channel type plus, for a measured family, the admission
// battery) serves as the fallback, and an empty fallback keeps the empty set so
// the caller reports its existing selection error rather than silently serving
// from the ordinary pool.
//
// With no capability data — the state before the first suite run, and the state
// a node is in when its index is unavailable — phase 1 is empty and the result
// is the official set, which is what keeps this layer no worse than the hard
// pin it replaced.

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
// isOfficial reports whether a candidate belongs to the family's
// official-behaviour set (the official channel type, plus the admission
// battery for a measured family).
// satisfiesMarks reports whether a candidate carries fresh passing marks for
// every behaviour this request requires; nil means "no capability data
// exists", which keeps the per-request phase empty and the result equal to the
// official set.
//
// Both predicates receive a channel id; the caller owns the cache lookups.
//
// Order (2026-10-03, per operator's statement of the feature's goal): the
// request's own required behaviours are the first gate, and every candidate is
// judged by them — not just the official set. Priority then decides among the
// candidates that carry them, which is what makes the higher-priority channel
// win when it measurably handles this request, and the next channel by
// priority step in when it does not. Only when no candidate carries the
// required behaviours does the official-behaviour set take over as the
// fallback; an empty fallback keeps the empty set so the caller reports its
// existing selection error rather than serving from the ordinary pool.
func (r Requirement) Narrow(candidates []int, isOfficial func(int) bool, satisfiesMarks func(int) bool) Narrowing {
	if !r.HasOpinion() || r.Shadow || isOfficial == nil {
		return Narrowing{}
	}
	if satisfiesMarks != nil && len(r.Marks) > 0 {
		marked := make([]int, 0, len(candidates))
		for _, candidate := range candidates {
			if satisfiesMarks(candidate) {
				marked = append(marked, candidate)
			}
		}
		if len(marked) > 0 {
			return Narrowing{Applied: true, Channels: marked, PermittedByMarks: true, Satisfied: marked}
		}
		// No candidate carries every required behaviour, so the per-request
		// gate cannot decide. Fall back to the official-behaviour set. The
		// empty Satisfied slice is carried on purpose: it says "the marks were
		// evaluated and none carried them", which is what separates a failed
		// mark from data that never existed.
		return Narrowing{Applied: true, Channels: officialCandidates(candidates, isOfficial), Satisfied: marked}
	}
	return Narrowing{Applied: true, Channels: officialCandidates(candidates, isOfficial)}
}

// officialCandidates keeps the candidates isOfficial admits, preserving the
// caller's order (the caller's selector decides by priority among them).
func officialCandidates(candidates []int, isOfficial func(int) bool) []int {
	official := make([]int, 0, len(candidates))
	for _, candidate := range candidates {
		if isOfficial(candidate) {
			official = append(official, candidate)
		}
	}
	return official
}
