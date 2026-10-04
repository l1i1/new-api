package main

// --payload-in: let the tool write a curated payload instead of its own.
//
// A curated payload is what an operator assembled while the tool could only
// write its own single sample: a subset of rows with the supported flag taken
// from a majority vote. The only way to land one was to POST a hand-assembled
// JSON body to /api/fit-capability/report, which made the tool not the only
// writer. --payload-in removes that deviation without trusting the file:
//
//   - the shape is validated with the server's own
//     fitpolicy.ValidateSuiteReport, with unknown JSON fields rejected so a
//     typo cannot silently drop the claim it was meant to carry;
//   - every row's (channel_id, family, model, behavior) must be inside this
//     run's probed scope;
//   - every row must be one this run itself decided, and its supported flag
//     must equal this run's own decision — the strict majority when
//     --rounds > 1, the one sample when --rounds 1. A row this run left
//     undecided cannot be corroborated and aborts the write;
//   - the row's evidence tier must be encodable under the current flags, so a
//     curated row cannot smuggle an acceptance-tier or unknown-tier verdict
//     past the gate that would have dropped it;
//   - everything else is regenerated rather than trusted: the envelope
//     (report_id, run_id, suite, generated_at), the binding (policy version,
//     hash and baseline), rounds, and each row's cases and expires_at all come
//     from this run. The file contributes the row set and the supported flags;
//     its values for the regenerated fields are ignored deliberately and
//     visibly, never silently believed.
//
// The write itself is the same single path as --write: same endpoint, same
// admin credential, same fitpolicy.SuiteReport body, same encoder.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/pkg/fitpolicy"
)

// payloadInReport records what --payload-in contributed, for --explain.
type payloadInReport struct {
	File       string `json:"file"`
	Rows       int    `json:"rows"`
	Validation string `json:"validation"`
	// OmittedDecided counts rows this run decided that the curated subset
	// leaves out: they keep their stored value and are not written.
	OmittedDecided int `json:"omitted_decided_rows,omitempty"`
}

// payloadRowKey is the identity of one report row, normalized exactly the way
// the payload encoder and the server normalize it: the channel id, and the
// lowercased trimmed family, model and behavior. Scope matching uses this key,
// so "in scope" means the same tuple the encoder would treat as the same row.
func payloadRowKey(channelID int, family, model, behavior string) string {
	return strings.Join([]string{
		itoa(channelID),
		strings.ToLower(strings.TrimSpace(family)),
		strings.ToLower(strings.TrimSpace(model)),
		strings.ToLower(strings.TrimSpace(behavior)),
	}, "\x00")
}

// loadCuratedPayload reads a --payload-in file and validates its shape with the
// server's own validator. Nothing in it is trusted for the write: the envelope
// it carries is replaced by this run's before the check, so a curated file
// needs no envelope of its own and a stale one cannot leak into the POST.
func loadCuratedPayload(path, reportID, runID, suite string, rounds int) (*fitpolicy.SuiteReport, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--payload-in: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	// A misspelled field ("suported": true) would otherwise be dropped and read
	// as its zero value, which for supported is the opposite claim.
	decoder.DisallowUnknownFields()
	var curated fitpolicy.SuiteReport
	if err := decoder.Decode(&curated); err != nil {
		return nil, fmt.Errorf("--payload-in %s: not a suite report: %w", path, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("--payload-in %s: trailing data after the suite report", path)
	}
	curated.ReportID = reportID
	curated.RunID = runID
	curated.Suite = suite
	curated.Rounds = rounds
	if err := fitpolicy.ValidateSuiteReport(&curated, fitpolicy.ReportBinding{}); err != nil {
		return nil, fmt.Errorf("--payload-in %s: %w", path, err)
	}
	return &curated, nil
}

// curatedReportFrom validates a curated payload against this run's own
// measurement and returns the exact report the write path will post: this run's
// own rows, restricted to the curated row set, with this run's envelope,
// binding, rounds and cases.
//
// It refuses — naming the offending row — when a curated row is outside the
// probed scope, when this run did not decide it, when its supported flag
// contradicts this run's decision, or when its evidence tier is not encodable
// under the current flags. Nothing is written on refusal: the caller returns
// the error before the single POST.
func curatedReportFrom(curated fitpolicy.SuiteReport, rows []probeResult, reportID, runID, suite string, binding policyBinding, generatedAt int64, rounds int, writeAcceptance bool) (fitpolicy.SuiteReport, error) {
	index := make(map[string]probeResult, len(rows))
	for _, row := range rows {
		index[payloadRowKey(row.ChannelID, row.Family, row.Model, row.Behavior)] = row
	}

	selected := make([]probeResult, 0, len(curated.Results))
	seen := make(map[string]bool, len(curated.Results))
	for position, result := range curated.Results {
		key := payloadRowKey(result.ChannelID, result.Family, result.Model, result.Behavior)
		row, inScope := index[key]
		if !inScope {
			return fitpolicy.SuiteReport{}, fmt.Errorf("--payload-in row %d (%s) is outside this run's probed scope", position, describePayloadRow(result))
		}
		if seen[key] {
			return fitpolicy.SuiteReport{}, fmt.Errorf("--payload-in row %d (%s) repeats an earlier row", position, describePayloadRow(result))
		}
		seen[key] = true

		switch row.Verdict {
		case verdictConsistent, verdictDivergence:
			measured := row.Verdict == verdictConsistent
			if measured != result.Supported {
				return fitpolicy.SuiteReport{}, fmt.Errorf("--payload-in row %d (%s) declares supported=%s but this run measured supported=%s (%s)",
					position, describePayloadRow(result), boolText(result.Supported), boolText(measured), row.Basis)
			}
			if !encodableStrength(row.Strength, writeAcceptance) {
				return fitpolicy.SuiteReport{}, fmt.Errorf("--payload-in row %d (%s): this run's evidence tier %q is not encodable here (acceptance-tier evidence needs --acceptance-marks)",
					position, describePayloadRow(result), row.Strength)
			}
		default:
			return fitpolicy.SuiteReport{}, fmt.Errorf("--payload-in row %d (%s): this run did not decide it (%s), so the curated value cannot be corroborated",
				position, describePayloadRow(result), row.Basis)
		}
		selected = append(selected, row)
	}

	report, _, err := buildSuiteReportRounds(reportID, runID, suite, binding, generatedAt, rounds, selected, writeAcceptance)
	if err != nil {
		return fitpolicy.SuiteReport{}, fmt.Errorf("--payload-in: %w", err)
	}
	if len(report.Results) != len(curated.Results) {
		return fitpolicy.SuiteReport{}, fmt.Errorf("--payload-in: only %d of %d curated rows survive this run's encoder", len(report.Results), len(curated.Results))
	}
	return report, nil
}

// describePayloadRow names one curated row the way an operator wrote it, so a
// refusal points at the row to fix.
func describePayloadRow(result fitpolicy.SuiteResult) string {
	return fmt.Sprintf("channel %d / %s / %s / %s", result.ChannelID, result.Family, result.Model, result.Behavior)
}
