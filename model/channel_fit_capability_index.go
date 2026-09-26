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

type fitCapabilityMark struct {
	Supported bool
	State     string
	At        int64
	ExpiresAt int64
	Revision  int64
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
	now := common.GetTimestamp()
	index := make(map[int]map[string]map[string]fitCapabilityMark, len(rows))
	for i := range rows {
		row := &rows[i]
		state := FitCapabilityState(row, now, DefaultFitCapabilityStaleAfterDays, "", "")
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
			Supported: row.Supported,
			State:     state,
			At:        row.At,
			ExpiresAt: row.ExpiresAt,
			Revision:  row.Revision,
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

// ChannelSatisfiesFitMarks reports whether a channel currently verifies every
// required behaviour for a model.
//
// An empty requirement is satisfied by definition. An unknown mark never
// satisfies a conservative policy; with a permissive policy an unknown mark is
// not treated as a failure either, which is the only difference between the two.
// A mark that is known but not in a satisfying state (stale, failed, expired)
// never satisfies, under either policy: those are explicit negative or stale
// results, not absence of information.
func ChannelSatisfiesFitMarks(channelID int, model string, marks []string, permissiveUnknown bool) bool {
	if len(marks) == 0 {
		return true
	}
	for _, behavior := range marks {
		mark, found := LookupFitCapability(channelID, model, behavior)
		if !found {
			if permissiveUnknown {
				continue
			}
			return false
		}
		if !FitCapabilityStateSatisfies(mark.State) {
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
