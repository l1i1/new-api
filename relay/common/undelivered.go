package common

import "github.com/QuantumNous/new-api/relaykit/dto"

// Undelivered reasons recorded on RelayInfo when a response did not deliver a
// usable terminal result to the caller. Settlement zeroes the charge for every
// such request: the caller did not receive the answer it asked for, so it must
// not be billed (and the consume log, when one is still written, must show zero
// token counts).
const (
	UndeliveredClientGone      = "client_gone"
	UndeliveredMissingFinish   = "missing_finish_reason"
	UndeliveredAbnormalFinish  = "abnormal_finish_reason"
	UndeliveredStreamPrefix    = "stream_"
	UndeliveredUpstreamFailure = "upstream_error_before_delivery"
)

// MarkUndelivered records that this request did not deliver a usable terminal
// result. The first reason wins; the upstream-reported counts are kept only for
// the consume log's audit marker, never for billing.
func (info *RelayInfo) MarkUndelivered(reason string, usage *dto.Usage) {
	if info == nil || reason == "" {
		return
	}
	if info.UndeliveredReason == "" {
		info.UndeliveredReason = reason
	}
	if usage == nil {
		return
	}
	if info.UndeliveredPromptTokens == 0 && info.UndeliveredCompletionTokens == 0 {
		info.UndeliveredPromptTokens = int64(max(usage.PromptTokens, usage.InputTokens))
		info.UndeliveredCompletionTokens = int64(max(usage.CompletionTokens, usage.OutputTokens))
	}
}

// UndeliveredSettlementReason reports why this request must settle at zero.
// An explicitly marked reason takes precedence; otherwise a streaming request
// whose transport did not end normally (client disconnect, scanner error,
// timeout, panic, ping failure) counts as undelivered. Non-streaming requests
// carry no StreamStatus, so they are only affected when a handler marked them.
func (info *RelayInfo) UndeliveredSettlementReason() string {
	if info == nil {
		return ""
	}
	if info.UndeliveredReason != "" {
		return info.UndeliveredReason
	}
	if info.StreamStatus != nil && !info.StreamStatus.IsNormalEnd() &&
		info.StreamStatus.EndReason != StreamEndReasonNone {
		if info.StreamStatus.EndReason == StreamEndReasonClientGone {
			return UndeliveredClientGone
		}
		return UndeliveredStreamPrefix + string(info.StreamStatus.EndReason)
	}
	return ""
}

// ZeroUsage returns a usage with every count cleared. Undelivered responses
// render this to the caller so a failed generation never reports token usage.
func ZeroUsage() *dto.Usage {
	return &dto.Usage{}
}
