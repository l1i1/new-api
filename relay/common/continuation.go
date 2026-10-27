package common

import (
	"net/http"

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
type ChatContinuationConfig struct {
	MaxSteps int
	Send     func(partialContent string, remainingTokens int) (*http.Response, error)
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
