package fitpolicy

import (
	"fmt"
	"regexp"
	"strings"
)

// Suite report intake.
//
// A capability report is the only way a measured result reaches the capability
// table, so it is validated before anything is written. Two properties matter
// more than the rest:
//
//  1. The report is bound to the policy and baseline it was measured against.
//     A report produced for a different rule set describes behaviour under rules
//     that no longer exist; applying it would mark channels as verified against
//     a requirement that has since changed.
//  2. The shape is bounded. The report arrives over HTTP, so its size and field
//     shapes are limited here rather than trusted, and a malformed field is
//     rejected instead of being coerced into a mark.

const (
	// MaxSuiteReportResults bounds one report. A report covering every channel
	// of every family is still well under this; anything larger is a mistake or
	// an attack, not a measurement.
	MaxSuiteReportResults = 2048
	// MaxSuiteReportRounds bounds the repeated rounds a report may claim.
	MaxSuiteReportRounds = 1000
	// maxSuiteReportFieldLength bounds the free-text identifiers.
	maxSuiteReportFieldLength = 128
)

// suiteBehaviorPattern is the accepted behaviour-identifier shape. It is
// deliberately strict: a typo would otherwise create a mark nothing ever reads,
// and the channel would look verified for a behaviour that was never measured.
var suiteBehaviorPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

// SuiteReport is one suite run's measured results.
type SuiteReport struct {
	ReportID      string        `json:"report_id"`
	RunID         string        `json:"run_id"`
	Suite         string        `json:"suite"`
	PolicyVersion int           `json:"policy_version"`
	PolicyHash    string        `json:"policy_hash"`
	BaselineHash  string        `json:"baseline_hash"`
	GeneratedAt   int64         `json:"generated_at"`
	Rounds        int           `json:"rounds"`
	Results       []SuiteResult `json:"results"`
}

// SuiteResult is one measured behaviour for one channel and model.
type SuiteResult struct {
	ChannelID int    `json:"channel_id"`
	Family    string `json:"family"`
	Model     string `json:"model"`
	Behavior  string `json:"behavior"`
	Supported bool   `json:"supported"`
	Cases     string `json:"cases"`
	// ExpiresAt is optional. A suite result normally has no explicit expiry and
	// ages out through the freshness window instead.
	ExpiresAt int64 `json:"expires_at"`
}

// ReportBinding is what the receiving node requires a report to have been
// measured against. An empty PolicyHash or BaselineHash means "do not check
// this field".
type ReportBinding struct {
	PolicyVersion int
	PolicyHash    string
	BaselineHash  string
}

// ValidateSuiteReport checks the shape of a report and that it matches the
// binding in force. It returns the first problem it finds rather than a list,
// because a report is applied or rejected as a unit.
func ValidateSuiteReport(report *SuiteReport, binding ReportBinding) error {
	if report == nil {
		return fmt.Errorf("fitpolicy: report is empty")
	}
	if err := requireBoundedField("report_id", report.ReportID); err != nil {
		return err
	}
	if err := requireBoundedField("run_id", report.RunID); err != nil {
		return err
	}
	if err := requireBoundedField("suite", report.Suite); err != nil {
		return err
	}
	if report.Rounds <= 0 {
		return fmt.Errorf("fitpolicy: report rounds must be positive")
	}
	if report.Rounds > MaxSuiteReportRounds {
		return fmt.Errorf("fitpolicy: report rounds %d exceeds the limit of %d", report.Rounds, MaxSuiteReportRounds)
	}
	if len(report.Results) == 0 {
		return fmt.Errorf("fitpolicy: report carries no results")
	}
	if len(report.Results) > MaxSuiteReportResults {
		return fmt.Errorf("fitpolicy: report carries %d results, above the limit of %d", len(report.Results), MaxSuiteReportResults)
	}

	// Binding first: a mismatched report is stale as a whole, and reporting a
	// per-result shape error for it would be misleading.
	if binding.PolicyVersion != 0 && report.PolicyVersion != binding.PolicyVersion {
		return fmt.Errorf("fitpolicy: report was measured against policy version %d, but %d is live", report.PolicyVersion, binding.PolicyVersion)
	}
	if binding.PolicyHash != "" && report.PolicyHash != binding.PolicyHash {
		return fmt.Errorf("fitpolicy: report was measured against policy hash %s, but %s is live", report.PolicyHash, binding.PolicyHash)
	}
	if binding.BaselineHash != "" && report.BaselineHash != binding.BaselineHash {
		return fmt.Errorf("fitpolicy: report was measured against baseline %s, but %s is in force", report.BaselineHash, binding.BaselineHash)
	}

	seen := make(map[string]struct{}, len(report.Results))
	for i := range report.Results {
		result := &report.Results[i]
		if result.ChannelID <= 0 {
			return fmt.Errorf("fitpolicy: result %d has no channel_id", i)
		}
		if err := requireBoundedField("model", result.Model); err != nil {
			return fmt.Errorf("fitpolicy: result %d: %w", i, err)
		}
		if !IsRegisteredFamily(result.Family) {
			return fmt.Errorf("fitpolicy: result %d names unregistered family %q", i, result.Family)
		}
		behavior := normalizeBehavior(result.Behavior)
		if len(behavior) > 64 || !suiteBehaviorPattern.MatchString(behavior) {
			return fmt.Errorf("fitpolicy: result %d has malformed behavior %q", i, result.Behavior)
		}
		if len(result.Cases) > 32 {
			return fmt.Errorf("fitpolicy: result %d has an over-long cases field", i)
		}
		key := fmt.Sprintf("%d\x00%s\x00%s\x00%s", result.ChannelID, strings.ToLower(result.Family), strings.ToLower(result.Model), behavior)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("fitpolicy: result %d repeats channel %d / %s / %s", i, result.ChannelID, result.Model, behavior)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// normalizeBehavior lowers and trims a behaviour name so validation and the
// database agree on the stored form.
func normalizeBehavior(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// NormalizeSuiteReport lowercases the keys the capability table normalizes too,
// so a validated report applies to exactly the rows it was validated against.
func NormalizeSuiteReport(report *SuiteReport) {
	if report == nil {
		return
	}
	for i := range report.Results {
		report.Results[i].Family = strings.ToLower(strings.TrimSpace(report.Results[i].Family))
		report.Results[i].Model = strings.ToLower(strings.TrimSpace(report.Results[i].Model))
		report.Results[i].Behavior = normalizeBehavior(report.Results[i].Behavior)
	}
}

func requireBoundedField(name, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("fitpolicy: %s is required", name)
	}
	if len(trimmed) > maxSuiteReportFieldLength {
		return fmt.Errorf("fitpolicy: %s exceeds %d characters", name, maxSuiteReportFieldLength)
	}
	return nil
}
