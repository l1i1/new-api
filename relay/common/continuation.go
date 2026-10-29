package common

import (
	"errors"
	"net/http"
	"sync"

	rootcommon "github.com/QuantumNous/new-api/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// ChatContinuationConfig carries one transparent continuation channel for an
// OpenAI chat stream that stopped at an upstream output cap while the client's
// budget still had room. Send issues one continuation upstream request for the
// given visible answer prefix; MaxSteps bounds how many rounds the handler may
// stitch into the same client response.
//
// It is nil unless the relay layer enabled continuation for this request (the
// feature is off by default and priced per channel), so the ordinary streaming
// path stays byte-identical when the feature is disabled.
// ContinuationRoundChannel records which channel served the most recent
// continuation round. A cross-channel resume is credited (and blamed) on the
// channel that actually ran the round, not on the channel that failed to start it;
// otherwise a channel that cannot resume at all but always hops would be
// recommended by the capability telemetry (2026-10-29 review, H5).
type ContinuationRoundChannel struct {
	mu sync.Mutex
	id int
}

// Set records the channel of the round that just ran.
func (c *ContinuationRoundChannel) Set(id int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.id = id
	c.mu.Unlock()
}

// Last returns the last recorded channel id, 0 when no round ran.
func (c *ContinuationRoundChannel) Last() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.id
}

// Continuation repair failure reasons recorded in the cut diagnostics. They
// name why the rotation ended without a usable answer, which is what an
// operator needs to tell "the budget ran out" from "no channel left".
const (
	ContinuationFailureRoundIncomplete = "round_incomplete"
	ContinuationFailureRoundsExhausted = "rounds_exhausted"
	ContinuationFailureNoCandidate     = "no_candidate"
	ContinuationFailureElapsedCap      = "elapsed_cap"
	ContinuationFailureBudgetExhausted = "budget_exhausted"
	ContinuationFailureToolCalls       = "tool_calls_in_progress"
)

// ErrNoContinuationCandidate is returned by a cross-channel continuation sender
// when every channel the request could rotate to has already been tried. Callers
// treat it as "the rotation is over", never as a transient failure worth another
// round.
var ErrNoContinuationCandidate = errors.New("no continuation candidate channel")

// TriedChannels is the set of channels a request has already continued on. It
// is shared by every repair mode of one request: a round that rotates channels
// must never come back to one that already cut the answer, across the length,
// abnormal and missing modes. Access is concurrency-safe because a continuation
// round reads it from the request goroutine while another mode may observe it.
type TriedChannels struct {
	mu  sync.Mutex
	ids []int
}

// Add records a channel and reports whether it was not already present.
func (t *TriedChannels) Add(id int) bool {
	if t == nil || id <= 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, existing := range t.ids {
		if existing == id {
			return false
		}
	}
	t.ids = append(t.ids, id)
	return true
}

// Contains reports whether the channel was already tried.
func (t *TriedChannels) Contains(id int) bool {
	if t == nil || id <= 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, existing := range t.ids {
		if existing == id {
			return true
		}
	}
	return false
}

// IDs returns a copy of the tried channel ids in the order they were added.
func (t *TriedChannels) IDs() []int {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]int, len(t.ids))
	copy(out, t.ids)
	return out
}

type ChatContinuationConfig struct {
	MaxSteps int
	// MaxRounds is the total number of continuation rounds the handler may run.
	// It is the rotation-era bound: round 1 runs on the original channel and every
	// later round on a different one. Zero falls back to MaxSteps so a deployment
	// that only sets the legacy key keeps its old round count.
	MaxRounds int
	Send      func(partialContent string, remainingTokens int) (*http.Response, error)
	// RoundChannel, when set, tracks the channel of the latest round.
	RoundChannel *ContinuationRoundChannel
	// Fallback, when non-nil, is used once after the same-channel sender fails to
	// even start the round. It is expected to resume the same prefix on a
	// different channel with a full rebind (selection, model mapping, credential,
	// header/param overrides, concurrency slot). The round still counts as one
	// step: the fallback is neither a retry budget nor a second billed answer.
	Fallback func(partialContent string, remainingTokens int) (*http.Response, error)
	// Tried is the shared set of channels this request has already continued on.
	// The cross-channel sender excludes every entry, so rounds rotate instead of
	// re-picking a channel that already cut the answer.
	Tried *TriedChannels
	// Abnormal, when non-nil, enables the reasoning continuation mode for
	// streams that died with an abnormal finish_reason before any visible
	// content (900s cut-off shape). Send receives the captured reasoning text.
	Abnormal *ChatAbnormalContinuationConfig
	// Missing, when non-nil, enables the mode for streams that reached EOF/[DONE]
	// without any terminal finish_reason and without visible content (60s silent
	// cut shape). It reuses the same reasoning-prefix sender contract.
	Missing *ChatAbnormalContinuationConfig
}

// ChatAbnormalContinuationConfig carries one reasoning-continuation sender.
// Unlike the output-cap mode it replays reasoning as the assistant body, so the
// captured text must come from the same attempt and must not be re-billed as
// extra input.
type ChatAbnormalContinuationConfig struct {
	MaxSteps int
	// MaxRounds mirrors ChatContinuationConfig.MaxRounds for the reasoning modes.
	MaxRounds int
	Send      func(reasoning string, remainingTokens int) (*http.Response, error)
	// Fallback mirrors ChatContinuationConfig.Fallback for the reasoning modes.
	Fallback func(reasoning string, remainingTokens int) (*http.Response, error)
	// RoundChannel mirrors ChatContinuationConfig.RoundChannel.
	RoundChannel *ContinuationRoundChannel
	// Tried mirrors ChatContinuationConfig.Tried.
	Tried             *TriedChannels
	MaxReasoningBytes int
	MinReasoningChars int
}

// ChatAbnormalContinuationStepsForRequest reports how many reasoning
// continuation rounds the current request may use. 0 keeps the ordinary
// abnormal-finish failure path.
// continuationEligible reports whether this request may be transparently
// continued by one of the repair modes.
//
// A streaming client is the original case. A non-streaming client on a channel
// configured with internal_stream_for_nonstream has exactly the same observable
// upstream stream: the relay owns the whole body until it marshals one JSON
// response, so a cut round can be resumed without the caller seeing a seam. The
// 2026-10-29 900s-wall failures showed why that matters - a non-streaming
// request died at the provider wall on three channels in a row (45 minutes) and
// returned 502 with nothing to show for it.
func continuationEligible(info *RelayInfo) bool {
	if info == nil || info.RelayMode != relayconstant.RelayModeChatCompletions || info.RelayFormat != types.RelayFormatOpenAI {
		return false
	}
	return info.IsStream || info.ChannelSetting.InternalStreamForNonStream
}

func ChatAbnormalContinuationStepsForRequest(info *RelayInfo) int {
	if !continuationEligible(info) {
		return 0
	}
	if !rootcommon.ChatContinuationOnAbnormal || rootcommon.ChatContinuationAbnormalMaxSteps <= 0 || !rootcommon.ChatContinuationChannelAllowed(info.ChannelId) {
		return 0
	}
	return rootcommon.ChatContinuationAbnormalMaxSteps
}

// ChatMissingContinuationStepsForRequest reports how many reasoning
// continuation rounds the current missing-finish request may use.
func ChatMissingContinuationStepsForRequest(info *RelayInfo) int {
	if !continuationEligible(info) {
		return 0
	}
	if !rootcommon.ChatContinuationOnMissing || rootcommon.ChatContinuationMissingMaxSteps <= 0 || !rootcommon.ChatContinuationChannelAllowed(info.ChannelId) {
		return 0
	}
	return rootcommon.ChatContinuationMissingMaxSteps
}

// ChatContinuationStepsForRequest reports how many continuation rounds the
// current request may use: a streaming OpenAI chat-completions request on a
// whitelisted channel with the feature enabled. 0 keeps the ordinary
// single-round streaming path.
func ChatContinuationStepsForRequest(info *RelayInfo) int {
	if !continuationEligible(info) {
		return 0
	}
	if rootcommon.ChatContinuationMaxSteps <= 0 || !rootcommon.ChatContinuationChannelAllowed(info.ChannelId) {
		return 0
	}
	return rootcommon.ChatContinuationMaxSteps
}
