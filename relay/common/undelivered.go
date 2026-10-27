package common

import "github.com/QuantumNous/new-api/relaykit/dto"

// Undelivered reasons recorded on RelayInfo when a response did not deliver a
// usable terminal result because of an upstream fault. Settlement zeroes the
// charge for every such request: the caller did not receive the answer it asked
// for, so it must not be billed (and the consume log, when one is still written,
// must show zero token counts).
//
// A caller-side disconnect is deliberately NOT in this set: the caller chose to
// leave after the provider had already been paid for the generated tokens, so
// the observed usage stays billable (anti-free-ride). See
// UndeliveredSettlementReason.
const (
	// UndeliveredClientGone is retained for diagnostics/tests only; it never
	// zeroes a settlement.
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
// whose transport did not end normally for an upstream reason (scanner error,
// timeout, panic, ping failure) counts as undelivered. A caller-side disconnect
// (client_gone) is excluded: it stays billable. Non-streaming requests carry no
// StreamStatus, so they are only affected when a handler marked them.
func (info *RelayInfo) UndeliveredSettlementReason() string {
	if info == nil {
		return ""
	}
	if info.UndeliveredReason != "" {
		if info.UndeliveredReason == UndeliveredClientGone {
			// The caller walked away; the provider still generated tokens for it.
			return ""
		}
		return info.UndeliveredReason
	}
	if info.StreamStatus != nil && !info.StreamStatus.IsNormalEnd() &&
		info.StreamStatus.EndReason != StreamEndReasonNone {
		if info.StreamStatus.EndReason == StreamEndReasonClientGone {
			return ""
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
