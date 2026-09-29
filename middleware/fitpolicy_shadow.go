package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// Fit-policy application: scope gating, explicit-pin short circuit and shadow
// observation.
//
// Two rules shape this file:
//
//  1. Scope is decided here, once, and it is exact. The distributor middleware
//     serves many protocols, and the shipped pin predicate matches any path
//     ending in "/chat/completions" — which includes the playground route
//     "/pg/chat/completions". The policy must not leak there, so this file
//     gates on the exact path instead of reusing that suffix test.
//  2. In shadow the requirement is never attached to the request context. That
//     makes shadow inert by construction: a selector cannot act on a requirement
//     it cannot read, so no future consumer can accidentally narrow routing
//     before the equivalence window is over.
//
// The trace carries identifiers and behaviour names only. Request bodies,
// messages, tools and credentials must never reach the log.

const (
	// fitPolicyScopedPath is the only path the policy applies to: OpenAI chat
	// completions on the versioned API. The relay format is implied by the route
	// (relay-router.go maps exactly this path to RelayFormatOpenAI).
	fitPolicyScopedPath = "/v1/chat/completions"

	// fitPolicyShadowLogBurst is how many individual shadow decisions are logged
	// up front, so a dry run costs a bounded number of lines instead of one per
	// request. After the burst the site refills at fitPolicyShadowLogInterval, so
	// a long-running dry run stays visible instead of going permanently silent
	// once the quota is spent.
	fitPolicyShadowLogBurst    = 20
	fitPolicyShadowLogInterval = time.Minute

	// fitPolicyInertLogBurst/fitPolicyInertLogInterval bound the "Route is on but
	// nothing can act" alert. One line immediately, then at most one per
	// interval: a misconfiguration has to be loud, but not once per request, and
	// a state that is still broken an hour later must not have gone silent.
	fitPolicyInertLogBurst    = 1
	fitPolicyInertLogInterval = 5 * time.Minute
)

var (
	// fitPolicyShadowCount is the lifetime total of dry-run decisions. It is
	// reported alongside the sampled lines so a rate-limited line still says how
	// much traffic it stands for.
	fitPolicyShadowCount atomic.Int64
	fitPolicyShadowLog   = common.NewLogBudget(fitPolicyShadowLogBurst, fitPolicyShadowLogInterval)
	fitPolicyInertLog    = common.NewLogBudget(fitPolicyInertLogBurst, fitPolicyInertLogInterval)
)

// fitPolicyInScope reports whether this request may be governed by the policy.
// Everything else — the playground path, /v1/completions, /v1/messages,
// /v1/responses/compact, Gemini/embedding/audio/rerank, realtime WS, the
// Responses WebSocket, task and task-plugin selection — keeps today's
// behaviour.
func fitPolicyInScope(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	if c.Request.Method != http.MethodPost {
		return false
	}
	return c.Request.URL.Path == fitPolicyScopedPath
}

// fitPolicyExplicitPinActive reports whether the request is already bound to one
// channel by an explicit pin (token/origin-task) or a specific-channel id. Those
// branches select the channel before any fit evaluation and must keep their
// existing filter/policy/error/retry behaviour, so the policy stays out of them.
func fitPolicyExplicitPinActive(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if _, pinned := c.Get("specific_channel_id"); pinned {
		return true
	}
	if _, found, _ := service.GetChannelConstraints(c).ResolvedPin(); found {
		return true
	}
	return false
}

// applyFitPolicy evaluates the policy for one request and — only outside shadow —
// attaches the requirement for the selection path to consume.
//
// Shadow no longer means "compare against the shipped predicate": those
// predicates are retired from the request path, so there is no second opinion to
// disagree with. It now means "decide, record the decision, act on nothing",
// which is what makes a policy edit reviewable before it is allowed to route.
func applyFitPolicy(c *gin.Context, req v4OfficialPinRequest, routeEnabled bool) {
	// The observer is started here rather than at startup so a build that never
	// reaches this path does not run a timer, and so the very first request
	// already counts. Its counters are what make the returns below legible.
	startFitPolicyObserver()
	snapshot := fitpolicy.Current()
	if snapshot == nil {
		fitPolicyStats.noSnapshot.Add(1)
		alertFitPolicyInert(c, req.Model, routeEnabled, "no_snapshot")
		return
	}
	if !snapshot.Enabled() {
		fitPolicyStats.disabled.Add(1)
		alertFitPolicyInert(c, req.Model, routeEnabled, "disabled")
		return
	}
	if !fitPolicyInScope(c) {
		fitPolicyStats.outOfScope.Add(1)
		return
	}
	if fitPolicyExplicitPinActive(c) {
		fitPolicyStats.explicitPin.Add(1)
		return
	}
	requirement := snapshot.Decide(req.Model, routeEnabled, fitpolicy.RequestView{
		Model:           req.Model,
		LogProbs:        req.LogProbs,
		ReasoningEffort: req.ReasoningEffort,
		Thinking:        req.THINKING,
		ToolChoice:      req.ToolChoice,
		ResponseFormat:  req.ResponseFormat,
		Messages:        req.Messages,
	})
	if !requirement.HasOpinion() {
		fitPolicyStats.noOpinion.Add(1)
		return
	}
	fitPolicyStats.evaluated.Add(1)
	if requirement.Shadow {
		fitPolicyStats.shadowed.Add(1)
		alertFitPolicyInert(c, req.Model, routeEnabled, "shadow")
		reportFitPolicyShadow(c, req.Model, requirement)
		return
	}
	common.SetContextKey(c, constant.ContextKeyFitRequirement, requirement)
	// The pin flag is the same decision in the second shape the selection path
	// already reads, not a second decision.
	//
	// Four sites ask "is this request pinned to official behaviour?": the legacy
	// official narrowing in channel selection, the affinity gate and the affinity
	// recording rule in the distributor, and officialFitPinKeepsVerdict in
	// service/relay_error.go — the one that must keep the official verdict
	// instead of failing the request over to another channel. Since the
	// compiled-in predicates were retired nothing wrote this flag, so all four
	// took the unpinned branch: the retry guard stopped protecting the official
	// text (an operator retry keyword re-pinned the request, excluded the only
	// official channel, and answered 503 instead of the official body), and a
	// pinned request could reuse or leave behind an aggregator affinity binding.
	// Writing it here — where the requirement is decided — is what keeps those
	// read sites meaning what they say.
	//
	// The two are written together on purpose: the flag says "official behaviour
	// is required", and the requirement is the only producer of that statement
	// on this path. The one path that produces an explicit pin instead
	// (token/origin task or a specific channel id) returns above; it never
	// reaches selection with the official-fit pin as its meaning, and the retry
	// guard already answers it through the pin and constraint checks in
	// DecideRelayRetry.
	common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
}

// alertFitPolicyInert reports the one state where the layer is installed but can
// act on nothing while the user asked for it: the Route dimension is on for this
// model, so the request is supposed to be pinned to official behaviour, and yet
// no requirement can be attached. Every consequence of that state is silent —
// the request quietly routes like ordinary traffic and the retry guard is off —
// so a counter is not enough to notice it.
//
// The budget keeps one line immediate and the rest rare; the detailed counters
// stay available in the periodic observer summary for volume.
func alertFitPolicyInert(c *gin.Context, modelName string, routeEnabled bool, reason string) {
	if !routeEnabled || !fitPolicyInScope(c) {
		return
	}
	if !fitPolicyInertLog.Allow() {
		return
	}
	common.SysError("fitpolicy: official-fit Route is enabled but no requirement can be attached " +
		"(reason=" + reason + ", model=" + strconv.Quote(modelName) + "); the official-fit pin is off for these requests. " +
		"Write a live policy document to option " + fitpolicy.OptionKey + " (or remove the Route dimension until one exists).")
}

// fitPolicyAllowsAffinity reports whether an affinity-bound channel may be
// reused for this request.
//
// The affinity branch selects its channel directly instead of going through the
// selector, so it is the one fast path that could hand the request a channel the
// fit narrowing would have rejected. A binding is only meant to be a shortcut to
// the same candidate a fresh selection would return, so it has to clear the same
// fit requirement: official behaviour when the request needs it, plus every
// required mark once capability data exists. When it fails, the caller drops the
// binding and falls through to selection.
func fitPolicyAllowsAffinity(c *gin.Context, channelID int, modelName string) bool {
	filter := model.FitChannelFilterForRequest(c)
	if filter == nil || model.OfficialFitChannelType(modelName) == 0 {
		return true
	}
	if !model.ChannelIsOfficialFitForModel(channelID, modelName) {
		return false
	}
	if filter.MarkSatisfied != nil && !filter.MarkSatisfied(channelID) {
		return false
	}
	return true
}

// reportFitPolicyShadow records one shadow-mode decision. It carries identifiers,
// behaviour names and the mark set only — never request bodies, messages, tools
// or credentials.
func reportFitPolicyShadow(c *gin.Context, model string, requirement fitpolicy.Requirement) {
	total := fitPolicyShadowCount.Add(1)
	if !fitPolicyShadowLog.Allow() {
		return
	}
	requestID := ""
	if c != nil {
		requestID = c.GetString(common.RequestIdKey)
	}
	common.SysLog(strings.Join([]string{
		"fitpolicy shadow decision:",
		"request_id=" + requestID,
		// %q rather than a bare value: the model id comes from the request body,
		// so a newline in it would forge a second log line.
		"model=" + strconv.Quote(model),
		"family=" + requirement.Family,
		"required_marks=" + strings.Join(requirement.Marks, ","),
		"policy_version=" + itoa(requirement.PolicyVersion),
		"policy_hash=" + requirement.PolicyHash,
		"total=" + itoa64(total),
	}, " "))
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

func itoa64(value int64) string {
	return strconv.FormatInt(value, 10)
}
