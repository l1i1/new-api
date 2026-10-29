package common

import (
	"sync"
	"time"
)

// Channel cooldown is the short-lived, in-memory "do not route here" signal for
// channels that keep breaking in ways a retry cannot fix: a stream that dies
// after delivering content, or an upstream that answers without any answer text
// at all (2026-10-28: ch77 emitted "Upstream completed without answer text" and
// kept absorbing customer traffic because its channel status stayed enabled).
//
// It is deliberately not a channel status change: status lives in the database,
// is mirrored across instances and survives restarts, while this is a best-effort
// admission hint that expires by itself and is recomputed per instance. A channel
// that is cooling is skipped by selection unless every remaining candidate is
// cooling, so a bad upstream stops absorbing traffic without ever emptying the
// pool.
const (
	// channelFailureWindow is the sliding window that counts failures.
	channelFailureWindow = 2 * time.Minute
	// channelFailureThreshold is how many failures inside the window promote a
	// channel into cooldown. One failure can be a single bad request; two inside
	// two minutes is an upstream pattern.
	channelFailureThreshold = 2
	// channelCooldownDuration is how long the channel is skipped afterwards.
	channelCooldownDuration = 3 * time.Minute
)

type channelCooldownState struct {
	until    time.Time
	failures []time.Time
}

var channelCooldown = struct {
	sync.Mutex
	states map[int]*channelCooldownState
}{states: make(map[int]*channelCooldownState)}

// RecordChannelFailure notes one non-retryable channel failure and promotes the
// channel into cooldown once it repeats inside the sliding window.
func RecordChannelFailure(channelID int) {
	if channelID <= 0 {
		return
	}
	now := time.Now()
	channelCooldown.Lock()
	defer channelCooldown.Unlock()
	state := channelCooldown.states[channelID]
	if state == nil {
		state = &channelCooldownState{}
		channelCooldown.states[channelID] = state
	}
	kept := state.failures[:0]
	for _, at := range state.failures {
		if now.Sub(at) <= channelFailureWindow {
			kept = append(kept, at)
		}
	}
	state.failures = append(kept, now)
	if len(state.failures) >= channelFailureThreshold {
		state.until = now.Add(channelCooldownDuration)
		state.failures = nil
	}
}

// ChannelInCooldown reports whether selection should currently skip the channel.
func ChannelInCooldown(channelID int) bool {
	if channelID <= 0 {
		return false
	}
	now := time.Now()
	channelCooldown.Lock()
	defer channelCooldown.Unlock()
	state := channelCooldown.states[channelID]
	if state == nil {
		return false
	}
	if state.until.IsZero() {
		// Still collecting failures: the countdown must not wipe the history,
		// otherwise a channel that fails once, gets probed, and fails again never
		// reaches the threshold.
		return false
	}
	if now.Before(state.until) {
		return true
	}
	delete(channelCooldown.states, channelID)
	return false
}

// ClearChannelCooldown forgets the channel's cooldown and failure history. Used
// by tests and by an operator path that wants to put a channel back immediately.
func ClearChannelCooldown(channelID int) {
	channelCooldown.Lock()
	defer channelCooldown.Unlock()
	delete(channelCooldown.states, channelID)
}
