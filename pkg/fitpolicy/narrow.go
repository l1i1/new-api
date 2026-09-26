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
	// MatchedMarks reports whether phase 1 produced the result (at least one
	// candidate satisfied every required behaviour). It distinguishes "narrowed
	// to verified channels" from "fell back to the official set" for
	// observability.
	MatchedMarks bool
}

// Narrow applies the two-phase narrowing.
//
// isOfficial reports whether a candidate is an official-behaviour channel
// (official_fit_models allowlist or the family's official channel type).
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
			return Narrowing{Applied: true, Channels: marked, MatchedMarks: true}
		}
	}
	// Phase 2: no verified candidate, so use the official set unchanged. This is
	// where step A always lands.
	return Narrowing{Applied: true, Channels: official}
}
