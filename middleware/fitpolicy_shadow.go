package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

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

	// fitPolicyDivergenceLogLimit bounds how many individual divergences are
	// logged, so a systematically wrong policy costs a bounded number of lines
	// instead of one per request.
	fitPolicyDivergenceLogLimit = 20
	// fitPolicyDivergenceSampleEvery logs every Nth divergence after the limit
	// so a long-running bad policy is still visible.
	fitPolicyDivergenceSampleEvery = 1000
)

var fitPolicyDivergenceCount atomic.Int64

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

// applyFitPolicy evaluates the policy for one request, records a shadow
// divergence when the policy disagrees with the shipped predicate, and — only
// outside shadow — attaches the requirement for the selection path to consume.
func applyFitPolicy(c *gin.Context, req v4OfficialPinRequest, routeEnabled, legacyPinned bool) {
	snapshot := fitpolicy.Current()
	if snapshot == nil || !snapshot.Enabled() {
		return
	}
	if !fitPolicyInScope(c) || fitPolicyExplicitPinActive(c) {
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
		return
	}
	if requirement.Shadow {
		if requirement.HasOpinion() != legacyPinned {
			reportFitPolicyDivergence(c, req.Model, requirement, legacyPinned)
		}
		return
	}
	common.SetContextKey(c, constant.ContextKeyFitRequirement, requirement)
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

func reportFitPolicyDivergence(c *gin.Context, model string, requirement fitpolicy.Requirement, legacyPinned bool) {
	total := fitPolicyDivergenceCount.Add(1)
	if total > fitPolicyDivergenceLogLimit && total%fitPolicyDivergenceSampleEvery != 0 {
		return
	}
	requestID := ""
	if c != nil {
		requestID = c.GetString(common.RequestIdKey)
	}
	common.SysLog(strings.Join([]string{
		"fitpolicy shadow divergence:",
		"request_id=" + requestID,
		"model=" + model,
		"family=" + requirement.Family,
		"legacy_pinned=" + boolText(legacyPinned),
		"policy_requires=true",
		"required_marks=" + strings.Join(requirement.Marks, ","),
		"policy_version=" + itoa(requirement.PolicyVersion),
		"policy_hash=" + requirement.PolicyHash,
		"total=" + itoa64(total),
	}, " "))
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

func itoa64(value int64) string {
	return strconv.FormatInt(value, 10)
}
