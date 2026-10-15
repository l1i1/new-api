package billingexpr

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/officialfit"
	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// L2-A usage-deviation guard (alarm-only). INC-20261013-02: the neurvibe
// upstream's continuation retries accumulate k rounds of internal usage into
// one response. The k-times values stay plausible (they never trip the L1
// sanity gate), so this guard compares the upstream-reported usage against
// what the request itself allows and flags the deviation WITHOUT changing
// billing. Phase B (billing effect) is deliberately not implemented here.
// ---------------------------------------------------------------------------

// DeviationPromptTolerance is how far upstream prompt_tokens may exceed the
// local estimate before the row is flagged. Cache hits can make the
// upstream's number SMALLER, never larger, and tokenizers differ by a few
// percent, so 20% headroom (plan §2.2 criterion 1) keeps honest traffic
// quiet. Zero local estimate disables the check (no baseline, no verdict).
//
// The 20% band was calibrated on the kimi-k3 incident traffic and on the
// kimi-k3 tokenizer, which is why criterion 1 is scoped to that family (see
// CheckUsageDeviation): on other families the local estimator is known to be
// systematically biased (glm-5.3 measures ~0.695 of the upstream's count on
// large contexts, incident-20261014 night audit), so the same band reports
// estimator bias instead of accumulation.
const DeviationPromptTolerance = 0.20

// DeviationPromptAbsoluteFloor is the minimum absolute excess (upstream
// prompt_tokens minus the local estimate) criterion 1 requires. A pure ratio
// test fires on healthy short requests whose tokenizer difference is a
// handful of tokens (measured: upstream 13 vs local 9 on glm-5.3, ratio 1.44),
// which is estimator noise, not accumulation. Every accumulation observed in
// INC-20261013-02 carries thousands of surplus tokens, so the floor removes
// the noise without touching the signal.
const DeviationPromptAbsoluteFloor = 1024

// UsageDeviationKind identifies which guard criterion fired.
type UsageDeviationKind string

const (
	// DeviationPromptExceeded: upstream prompt_tokens exceeds the local
	// estimate by more than DeviationPromptTolerance.
	DeviationPromptExceeded UsageDeviationKind = "prompt_exceeded_estimate"
	// DeviationCompletionOverBudget: upstream completion_tokens exceeds the
	// client's declared output budget. Output can never legitimately exceed
	// the budget the request asked for (plan §2.2 criterion 2 — the cleanest
	// one: any excess is necessarily accumulation).
	DeviationCompletionOverBudget UsageDeviationKind = "completion_over_budget"
	// DeviationCacheMultiple: cache_tokens is an exact integer multiple
	// (k >= 2) of one of the payload family's known cache prefixes — the
	// k-fingerprint from the incident quantification.
	DeviationCacheMultiple UsageDeviationKind = "cache_integer_multiple"
)

// UsageDeviation describes one flagged settlement. It is attached to the
// consume log's other.admin_info.usage_deviation (same shape as
// billing_anomaly) and emits a backend warn, but never changes billing.
type UsageDeviation struct {
	Kind  UsageDeviationKind `json:"kind"`
	Field string             `json:"field"`
	Value float64            `json:"value"`
	// Baseline is the local number the value was compared against (local
	// prompt estimate or client max_tokens), when the criterion has one.
	Baseline float64 `json:"baseline,omitempty"`
}

// AuditMap renders the deviation as the marker stored under a log's
// admin_info.usage_deviation.
func (d *UsageDeviation) AuditMap() map[string]any {
	if d == nil {
		return nil
	}
	return map[string]any{
		"kind":     d.Kind,
		"field":    d.Field,
		"value":    d.Value,
		"baseline": d.Baseline,
		"action":   "observed",
	}
}

// Error lets the deviation double as the message for backend audit logging.
func (d *UsageDeviation) Error() string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("usage deviation (%s): field=%s value=%g baseline=%g", d.Kind, d.Field, d.Value, d.Baseline)
}

// cachePrefixFamily lists the modal cache prefixes seen in the affected
// customer's payload family (INC-02 §1.2 S3). A cache value that is an exact
// multiple (k>=2) of one of these with the small-completion shape is the
// accumulation fingerprint. Values outside this family simply never match.
//
// The family is a property of that one customer's kimi-k3 payload shape, not
// a universal constant: glm-5.3 upstreams report cache in 3584-token blocks,
// so an ordinary single-round glm call reports cache=7168 = 2 × 3584 and
// trips the fingerprint on 100% of its rows. Criterion 3 is therefore scoped
// to the kimi-k3 family (see CheckUsageDeviation).
var cachePrefixFamily = []float64{2560, 16384, 11008, 2816, 3584, 4096}

// CheckUsageDeviation evaluates the guard against one settlement input.
//
//   - modelName: the billing model name; it decides which criteria apply (see
//     the scoping note below);
//   - promptTokens / completionTokens / cacheTokens: the upstream-reported
//     usage the settlement is about to bill;
//   - localPromptEstimate: the platform's own tokenization of the request
//     (RelayInfo.GetEstimatePromptTokens()); 0 disables criterion 1;
//   - clientMaxTokens: the output budget the request declared
//     (max_tokens / max_completion_tokens / max_output_tokens, whichever the
//     format carries); 0 disables criterion 2.
//
// Scoping. Criteria 1 and 3 were calibrated on INC-20261013-02, a kimi-k3
// incident, and both are comparing against a baseline that only holds for
// that family: the local estimator's bias and the payload's cache block
// granularity. Applied to every model they measure the estimator and the
// upstream's cache block size instead of accumulation — in production that
// produced a permanent ~100% false-positive rate on glm-5.3-flash (cache
// 7168 = 2 × 3584 on every row) and ~1.4x ratio noise on glm-5.3, which
// drowned the guard's alarm in noise. Both are now restricted to the kimi-k3
// family. Criterion 2 (output above the client's declared budget) is a
// format invariant that holds for every model and stays global.
//
// The first matching criterion wins; nil means no deviation was observed and
// billing proceeds (and, in phase A, would also proceed when a deviation IS
// observed — the guard only flags).
func CheckUsageDeviation(modelName string, promptTokens, completionTokens, cacheTokens, localPromptEstimate, clientMaxTokens float64) *UsageDeviation {
	kimiK3 := officialfit.FamilyOf(modelName) == officialfit.FamilyKimiK3
	// Criterion 2 — the cleanest: output above the client's declared budget
	// can only be accumulation. Applies to both stream and non-stream (the
	// incident proved both amplify), and to every model — it is a property of
	// the request format, not of one upstream's accounting.
	if clientMaxTokens > 0 && completionTokens > clientMaxTokens {
		return &UsageDeviation{
			Kind:     DeviationCompletionOverBudget,
			Field:    "c",
			Value:    completionTokens,
			Baseline: clientMaxTokens,
		}
	}
	// Criterion 1 — upstream prompt exceeds the local estimate beyond the
	// tokenizer-difference headroom. Cache hits only ever shrink the
	// upstream's number, so a large positive gap means internal rounds were
	// accumulated into prompt_tokens. kimi-k3 only (see the scoping note):
	// both the ratio band and the absolute floor are calibrated for its
	// estimator.
	if kimiK3 && localPromptEstimate > 0 && promptTokens > localPromptEstimate*(1+DeviationPromptTolerance) &&
		promptTokens-localPromptEstimate >= DeviationPromptAbsoluteFloor {
		return &UsageDeviation{
			Kind:     DeviationPromptExceeded,
			Field:    "p",
			Value:    promptTokens,
			Baseline: localPromptEstimate,
		}
	}
	// Criterion 3 — cache k-fingerprint. Only meaningful together with the
	// small-completion shape (the trigger condition of the incident); a long
	// completion legitimately carries large cache values. kimi-k3 only (see
	// the scoping note): the prefix family is that payload's cache block
	// granularity, and other upstreams' block sizes land on it by chance.
	if kimiK3 && cacheTokens > 0 && completionTokens > 0 && completionTokens <= 300 {
		for _, prefix := range cachePrefixFamily {
			k := cacheTokens / prefix
			if k >= 2 && k == float64(int64(k)) {
				return &UsageDeviation{
					Kind:     DeviationCacheMultiple,
					Field:    "cr",
					Value:    cacheTokens,
					Baseline: prefix,
				}
			}
		}
	}
	return nil
}

// ReportUsageDeviation emits the backend warn for a flagged settlement. The
// marker itself is attached by the settlement path (same place L1 attaches
// billing_anomaly); this is the on-call-visible half.
func ReportUsageDeviation(ctx *gin.Context, d *UsageDeviation, userId int, modelName string) {
	if d == nil {
		return
	}
	msg := fmt.Sprintf("usage deviation observed (billing unchanged): %s user=%d model=%s", d.Error(), userId, modelName)
	if ctx != nil {
		logger.LogWarn(ctx, msg)
	} else {
		common.SysLog(msg)
	}
}
