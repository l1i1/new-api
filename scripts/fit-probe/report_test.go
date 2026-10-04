package main

// The payload is the only artefact this tool can POST, so its encoding is
// pinned here: which verdicts reach results[], which are omitted, and that the
// encoded bytes carry the schema controller.PostFitCapabilityReport decodes.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/pkg/fitpolicy"
)

// payloadKeys is the exact top-level shape fitpolicy.SuiteReport encodes. The
// dry run prints these bytes, so a missing key is a broken payload shape.
var payloadKeys = []string{
	"report_id",
	"run_id",
	"suite",
	"policy_version",
	"policy_hash",
	"baseline_hash",
	"generated_at",
	"rounds",
	"results",
}

func TestBuildSuiteReportEncoding(t *testing.T) {
	binding := policyBinding{Version: 7, Hash: strings.Repeat("a", 64), Baseline: strings.Repeat("b", 64)}
	structural := probeResult{ChannelID: 11, Family: "deepseek-v4", Model: "deepseek-v4-pro", Behavior: fitpolicy.BehaviorLogprobsDualPath, Verdict: verdictConsistent, Strength: strengthStructural}
	divergent := probeResult{ChannelID: 12, Family: "kimi-k3", Model: "kimi-k3", Behavior: fitpolicy.BehaviorResponseFormatJSON, Verdict: verdictDivergence, Strength: strengthStructural}
	acceptanceOnly := probeResult{ChannelID: 13, Family: "glm-5.3", Model: "glm-5.3", Behavior: fitpolicy.BehaviorFamilyWhole, Verdict: verdictConsistent, Strength: strengthAcceptance}
	placeholder := probeResult{ChannelID: 0, Family: "deepseek-v4", Model: "deepseek-v4-pro", Behavior: fitpolicy.BehaviorImageParts, Verdict: verdictInconclusive, Strength: strengthAcceptance, Placeholder: true}

	cases := []struct {
		name        string
		results     []probeResult
		acceptance  bool
		wantCount   int
		wantSupport []bool
	}{
		{name: "dry-run placeholder is omitted", results: []probeResult{placeholder}},
		{name: "structural consistency encodes supported=true", results: []probeResult{structural}, wantCount: 1, wantSupport: []bool{true}},
		{name: "divergence encodes supported=false", results: []probeResult{divergent}, wantCount: 1, wantSupport: []bool{false}},
		{name: "acceptance-only consistency is omitted by default", results: []probeResult{acceptanceOnly}},
		{name: "acceptance-only consistency encodes when requested", results: []probeResult{acceptanceOnly}, acceptance: true, wantCount: 1, wantSupport: []bool{true}},
		{name: "duplicate channel/model/behaviour is encoded once", results: []probeResult{structural, structural}, wantCount: 1, wantSupport: []bool{true}},
		{name: "mixed rows keep only the decisive ones", results: []probeResult{placeholder, divergent, structural}, wantCount: 2, wantSupport: []bool{false, true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.wantSupport) != tc.wantCount {
				t.Fatalf("test table is inconsistent: %d expectations for %d results", len(tc.wantSupport), tc.wantCount)
			}
			report, omitted, err := buildSuiteReport("report-1", "run-1", "suite-1", binding, 1700000000, tc.results, tc.acceptance)
			if err != nil {
				t.Fatalf("buildSuiteReport: %v", err)
			}
			if len(report.Results) != tc.wantCount {
				t.Fatalf("results = %d, want %d", len(report.Results), tc.wantCount)
			}
			if len(omitted) != len(tc.results)-tc.wantCount {
				t.Fatalf("omitted = %d, want %d", len(omitted), len(tc.results)-tc.wantCount)
			}
			for i, want := range tc.wantSupport {
				if report.Results[i].Supported != want {
					t.Errorf("results[%d].supported = %v, want %v", i, report.Results[i].Supported, want)
				}
			}

			encoded, err := marshalPayload(report)
			if err != nil {
				t.Fatalf("marshalPayload: %v", err)
			}
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("payload is not JSON: %v", err)
			}
			for _, key := range payloadKeys {
				if _, present := decoded[key]; !present {
					t.Errorf("payload is missing top-level key %q", key)
				}
			}
			if len(decoded) != len(payloadKeys) {
				t.Errorf("payload has %d top-level keys, want %d", len(decoded), len(payloadKeys))
			}

			// These are the bytes the server decodes, so they must round-trip
			// into the server's own type unchanged.
			var round fitpolicy.SuiteReport
			if err := json.Unmarshal(encoded, &round); err != nil {
				t.Fatalf("payload does not decode as fitpolicy.SuiteReport: %v", err)
			}
			if round.ReportID != "report-1" || round.RunID != "run-1" || round.Suite != "suite-1" || round.Rounds != 1 || round.GeneratedAt != 1700000000 {
				t.Errorf("envelope did not round-trip: %+v", round)
			}
			if round.PolicyVersion != binding.Version || round.PolicyHash != binding.Hash || round.BaselineHash != binding.Baseline {
				t.Errorf("binding did not round-trip: %+v", round)
			}
			if got := len(round.Results); got != tc.wantCount {
				t.Errorf("decoded results = %d, want %d", got, tc.wantCount)
			}
		})
	}
}
