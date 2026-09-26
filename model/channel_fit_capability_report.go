package model

import (
	"errors"

	"github.com/QuantumNous/new-api/pkg/fitpolicy"
)

// Suite report applier.
//
// A report is applied result by result, each under its own CAS. One conflicting
// item must not discard the rest of a run: a report covering many channels will
// routinely race with other writers, and failing the whole batch would mean a
// completed measurement is thrown away because one channel changed.
//
// The applier never sets Force. Replacing an operator's live manual mark is a
// deliberate human decision that requires the capability.force action, and a
// suite applier must not be able to reach that state even if a buggy or
// malicious report asks for it.

// FitCapabilityApplyOutcome records what happened to one report result.
type FitCapabilityApplyOutcome struct {
	ChannelId int    `json:"channel_id"`
	Family    string `json:"family"`
	Model     string `json:"model"`
	Behavior  string `json:"behavior"`
	Applied   bool   `json:"applied"`
	Revision  int64  `json:"revision,omitempty"`
	// Conflict is set when the item was rejected by the CAS or by the sticky
	// rule. It is not an error in the report; it means the stored state moved.
	Conflict string `json:"conflict,omitempty"`
	// Error is set when the item failed for a reason unrelated to CAS.
	Error string `json:"error,omitempty"`
}

// FitCapabilityReportSummary is the per-run outcome.
type FitCapabilityReportSummary struct {
	Applied   int                         `json:"applied"`
	Conflicts int                         `json:"conflicts"`
	Failed    int                         `json:"failed"`
	Outcomes  []FitCapabilityApplyOutcome `json:"outcomes"`
}

// ApplyFitCapabilityReport writes every result of a validated report.
//
// The caller must have validated the report first (shape and policy/baseline
// binding); this function assumes the contents are well formed and only handles
// the concurrent-state outcomes.
func ApplyFitCapabilityReport(report fitpolicy.SuiteReport, now int64) FitCapabilityReportSummary {
	measuredAt := report.GeneratedAt
	if measuredAt <= 0 {
		measuredAt = now
	}
	// Clamp a future timestamp. Freshness is computed as now - At, so a report
	// dated in the future would produce marks that never age out — a single bad
	// clock or a hand-edited report could make a measurement permanent.
	if measuredAt > now {
		measuredAt = now
	}
	summary := FitCapabilityReportSummary{Outcomes: make([]FitCapabilityApplyOutcome, 0, len(report.Results))}
	for i := range report.Results {
		result := &report.Results[i]
		outcome := FitCapabilityApplyOutcome{
			ChannelId: result.ChannelID,
			Family:    result.Family,
			Model:     result.Model,
			Behavior:  result.Behavior,
		}

		// Read for the current revision, then let the CAS catch anything that
		// changes between this read and the update.
		current, found, err := GetChannelFitCapability(result.ChannelID, result.Family, result.Model, result.Behavior)
		if err != nil {
			outcome.Error = err.Error()
			summary.Failed++
			summary.Outcomes = append(summary.Outcomes, outcome)
			continue
		}
		expected := int64(0)
		if found {
			expected = current.Revision
		}

		row, err := ApplyChannelFitCapability(FitCapabilityWrite{
			ChannelId:        result.ChannelID,
			Family:           result.Family,
			Model:            result.Model,
			Behavior:         result.Behavior,
			Supported:        result.Supported,
			Source:           FitCapabilitySourceSuite,
			Suite:            report.Suite,
			Cases:            result.Cases,
			Rounds:           report.Rounds,
			At:               measuredAt,
			ExpiresAt:        result.ExpiresAt,
			PolicyVersion:    report.PolicyVersion,
			PolicyHash:       report.PolicyHash,
			BaselineHash:     report.BaselineHash,
			ReportId:         report.ReportID,
			RunId:            report.RunID,
			Force:            false,
			ExpectedRevision: expected,
		}, now)
		if err != nil {
			var conflict *FitCapabilityConflictError
			if errors.As(err, &conflict) {
				outcome.Conflict = conflict.Reason
				summary.Conflicts++
			} else {
				outcome.Error = err.Error()
				summary.Failed++
			}
			summary.Outcomes = append(summary.Outcomes, outcome)
			continue
		}
		outcome.Applied = true
		if row != nil {
			outcome.Revision = row.Revision
		}
		summary.Applied++
		summary.Outcomes = append(summary.Outcomes, outcome)
	}
	return summary
}
