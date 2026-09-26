package middleware

import (
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/gin-gonic/gin"
)

// Shadow observation for the hot-reloadable fit policy.
//
// Shadow mode answers one question: does the data layer reproduce the shipped
// Go predicate? It therefore never touches routing — it evaluates the policy
// next to the predicate and reports only divergences. The built-in policy is
// proven equivalent by middleware/fitpolicy_equivalence_test.go, so in a healthy
// deployment this counter stays at zero and the log stays quiet; a non-zero
// count means a policy edit changed behaviour and must be investigated before
// the layer is allowed to make routing decisions.
//
// The trace carries identifiers and behaviour names only. Request bodies,
// messages, tools and credentials must never reach the log.

const (
	// fitPolicyDivergenceLogLimit bounds how many individual divergences are
	// logged, so a systematically wrong policy costs a bounded number of lines
	// instead of one per request.
	fitPolicyDivergenceLogLimit = 20
	// fitPolicyDivergenceSampleEvery logs every Nth divergence after the limit
	// so a long-running bad policy is still visible.
	fitPolicyDivergenceSampleEvery = 1000
)

var (
	fitPolicyDivergenceCount    atomic.Int64
	fitPolicyLastRequirementLog atomic.Int64
)

// observeFitPolicyShadow evaluates the policy beside the legacy pin decision.
// legacyPinned is the decision the request will actually use, so a mismatch is
// exactly the set of requests whose routing would change if the policy were
// promoted out of shadow.
func observeFitPolicyShadow(c *gin.Context, req v4OfficialPinRequest, routeEnabled, legacyPinned bool) {
	snapshot := fitpolicy.Current()
	if snapshot == nil || !snapshot.Enabled() {
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
	if requirement.HasOpinion() == legacyPinned {
		return
	}
	reportFitPolicyDivergence(c, req.Model, requirement, legacyPinned, snapshot)
}

func reportFitPolicyDivergence(c *gin.Context, model string, requirement fitpolicy.Requirement, legacyPinned bool, snapshot *fitpolicy.Snapshot) {
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
		"policy_requires=" + boolText(requirement.HasOpinion()),
		"required_marks=" + strings.Join(requirement.Marks, ","),
		"policy_version=" + itoa(requirement.PolicyVersion),
		"policy_hash=" + requirement.PolicyHash,
		"total=" + itoa64(total),
		"shadow=" + boolText(snapshot.Shadow()),
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
