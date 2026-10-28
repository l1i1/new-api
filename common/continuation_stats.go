package common

import (
	"sort"
	"sync"
	"time"
)

// Continuation capability telemetry.
//
// The continuation whitelist used to be curated by hand: an operator enabled a
// channel, watched captures, and moved on. That does not scale to dozens of
// channels. These counters record what real continuation rounds actually did per
// channel, so the whitelist can be derived from evidence instead of guesswork:
// a channel that reliably resumes is a candidate, one that keeps failing is not.
//
// Counters are per instance and in memory on purpose: this is an admission hint,
// not billing data. A snapshot endpoint exposes them for the whitelist tool.
type ContinuationChannelStats struct {
	Attempts      int64 `json:"attempts"`
	Successes     int64 `json:"successes"`
	Failures      int64 `json:"failures"`
	LastSuccessAt int64 `json:"last_success_at"`
	LastFailureAt int64 `json:"last_failure_at"`
}

var continuationStats = struct {
	sync.Mutex
	byChannel map[int]*ContinuationChannelStats
}{byChannel: make(map[int]*ContinuationChannelStats)}

// RecordContinuationOutcome notes whether a continuation round on one channel
// produced a usable answer (success) or ran without producing anything (failure).
func RecordContinuationOutcome(channelID int, success bool) {
	if channelID <= 0 {
		return
	}
	now := time.Now().Unix()
	continuationStats.Lock()
	defer continuationStats.Unlock()
	stats := continuationStats.byChannel[channelID]
	if stats == nil {
		stats = &ContinuationChannelStats{}
		continuationStats.byChannel[channelID] = stats
	}
	stats.Attempts++
	if success {
		stats.Successes++
		stats.LastSuccessAt = now
	} else {
		stats.Failures++
		stats.LastFailureAt = now
	}
}

// ContinuationCapabilitySnapshot returns a copy of the per-channel counters.
func ContinuationCapabilitySnapshot() map[int]ContinuationChannelStats {
	continuationStats.Lock()
	defer continuationStats.Unlock()
	out := make(map[int]ContinuationChannelStats, len(continuationStats.byChannel))
	for channelID, stats := range continuationStats.byChannel {
		out[channelID] = *stats
	}
	return out
}

// ContinuationCapabilityCandidate reports whether the counters support enabling
// continuation for a channel: enough successes to prove the upstream accepts a
// replayed prefix, and a success rate that makes the feature worth the extra
// round. minAttempts keeps a single lucky round from widening the whitelist.
func ContinuationCapabilityCandidate(stats ContinuationChannelStats, minAttempts int, minSuccessRate float64) bool {
	if minAttempts <= 0 {
		minAttempts = 3
	}
	if stats.Attempts < int64(minAttempts) {
		return false
	}
	return float64(stats.Successes)/float64(stats.Attempts) >= minSuccessRate
}

// RecommendedContinuationChannels lists the channels whose counters qualify,
// sorted ascending for a stable comma separated whitelist value.
func RecommendedContinuationChannels(minAttempts int, minSuccessRate float64) []int {
	snapshot := ContinuationCapabilitySnapshot()
	ids := make([]int, 0, len(snapshot))
	for channelID, stats := range snapshot {
		if ContinuationCapabilityCandidate(stats, minAttempts, minSuccessRate) {
			ids = append(ids, channelID)
		}
	}
	sort.Ints(ids)
	return ids
}
