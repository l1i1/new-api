package common

import (
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

type ChatContinuationConfig struct {
	MaxSteps int
	Send     func(partialContent string, remainingTokens int) (*http.Response, error)
	// RoundChannel, when set, tracks the channel of the latest round.
	RoundChannel *ContinuationRoundChannel
	// Fallback, when non-nil, is used once after the same-channel sender fails to
	// even start the round. It is expected to resume the same prefix on a
	// different channel with a full rebind (selection, model mapping, credential,
	// header/param overrides, concurrency slot). The round still counts as one
	// step: the fallback is neither a retry budget nor a second billed answer.
	Fallback func(partialContent string, remainingTokens int) (*http.Response, error)
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
	Send     func(reasoning string, remainingTokens int) (*http.Response, error)
	// Fallback mirrors ChatContinuationConfig.Fallback for the reasoning modes.
	Fallback func(reasoning string, remainingTokens int) (*http.Response, error)
	// RoundChannel mirrors ChatContinuationConfig.RoundChannel.
	RoundChannel      *ContinuationRoundChannel
	MaxReasoningBytes int
	MinReasoningChars int
}

// ChatAbnormalContinuationStepsForRequest reports how many reasoning
// continuation rounds the current request may use. 0 keeps the ordinary
// abnormal-finish failure path.
func ChatAbnormalContinuationStepsForRequest(info *RelayInfo) int {
	if info == nil || !info.IsStream || info.RelayMode != relayconstant.RelayModeChatCompletions || info.RelayFormat != types.RelayFormatOpenAI {
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
	if info == nil || !info.IsStream || info.RelayMode != relayconstant.RelayModeChatCompletions || info.RelayFormat != types.RelayFormatOpenAI {
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
	if info == nil || !info.IsStream || info.RelayMode != relayconstant.RelayModeChatCompletions || info.RelayFormat != types.RelayFormatOpenAI {
		return 0
	}
	if rootcommon.ChatContinuationMaxSteps <= 0 || !rootcommon.ChatContinuationChannelAllowed(info.ChannelId) {
		return 0
	}
	return rootcommon.ChatContinuationMaxSteps
}
