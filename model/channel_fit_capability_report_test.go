package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reportWith(results ...fitpolicy.SuiteResult) fitpolicy.SuiteReport {
	return fitpolicy.SuiteReport{
		ReportID:      "report-1",
		RunID:         "run-1",
		Suite:         "cdp-k3",
		PolicyVersion: 1,
		PolicyHash:    "policy-a",
		BaselineHash:  "baseline-a",
		GeneratedAt:   1_700_000_000,
		Rounds:        3,
		Results:       results,
	}
}

func TestApplyFitCapabilityReportInsertsThenAdvancesRevisions(t *testing.T) {
	setupFitCapabilityDB(t)

	report := reportWith(
		fitpolicy.SuiteResult{ChannelID: 1, Family: "kimi-k3", Model: "kimi-k3", Behavior: "tools.dynamic_names", Supported: true, Cases: "30/30"},
		fitpolicy.SuiteResult{ChannelID: 2, Family: "kimi-k3", Model: "kimi-k3", Behavior: "tools.dynamic_names", Supported: false, Cases: "2/8"},
	)
	summary := ApplyFitCapabilityReport(report, 1_700_000_100)
	require.Equal(t, 2, summary.Applied)
	require.Equal(t, 0, summary.Conflicts)
	require.Equal(t, 0, summary.Failed)

	first, found, err := GetChannelFitCapability(1, "kimi-k3", "kimi-k3", "tools.dynamic_names")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(1), first.Revision)
	assert.True(t, first.Supported)
	assert.Equal(t, "cdp-k3", first.Suite)
	assert.Equal(t, "report-1", first.ReportId)
	assert.Equal(t, 3, first.Rounds)

	// A negative result is stored as a claim, not as absence.
	second, _, err := GetChannelFitCapability(2, "kimi-k3", "kimi-k3", "tools.dynamic_names")
	require.NoError(t, err)
	assert.False(t, second.Supported)

	// Re-applying the same run advances the revision through the read-then-CAS
	// path instead of failing as a duplicate insert.
	summary = ApplyFitCapabilityReport(report, 1_700_000_200)
	require.Equal(t, 2, summary.Applied)
	again, _, err := GetChannelFitCapability(1, "kimi-k3", "kimi-k3", "tools.dynamic_names")
	require.NoError(t, err)
	assert.Equal(t, int64(2), again.Revision)
}

// TestApplyFitCapabilityReportReportsConflictsPerItem pins that one conflicting
// item does not discard the rest of a completed run.
func TestApplyFitCapabilityReportReportsConflictsPerItem(t *testing.T) {
	setupFitCapabilityDB(t)

	// A live operator mark on channel 1; channel 2 is untouched.
	_, err := ApplyChannelFitCapability(FitCapabilityWrite{
		ChannelId: 1, Family: "kimi-k3", Model: "kimi-k3", Behavior: "usage.thinking_counting",
		Supported: true, Source: FitCapabilitySourceManual, At: 1_700_000_000, ExpectedRevision: 0,
	}, 1_700_000_100)
	require.NoError(t, err)

	report := reportWith(
		fitpolicy.SuiteResult{ChannelID: 1, Family: "kimi-k3", Model: "kimi-k3", Behavior: "usage.thinking_counting", Supported: false, Cases: "0/8"},
		fitpolicy.SuiteResult{ChannelID: 2, Family: "kimi-k3", Model: "kimi-k3", Behavior: "usage.thinking_counting", Supported: true, Cases: "8/8"},
	)
	summary := ApplyFitCapabilityReport(report, 1_700_000_200)

	assert.Equal(t, 1, summary.Applied, "the unaffected channel must still be recorded")
	assert.Equal(t, 1, summary.Conflicts)
	assert.Equal(t, 0, summary.Failed)

	var conflictOutcome *FitCapabilityApplyOutcome
	for i := range summary.Outcomes {
		if summary.Outcomes[i].ChannelId == 1 {
			conflictOutcome = &summary.Outcomes[i]
		}
	}
	require.NotNil(t, conflictOutcome)
	assert.False(t, conflictOutcome.Applied)
	assert.Contains(t, conflictOutcome.Conflict, "manual")

	// The operator's mark is untouched: an applier without force cannot replace it.
	stored, _, err := GetChannelFitCapability(1, "kimi-k3", "kimi-k3", "usage.thinking_counting")
	require.NoError(t, err)
	assert.Equal(t, FitCapabilitySourceManual, stored.Source)
	assert.True(t, stored.Supported)
	assert.Equal(t, int64(1), stored.Revision)

	// And the applier never sets force, so the stored row cannot claim it was
	// overridden deliberately.
	applied, _, err := GetChannelFitCapability(2, "kimi-k3", "kimi-k3", "usage.thinking_counting")
	require.NoError(t, err)
	assert.False(t, applied.Force)
}

// TestApplyFitCapabilityReportFallsBackToNowForAMissingMeasurementTime keeps the
// freshness window meaningful: a report that omits generated_at must not produce
// marks that look ancient (and therefore stale) the moment they land.
func TestApplyFitCapabilityReportFallsBackToNowForAMissingMeasurementTime(t *testing.T) {
	setupFitCapabilityDB(t)

	report := reportWith(fitpolicy.SuiteResult{
		ChannelID: 3, Family: "kimi-k3", Model: "kimi-k3", Behavior: "tools.dynamic_names", Supported: true,
	})
	report.GeneratedAt = 0

	now := common.GetTimestamp()
	summary := ApplyFitCapabilityReport(report, now)
	require.Equal(t, 1, summary.Applied)

	stored, _, err := GetChannelFitCapability(3, "kimi-k3", "kimi-k3", "tools.dynamic_names")
	require.NoError(t, err)
	assert.Equal(t, now, stored.At)

	InitFitCapabilityIndex()
	assert.True(t, ChannelSatisfiesFitMarks(3, FitMarkRequirement{Model: "kimi-k3", Marks: []string{"tools.dynamic_names"}}),
		"a report without a measurement time must still land as fresh")
}
