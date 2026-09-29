package model

import (
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/gin-gonic/gin"
)

// fitNarrowingLogBurst bounds how many narrowing decisions are logged up front.
// The first few are what a diagnosis needs; a systematic fault should cost a
// bounded number of lines rather than one per request. After the burst the site
// refills at fitNarrowingLogInterval, so a node that starts failing later — or a
// fault that comes back after being fixed — is still reported instead of finding
// the quota spent for the life of the process.
const (
	fitNarrowingLogBurst    = 40
	fitNarrowingLogInterval = 30 * time.Second
)

var fitNarrowingLog = common.NewLogBudget(fitNarrowingLogBurst, fitNarrowingLogInterval)

// Fit-policy narrowing for the channel selector.
//
// The requirement travels in the gin context and is read here, next to the
// candidate set it constrains. A nil filter — or a filter whose requirement has
// no opinion — leaves the legacy official pin untouched, so a request the policy
// says nothing about behaves exactly as it did before this layer existed.
//
// MarkSatisfied is built from the capability index, which fails open: with no
// capability rows every lookup misses, so phase 1 stays empty and the result is
// phase 2 — the official set, which is the legacy pin's answer. That is what
// keeps the layer safe before any measurement exists, and it is why the index is
// built in FitChannelFilterForRequest rather than lazily from inside the selector.

// FitChannelFilter carries one request's fit-policy narrowing into the selector.
type FitChannelFilter struct {
	// Requirement is the immutable policy opinion for this request.
	Requirement fitpolicy.Requirement
	// MarkSatisfied reports whether a channel is not ruled out by every required
	// behaviour. It is nil only for a hand-built filter: the one
	// FitChannelFilterForRequest produces always reads the capability index, which
	// answers "not verified" while it is empty or unavailable.
	MarkSatisfied func(channelID int) bool
}

// FitChannelFilterForRequest returns the filter attached to this request, or nil
// when the policy has no opinion about it.
func FitChannelFilterForRequest(c *gin.Context) *FitChannelFilter {
	if c == nil {
		return nil
	}
	requirement, ok := common.GetContextKeyType[fitpolicy.Requirement](c, constant.ContextKeyFitRequirement)
	if !ok || !requirement.HasOpinion() {
		return nil
	}
	// Build the capability index here, while the request is being prepared.
	//
	// This is the only place on the request path that may build it: the selector
	// reads marks while holding channelSyncLock, and a first-use build there is a
	// full table scan with the channel cache held — which does not deadlock, but
	// takes every channel write down for its duration, and would be repeated for
	// every mark of every candidate whenever the build fails. The construction
	// happens outside the lock (the selector is called with the filter already
	// built), so the scan is paid here and selection only ever reads.
	ensureFitCapabilityIndexBuilt()
	// tokeness-fitpolicy:begin （上游 merge 后请保留；见 docs/fitpolicy-tech-spec.md）
	// The mark lookup is the capability index. With no capability rows every
	// lookup misses, so phase 1 stays empty and selection falls back to the
	// official set — today's behaviour, which is what the spec requires when
	// capability data is absent.
	//
	// The bindings come from the requirement, which carries the policy identity
	// of the snapshot that produced it. Evaluating them per request is what makes
	// a rule change invalidate old measurements without waiting for a cache
	// rebuild, and what keeps a retry bound to the policy it started under.
	return &FitChannelFilter{
		Requirement: requirement,
		MarkSatisfied: func(channelID int) bool {
			return ChannelSatisfiesFitMarks(channelID, FitMarkRequirement{
				Model:             requirement.Model,
				Marks:             requirement.Marks,
				PermissiveUnknown: requirement.UnknownMarkPolicy == fitpolicy.UnknownMarkPermissive,
				PolicyHash:        requirement.PolicyHash,
				BaselineHash:      requirement.BaselineHash,
			})
		},
	}
	// tokeness-fitpolicy:end
}

// hasOpinion reports whether the policy constrains this request. It is what
// keeps the selection metadata fast path disabled: that shortcut picks from the
// whole model's candidate set, so using it after the narrowing removed
// candidates would silently undo the narrowing.
func (f *FitChannelFilter) hasOpinion() bool {
	return f != nil && f.Requirement.HasOpinion() && !f.Requirement.Shadow
}

// narrowChannels applies the two-phase narrowing and reports whether the policy
// had an opinion. Caller must hold channelSyncLock (read lock).
//
// The official predicate is the same cache lookup the legacy pin uses, so phase
// 2 reproduces the legacy result exactly rather than approximating it.
func (f *FitChannelFilter) narrowChannels(channels []int, model string) ([]int, bool) {
	if f == nil {
		return channels, false
	}
	result := f.Requirement.Narrow(
		channels,
		func(channelID int) bool { return officialFitChannelMatchesLocked(channelID, model) },
		f.MarkSatisfied,
	)
	reportFitNarrowing(model, channels, result)
	if !result.Applied {
		return channels, false
	}
	return result.Channels, true
}

// reportFitNarrowing records how one narrowing decision came out. An empty result
// is indistinguishable from "the pool was empty" in the selection error, and the
// inputs below are what tell the two apart: whether the policy narrowed at all,
// which candidates it was offered, which of them carried the marks, and what it
// kept. The candidate ids are what distinguish "the marked channel was never a
// candidate" from "it was a candidate and still failed", which have completely
// different causes.
//
// Which candidates carried the marks is read out of result.Satisfied rather than
// asked again: that is the same question phase 1 already answered, and running
// the mark look-ups a second time on the request path — for a line that is
// usually not even written — is work the narrowing already paid for.
//
// The line carries identifiers and counts only — never request bodies, messages,
// tools or credentials — and goes through a budget so a systematic fault costs a
// bounded number of lines rather than one per request.
func reportFitNarrowing(model string, channels []int, result fitpolicy.Narrowing) {
	if !fitNarrowingLog.Allow() {
		return
	}
	// A non-nil Satisfied set is exactly the "the marks were evaluated" marker
	// the narrowing itself uses, so no second copy of that condition can drift
	// from it.
	marksEvaluated := result.Satisfied != nil
	verdicts := make([]string, 0, len(channels))
	for _, channelID := range channels {
		mark := "no-marks"
		if marksEvaluated {
			mark = "failed"
			if result.Matched(channelID) {
				mark = "ok"
			}
		}
		verdicts = append(verdicts, strconv.Itoa(channelID)+":"+mark)
	}
	common.SysLog(fmt.Sprintf(
		// %q for the model id: it comes from the request body, so a newline in it
		// would otherwise forge a second log line.
		"fitpolicy narrow: model=%q candidates=%v applied=%t official_and_permitted=%t kept=%v marks=%v",
		model, channels, result.Applied, result.PermittedByMarks, result.Channels, verdicts))
}
