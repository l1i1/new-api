package model

import (
	"fmt"
	"strings"
	"sync"
	"time"

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
	PolicyHash   string
	BaselineHash string
	Revision     int64
}

const (
	// fitCapabilityIndexLogBurst is how many index lines are written up front,
	// and fitCapabilityIndexLogInterval is how often the site refills after that.
	// A build runs on startup, on every channel-cache rebuild and on every config
	// epoch, and the failure it reports — the table is missing on a node that has
	// not migrated yet — is an expected state that can last for a deployment, so
	// the site has to stay both bounded and alive.
	fitCapabilityIndexLogBurst    = 6
	fitCapabilityIndexLogInterval = time.Minute

	// fitCapabilityIndexRetryCooldown is how long a failed build is left alone
	// before the next first-use attempt.
	//
	// Without it a failure is not a one-off: built stays false, so every mark of
	// every candidate of every request retries the full table scan. With a
	// missing table that is not a degraded node, it is a query storm on the
	// database that is already the reason the build failed. The cooldown keeps
	// the fail-open answer ("not verified") while making the retry rate bounded;
	// the explicit rebuilds on the channel-cache path ignore it on purpose,
	// because those are deliberate refreshes rather than first-use guesses.
	fitCapabilityIndexRetryCooldown = 30 * time.Second
)

var (
	fitCapabilityIndexLog  = common.NewLogBudget(fitCapabilityIndexLogBurst, fitCapabilityIndexLogInterval)
	fitCapabilityIndexLock sync.RWMutex
	// fitCapabilityIndex: channelID → model → behaviour → mark
	fitCapabilityIndex map[int]map[string]map[string]fitCapabilityMark
	// fitCapabilityIndexBuilt records whether a successful build has happened.
	// It distinguishes "no marks exist" from "the index is unavailable"; both
	// answer conservatively, but only the latter should be logged.
	fitCapabilityIndexBuilt bool

	// fitCapabilityIndexBuildLock makes the build single-flight: one builder at a
	// time, and concurrent first-use callers that arrive while a build is running
	// wait for it instead of starting a second scan of the same table.
	fitCapabilityIndexBuildLock sync.Mutex
	// fitCapabilityIndexRetryAfter is the earliest time a first-use build may be
	// attempted again after a failure. It is read and written under
	// fitCapabilityIndexBuildLock.
	fitCapabilityIndexRetryAfter time.Time
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
	fitCapabilityIndexBuildLock.Lock()
	defer fitCapabilityIndexBuildLock.Unlock()
	buildFitCapabilityIndexLocked()
}

// buildFitCapabilityIndexLocked performs one build. The caller holds
// fitCapabilityIndexBuildLock, which is what makes the two callers below —
// one deliberate, one first-use — collapse into a single scan instead of racing.
func buildFitCapabilityIndexLocked() bool {
	if DB == nil {
		// No handle yet. This is reachable before InitDB finishes and in tests
		// that never open one; answering "unavailable" is the documented
		// fail-open behaviour and is far better than dereferencing a nil handle
		// on the request path. No cooldown is armed here: this branch touches no
		// database, so retrying costs nothing, and arming one would delay the
		// first real build behind a wiring state that says nothing about the
		// table.
		if fitCapabilityIndexLog.Allow() {
			common.SysLog("channel fit capability index: database handle is not initialised yet")
		}
		return false
	}
	rows, err := ListAllChannelFitCapabilities()
	if err != nil {
		fitCapabilityIndexRetryAfter = time.Now().Add(fitCapabilityIndexRetryCooldown)
		if fitCapabilityIndexLog.Allow() {
			common.SysLog("failed to load channel fit capabilities: " + err.Error())
		}
		return false
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
			PolicyHash:   row.PolicyHash,
			BaselineHash: row.BaselineHash,
			Revision:     row.Revision,
		}
	}
	fitCapabilityIndexLock.Lock()
	fitCapabilityIndex = index
	fitCapabilityIndexBuilt = true
	fitCapabilityIndexLock.Unlock()
	// A successful build clears the cooldown: the next first-use call has nothing
	// to wait for, and a later failure starts its own window.
	fitCapabilityIndexRetryAfter = time.Time{}
	// Counts only: an index built from the wrong rows is indistinguishable at
	// request time from one whose rows do not match, and both look like "no
	// capability data" in the narrowing result.
	if fitCapabilityIndexLog.Allow() {
		common.SysLog(fmt.Sprintf("fitpolicy capability index: rows=%d", len(rows)))
	}
	return true
}

// FitCapabilityIndexReady reports whether a successful build has happened. It is
// used by tests and by the management surface, not by the selection decision.
func FitCapabilityIndexReady() bool {
	fitCapabilityIndexLock.RLock()
	defer fitCapabilityIndexLock.RUnlock()
	return fitCapabilityIndexBuilt
}

// LookupFitCapability returns the indexed mark for a channel/model/behaviour,
// building the index on first use.
//
// It must not be called while channelSyncLock is held: a first-use build runs a
// full table scan, and doing that with the channel cache held blocks every
// channel write for its duration. The selection path does not go through here —
// it uses channelFitCapabilityMark, which never builds — and the out-of-lock
// trigger is FitChannelFilterForRequest. This entry point exists for the
// management surface and for callers that are not on the selection path.
func LookupFitCapability(channelID int, model, behavior string) (fitCapabilityMark, bool) {
	ensureFitCapabilityIndexBuilt()
	return channelFitCapabilityMark(channelID, model, behavior)
}

// channelFitCapabilityMark reads the index without building it.
//
// This is the selection path's lookup. When the index has never been built the
// answer is "not verified", which is the conservative answer and the documented
// fail-open behaviour; the build itself is triggered outside the lock by
// FitChannelFilterForRequest and on the channel-cache path.
func channelFitCapabilityMark(channelID int, model, behavior string) (fitCapabilityMark, bool) {
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

// ensureFitCapabilityIndexBuilt builds the index on first use.
//
// The startup path builds it too, but the two call sites that did so both sit
// behind the memory-cache switch, so on the database path — which is the path
// both sites actually run — nothing ever built it. A restart therefore left the
// index empty, every mark look-up missed, and the whole capability layer went
// silently inert while the table looked correct; the narrowest phase could never
// match, so every requirement quietly degraded to the plain official set.
//
// Building on first use removes the class of failure rather than the instance:
// the wiring cannot be forgotten again, and a restart cannot leave a node
// vouching for nothing while claiming to consult measurements.
//
// The build is single-flight, and a failed build is not retried until
// fitCapabilityIndexRetryCooldown has passed. Both matter on the hot path: the
// callers are per-request, so without them a node whose table is missing (the
// expected state before a migration reaches it) would run one full scan per mark
// of every candidate of every request.
func ensureFitCapabilityIndexBuilt() {
	if FitCapabilityIndexReady() {
		return
	}
	fitCapabilityIndexBuildLock.Lock()
	defer fitCapabilityIndexBuildLock.Unlock()
	// Re-check under the lock: this is what makes concurrent first-use callers
	// collapse into the single build the winner performs.
	if FitCapabilityIndexReady() {
		return
	}
	if time.Now().Before(fitCapabilityIndexRetryAfter) {
		return
	}
	buildFitCapabilityIndexLocked()
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
		// channelFitCapabilityMark, not LookupFitCapability: this runs under
		// channelSyncLock during selection, and a first-use build here would run a
		// full table scan with the channel cache held. An unbuilt index answers
		// "not verified", which is the same answer an empty one gives.
		mark, found := channelFitCapabilityMark(channelID, requirement.Model, behavior)
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
			PolicyHash:   mark.PolicyHash,
			BaselineHash: mark.BaselineHash,
			Revision:     mark.Revision,
		}
		state := FitCapabilityState(row, now, requirement.PolicyHash, requirement.BaselineHash)
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
//
// The retry cooldown is cleared with it: a test that observed a failed build
// would otherwise leave the next test's first-use build suppressed for the rest
// of the cooldown, which makes results depend on test order.
func ResetFitCapabilityIndexForTest() {
	fitCapabilityIndexBuildLock.Lock()
	fitCapabilityIndexRetryAfter = time.Time{}
	fitCapabilityIndexBuildLock.Unlock()
	fitCapabilityIndexLock.Lock()
	fitCapabilityIndex = nil
	fitCapabilityIndexBuilt = false
	fitCapabilityIndexLock.Unlock()
}
