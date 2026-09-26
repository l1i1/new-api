package model

import (
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

// In-memory capability index.
//
// Selection runs on the hot path, so it cannot read the capability table per
// candidate. The index is rebuilt with the channel cache and refreshed on the
// same config-epoch notification that already invalidates the channel cache, so
// a mark written on one node reaches the others through the path that is already
// proven for channel changes.
//
// The index fails open. If the table is missing (a node that has not migrated
// yet), the query fails, or the index has never been built, every lookup reports
// "not verified". That is the conservative answer and it also keeps behaviour
// identical to the world before this table existed, which is what the spec
// requires when capability data is absent.

// fitCapabilityMark is the raw stored state, not a precomputed verdict.
//
// Freezing the verdict at build time would be wrong in two ways: freshness and
// expiry move with the clock, and the policy hash they are bound to changes when
// the rules change — neither of which triggers a channel-cache rebuild. The
// lookup therefore re-derives the state from these fields using the current time
// and the binding in force for the request.
type fitCapabilityMark struct {
	Supported    bool
	Source       string
	At           int64
	ExpiresAt    int64
	PolicyHash   string
	BaselineHash string
	Revision     int64
}

var (
	fitCapabilityIndexLock sync.RWMutex
	// fitCapabilityIndex: channelID → model → behaviour → mark
	fitCapabilityIndex map[int]map[string]map[string]fitCapabilityMark
	// fitCapabilityIndexBuilt records whether a successful build has happened.
	// It distinguishes "no marks exist" from "the index is unavailable"; both
	// answer conservatively, but only the latter should be logged.
	fitCapabilityIndexBuilt bool
)

// Lock ordering: selection holds channelSyncLock (read) and then takes
// fitCapabilityIndexLock (read) for each mark lookup, so the only nesting is
// channel → index. Nothing may take fitCapabilityIndexLock and then reach for
// channelSyncLock, and this function must stay outside channelSyncLock — it is
// called after the channel cache releases it for exactly that reason.
//
// InitFitCapabilityIndex rebuilds the capability index from the database.
//
// A failure is logged and leaves the previous index in place rather than
// clearing it: a transient database error must not make verified channels look
// unverified and push traffic off them.
func InitFitCapabilityIndex() {
	rows, err := ListAllChannelFitCapabilities()
	if err != nil {
		common.SysLog("failed to load channel fit capabilities: " + err.Error())
		return
	}
	index := make(map[int]map[string]map[string]fitCapabilityMark, len(rows))
	for i := range rows {
		row := &rows[i]
		byModel, ok := index[row.ChannelId]
		if !ok {
			byModel = make(map[string]map[string]fitCapabilityMark)
			index[row.ChannelId] = byModel
		}
		byBehavior, ok := byModel[row.Model]
		if !ok {
			byBehavior = make(map[string]fitCapabilityMark)
			byModel[row.Model] = byBehavior
		}
		byBehavior[row.Behavior] = fitCapabilityMark{
			Supported:    row.Supported,
			Source:       row.Source,
			At:           row.At,
			ExpiresAt:    row.ExpiresAt,
			PolicyHash:   row.PolicyHash,
			BaselineHash: row.BaselineHash,
			Revision:     row.Revision,
		}
	}
	fitCapabilityIndexLock.Lock()
	fitCapabilityIndex = index
	fitCapabilityIndexBuilt = true
	fitCapabilityIndexLock.Unlock()
}

// FitCapabilityIndexReady reports whether a successful build has happened. It is
// used by tests and by the management surface, not by the selection decision.
func FitCapabilityIndexReady() bool {
	fitCapabilityIndexLock.RLock()
	defer fitCapabilityIndexLock.RUnlock()
	return fitCapabilityIndexBuilt
}

// LookupFitCapability returns the indexed mark for a channel/model/behaviour.
func LookupFitCapability(channelID int, model, behavior string) (fitCapabilityMark, bool) {
	fitCapabilityIndexLock.RLock()
	defer fitCapabilityIndexLock.RUnlock()
	byModel, ok := fitCapabilityIndex[channelID]
	if !ok {
		return fitCapabilityMark{}, false
	}
	byBehavior, ok := byModel[strings.ToLower(strings.TrimSpace(model))]
	if !ok {
		return fitCapabilityMark{}, false
	}
	mark, ok := byBehavior[strings.ToLower(strings.TrimSpace(behavior))]
	return mark, ok
}

// FitMarkRequirement is what one request needs a channel's marks to prove.
type FitMarkRequirement struct {
	Model string
	Marks []string
	// PermissiveUnknown treats an absent mark as "not disproven" instead of
	// "not verified". It is the policy's UnknownMarkPolicy turn.
	PermissiveUnknown bool
	// PolicyHash and BaselineHash are the bindings in force for this request. A
	// suite result measured against anything else is stale, which is what keeps a
	// rule change from silently validating old measurements.
	PolicyHash   string
	BaselineHash string
}

// ChannelSatisfiesFitMarks reports whether a channel currently verifies every
// required behaviour for a model.
//
// An empty requirement is satisfied by definition. An unknown mark never
// satisfies a conservative policy; with a permissive policy an unknown mark is
// not treated as a failure either, which is the only difference between the two.
// A mark that is known but not in a satisfying state (stale, failed, expired)
// never satisfies, under either policy: those are explicit negative or stale
// results, not absence of information.
//
// The state is derived here, from the current clock and the request's policy
// binding, rather than read from a value computed when the index was built.
func ChannelSatisfiesFitMarks(channelID int, requirement FitMarkRequirement) bool {
	if len(requirement.Marks) == 0 {
		return true
	}
	now := common.GetTimestamp()
	for _, behavior := range requirement.Marks {
		mark, found := LookupFitCapability(channelID, requirement.Model, behavior)
		if !found {
			if requirement.PermissiveUnknown {
				continue
			}
			return false
		}
		row := &ChannelFitCapability{
			ChannelId:    channelID,
			Model:        requirement.Model,
			Behavior:     behavior,
			Supported:    mark.Supported,
			Source:       mark.Source,
			At:           mark.At,
			ExpiresAt:    mark.ExpiresAt,
			PolicyHash:   mark.PolicyHash,
			BaselineHash: mark.BaselineHash,
			Revision:     mark.Revision,
		}
		state := FitCapabilityState(row, now, DefaultFitCapabilityStaleAfterDays, requirement.PolicyHash, requirement.BaselineHash)
		if !FitCapabilityStateSatisfies(state) {
			return false
		}
		if !mark.Supported {
			return false
		}
	}
	return true
}

// ResetFitCapabilityIndexForTest clears the index so a test can observe the
// unavailable-index behaviour. It is not used by production code.
func ResetFitCapabilityIndexForTest() {
	fitCapabilityIndexLock.Lock()
	fitCapabilityIndex = nil
	fitCapabilityIndexBuilt = false
	fitCapabilityIndexLock.Unlock()
}
