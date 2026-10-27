package common

import "net/http"

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
