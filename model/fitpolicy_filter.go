package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/gin-gonic/gin"
)

// Fit-policy narrowing for the channel selector.
//
// The requirement travels in the gin context and is read here, next to the
// candidate set it constrains. A nil filter — or a filter whose requirement has
// no opinion — leaves the legacy official pin untouched, so a request the policy
// says nothing about behaves exactly as it did before this layer existed.
//
// Step A note: MarkSatisfied is nil because the channel capability table does
// not exist yet. Phase 1 (verified channels) is therefore always empty and the
// result is phase 2, today's official set. That is what makes landing this
// wiring safe before the capability data does.

// FitChannelFilter carries one request's fit-policy narrowing into the selector.
type FitChannelFilter struct {
	// Requirement is the immutable policy opinion for this request.
	Requirement fitpolicy.Requirement
	// MarkSatisfied reports whether a channel currently satisfies every required
	// behaviour. Nil means no capability data exists; phase 1 stays empty and the
	// result equals the official pin.
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
	if !result.Applied {
		return channels, false
	}
	return result.Channels, true
}
