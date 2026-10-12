package operation_setting

import (
	"fmt"
	"strings"
)

// RetryErrorFidelityMode values.
//
// The mode governs two halves of one promise: the client must see what actually
// happened to its request, not a routing artefact produced by the platform's own
// failover bookkeeping.
//
//   - off: today's behaviour everywhere.
//   - shadow: evaluate the guard and let the request proceed unchanged; the only
//     observable effect is the routing-fidelity error row written when a retry
//     ends with no candidate (see recordNoCandidateRetryError), which is what
//     makes the flip to "on" reviewable on live traffic.
//   - on: the guard may change the outcome.
const (
	RetryErrorFidelityOff    = "off"
	RetryErrorFidelityShadow = "shadow"
	RetryErrorFidelityOn     = "on"
)

// RetryErrorFidelityMode decides whether a fit-pinned request keeps the official
// upstream verdict instead of failing over, and whether a retry that runs out of
// candidates reports the last real upstream error instead of the routing error.
//
// Why this exists (2026-10-12, live production): the GLM 5.3 family is narrowed
// by the live official-fit policy to its single official-type channel. The
// operator's failover list contains 400, so an upstream 400 ("角色信息不正确",
// Zhipu error code 1214 — a verdict about the request body, which no other
// channel can change) entered the retry loop; the loop excludes the failed
// channel first, the exclusion empties the candidate set, the re-selection
// returns temporarily_unavailable, and controller/relay.go answers with
// get_channel_failed. The client therefore received a 500 whose text says
// nothing about the real cause, while the upstream 400 was recorded in the error
// log — the two records disagree and the routing error wins.
//
// The keyword branch of DecideRelayRetry already stops a pinned request from
// failing over (officialFitPinKeepsVerdict); the status-code branch never got
// that guard, because the guard's author believed production excludes 400 from
// the failover list. The live option row is a legacy value that still lists 400,
// so that assumption does not hold. This mode is the switch for closing the gap
// without touching the failover list itself: an unpinned request — which is what
// the aggregator-style content-filter 400s that failover really rescues look
// like — keeps retrying exactly as before.
//
// Default shadow: the guard is live code but changes no client-visible outcome
// until an operator flips it, so the evidence can be read off production first.
var RetryErrorFidelityMode = RetryErrorFidelityShadow

// RetryErrorFidelityModeOrDefault normalises the stored value. An unknown or
// empty string reads as the default rather than as "on": a typo in the option
// row must not silently change what customers receive.
func RetryErrorFidelityModeOrDefault() string {
	mode := strings.ToLower(strings.TrimSpace(RetryErrorFidelityMode))
	switch mode {
	case RetryErrorFidelityOff, RetryErrorFidelityShadow, RetryErrorFidelityOn:
		return mode
	}
	return RetryErrorFidelityShadow
}

// SetRetryErrorFidelityMode validates an operator-supplied value.
func SetRetryErrorFidelityMode(value string) error {
	mode := strings.ToLower(strings.TrimSpace(value))
	switch mode {
	case RetryErrorFidelityOff, RetryErrorFidelityShadow, RetryErrorFidelityOn:
		RetryErrorFidelityMode = mode
		return nil
	}
	return fmt.Errorf("unsupported RetryErrorFidelityMode %q (want %s, %s or %s)",
		value, RetryErrorFidelityOff, RetryErrorFidelityShadow, RetryErrorFidelityOn)
}

// RetryErrorFidelityEnforces reports whether the guard may change a decision.
func RetryErrorFidelityEnforces() bool {
	return RetryErrorFidelityModeOrDefault() == RetryErrorFidelityOn
}

// IsVerdictStatusCode reports whether an upstream status is a verdict about the
// request rather than evidence about the channel.
//
// The distinction is what keeps the guard narrow. A 4xx (except the two below)
// says the upstream read the request and refused it; every channel reads the
// same request, so failing over cannot produce a different answer, and on a
// pinned request the refusal IS the answer the fit contract promises to pass
// through verbatim. A 5xx, a 408 or a 429 is about the channel or its transport
// instead — there is no answer to protect — and the failover list decides those
// on its own terms: it omits 408 entirely (the legacy "timeout is not retryable"
// rule) and keeps 429 retryable so a saturated channel can be escaped.
func IsVerdictStatusCode(code int) bool {
	switch code {
	case 408, 429:
		return false
	}
	return code >= 400 && code < 500
}
