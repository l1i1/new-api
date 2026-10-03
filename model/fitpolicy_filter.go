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

	// fitAdmissionShadowLogBurst/Interval bound the shadow-admission lines: in
	// declared mode with a battery declared, every narrowing can say what the
	// measured set would have been, which is the equivalence window's data.
	// The burst keeps a full traffic day readable in the log without one line
	// per request.
	fitAdmissionShadowLogBurst    = 20
	fitAdmissionShadowLogInterval = time.Minute

	// fitAdmissionEmptyLogBurst/Interval bound the measured-empty alert: a
	// family running measured whose battery admits no candidate is the
	// availability cliff this layer must never be silent about. One line
	// immediately, then one per interval, so a state that is still broken an
	// hour later has not gone quiet.
	fitAdmissionEmptyLogBurst    = 2
	fitAdmissionEmptyLogInterval = 5 * time.Minute
)

var (
	fitNarrowingLog       = common.NewLogBudget(fitNarrowingLogBurst, fitNarrowingLogInterval)
	fitAdmissionShadowLog = common.NewLogBudget(fitAdmissionShadowLogBurst, fitAdmissionShadowLogInterval)
	fitAdmissionEmptyLog  = common.NewLogBudget(fitAdmissionEmptyLogBurst, fitAdmissionEmptyLogInterval)
)

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
// 2 reproduces the legacy result exactly rather than approximating it — under
// the declared admission source. Under the measured source the predicate is the
// admission battery instead of the allowlist (see
// officialFitChannelMatchesAdmissionLocked); the narrowing structure is
// unchanged, which is what lets the equivalence window compare the two answers
// on real traffic.
func (f *FitChannelFilter) narrowChannels(channels []int, model string) ([]int, bool) {
	if f == nil {
		return channels, false
	}
	admission := f.Requirement.Admission
	isOfficial := func(channelID int) bool {
		return officialFitChannelMatchesAdmissionLocked(channelID, model, admission)
	}
	result := f.Requirement.Narrow(channels, isOfficial, f.MarkSatisfied)
	reportFitAdmissionShadow(model, channels, admission, isOfficial)
	reportFitNarrowing(model, channels, result)
	if result.Applied && admission.Measured() && len(result.Channels) == 0 {
		alertMeasuredAdmissionEmpty(model, channels, admission)
	}
	if !result.Applied {
		return channels, false
	}
	return result.Channels, true
}

// reportFitAdmissionShadow logs what the measured admission would have kept, in
// declared mode, for a family that declares a battery. This is the equivalence
// window's instrument: flipping the family to measured is a document edit, and
// these lines are the evidence of what that edit would do to real traffic
// before it does it.
//
// The line carries identifiers and counts only. It is budgeted, and the sets
// are only computed when a line will actually be written.
func reportFitAdmissionShadow(model string, channels []int, admission fitpolicy.Admission, isOfficial func(int) bool) {
	if admission.Measured() || len(admission.Battery) == 0 {
		return
	}
	if !fitAdmissionShadowLog.Allow() {
		return
	}
	declared := make([]int, 0, len(channels))
	for _, channelID := range channels {
		if isOfficial(channelID) {
			declared = append(declared, channelID)
		}
	}
	measuredAdmission := admission.WithSourceMeasured()
	measured := make([]int, 0, len(channels))
	for _, channelID := range channels {
		if officialFitChannelMatchesAdmissionLocked(channelID, model, measuredAdmission) {
			measured = append(measured, channelID)
		}
	}
	common.SysLog(fmt.Sprintf(
		"fitpolicy admission shadow: model=%q candidates=%v declared=%v measured=%v battery=%v",
		model, channels, declared, measured, admission.Battery))
}

// alertMeasuredAdmissionEmpty reports the one state a measured family must
// never reach silently: the battery admitted no candidate at all. Every
// consequence is a hard failure (the narrowing keeps the empty set so the
// request fails honestly instead of degrading to an unverified aggregator), so
// the line names the rollback — the admission source is a document edit.
func alertMeasuredAdmissionEmpty(model string, channels []int, admission fitpolicy.Admission) {
	if !fitAdmissionEmptyLog.Allow() {
		return
	}
	common.SysError(fmt.Sprintf(
		"fitpolicy: measured admission kept no candidate for model=%q (candidates=%v, battery=%v, policy_hash=%s); "+
			"pinned requests fail honestly until marks refresh or the family's admission_source returns to declared",
		model, channels, admission.Battery, admission.PolicyHash))
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
