package service

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/types"
)

// upstreamRequestRejectionMarkers are case-insensitive substrings that identify
// an upstream 4xx as a rejection of the request itself rather than of the
// channel that served it. Every entry is a context-window rejection recorded on
// a live channel: the upstreams word it differently, but all of them say the
// prompt is longer than the model accepts.
//
// A channel cannot change that verdict -- the limit belongs to the model and the
// request is already past it -- so failing over reproduces the same 400 on every
// remaining channel while the client waits out the whole retry budget.
//
// Parameter rejections that a different channel may well accept (max_tokens
// outside one aggregator's range, an image part a text-only route refuses) are
// deliberately absent: those are what the force-retry rule for 400 is for.
var upstreamRequestRejectionMarkers = []string{
	"context length", // "maximum context length", "the model's context length", "context length exceeded"
	"context_length_exceeded",
	"prompt is too long",
	"input is too long",
	"token exceed the limit",
	"exceeds the maximum allowed input length",
	"exceeds the maximum length",
	"range of input length should be",
}

// IsUpstreamRequestRejection reports whether an upstream error rejects the
// request itself, so retrying it on another channel cannot succeed. Only client
// error statuses qualify: an upstream that reports the same wording as a 5xx may
// be having a transient problem, and that stays retryable.
func IsUpstreamRequestRejection(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	if err.StatusCode < http.StatusBadRequest || err.StatusCode >= http.StatusInternalServerError {
		return false
	}
	message := strings.ToLower(err.Error())
	if message == "" {
		return false
	}
	for _, marker := range upstreamRequestRejectionMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
