package main

// Redaction and the report payload encoder.
//
// The encoder builds a fitpolicy.SuiteReport — the exact type
// controller.PostFitCapabilityReport decodes
// (controller/channel_fit_capability.go:385) — so the payload cannot drift from
// the server schema. buildSuiteReport also runs the same
// fitpolicy.ValidateSuiteReport the server runs, offline, as a self-check.

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/pkg/fitpolicy"
)

// secretPatterns catch the shapes a leaked credential takes in free text. They
// are applied to error fingerprints and log lines, never to the report payload:
// the payload carries policy_hash verbatim and that is a 64-char hex digest a
// generic rule would destroy.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)sk-[A-Za-z0-9_\-]{4,}`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]{4,}`),
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)\s*[=:]\s*[^\s,;"']+`),
	regexp.MustCompile(`[A-Za-z0-9_\-]{40,}`),
}

// redactText makes an arbitrary upstream string safe to log: secrets are
// replaced, whitespace is collapsed and the result is bounded.
func redactText(value string, limit int) string {
	redacted := value
	for _, pattern := range secretPatterns {
		redacted = pattern.ReplaceAllString(redacted, "[redacted]")
	}
	redacted = strings.Join(strings.Fields(redacted), " ")
	if limit > 0 && len(redacted) > limit {
		redacted = redacted[:limit] + "…"
	}
	return redacted
}

// redactURL keeps only scheme://host so a base URL that carries a credential in
// its query string never reaches a log line or the report.
func redactURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return redactText(trimmed, 80)
	}
	return parsed.Scheme + "://" + parsed.Host
}

func itoa(value int) string { return strconv.Itoa(value) }

// markMappingRow documents how a policy mark reaches the report payload. It is
// emitted so the mapping the live policy uses and the mapping the endpoint
// accepts are visible side by side.
type markMappingRow struct {
	PolicyMark   string `json:"policy_mark"`
	ReportField  string `json:"report_field"`
	ReportValue  string `json:"report_value"`
	ServerSource string `json:"server_source"`
	Note         string `json:"note,omitempty"`
}

func markMapping(behaviors []string) []markMappingRow {
	rows := make([]markMappingRow, 0, len(behaviors))
	for _, behavior := range behaviors {
		rows = append(rows, markMappingRow{
			PolicyMark:   behavior,
			ReportField:  "results[].behavior",
			ReportValue:  behavior,
			ServerSource: "suite",
			Note:         "the policy mark name is the report behavior verbatim; family must be the registered family id of the model",
		})
	}
	return rows
}

// probeResult is one (channel, model, behaviour) measurement with its evidence.
// It is the enriched record; resultToPayload narrows it to the server schema.
type probeResult struct {
	ChannelID   int     `json:"channel_id"`
	ChannelName string  `json:"channel_name,omitempty"`
	ChannelType int     `json:"channel_type,omitempty"`
	Family      string  `json:"family"`
	Model       string  `json:"model"`
	Behavior    string  `json:"behavior"`
	Verdict     verdict `json:"verdict"`
	Strength    string  `json:"evidence_strength"`
	Basis       string  `json:"basis"`
	// DivergenceShape is the reproducible shape of a divergence: which fields
	// differed, with the official and channel values.
	DivergenceShape   string         `json:"divergence_shape,omitempty"`
	Path              string         `json:"path"`
	MinimalRequest    map[string]any `json:"minimal_request"`
	HTTPStatus        int            `json:"http_status"`
	Attempts          int            `json:"attempts"`
	ChannelSignature  signature      `json:"channel_signature"`
	BaselineSignature *signature     `json:"baseline_signature,omitempty"`
	Transport         string         `json:"transport_error,omitempty"`
	// Placeholder marks a row produced without any probe: an offline preview or
	// a mark this tool has no minimal probe for.
	Placeholder bool `json:"placeholder,omitempty"`

	// Rounds, Votes and RoundRecords describe a multi-round decision. They are
	// empty on a single-round run, where the row *is* the one sample and the
	// payload claims rounds 1 with cases 1/1 — the legacy shape, unchanged.
	//
	// Rounds is the number of independent rounds the row was measured in, Votes
	// how many of them agreed on Verdict (the numerator of the encoded cases
	// field), and RoundRecords carries each round's own verdict, evidence tier,
	// status and basis so an undecided row's evidence survives into --explain.
	Rounds       int           `json:"rounds,omitempty"`
	Votes        int           `json:"votes,omitempty"`
	RoundRecords []roundRecord `json:"round_records,omitempty"`
}

// roundRecord is one measurement round's contribution to a row. A round that
// ended inconclusive is a record too: it is an abstention in the vote, and its
// reason is part of the row's evidence.
type roundRecord struct {
	Round      int       `json:"round"`
	Verdict    verdict   `json:"verdict"`
	Strength   string    `json:"evidence_strength,omitempty"`
	Basis      string    `json:"basis,omitempty"`
	Shape      string    `json:"divergence_shape,omitempty"`
	HTTPStatus int       `json:"http_status,omitempty"`
	Attempts   int       `json:"attempts,omitempty"`
	Transport  string    `json:"transport_error,omitempty"`
	Signature  signature `json:"channel_signature"`
	// Baseline is this round's own measured official baseline, when one was
	// configured. It is re-measured every round, so it belongs to the round.
	Baseline *signature `json:"baseline_signature,omitempty"`
}

// resultToPayload maps one probe result onto a SuiteReport result, or reports
// that it must be omitted. Only decisive evidence reaches the payload: a
// known-tier consistent observation becomes supported=true, a known-tier
// divergence becomes supported=false, and everything else — an inconclusive
// probe, an unknown evidence tier — is omitted (an invented false is the same
// lie as an invented true).
func resultToPayload(result probeResult, writeAcceptance bool) (fitpolicy.SuiteResult, bool) {
	if result.Model == "" || result.Family == "" || result.Behavior == "" {
		return fitpolicy.SuiteResult{}, false
	}
	// A majority row must carry its tally. A row that claims several rounds
	// without a vote count would encode cases "0/N" and assert a decision it
	// cannot show, so it is omitted instead.
	cases := "1/1"
	if result.Rounds > 1 {
		if result.Votes <= 0 {
			return fitpolicy.SuiteResult{}, false
		}
		cases = itoa(result.Votes) + "/" + itoa(result.Rounds)
	}
	switch result.Verdict {
	case verdictConsistent:
		if !encodableStrength(result.Strength, writeAcceptance) {
			return fitpolicy.SuiteResult{}, false
		}
		return fitpolicy.SuiteResult{
			ChannelID: result.ChannelID,
			Family:    result.Family,
			Model:     result.Model,
			Behavior:  result.Behavior,
			Supported: true,
			Cases:     cases,
		}, true
	case verdictDivergence:
		// The same allow-list gates both directions, so the weakest signal
		// this tool can observe (an acceptance-tier prompt_tokens difference)
		// is not written as supported=false unless --acceptance-marks says
		// acceptance-level evidence may decide.
		if !encodableStrength(result.Strength, writeAcceptance) {
			return fitpolicy.SuiteResult{}, false
		}
		return fitpolicy.SuiteResult{
			ChannelID: result.ChannelID,
			Family:    result.Family,
			Model:     result.Model,
			Behavior:  result.Behavior,
			Supported: false,
			Cases:     cases,
		}, true
	default:
		return fitpolicy.SuiteResult{}, false
	}
}

// encodableStrength reports whether one evidence tier may be written to the
// payload. It is an allow-list, not a deny-list: only an explicitly known tier
// is encodable, and the acceptance tier needs --acceptance-marks. A zero-value
// or unknown tier is omitted in both verdict directions, so a missing strength
// assignment on a new probe path can never be promoted into a stored mark.
func encodableStrength(strength string, writeAcceptance bool) bool {
	switch strength {
	case strengthStructural:
		return true
	case strengthAcceptance:
		return writeAcceptance
	default:
		return false
	}
}

// buildSuiteReport assembles a single-round payload: the legacy entry point,
// which claims rounds 1 and encodes cases 1/1.
func buildSuiteReport(reportID, runID, suite string, binding policyBinding, generatedAt int64, results []probeResult, writeAcceptance bool) (fitpolicy.SuiteReport, []probeResult, error) {
	return buildSuiteReportRounds(reportID, runID, suite, binding, generatedAt, 1, results, writeAcceptance)
}

// buildSuiteReportRounds assembles the payload and refuses to return one the
// server would reject. binding is the live policy binding read from
// GET /api/fit-policy, and rounds is the number of measurement rounds the
// report claims: a multi-round report encodes each row's majority tally as its
// cases field, so the stored row says "2/3" rather than a flat "1/1".
func buildSuiteReportRounds(reportID, runID, suite string, binding policyBinding, generatedAt int64, rounds int, results []probeResult, writeAcceptance bool) (fitpolicy.SuiteReport, []probeResult, error) {
	if rounds < 1 {
		rounds = 1
	}
	report := fitpolicy.SuiteReport{
		ReportID:      reportID,
		RunID:         runID,
		Suite:         suite,
		PolicyVersion: binding.Version,
		PolicyHash:    binding.Hash,
		BaselineHash:  binding.Baseline,
		GeneratedAt:   generatedAt,
		Rounds:        rounds,
		Results:       make([]fitpolicy.SuiteResult, 0, len(results)),
	}
	var omitted []probeResult
	seen := map[string]bool{}
	for _, result := range results {
		encoded, ok := resultToPayload(result, writeAcceptance)
		if !ok {
			omitted = append(omitted, result)
			continue
		}
		key := payloadRowKey(encoded.ChannelID, encoded.Family, encoded.Model, encoded.Behavior)
		if seen[key] {
			omitted = append(omitted, result)
			continue
		}
		seen[key] = true
		if len(report.Results) >= fitpolicy.MaxSuiteReportResults {
			omitted = append(omitted, result)
			continue
		}
		report.Results = append(report.Results, encoded)
	}
	if len(report.Results) == 0 {
		return report, omitted, nil
	}
	if err := fitpolicy.ValidateSuiteReport(&report, fitpolicy.ReportBinding{
		PolicyVersion: binding.Version,
		PolicyHash:    binding.Hash,
		BaselineHash:  binding.Baseline,
	}); err != nil {
		return fitpolicy.SuiteReport{}, omitted, err
	}
	return report, omitted, nil
}

// policyBinding is the live binding a report must carry.
type policyBinding struct {
	Version  int    `json:"version"`
	Hash     string `json:"hash"`
	Baseline string `json:"baseline"`
}

// newReportID builds a run-scoped identifier. It uses no secret material.
func newReportID(now time.Time) string {
	return "fit-probe-" + now.UTC().Format("20060102T150405Z")
}

// marshalPayload renders the payload exactly as it would be posted.
func marshalPayload(report fitpolicy.SuiteReport) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}
