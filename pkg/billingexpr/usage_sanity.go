package billingexpr

import (
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
)

// MaxPlausibleTokensPerField is the loose upper bound for any single token
// dimension in a settlement input. It is 10x the largest legitimate context we
// have ever seen, so it never rejects honest usage while still catching the
// garbage counts upstream bugs can report (e.g. 2026-10-13 kimi-k3 returned
// prompt_tokens=1.39e15 and completion_tokens=5.6e14).
const MaxPlausibleTokensPerField = 10_000_000

// UsageAnomalyKind identifies which sanity gate rejected the billing input.
type UsageAnomalyKind string

const (
	// UsageAnomalyOutOfRange marks a token field that is negative, exceeds
	// MaxPlausibleTokensPerField, or is NaN.
	UsageAnomalyOutOfRange UsageAnomalyKind = "usage_out_of_range"
	// UsageAnomalyQuotaSaturation marks a quota conversion that clamped
	// (overflow/underflow/NaN): the clamped value must not be charged as-is.
	UsageAnomalyQuotaSaturation UsageAnomalyKind = "quota_saturation"
)

// UsageAnomaly describes one rejected billing input. It is attached to the
// consume log's other.admin_info.billing_anomaly so admins can investigate
// while the request itself is billed as zero.
type UsageAnomaly struct {
	Kind      UsageAnomalyKind `json:"kind"`
	Field     string           `json:"field"`
	Value     float64          `json:"value"`
	Threshold float64          `json:"threshold"`
}

// AuditMap renders the anomaly as the marker stored under a log's
// admin_info.billing_anomaly, mirroring common.QuotaClamp.AuditMap.
func (a *UsageAnomaly) AuditMap() map[string]any {
	if a == nil {
		return nil
	}
	return map[string]any{
		"kind":      a.Kind,
		"field":     a.Field,
		"value":     a.Value,
		"threshold": a.Threshold,
	}
}

// Error lets the anomaly double as the message for backend audit logging.
func (a *UsageAnomaly) Error() string {
	if a == nil {
		return ""
	}
	return fmt.Sprintf("billing usage anomaly (%s): field=%s value=%g threshold=%g", a.Kind, a.Field, a.Value, a.Threshold)
}

// tokenParamFields lists every TokenParams dimension that feeds billing, in
// field-name / getter pairs. Any field being out of range marks the whole
// settlement as anomalous.
var tokenParamFields = []struct {
	name  string
	value func(TokenParams) float64
}{
	{"p", func(tp TokenParams) float64 { return tp.P }},
	{"c", func(tp TokenParams) float64 { return tp.C }},
	{"len", func(tp TokenParams) float64 { return tp.Len }},
	{"cr", func(tp TokenParams) float64 { return tp.CR }},
	{"cc", func(tp TokenParams) float64 { return tp.CC }},
	{"cc1h", func(tp TokenParams) float64 { return tp.CC1h }},
	{"img", func(tp TokenParams) float64 { return tp.Img }},
	{"img_cr", func(tp TokenParams) float64 { return tp.ImgCR }},
	{"img_o", func(tp TokenParams) float64 { return tp.ImgO }},
	{"ai", func(tp TokenParams) float64 { return tp.AI }},
	{"ao", func(tp TokenParams) float64 { return tp.AO }},
}

// CheckTokenParamsSanity validates settlement inputs before they become a
// charge. Any token dimension that is negative, NaN, or above
// MaxPlausibleTokensPerField marks the usage as anomalous: the caller must
// bill zero (recording this anomaly) instead of computing a quota from it.
// A nil return means the inputs are plausible and billing proceeds normally.
func CheckTokenParamsSanity(params TokenParams) *UsageAnomaly {
	for _, field := range tokenParamFields {
		if anomaly := CheckTokenCount(field.name, field.value(params)); anomaly != nil {
			return anomaly
		}
	}
	return nil
}

// CheckTokenCount applies the shared plausibility bound to one token
// dimension. It is the single source of truth for "this count cannot be real":
// the settlement gate and the client-view sanitation both use it, so the count
// a customer is shown and the count we bill from can never disagree about what
// is garbage. field is the wire name recorded in the audit marker.
func CheckTokenCount(field string, value float64) *UsageAnomaly {
	if value < 0 || value > MaxPlausibleTokensPerField || math.IsNaN(value) {
		return &UsageAnomaly{
			Kind:      UsageAnomalyOutOfRange,
			Field:     field,
			Value:     value,
			Threshold: MaxPlausibleTokensPerField,
		}
	}
	return nil
}

// SaturationAnomaly converts a non-nil quota clamp into the same anomaly
// shape used for out-of-range usage, so both gates share one zeroing path.
func SaturationAnomaly(clamp *common.QuotaClamp) *UsageAnomaly {
	if clamp == nil {
		return nil
	}
	return &UsageAnomaly{
		Kind:      UsageAnomalyQuotaSaturation,
		Field:     "quota",
		Value:     clamp.Original,
		Threshold: common.MaxQuota,
	}
}
