package service

import (
	"net/http"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// IsNeverRetryUpstreamError reports whether an upstream error is configured to
// never be retried, so failing over to another channel cannot change the
// answer. The keywords come from the operator-editable "Never-retry keywords"
// list (Routing Reliability); its default is the set of context-window
// rejections recorded on the live channels, where the limit belongs to the
// model rather than to the channel that served it.
//
// The status code does not gate the match any more (2026-10-29): one channel
// wraps the same context-window rejection as 502 while another answers it as
// 400, and the wording — not the wrapper — is the operator's statement that the
// request itself is unservable. A transient upstream fault does not carry a
// context-window wording, so the keyword list stays the safety net: an
// unmatched 5xx is still retried and failed over as before.
func IsNeverRetryUpstreamError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	if err.StatusCode < http.StatusBadRequest {
		return false
	}
	return model.CurrentRequestPolicy().MatchesNeverRetryKeywords(err.Error())
}

// NormalizeRequestRejectionError maps a request-level rejection onto the status
// the caller can act on. A channel that wraps the refusal as 502 otherwise
// makes the client retry a request that can never succeed (and makes the
// message depend on which channel answered last), while 400 + the upstream
// wording tells the caller to trim history and re-send (2026-10-29: 64
// context-window requests in 15 minutes each spent one useless channel hop).
func NormalizeRequestRejectionError(err *types.NewAPIError) *types.NewAPIError {
	if err == nil || err.StatusCode < http.StatusInternalServerError {
		return err
	}
	err.StatusCode = http.StatusBadRequest
	return err
}
