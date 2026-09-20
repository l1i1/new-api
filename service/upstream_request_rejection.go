package service

import (
	"net/http"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// IsNeverRetryUpstreamError reports whether an upstream error is configured to
// never be retried, so failing over to another channel cannot change the
// answer. The keywords come from the operator-editable "Never-retry keywords"
// list (Routing Reliability); its default is the set of context-window
// rejections recorded on the live channels, where the limit belongs to the
// model rather than to the channel that served it.
//
// Only client error statuses qualify. The same wording behind a 5xx may be a
// transient upstream fault, and that stays retryable.
func IsNeverRetryUpstreamError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	if err.StatusCode < http.StatusBadRequest || err.StatusCode >= http.StatusInternalServerError {
		return false
	}
	return operation_setting.MatchesNeverRetryKeywords(err.Error())
}
