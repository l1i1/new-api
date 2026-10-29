package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// RelayRequestContextDone reports whether the request context behind a relay
// attempt has already been canceled. A retry can only pick another channel; it
// cannot deliver an answer to a peer that is no longer connected, and every
// further attempt would fail instantly against the canceled context while
// spending upstream capacity nobody is waiting for. The context also dies on
// server shutdown and middleware cancellation, so callers should treat a
// positive result as "the request is over", not as evidence about the client.
func RelayRequestContextDone(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	return c.Request.Context().Err() != nil
}

// DecideRelayRetry is the single retry decision for relay attempts. The reason
// is recorded in the request policy decision events of the log details.
//
// The decision itself is pure routing policy; the side effects a retry needs run
// here, after the verdict is known and only when the request keeps going: the
// failed attempt's stickiness is dropped and a fidelity pin that cannot be
// served is relaxed for the remaining attempts. Clearing earlier would let a
// stop-verdict test mutate the affinity state the same function still has to
// read (session_mode=strict would then look like an ordinary retry).
func DecideRelayRetry(c *gin.Context, err *types.NewAPIError, retryTimes int) PolicyDecision {
	decision := decideRelayRetry(c, err, retryTimes)
	if c == nil || err == nil {
		return decision
	}
	if decision.Action != "retry" {
		// A terminal answer leaves the next request to choose again: drop the
		// binding so a client retry does not walk back into the failure, but keep
		// this request's context untouched (strict-session contracts and the
		// affinity transform still read it after the decision).
		if dropsStickyRoutingAfterFailure(err) {
			DropStickyRoutingAfterUpstreamFailure(c)
		}
		return decision
	}
	if dropsStickyRoutingAfterFailure(err) {
		// Any failed upstream attempt broke the "this channel/key serves this
		// conversation" hypothesis: drop both bindings so the retry does not walk
		// back into the same capacity (2026-10-29: ch8 key #4 absorbed 61% of a
		// token's prompt tokens and 33 of its 56 429s while sibling keys were idle).
		DropStickyRoutingAfterUpstreamFailure(c)
	}
	// Fidelity pin vs availability (2026-10-29 operator policy: 「pin 不能 pin 死，
	// 拟合渠道不可用可回退到普通渠道，可用性和一致性取平衡，优先拟合、可用兜底」).
	// A non-verdict failure — rate limit, outage, transport — means the official
	// capacity cannot serve right now. Credential rotation is the faithfulness-
	// preserving first move and already ran as prefer-channel; when no sibling
	// credential is left the remaining attempts fall back to the ordinary pool
	// instead of turning the outage into the customer's answer. The fit
	// requirement stays in the context, so the log still shows which rules pinned
	// the request.
	if shouldRelaxOfficialPinForAvailability(c, err) {
		RelaxOfficialPinForAvailability(c, fmt.Sprintf("official capacity failure (%d) and no sibling credential left", err.StatusCode))
	}
	return decision
}

func decideRelayRetry(c *gin.Context, err *types.NewAPIError, retryTimes int) PolicyDecision {
	if err == nil {
		return PolicyDecision{Action: "stop", Reason: "request_completed", Source: "system"}
	}
	// The request is already over (peer disconnected, server shutdown, or the
	// context was canceled by middleware). A retry can only pick another
	// channel, never bring the requester back, so stop before spending the
	// remaining attempt budget on attempts that fail instantly.
	if RelayRequestContextDone(c) {
		return PolicyDecision{Action: "stop", Reason: "request_context_done", Source: "system"}
	}
	if ShouldSkipRetryAfterChannelAffinityFailure(c) {
		source := RequestPolicy(c).SessionModeSource
		if source == "" {
			source = "session_rule"
		}
		if !(c != nil && common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) &&
			ShouldRotateMultiKeyCredentialOn(err.StatusCode, err.Error())) {
			return PolicyDecision{Action: "stop", Reason: "strict_session", Source: source}
		}
	}
	if GetChannelConstraints(c).SuppressesRetry() {
		return PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}
	}
	if c != nil {
		if _, pinned := c.Get("specific_channel_id"); pinned {
			return PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}
		}
	}
	if types.IsChannelError(err) {
		return PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}
	}
	if retryTimes <= 0 {
		return PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}
	}
	// A request-level rejection is answered identically by every channel. Keep
	// this after the attempt budget check so an exhausted request reports the
	// same terminal reason as other failures, and before all configurable retry
	// rules so a matching never-retry keyword always wins.
	if IsNeverRetryUpstreamError(err) {
		return PolicyDecision{Action: "stop", Reason: "request_rejected", Source: "system"}
	}
	code := err.StatusCode
	if code >= 100 && code <= 599 && operation_setting.IsNeverRetryStatusCode(code) {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	// A locally generated *final* refusal (the platform's own quota/billing
	// check) is not evidence about a channel: the relay never asked one, and no
	// other channel can serve it. Stop before the keyword list, whose wording
	// deliberately contains the platform's own billing text (额度/余额) so that
	// it reaches this path at all; otherwise a platform 403 would enter the
	// retry loop in shadow mode. A local *conversion* refusal is deliberately
	// NOT final here: adaptors report channel-capability gaps through it, and the
	// keyword list is what lets those fail over to a capable channel.
	if err.GetErrorType() == types.ErrorTypeNewAPIError && isLocalFinalRefusal(err) {
		return PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}
	}
	// A transport failure never produced an upstream verdict: the attempt did not
	// reach the channel's answer, so another attempt can still serve the request.
	// The motivating case is the streaming response-header budget
	// (RELAY_STREAM_RESPONSE_HEADER_TIMEOUT): the upstream accepted the request
	// and then sent nothing for the whole budget, and before this branch the
	// request ended with a local 500 while the retry budget was untouched.
	// Retrying here stays bounded by the attempt budget checked above and by the
	// never-retry rules before this point; a client disconnect is caught at the
	// top of the function (request_context_done) and never reaches here.
	if err.GetErrorCode() == types.ErrorCodeDoRequestFailed {
		// A chain of silent upstreams must not turn into attempt-count × first-byte
		// timeout of wall clock. The budget is measured from the first attempt
		// selection (the policy state is created there), so it bounds this failure
		// class only; a request that is actually streaming keeps its own budget.
		if budget := time.Duration(common.RelayTransportRetryBudgetSeconds) * time.Second; budget > 0 {
			if elapsed := time.Since(RequestPolicy(c).StartedAt); elapsed >= budget {
				return PolicyDecision{Action: "stop", Reason: "transport_retry_budget_exhausted", Source: "system"}
			}
		}
		return PolicyDecision{Action: "retry", Reason: "transport_error", Source: "system"}
	}
	// A malformed upstream response can succeed on another channel. Local
	// conversion failures carry an explicit skip-retry marker instead.
	// An operator-configured keyword is an explicit channel capability signal;
	// it takes precedence over a generic skip-retry marker.
	if operation_setting.MatchesAutomaticRetryKeywords(err.Error()) {
		// Availability first (operator policy). The keyword list describes
		// upstream wording, and only a verdict-shaped status may keep the pin;
		// the decision itself goes through the same mode-aware guard as the
		// status-code branch so a keyword match and a status-code match can never
		// disagree (the keyword branch used to ignore RetryErrorFidelityMode and
		// stop in shadow mode). Measured 2026-10-21: "all accounts are
		// temporarily unavailable" (503) matched the keyword list, the guard kept
		// the pin, and 194 buyer requests failed that a failover could have
		// served.
		if decision, guarded := pinnedOfficialVerdictDecision(c, err); guarded {
			return decision
		}
		return PolicyDecision{Action: "retry", Reason: "retry_keyword_matched", Source: "global"}
	}
	if types.IsSkipRetryError(err) {
		return PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}
	}
	if code >= 200 && code < 300 {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if code < 100 || code > 599 {
		return PolicyDecision{Action: "retry", Reason: "unrecognized_status", Source: "system"}
	}
	// Force-retry rules apply only to upstream errors. Local validation errors
	// must remain non-retryable even when their status is configured (for
	// example, HTTP 400).
	if err.GetErrorType() != types.ErrorTypeNewAPIError && operation_setting.ShouldRetryByStatusCode(code) {
		if decision, guarded := pinnedOfficialVerdictDecision(c, err); guarded {
			return decision
		}
		return PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}
	}
	return PolicyDecision{Action: "stop", Reason: "status_not_retryable", Source: "global"}
}

// isLocalFinalRefusal reports whether a locally generated error is the
// platform's own final answer (billing/quota bookkeeping), as opposed to a
// conversion error that may describe a channel capability gap.
func isLocalFinalRefusal(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	switch err.GetErrorCode() {
	case types.ErrorCodeInsufficientUserQuota,
		types.ErrorCodePreConsumeTokenQuotaFailed,
		types.ErrorCodeUpdateDataError:
		return true
	}
	return false
}

func ShouldRetryRelayError(c *gin.Context, openaiErr *types.NewAPIError, retryTimes int) bool {
	return DecideRelayRetry(c, openaiErr, retryTimes).Action == "retry"
}

// officialFitPinKeepsVerdict reports whether a retry-keyword match must NOT
// trigger channel failover because the request is pinned to an official-fit
// channel and retrying cannot produce a different answer.
//
// Why this is needed: the operator keyword list is written for aggregator
// traffic, where one upstream's wording is a channel capability gap that another
// channel can serve — that is the entire point of the list. An official-fit
// request is a different kind of traffic. The pin narrows channel selection to
// the channels that are official-behaving for the model
// (model.preferOfficialFitChannels), and a family may have a single such channel
// (Kimi K3, GLM 5.3). The official channel answered with a verdict, so this is
// not an outage: a retry may now fall through to the ordinary priority pool
// (the 2026-10-19 empty-match policy), which is an unverified upstream that
// answers in its own wording — the exact opposite of what the pin exists for
// (the fit contract promises the official verdict passes through verbatim).
// Stopping keeps that verdict intact; availability fallback applies to channel
// outages, not to answers. A retry that does run out of candidates answers with
// this upstream error rather than a routing error
// (controller/relay.go:answerRetrySelectionFailure), so the verdict still reaches
// the client; what this guard prevents is a *successful* non-official answer in
// another upstream's wording.
//
// The marker is written by the fit-policy layer alone
// (middleware.applyFitPolicy, reached through
// markV4OfficialPinFromDistributor): the request must be on the exact scoped
// path, the model must belong to a registered official-fit family, the user's
// Route dimension must be on, and the live policy document must have an opinion
// about this shape. A request bound by an explicit token or origin-task pin
// never carries it — those stop earlier in this function through the
// specific_channel_id and constraint checks, which are their own contract. That
// makes the pin flag sufficient evidence here; no model lookup is needed.
//
// Credential rotation is the one retry that keeps this contract: the next key of
// the same official channel is the same official endpoint, so the request is
// still served the official way. It is therefore allowed — but only while another
// enabled credential actually remains (see anotherCredentialAvailable), because
// a one-key channel reported as multi-key would otherwise consume the retry,
// exhaust immediately, and reach the same routing error through the back door.
func officialFitPinKeepsVerdict(c *gin.Context, err *types.NewAPIError) bool {
	if c == nil || err == nil {
		return false
	}
	if !common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin) {
		return false
	}
	// Rotation keeps the contract: the next key of the same official channel is
	// the same official endpoint. Prefer it whenever one is actually left.
	if ShouldRotateMultiKeyCredentialOn(err.StatusCode, err.Error()) && anotherCredentialAvailable(c) {
		return false
	}
	// A capability gap is a statement about THIS channel (it cannot convert or
	// serve the shape), not a verdict about the request: another channel may
	// serve it, so the pin must not turn the gap into the customer's answer.
	// The keyword list explicitly carries these wordings for that reason; this
	// exemption keeps them working in `on` mode as well, where the guard is
	// enforced.
	if isChannelCapabilityGap(err) {
		return false
	}
	// Availability escape hatch (2026-10-10 policy change, requested by the
	// operator): a credential/quota failure means the pinned channel has no
	// usable key or no remaining balance, so the error it carries is not an
	// official verdict at all — there is no answer for the fit contract to
	// protect. Keeping the verdict here only converts "the channel is out of
	// capacity" into "the customer sees the outage", while failing over can
	// still serve the request (on a non-official channel, i.e. at the cost of
	// the fidelity promise). The operator chose success rate over fidelity for
	// this case: 「必要时可牺牲拟合换成功率」. Rotation already took precedence
	// above, so a channel with a spare key still keeps serving officially.
	if isCredentialExhaustedFailure(err) {
		return false
	}
	return true
}

// pinnedOfficialVerdictDecision is the status-code half of the guard the keyword
// branch applies above. It exists because the same failure the keyword guard was
// written for is reachable through the failover status list: the operator's list
// contains 400, and the GLM 5.3 family is pinned by the live policy for every
// request.
//
// A pinned request must not fail over on a verdict. The official channel
// answered, so the pin is not out of capacity: retrying either reaches a second
// official channel (same verdict contract) or, since the 2026-10-19 empty-match
// policy, falls through to the ordinary priority pool and replaces the official
// verdict with an unverified answer — the exact opposite of what the pin exists
// for. (If the retry instead runs out of candidates it answers with the official
// verdict itself, so the case this guard removes is the successful fallback.)
// Only verdict statuses qualify: a channel outage (5xx, 408, 429) is not an
// answer, so failover stays available there and the operator's capacity policy
// (「必要时可牺牲拟合换成功率」) is untouched.
//
// The second return value reports whether the guard decided anything; false
// means the caller keeps its configured failover behaviour, which is also what
// shadow mode returns after recording what it would have done.
func pinnedOfficialVerdictDecision(c *gin.Context, err *types.NewAPIError) (PolicyDecision, bool) {
	if err == nil || !operation_setting.IsVerdictStatusCode(err.StatusCode) {
		return PolicyDecision{}, false
	}
	if !officialFitPinKeepsVerdict(c, err) {
		return PolicyDecision{}, false
	}
	switch operation_setting.RetryErrorFidelityModeOrDefault() {
	case operation_setting.RetryErrorFidelityOn:
		return PolicyDecision{Action: "stop", Reason: "pinned_official_verdict", Source: "channel_constraint"}, true
	case operation_setting.RetryErrorFidelityShadow:
		// Shadow keeps the decision and records the evidence. The row that makes
		// this reviewable on live traffic is the routing-fidelity error log
		// written when the retry then finds no candidate
		// (recordNoCandidateRetryError in controller/relay.go), which carries the
		// same request_id as the upstream verdict it displaced.
		logger.LogInfo(c, fmt.Sprintf(
			"retry-fidelity(shadow): pinned request would keep the upstream verdict instead of failing over (status=%d, code=%s, upstream=%q)",
			err.StatusCode, err.GetErrorCode(), common.LocalLogPreview(err.Error())))
	}
	return PolicyDecision{}, false
}

// isChannelCapabilityGap reports an error that says "this channel cannot do
// this" rather than "the request is invalid". Failover may find a channel that
// can, so the official-fit guard must not preserve it as a verdict.
func isChannelCapabilityGap(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	switch err.GetErrorCode() {
	case types.ErrorCodeConvertRequestFailed,
		types.ErrorCodeChannelUnsupportedEndpoint,
		types.ErrorCodeChannelUnsupportedFeature:
		return true
	}
	return false
}

// credentialExhaustedKeywords are the wordings upstreams use when the channel
// itself has run out of usable credentials or quota. They are deliberately
// phrased at the credential/quota level ("no auth available", "usage limit")
// rather than at the answer level, because the escape hatch above must not fire
// on errors an official channel can legitimately produce about the request.
var credentialExhaustedKeywords = []string{
	"auth_unavailable",
	"no auth available",
	"no available auth",
	"insufficient credits",
	"credit insufficient",
	"insufficient balance",
	"balance insufficient",
	"insufficient_quota",
	"insufficient quota",
	"exceeded your current quota",
	"usage limit",
	"quota exceeded",
	"没有可用的计费资源",
	"额度不足",
	"余额不足",
	// Account-pool exhaustion (measured 2026-10-21 on the WorkBuddy-backed
	// channel and its resellers): every credential of the channel is
	// temporarily unavailable. It is exactly the credential-level condition the
	// escape hatch exists for, so a pinned request may fail over and still
	// serve instead of surfacing the pool outage.
	"all accounts are temporarily unavailable",
	"no_healthy_account",
	"no healthy account",
	"temporarily unavailable",
	"暂时不可用",
}

// isCredentialExhaustedFailure reports whether an upstream error means the
// channel cannot authenticate or has no quota left. Only statuses an upstream
// uses for that condition qualify, and the wording list above must match, so a
// generic 5xx (transient overload) still keeps the official verdict. Locally
// generated errors are excluded: the operator keyword list contains 额度/余额,
// which also matches this platform's own billing text (用户额度不足, a 403 from
// billing_session.go). That is not a channel credential failure — the pinned
// channel was never tried — so failing over cannot help and would only replace
// the platform's own 403 with a routing error when no second official channel
// exists. Upstream errors carry ErrorTypeOpenAIError/ErrorTypeClaudeError, so
// the guard costs the real escape path nothing.
// IsEngineOverloadFailure reports a failure that belongs to the upstream engine
// or service as a whole rather than to one credential: "The engine is currently
// overloaded" and "service temporarily unavailable" arrive identically on every
// key of the channel, so rotating credentials cannot help — the channel has to
// step aside and the request fail over (2026-10-29: during one official 429
// storm, 2098 attempt-level 429s in 30 minutes burned every retry on the same
// channel and 165 requests in ten minutes reached the caller as 429).
func IsEngineOverloadFailure(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	for _, keyword := range []string{
		"engine is currently overloaded",
		"currently overloaded",
		"service is temporarily unavailable",
		"temporarily unavailable",
		"server overloaded",
		"overloaded, please try again",
	} {
		if strings.Contains(msg, keyword) {
			return true
		}
	}
	return false
}

// shouldRelaxOfficialPinForAvailability reports whether a pinned request must
// give up the official-fit narrowing for its remaining attempts: a non-verdict
// failure (the upstream could not answer at all) with no sibling credential left
// to rotate to. Verdicts keep the contract; capacity outages take the fallback.
func shouldRelaxOfficialPinForAvailability(c *gin.Context, err *types.NewAPIError) bool {
	if c == nil || err == nil || err.StatusCode <= 0 {
		return false
	}
	if !common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin) {
		return false
	}
	if operation_setting.IsVerdictStatusCode(err.StatusCode) {
		return false
	}
	// An engine-wide overload is not fixed by rotating to a sibling key: the
	// upstream refuses every credential the same way, so the pin must step aside
	// immediately instead of spending the attempt budget on the same engine.
	if IsEngineOverloadFailure(err.Error()) {
		return true
	}
	return !anotherCredentialAvailable(c)
}

// dropsStickyRoutingAfterFailure reports whether this failure invalidates the
// channel/key bindings recorded for the conversation. Every upstream verdict
// does; a local validation error (no channel was contacted) does not, and the
// caller of ClearCurrentChannelAffinityCache would be a no-op anyway.
func dropsStickyRoutingAfterFailure(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	if err.StatusCode >= http.StatusBadRequest {
		return true
	}
	return types.IsChannelError(err)
}

func isCredentialExhaustedFailure(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	if err.GetErrorType() == types.ErrorTypeNewAPIError {
		return false
	}
	switch err.StatusCode {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden,
		http.StatusTooManyRequests, http.StatusServiceUnavailable:
	default:
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, keyword := range credentialExhaustedKeywords {
		if strings.Contains(msg, keyword) {
			return true
		}
	}
	return false
}

// anotherCredentialAvailable reports whether the channel selected for this
// request still holds an enabled credential that this request has not used. It
// mirrors the selection side (model.selectMultiKeyCredential): for each key of
// the channel, skip the ones the status list marks disabled and the ones this
// request already tried, and report whether anything is left. A disabled
// credential is skipped rather than counted because rotation is only real when
// the next key can actually be handed to the upstream.
func anotherCredentialAvailable(c *gin.Context) bool {
	if !common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) {
		return false
	}
	channelID := common.GetContextKeyInt(c, constant.ContextKeyChannelId)
	if channelID <= 0 {
		return false
	}
	channel, err := model.CacheGetChannel(channelID)
	if err != nil || channel == nil || !channel.ChannelInfo.IsMultiKey {
		return false
	}
	keys := channel.Keys
	if len(keys) == 0 {
		keys = channel.GetKeys()
	}
	if len(keys) == 0 {
		return false
	}
	tried, _ := common.GetContextKeyType[map[int]map[string]struct{}](c, constant.ContextKeyChannelMultiKeyTried)
	triedForChannel := tried[channelID]
	statusList := channel.ChannelInfo.MultiKeyStatusList
	for index, key := range keys {
		if status, ok := statusList[index]; ok && status != common.ChannelStatusEnabled {
			continue
		}
		if _, used := triedForChannel[model.ChannelCredentialFingerprint(key)]; used {
			continue
		}
		return true
	}
	return false
}

func ProcessChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	if err == nil {
		return
	}
	// An upstream failure invalidates the affinity binding. This is shared by
	// HTTP and Responses WebSocket relay paths; otherwise a persistent WS retry
	// can keep selecting the same broken channel until the affinity TTL expires.
	EvictChannelAffinityOnUpstreamFailure(c, err)
	logger.LogError(c, fmt.Sprintf("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.MaskSensitiveErrorWithStatusCode())))
	if ShouldDisableChannel(err) && channelError.AutoBan {
		reason := err.MaskSensitiveErrorWithStatusCode()
		gopool.Go(func() {
			DisableChannelBecauseRelayError(channelError, reason)
		})
	}

	if constant.ErrorLogEnabled && types.IsRecordErrorLog(err) {
		userId := c.GetInt("id")
		tokenName := c.GetString("token_name")
		modelName := c.GetString("original_model")
		tokenId := c.GetInt("token_id")
		userGroup := c.GetString("group")
		other := model.NewLogOther()
		if c.Request != nil && c.Request.URL != nil {
			other.SetPublic("request_path", c.Request.URL.Path)
		}
		other.SetPublic("error_type", err.GetErrorType())
		other.SetPublic("error_code", err.GetErrorCode())
		other.SetPublic("status_code", err.StatusCode)
		AppendRelayLogAdminInfo(c, relayInfo, other)
		AppendResponseModelLogInfo(relayInfo, other)
		AppendTaskPluginContextAuditInfo(c, other)
		startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
		if startTime.IsZero() {
			startTime = time.Now()
		}
		useTimeSeconds := int(time.Since(startTime).Seconds())
		model.RecordErrorLog(c, userId, channelError.ChannelId, modelName, tokenName, err.MaskSensitiveErrorWithStatusCode(), tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
	}
}
