package main

// Regression tests for the correctness bugs the fit-probe audit
// (scratch/fit-probe-matrix.md) found in the mark encoder. Every test in this
// file fails against the pre-fix code and passes after it: they exist so the
// four bugs cannot come back unnoticed.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/pkg/fitpolicy"
)

// R1: kimi-k3 usage.thinking_counting is a prompt_tokens-only dimension
// (spec.go:103), so an accepted probe may never be called divergent for an
// absent structure. Before the fix the absence branch (probe.go:514-518) fired
// without checking whether the structural evidence was actually present, which
// wrote supported=false for every accepted kimi-k3 probe.
func TestR1KimiThinkingAbsenceNeverDiverges(t *testing.T) {
	spec, known := probeSpecs[fitpolicy.BehaviorThinkingCounting]
	if !known {
		t.Fatalf("no probe spec for %s", fitpolicy.BehaviorThinkingCounting)
	}
	exp := expectationFor("kimi-k3", fitpolicy.BehaviorThinkingCounting)
	if got := strengthFor(exp); got != strengthAcceptance {
		t.Fatalf("kimi-k3 %s strength = %q, want %q", exp.Behavior, got, strengthAcceptance)
	}

	withEvidence := signature{
		Status: 200, OK: true, JSON: true, Accepted: true,
		HasContent: true, HasFinishReason: true, HasUsage: true,
		PromptTokens: 42, CompletionTokens: 1, ReasoningTokens: 0,
	}
	withoutEvidence := withEvidence
	withoutEvidence.HasUsage = false
	withoutEvidence.ReasoningTokens = -1

	cases := []struct {
		name    string
		channel signature
	}{
		{name: "structural evidence present", channel: withEvidence},
		{name: "structural evidence absent", channel: withoutEvidence},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdictValue, _, basis, shape := classify(spec, exp, nil, tc.channel)
			if verdictValue == verdictDivergence {
				t.Fatalf("classify = divergence (%s; %s): a prompt_tokens-only dimension must not be called divergent without the measured baseline, that writes supported=false on every accepted kimi-k3 probe", basis, shape)
			}
			if verdictValue != verdictInconclusive {
				t.Fatalf("classify = %q, want %q (%s)", verdictValue, verdictInconclusive, basis)
			}
			row := probeResult{
				ChannelID: 1, Family: "kimi-k3", Model: "kimi-k3",
				Behavior: fitpolicy.BehaviorThinkingCounting,
				Verdict:  verdictValue, Strength: strengthAcceptance,
			}
			if _, encoded := resultToPayload(row, false); encoded {
				t.Fatal("an inconclusive kimi-k3 thinking_counting row reached the payload")
			}
		})
	}
}

// R3: a prompt_tokens mismatch was hard-coded to strengthStructural
// (probe.go:496-500) while equality returned the discriminator-derived
// acceptance strength, so the weakest signal was written to the report without
// the --acceptance-marks gate. Both directions now carry the same tier and the
// same gate.
func TestR3PromptTokensMismatchIsNotStrongerThanEquality(t *testing.T) {
	spec := probeSpecs[fitpolicy.BehaviorHistoryAssistantFirst]
	exp := expectationFor("kimi-k3", fitpolicy.BehaviorHistoryAssistantFirst)
	baseline := &signature{Status: 200, OK: true, JSON: true, Accepted: true, HasContent: true, PromptTokens: 10}
	mismatch := signature{Status: 200, OK: true, JSON: true, Accepted: true, HasContent: true, PromptTokens: 12}
	equal := mismatch
	equal.PromptTokens = baseline.PromptTokens

	verdictValue, strength, basis, shape := classify(spec, exp, baseline, mismatch)
	if verdictValue != verdictDivergence {
		t.Fatalf("mismatch verdict = %q (%s), want divergence", verdictValue, basis)
	}
	if strength != strengthAcceptance {
		t.Fatalf("mismatch strength = %q (%s), want %q: prompt_tokens is this dimension's only discriminator, so a token-count difference cannot be structural", strength, shape, strengthAcceptance)
	}
	row := probeResult{
		ChannelID: 1, Family: "kimi-k3", Model: "kimi-k3",
		Behavior: exp.Behavior, Verdict: verdictValue, Strength: strength,
	}
	if _, encoded := resultToPayload(row, false); encoded {
		t.Fatal("acceptance-strength divergence was encoded without --acceptance-marks: the gate is still asymmetric")
	}
	encoded, ok := resultToPayload(row, true)
	if !ok || encoded.Supported {
		t.Fatalf("with --acceptance-marks the row must encode supported=false, got encoded=%v supported=%v", ok, encoded.Supported)
	}

	// The equality direction carries the same tier and the same gate.
	equalVerdict, equalStrength, equalBasis, _ := classify(spec, exp, baseline, equal)
	if equalVerdict != verdictConsistent || equalStrength != strengthAcceptance {
		t.Fatalf("equal prompt_tokens = (%q, %q) (%s), want (%q, %q)", equalVerdict, equalStrength, equalBasis, verdictConsistent, strengthAcceptance)
	}
	equalRow := probeResult{
		ChannelID: 1, Family: "kimi-k3", Model: "kimi-k3",
		Behavior: exp.Behavior, Verdict: equalVerdict, Strength: equalStrength,
	}
	if _, encoded := resultToPayload(equalRow, false); encoded {
		t.Fatal("acceptance-strength consistency was encoded without --acceptance-marks")
	}
}

// Fail-open strength gate: report.go:125 denied only the literal "acceptance",
// so a zero-value or unknown tier fell through to supported=true. The gate is
// now an allow-list: only an explicitly known tier may be encoded, and the
// acceptance tier still needs --acceptance-marks.
func TestStrengthGateFailsClosed(t *testing.T) {
	base := probeResult{ChannelID: 1, Family: "kimi-k3", Model: "kimi-k3", Behavior: fitpolicy.BehaviorToolsChoiceSemantics, Verdict: verdictConsistent}

	for _, strength := range []string{"", "archive", "STRONG"} {
		row := base
		row.Strength = strength
		if encoded, ok := resultToPayload(row, true); ok {
			t.Fatalf("consistent row with strength %q was encoded as supported=%v; an unknown tier must be omitted", strength, encoded.Supported)
		}
	}

	accepted := base
	accepted.Strength = strengthAcceptance
	if _, ok := resultToPayload(accepted, false); ok {
		t.Fatal("acceptance-tier consistency was encoded without --acceptance-marks")
	}
	if encoded, ok := resultToPayload(accepted, true); !ok || !encoded.Supported {
		t.Fatalf("acceptance-tier consistency with --acceptance-marks = (encoded=%v, supported=%v), want (true, true)", ok, encoded.Supported)
	}

	structural := base
	structural.Strength = strengthStructural
	if encoded, ok := resultToPayload(structural, false); !ok || !encoded.Supported {
		t.Fatalf("structural-tier consistency = (encoded=%v, supported=%v), want (true, true)", ok, encoded.Supported)
	}

	// The same gate through the encoder the POST actually uses.
	unknown := base
	unknown.Strength = ""
	report, omitted, err := buildSuiteReport("report-1", "run-1", "suite-1", testBinding(), 1700000000, []probeResult{unknown}, true)
	if err != nil {
		t.Fatalf("buildSuiteReport: %v", err)
	}
	if len(report.Results) != 0 || len(omitted) != 1 {
		t.Fatalf("encoder kept %d results and omitted %d, want 0 and 1", len(report.Results), len(omitted))
	}
}

// R5: thin positive evidence. choice["logprobs"] used to pass on any JSON
// object (so {} passed) and response_format.content used to pass on any valid
// JSON scalar (so a bare true passed). Both now require the shape the mark
// claims: token-level logprob data, and a JSON object respectively.
func TestR5EvidenceMustBeTheClaimedShape(t *testing.T) {
	envelope := func(choice string) []byte {
		return []byte(`{"choices":[{"finish_reason":"stop","message":{"content":"ok"},"logprobs":` + choice + `}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`)
	}
	for _, empty := range []string{`{}`, `{"content":[]}`, `{"refusal":null}`} {
		if sig := extractSignature(200, envelope(empty)); sig.HasLogprobs {
			t.Errorf("logprobs %s was accepted as evidence", empty)
		}
	}
	if sig := extractSignature(200, envelope(`{"content":[{"token":"ok","logprob":-0.1}]}`)); !sig.HasLogprobs {
		t.Error("token-level logprob data was not accepted as evidence")
	}

	spec := probeSpecs[fitpolicy.BehaviorResponseFormatJSON]
	exp := expectationFor("kimi-k3", fitpolicy.BehaviorResponseFormatJSON)
	scalar := extractSignature(200, []byte(`{"choices":[{"finish_reason":"stop","message":{"content":"true"}}]}`))
	if verdictValue, _, basis, _ := classify(spec, exp, nil, scalar); verdictValue != verdictInconclusive {
		t.Errorf("response_format over a bare JSON scalar = %q (%s), want inconclusive", verdictValue, basis)
	}
	object := extractSignature(200, []byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"ok\":true}"}}]}`))
	if verdictValue, strength, basis, _ := classify(spec, exp, nil, object); verdictValue != verdictConsistent || strength != strengthStructural {
		t.Errorf("response_format over a JSON object = (%q, %q) (%s), want (%q, %q)", verdictValue, strength, basis, verdictConsistent, strengthStructural)
	}
}

// R2: the measured official baseline used to be cached per (family, model)
// (main.go:303-311), so every mark after the first of a pair was compared
// against a response to a *different* request body and ~4-5 supported=false
// rows per channel per run were invented. The baseline is now keyed on the full
// request signature and the identical bytes go to the relay and to the official
// endpoint. The fake official endpoint answers deterministically from the
// request body, so a channel that matches it must never be a divergence.
func TestR2BaselineIsMeasuredPerBehaviourRequest(t *testing.T) {
	var (
		relayMu        sync.Mutex
		relayBodies    = map[string]int{}
		officialMu     sync.Mutex
		officialBodies = map[string]int{}
		authMu         sync.Mutex
		badAuth        []string
	)
	completion := func(bodies map[string]int, mu *sync.Mutex) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			authMu.Lock()
			if got := r.Header.Get("Authorization"); got != "Bearer relay-token-7" && got != "Bearer official-token" {
				badAuth = append(badAuth, got)
			}
			authMu.Unlock()
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			mu.Lock()
			bodies[string(body)]++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(fakeCompletion(body))
		}
	}

	relay := httptest.NewServer(completion(relayBodies, &relayMu))
	defer relay.Close()
	official := httptest.NewServer(completion(officialBodies, &officialMu))
	defer official.Close()

	admin := httptest.NewServer(fakeAdminMux(func() {
		t.Error("the report endpoint was hit although --write was never set")
	}))
	defer admin.Close()

	t.Setenv("FIT_PROBE_TEST_ADMIN_TOKEN", "admin-token")
	t.Setenv("FIT_PROBE_TEST_RELAY_TOKEN", "relay-token")
	t.Setenv("FIT_PROBE_TEST_OFFICIAL_TOKEN", "official-token")

	opts := options{
		baseURL:        admin.URL,
		relayBaseURL:   relay.URL,
		adminTokenEnv:  "FIT_PROBE_TEST_ADMIN_TOKEN",
		relayTokenEnv:  "FIT_PROBE_TEST_RELAY_TOKEN",
		officialURL:    stringList{official.URL},
		officialKeyEnv: stringList{"FIT_PROBE_TEST_OFFICIAL_TOKEN"},
		status:         "1",
		timeout:        5 * time.Second,
		suite:          "fit-probe-regression",
	}
	var stdout, stderr strings.Builder
	if err := run(context.Background(), opts, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v (stderr: %s)", err, stderr.String())
	}

	if len(badAuth) > 0 {
		t.Fatalf("unexpected credentials on fake probe requests: %v", badAuth)
	}
	// 3 deepseek-v4 marks + 5 kimi-k3 marks + 1 glm-5.3 mark.
	if len(relayBodies) != 9 {
		t.Fatalf("relay saw %d distinct probe bodies, want 9", len(relayBodies))
	}

	var report fitpolicy.SuiteReport
	if err := json.Unmarshal([]byte(stdout.String()), &report); err != nil {
		t.Fatalf("run output is not a suite report: %v\n%s", err, stdout.String())
	}
	if len(report.Results) == 0 {
		t.Fatalf("the fake channel matches the official endpoint on every mark, yet no result was encoded:\n%s", stdout.String())
	}
	for _, result := range report.Results {
		if !result.Supported {
			t.Errorf("%s/%s was written supported=false although the channel answered exactly like the official endpoint for the identical request", result.Family, result.Behavior)
		}
	}

	if len(officialBodies) != len(relayBodies) {
		t.Fatalf("official endpoint saw %d distinct bodies, relay saw %d: every mark needs its own identical baseline request", len(officialBodies), len(relayBodies))
	}
	for body := range relayBodies {
		if officialBodies[body] == 0 {
			t.Fatalf("this probe body was never measured against the official endpoint, so it cannot be compared:\n%s", body)
		}
	}
}

// fakeCompletion answers a chat-completions request deterministically from its
// body, so the same bytes always produce the same signature. It is the shared
// behaviour of the fake relay channel and the fake official endpoint.
func fakeCompletion(body []byte) map[string]any {
	var request map[string]any
	_ = json.Unmarshal(body, &request)
	message := map[string]any{"role": "assistant", "content": "ok"}
	choice := map[string]any{"index": 0, "message": message, "finish_reason": "stop"}
	if _, ok := request["tools"].([]any); ok {
		message["tool_calls"] = []any{map[string]any{
			"id": "call_1", "type": "function",
			"function": map[string]any{"name": "ping", "arguments": "{}"},
		}}
	}
	if format, ok := request["response_format"].(map[string]any); ok && format["type"] == "json_object" {
		message["content"] = `{"ok":true}`
	}
	reasoningTokens := 0
	if thinking, ok := request["thinking"].(map[string]any); ok && thinking["type"] == "enabled" {
		message["reasoning_content"] = "thinking"
		reasoningTokens = 7
	}
	if requested, ok := request["logprobs"].(bool); ok && requested {
		choice["logprobs"] = map[string]any{"content": []any{map[string]any{"token": "ok", "logprob": -0.1}}}
	}
	promptTokens := 1000 + len(body)
	return map[string]any{
		"id":      "chatcmpl-fake",
		"object":  "chat.completion",
		"model":   request["model"],
		"choices": []any{choice},
		"usage": map[string]any{
			"prompt_tokens":             promptTokens,
			"completion_tokens":         1,
			"total_tokens":              promptTokens + 1,
			"completion_tokens_details": map[string]any{"reasoning_tokens": reasoningTokens},
		},
	}
}

// fakeAdminMux serves the two admin reads fit-probe performs. onReport runs if
// the report endpoint is hit at all, so a test can assert that the single write
// path stayed closed.
func fakeAdminMux(onReport func()) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/fit-policy", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"source":             "test",
				"effective_document": "",
				"live": map[string]any{
					"installed": true, "version": 1, "enabled": true, "shadow": false,
					"hash": testBinding().Hash, "baseline": testBinding().Baseline,
				},
			},
		})
	})
	mux.HandleFunc("/api/channel/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"items": []any{map[string]any{
					"id": 7, "name": "fake", "type": 1, "status": 1,
					"models": "deepseek-v4-pro,kimi-k3,glm-5.3",
				}},
				"total": 1,
			},
		})
	})
	mux.HandleFunc("/api/fit-capability/report", func(w http.ResponseWriter, _ *http.Request) {
		if onReport != nil {
			onReport()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	return mux
}

// R2, unit level: one measured baseline per exact request. Two behaviours of the
// same (family, model) must not share a key, because their request bodies differ.
func TestBaselineKeyIsTheFullRequestSignature(t *testing.T) {
	thinking := probeSpecs[fitpolicy.BehaviorThinkingCounting]
	tools := probeSpecs[fitpolicy.BehaviorToolsChoiceSemantics]
	thinkingBody, err := probeBody(thinking, target{Family: "kimi-k3", Model: "kimi-k3", Behavior: thinking.Behavior})
	if err != nil {
		t.Fatalf("probeBody: %v", err)
	}
	sameBody, err := probeBody(thinking, target{Family: "kimi-k3", Model: "kimi-k3", Behavior: thinking.Behavior})
	if err != nil {
		t.Fatalf("probeBody: %v", err)
	}
	toolsBody, err := probeBody(tools, target{Family: "kimi-k3", Model: "kimi-k3", Behavior: tools.Behavior})
	if err != nil {
		t.Fatalf("probeBody: %v", err)
	}
	otherBody, err := probeBody(thinking, target{Family: "deepseek-v4", Model: "deepseek-v4-pro", Behavior: thinking.Behavior})
	if err != nil {
		t.Fatalf("probeBody: %v", err)
	}

	key := baselineKey("kimi-k3", "kimi-k3", thinking.Behavior, thinkingBody)
	if key != baselineKey("kimi-k3", "kimi-k3", thinking.Behavior, sameBody) {
		t.Error("the same request produced two different baseline keys")
	}
	if key == baselineKey("kimi-k3", "kimi-k3", tools.Behavior, toolsBody) {
		t.Error("two behaviours of one (family, model) share a baseline key")
	}
	if key == baselineKey("deepseek-v4", "deepseek-v4-pro", thinking.Behavior, otherBody) {
		t.Error("two families share a baseline key for one behaviour")
	}
}

// The unmeasurableBehaviors mechanism must actually be consulted: a required
// policy mark this tool has no minimal probe for is reported as not measured
// instead of vanishing from the output or being guessed at.
func TestUnmeasurableMarkIsReportedNotGuessed(t *testing.T) {
	const behavior = "future.mark"
	unmeasurableBehaviors[behavior] = "test-only reason"
	defer delete(unmeasurableBehaviors, behavior)

	row := unmeasurableRow(target{ChannelID: 4, Family: "kimi-k3", Model: "kimi-k3", Behavior: behavior})
	if row.Verdict != verdictInconclusive || !row.Placeholder {
		t.Fatalf("unmeasurable mark row = %+v, want an inconclusive placeholder", row)
	}
	if !strings.Contains(row.Basis, "test-only reason") {
		t.Fatalf("basis %q does not carry the unmeasurableBehaviors reason", row.Basis)
	}
	if _, encoded := resultToPayload(row, true); encoded {
		t.Fatal("an unmeasurable mark reached the payload")
	}
}

// R5 review decision, pinned deliberately: the deepseek-v4 thinking probe keeps
// accepting `completion_tokens_details.reasoning_tokens: 0` as evidence.
// usage.thinking_counting claims the channel *reports* the thinking count, and a
// reported zero is a report. Requiring a positive count would send a legitimate
// zero-reasoning answer (the probe caps max_tokens at 8) down the
// StructuralAbsenceIsDivergence branch and write a false supported=false. This
// test exists so a future tightening has to be a deliberate decision.
func TestThinkingCountingZeroReasoningTokensIsAccountingEvidence(t *testing.T) {
	spec := probeSpecs[fitpolicy.BehaviorThinkingCounting]
	exp := expectationFor("deepseek-v4", fitpolicy.BehaviorThinkingCounting)
	channel := signature{
		Status: 200, OK: true, JSON: true, Accepted: true,
		HasContent: true, HasUsage: true, PromptTokens: 9, ReasoningTokens: 0,
	}
	verdictValue, strength, basis, _ := classify(spec, exp, nil, channel)
	if verdictValue != verdictConsistent || strength != strengthStructural {
		t.Fatalf("reported reasoning_tokens=0 = (%q, %q) (%s), want (%q, %q)", verdictValue, strength, basis, verdictConsistent, strengthStructural)
	}
}

// The safety design must survive the fixes: dry-run stays the default and
// --write stays the single gate, refusing under dry-run, offline and a payload
// with no decisive measurement.
func TestWriteGateStaysClosed(t *testing.T) {
	var reports int32
	admin := httptest.NewServer(fakeAdminMux(func() { atomic.AddInt32(&reports, 1) }))
	defer admin.Close()
	t.Setenv("FIT_PROBE_TEST_ADMIN_TOKEN", "admin-token")
	t.Setenv("FIT_PROBE_TEST_RELAY_TOKEN", "relay-token")

	base := options{
		baseURL:       admin.URL,
		relayBaseURL:  admin.URL,
		adminTokenEnv: "FIT_PROBE_TEST_ADMIN_TOKEN",
		relayTokenEnv: "FIT_PROBE_TEST_RELAY_TOKEN",
		status:        "1",
		timeout:       5 * time.Second,
		write:         true,
	}

	dryRun := base
	dryRun.dryRun = true
	if err := run(context.Background(), dryRun, &strings.Builder{}, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "--dry-run") {
		t.Fatalf("--write under the default dry-run = %v, want a --dry-run refusal", err)
	}

	offline := base
	offline.baseURL = ""
	offline.relayBaseURL = ""
	if err := run(context.Background(), offline, &strings.Builder{}, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "--base-url") {
		t.Fatalf("--write offline = %v, want a --base-url refusal", err)
	}

	// Every probe fails unattributably, so nothing decisive is measured.
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream failure","code":"upstream_error"}}`))
	}))
	defer relay.Close()
	undecided := base
	undecided.relayBaseURL = relay.URL
	if err := run(context.Background(), undecided, &strings.Builder{}, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "no decisive measurements") {
		t.Fatalf("--write with no decisive measurement = %v, want a refusal", err)
	}

	if got := atomic.LoadInt32(&reports); got != 0 {
		t.Fatalf("the report endpoint was hit %d times; --write must be the only path and it refused", got)
	}
}

// R4: error attribution used to be a substring test over the redacted prose
// fingerprint (probe.go:320-331 pre-fix), so any generic upstream rejection that
// merely contained a field word — "content policy", "tools unavailable",
// "invalid usage" — was read as evidence that this channel rejected that exact
// parameter. With no measured baseline that coincidence became a structural
// divergence and was written as supported=false: a false statement about a
// channel, derived from prose. Attribution now reads only the error object's
// own structural fields: a locator value (error.param / error.field) matched by
// whole tokens, or a category value (error.code / error.type) exactly equal to
// the field name. Prose is never consulted, and anything else stays
// inconclusive.
func TestR4ErrorAttributionNeedsStructuralEvidence(t *testing.T) {
	cases := []struct {
		name      string
		status    int // 0 means 400
		family    string
		behavior  string
		errorBody string
		// wantField is the field annotateErrorField must record; "" means the
		// error must stay unattributed.
		wantField string
	}{
		{
			name: "prose mentions content", family: "deepseek-v4", behavior: fitpolicy.BehaviorImageParts,
			errorBody: `{"error":{"message":"content policy violation: request blocked by upstream","code":"upstream_error"}}`,
		},
		{
			name: "prose mentions usage", family: "deepseek-v4", behavior: fitpolicy.BehaviorThinkingCounting,
			errorBody: `{"error":{"message":"invalid usage of the model, please check the request","type":"invalid_request_error"}}`,
		},
		{
			name: "the message is exactly the field name", family: "kimi-k3", behavior: fitpolicy.BehaviorToolsDynamicNames,
			errorBody: `{"error":{"message":"tools","type":"invalid_request_error"}}`,
		},
		{
			name: "a category code merely contains the field name", family: "deepseek-v4", behavior: fitpolicy.BehaviorImageParts,
			errorBody: `{"error":{"message":"blocked by the provider","code":"content_filter"}}`,
		},
		{
			name: "an empty locator", family: "kimi-k3", behavior: fitpolicy.BehaviorHistoryAssistantFirst,
			errorBody: `{"error":{"message":"bad request","param":"","type":"invalid_request_error"}}`,
		},
		{
			name: "a locator that names another parameter", family: "kimi-k3", behavior: fitpolicy.BehaviorToolsChoiceSemantics,
			errorBody: `{"error":{"message":"bad request","param":"temperature","code":"invalid_request_error"}}`,
		},
		{
			name: "a non-string locator", family: "kimi-k3", behavior: fitpolicy.BehaviorToolsChoiceSemantics,
			errorBody: `{"error":{"message":"bad request","param":42}}`,
		},
		{
			// In-band error object on a 2xx answer: not a rejection this tool
			// may attribute, even with a locator present.
			name: "a 200 carrying an error object", status: 200, family: "kimi-k3", behavior: fitpolicy.BehaviorToolsChoiceSemantics,
			errorBody: `{"error":{"message":"upstream failed","param":"tools"}}`,
		},
		{
			name: "an explicit locator names the field", family: "deepseek-v4", behavior: fitpolicy.BehaviorLogprobsDualPath,
			errorBody: `{"error":{"message":"bad request","type":"invalid_request_error","param":"logprobs","code":"invalid_request_error"}}`,
			wantField: "logprobs",
		},
		{
			name: "a locator names a nested request path", family: "deepseek-v4", behavior: fitpolicy.BehaviorImageParts,
			errorBody: `{"error":{"message":"bad request","param":"messages[0].content[1].image_url.url"}}`,
			wantField: "image_url",
		},
		{
			name: "a category value is exactly the field name", family: "kimi-k3", behavior: fitpolicy.BehaviorToolsChoiceSemantics,
			errorBody: `{"error":{"message":"bad request","code":"tools"}}`,
			wantField: "tools",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, known := probeSpecs[tc.behavior]
			if !known {
				t.Fatalf("no probe spec for %s", tc.behavior)
			}
			status := tc.status
			if status == 0 {
				status = http.StatusBadRequest
			}
			sig := extractSignature(status, []byte(tc.errorBody))
			annotateErrorField(&sig, spec.Fields)
			if sig.ErrorField != tc.wantField {
				t.Fatalf("error_field = %q, want %q (status %d, fingerprint %q)",
					sig.ErrorField, tc.wantField, sig.Status, sig.ErrorFingerprint)
			}

			exp := expectationFor(tc.family, tc.behavior)
			verdictValue, strength, basis, shape := classify(spec, exp, nil, sig)
			row := probeResult{
				ChannelID: 1, Family: tc.family, Model: tc.family, Behavior: tc.behavior,
				Verdict: verdictValue, Strength: strength,
			}
			// The widest gate: even acceptance-level evidence would be written.
			encoded, encodable := resultToPayload(row, true)

			if tc.wantField == "" {
				if verdictValue != verdictInconclusive {
					t.Fatalf("unattributable rejection = %q (%s; %s), want %q: only a structurally named error may become a divergence", verdictValue, basis, shape, verdictInconclusive)
				}
				if encodable {
					t.Fatalf("an unattributable rejection was encoded as supported=%v", encoded.Supported)
				}
				return
			}
			if verdictValue != verdictDivergence || strength != strengthStructural {
				t.Fatalf("structurally attributed rejection = (%q, %q) (%s), want (%q, %q)", verdictValue, strength, basis, verdictDivergence, strengthStructural)
			}
			if !encodable || encoded.Supported {
				t.Fatalf("structurally attributed rejection = (encodable=%v, supported=%v), want (true, false)", encodable, encoded.Supported)
			}
			if !strings.Contains(shape, tc.wantField) {
				t.Fatalf("divergence shape %q does not name the attributed field %q", shape, tc.wantField)
			}
		})
	}
}

// R4 end to end, in the mode that matters: a doc-only run (no
// --official-base-url) against a channel whose upstream errors merely mention
// field words. Pre-fix, those coincidences were attributed, --write would have
// been handed 8 invented supported=false rows and the POST would have gone out.
// Now the run has zero decisive measurements and the armed write refuses.
func TestR4DocOnlyRunWritesNothingOnProseOnlyErrors(t *testing.T) {
	var reports int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"request rejected upstream: content policy check failed, tools unavailable, messages too long, usage unsupported, logprobs disabled, response_format unsupported","code":"upstream_error"}}`))
	}))
	defer relay.Close()

	admin := httptest.NewServer(fakeAdminMux(func() { atomic.AddInt32(&reports, 1) }))
	defer admin.Close()

	t.Setenv("FIT_PROBE_TEST_ADMIN_TOKEN", "admin-token")
	t.Setenv("FIT_PROBE_TEST_RELAY_TOKEN", "relay-token")

	opts := options{
		baseURL:       admin.URL,
		relayBaseURL:  relay.URL,
		adminTokenEnv: "FIT_PROBE_TEST_ADMIN_TOKEN",
		relayTokenEnv: "FIT_PROBE_TEST_RELAY_TOKEN",
		status:        "1",
		timeout:       5 * time.Second,
		suite:         "fit-probe-regression",
		write:         true,  // the write gate is armed ...
		dryRun:        false, // ... and left open, so only the measurement holds it
	}
	var stdout, stderr strings.Builder
	err := run(context.Background(), opts, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "no decisive measurements") {
		t.Fatalf("doc-only run over prose-only upstream errors = %v, want the --write refusal; stdout: %s", err, stdout.String())
	}
	if got := atomic.LoadInt32(&reports); got != 0 {
		t.Fatalf("the report endpoint was hit %d times: a coincidence in an error message became a stored mark", got)
	}
}

// R7: usage.thinking_counting compared reasoning_tokens as an exact integer
// across two independent calls while no sampling parameter is pinned. Reasoning
// tokens are completion tokens — the output of a sampled generation — so
// equality is not reproducible evidence, and a difference flipped a real match
// into supported=false. Only presence facts are structural now: sampling
// variance must not decide a mark.
func TestR7SampledTokenCountIsNotStructuralEvidence(t *testing.T) {
	spec := probeSpecs[fitpolicy.BehaviorThinkingCounting]
	exp := expectationFor("deepseek-v4", fitpolicy.BehaviorThinkingCounting)
	baseline := &signature{
		Status: 200, OK: true, JSON: true, Accepted: true,
		HasContent: true, HasFinishReason: true, HasUsage: true,
		HasReasoningContent: true, PromptTokens: 12, CompletionTokens: 8, ReasoningTokens: 7,
	}
	channel := *baseline
	channel.ReasoningTokens = 3 // identical request, one sample later

	verdictValue, strength, basis, shape := classify(spec, exp, baseline, channel)
	if verdictValue == verdictDivergence {
		t.Fatalf("a sampled reasoning-token count difference = divergence (%s; %s): sampling variance must never write supported=false", basis, shape)
	}
	if verdictValue != verdictConsistent || strength != strengthStructural {
		t.Fatalf("same request, different sample = (%q, %q) (%s), want (%q, %q)", verdictValue, strength, basis, verdictConsistent, strengthStructural)
	}

	// The presence facts this dimension really asserts are still compared: a
	// baseline that reports the reasoning accounting and a channel that does
	// not is a reproducible structural difference.
	silent := *baseline
	silent.HasReasoningContent = false
	silent.ReasoningTokens = -1
	if verdictValue, _, basis, _ := classify(spec, exp, baseline, silent); verdictValue != verdictDivergence {
		t.Fatalf("a channel that reports no thinking accounting = %q (%s), want divergence", verdictValue, basis)
	}
}

// R8: a structural absence the probe's own token cap explains. A real doc-only
// run produced 8 kimi-k3 tools.choice_semantics answers with empty content at
// completion_tokens 16/16 (one 19/16) and 1 deepseek-v4 usage.thinking_counting
// answer at 8/8. Pre-fix, finish_reason was surfaced only as a bool
// (probe.go:302-304) and completion_tokens was never compared with the request's
// max_tokens, so the absence branch (probe.go:678-682) read "the probe ran out of
// budget" as "the channel declined to produce the structure" and wrote
// supported=false; nine rows had to be withheld from a real write by hand.
// Truncation now makes an absence-derived divergence inconclusive. The rule is
// deliberately confined to absence: an explicit structural rejection stays a
// divergence and positive evidence stays supported=true.
func TestR8TruncatedAbsenceIsInconclusive(t *testing.T) {
	toolsSpec := probeSpecs[fitpolicy.BehaviorToolsChoiceSemantics]
	thinkingSpec := probeSpecs[fitpolicy.BehaviorThinkingCounting]
	// The recorded incident was 16/16 and 19/16 at the original tools cap of 16,
	// and 8/8 at the unchanged thinking cap. The tools cap was later raised to
	// toolsChoiceSemanticsProbeMaxTokens (R11) so a real tool call can finish, so
	// the cases whose truncation is "the completion reached the live cap" are
	// re-derived against the live cap; the cap-independent finish_reason=length
	// cases keep the recorded numbers.
	if toolsSpec.MaxTokens < 16 || thinkingSpec.MaxTokens != 8 {
		t.Fatalf("probe caps changed (tools.choice_semantics=%d, usage.thinking_counting=%d); the recorded incident was 16/16, 19/16 and 8/8",
			toolsSpec.MaxTokens, thinkingSpec.MaxTokens)
	}
	toolsExp := expectationFor("kimi-k3", fitpolicy.BehaviorToolsChoiceSemantics)
	thinkingExp := expectationFor("deepseek-v4", fitpolicy.BehaviorThinkingCounting)

	// chatAnswer builds one successful answer: the given finish_reason,
	// completion count and message content, optionally with a tool call.
	chatAnswer := func(reason string, completion int, content string, toolCall bool) signature {
		message := `"content":"` + content + `"`
		if toolCall {
			message += `,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"ping","arguments":"{}"}}]`
		}
		body := `{"choices":[{"finish_reason":"` + reason + `","message":{` + message + `}}],` +
			`"usage":{"prompt_tokens":23,"completion_tokens":` + itoa(completion) + `}}`
		return extractSignature(200, []byte(body))
	}

	truncated := []struct {
		name    string
		spec    ProbeSpec
		exp     Expectation
		channel signature
	}{
		{
			name: "kimi-k3 tools at 16/16 with finish_reason=length",
			spec: toolsSpec, exp: toolsExp,
			channel: chatAnswer("length", 16, "", false),
		},
		{
			name: "kimi-k3 tools at the live cap with a finish_reason that does not say length",
			spec: toolsSpec, exp: toolsExp,
			channel: chatAnswer("stop", toolsSpec.MaxTokens, "", false),
		},
		{
			name: "kimi-k3 tools past the live cap",
			spec: toolsSpec, exp: toolsExp,
			channel: chatAnswer("stop", toolsSpec.MaxTokens+3, "", false),
		},
		{
			name: "deepseek-v4 thinking accounting at 8/8",
			spec: thinkingSpec, exp: thinkingExp,
			channel: chatAnswer("length", 8, "", false),
		},
		{
			// The upstream itself says the answer was cut, so the absent
			// structure is truncation-explained even though content arrived.
			name: "content present but cut at the cap",
			spec: toolsSpec, exp: toolsExp,
			channel: chatAnswer("length", 16, "Sure, calling", false),
		},
	}
	for _, tc := range truncated {
		t.Run(tc.name, func(t *testing.T) {
			verdictValue, strength, basis, shape := classify(tc.spec, tc.exp, nil, tc.channel)
			if verdictValue == verdictDivergence {
				t.Fatalf("a response cut at the probe's own token cap = divergence (%s; %s): truncation explains the absent structure, so this is written as supported=false", basis, shape)
			}
			if verdictValue != verdictInconclusive {
				t.Fatalf("a truncated absence = %q (%s), want %q", verdictValue, basis, verdictInconclusive)
			}
			row := probeResult{
				ChannelID: 1, Family: tc.exp.Family, Model: tc.exp.Family,
				Behavior: tc.exp.Behavior, Verdict: verdictValue, Strength: strength,
			}
			// The widest gate: an inconclusive row must not reach the payload
			// even when acceptance-level marks are allowed.
			if encoded, ok := resultToPayload(row, true); ok {
				t.Fatalf("an inconclusive truncated absence was encoded as supported=%v", encoded.Supported)
			}
		})
	}

	// A non-truncated absence is unchanged: the model finished below its budget
	// without producing the structure. That is the divergence the mark is for.
	answered := chatAnswer("stop", 3, "", false)
	verdictValue, strength, basis, _ := classify(toolsSpec, toolsExp, nil, answered)
	if verdictValue != verdictDivergence || strength != strengthStructural {
		t.Fatalf("an absent structure on a finished answer = (%q, %q) (%s), want (%q, %q)",
			verdictValue, strength, basis, verdictDivergence, strengthStructural)
	}
	divergentRow := probeResult{
		ChannelID: 1, Family: "kimi-k3", Model: "kimi-k3",
		Behavior: fitpolicy.BehaviorToolsChoiceSemantics, Verdict: verdictValue, Strength: strength,
	}
	if encoded, ok := resultToPayload(divergentRow, true); !ok || encoded.Supported {
		t.Fatalf("a non-truncated absence = (encodable=%v, supported=%v), want (true, false)", ok, encoded.Supported)
	}

	// Positive evidence is not an absence: a tool call that arrived even at the
	// cap stays supported=true.
	withCall := chatAnswer("length", 16, "", true)
	verdictValue, strength, basis, _ = classify(toolsSpec, toolsExp, nil, withCall)
	if verdictValue != verdictConsistent || strength != strengthStructural {
		t.Fatalf("a tool call produced at the cap = (%q, %q) (%s), want (%q, %q)",
			verdictValue, strength, basis, verdictConsistent, strengthStructural)
	}
	positiveRow := probeResult{
		ChannelID: 1, Family: "kimi-k3", Model: "kimi-k3",
		Behavior: fitpolicy.BehaviorToolsChoiceSemantics, Verdict: verdictValue, Strength: strength,
	}
	if encoded, ok := resultToPayload(positiveRow, false); !ok || !encoded.Supported {
		t.Fatalf("positive evidence = (encodable=%v, supported=%v), want (true, true)", ok, encoded.Supported)
	}

	// An explicit structural rejection is not an absence either: truncation
	// must not soften it into an inconclusive row.
	rejected := extractSignature(http.StatusBadRequest, []byte(`{"error":{"message":"bad request","param":"tool_choice"}}`))
	annotateErrorField(&rejected, toolsSpec.Fields)
	verdictValue, strength, basis, _ = classify(toolsSpec, toolsExp, nil, rejected)
	if verdictValue != verdictDivergence || strength != strengthStructural {
		t.Fatalf("an explicit structural rejection = (%q, %q) (%s), want (%q, %q)",
			verdictValue, strength, basis, verdictDivergence, strengthStructural)
	}
	rejectedRow := probeResult{
		ChannelID: 1, Family: "kimi-k3", Model: "kimi-k3",
		Behavior: fitpolicy.BehaviorToolsChoiceSemantics, Verdict: verdictValue, Strength: strength,
	}
	if encoded, ok := resultToPayload(rejectedRow, true); !ok || encoded.Supported {
		t.Fatalf("an explicit structural rejection = (encodable=%v, supported=%v), want (true, false)", ok, encoded.Supported)
	}

	// The incident end to end: a doc-only run (no --official-base-url) against a
	// channel that truncates the tools probe. Nothing may be encoded
	// supported=false, and the write path must not be handed a single false row.
	t.Run("a doc-only run over a truncating channel writes no supported=false", func(t *testing.T) {
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			answer := fakeCompletion(body)
			var request map[string]any
			_ = json.Unmarshal(body, &request)
			if _, probesTools := request["tool_choice"]; probesTools {
				// The recorded shape: empty content at the whole 16-token cap.
				answer["choices"] = []any{map[string]any{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": ""},
					"finish_reason": "length",
				}}
				answer["usage"] = map[string]any{"prompt_tokens": 23, "completion_tokens": 16, "total_tokens": 39}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(answer)
		}))
		defer relay.Close()

		admin := httptest.NewServer(fakeAdminMux(func() {
			t.Error("the report endpoint was hit although --write was never set")
		}))
		defer admin.Close()

		t.Setenv("FIT_PROBE_TEST_ADMIN_TOKEN", "admin-token")
		t.Setenv("FIT_PROBE_TEST_RELAY_TOKEN", "relay-token")

		opts := options{
			baseURL:       admin.URL,
			relayBaseURL:  relay.URL,
			adminTokenEnv: "FIT_PROBE_TEST_ADMIN_TOKEN",
			relayTokenEnv: "FIT_PROBE_TEST_RELAY_TOKEN",
			status:        "1",
			timeout:       5 * time.Second,
			suite:         "fit-probe-regression",
			explain:       true,
		}
		var stdout, stderr strings.Builder
		if err := run(context.Background(), opts, &stdout, &stderr); err != nil {
			t.Fatalf("run: %v (stderr: %s)", err, stderr.String())
		}
		var full fullReport
		if err := json.Unmarshal([]byte(stdout.String()), &full); err != nil {
			t.Fatalf("run output is not a full report: %v\n%s", err, stdout.String())
		}

		probed := false
		seen := make([]string, 0, len(full.Results))
		for _, row := range full.Results {
			seen = append(seen, row.Family+"/"+string(row.Behavior))
			if row.Behavior != fitpolicy.BehaviorToolsChoiceSemantics {
				continue
			}
			probed = true
			if row.Verdict == verdictDivergence {
				t.Fatalf("%s/%s = divergence on an answer the probe itself cut off (%s; %s)",
					row.Family, row.Behavior, row.Basis, row.DivergenceShape)
			}
		}
		if !probed {
			t.Fatalf("no %s row was probed (probed: %v); the fixture no longer covers the recorded incident",
				fitpolicy.BehaviorToolsChoiceSemantics, seen)
		}

		// The bytes the POST would carry: this is where a false supported=false
		// becomes a stored mark.
		var payload fitpolicy.SuiteReport
		if err := json.Unmarshal(full.Payload, &payload); err != nil {
			t.Fatalf("the payload is not a suite report: %v", err)
		}
		if len(payload.Results) == 0 {
			t.Fatal("the doc-only run encoded no results at all; the fixture no longer exercises the encoder")
		}
		for _, result := range payload.Results {
			if !result.Supported {
				t.Fatalf("%s/%s would have been submitted as supported=false although the only divergence evidence was an answer the probe cut off",
					result.Family, result.Behavior)
			}
		}
	})
}

// R9: R8's second truncation clause (completion_tokens >= max_tokens with no
// content) was behaviour-agnostic, so it also swallowed absences of an
// *envelope* field. logprobs does not need generation budget: it is attached to
// whatever tokens the upstream did emit, and a real run observed it at
// completion_tokens=1/max_tokens=1 (round 1, ch=21) and at 41 (round 2, ch=4).
// The cap therefore cannot explain a missing logprobs, and the guard turned
// every envelope-mark absence at the cap into an inconclusive row — the 16
// stored logprobs.dual_path=false marks of round 1 lost the divergence their
// evidence still supports. Post-fix the second clause applies only to marks
// whose evidence is generated output (tool_calls, reasoning accounting), and
// R10 later raised this probe's cap off 1 and narrowed the surviving
// length-cut clause to a cut that emitted no token at all. The envelope rule
// this test protects — the probe's own cap never explains an absent envelope
// field — is unchanged by that.
func TestR9EnvelopeAbsenceAtTheCapIsNotTruncation(t *testing.T) {
	logprobsSpec := probeSpecs[fitpolicy.BehaviorLogprobsDualPath]
	// The envelope register only reproduces at a cap the classifier reads; R10
	// pins the exact value, here only the floor that keeps the incident
	// (empty content at a spent cap) reachable matters.
	if logprobsSpec.MaxTokens < 2 {
		t.Fatalf("logprobs.dual_path probe cap = %d, want above 1: at 1 a spent cap is the only answer a channel can give", logprobsSpec.MaxTokens)
	}
	logprobsExp := expectationFor("deepseek-v4", fitpolicy.BehaviorLogprobsDualPath)

	// answer builds one successful envelope-mark answer: the given finish_reason
	// and completion count, empty content, and optionally the logprobs field.
	answer := func(reason string, completion int, logprobs bool) signature {
		choice := `{"finish_reason":"` + reason + `","message":{"content":""}`
		if logprobs {
			choice += `,"logprobs":{"content":[{"token":"hi","logprob":-0.1}]}`
		}
		choice += `}`
		body := `{"choices":[` + choice + `],"usage":{"prompt_tokens":5,"completion_tokens":` + itoa(completion) + `}}`
		return extractSignature(200, []byte(body))
	}

	// The envelope evidence decides on its own merits: present means consistent,
	// absent at the cap without finish_reason=length means the divergence the
	// mark exists to record.
	decisive := []struct {
		name    string
		channel signature
		verdict verdict
	}{
		{
			name:    "logprobs present at the cap with empty content",
			channel: answer("stop", logprobsSpec.MaxTokens, true),
			verdict: verdictConsistent,
		},
		{
			name:    "logprobs absent at the cap with empty content",
			channel: answer("stop", logprobsSpec.MaxTokens, false),
			verdict: verdictDivergence,
		},
		{
			name:    "logprobs absent past the cap with empty content",
			channel: answer("stop", logprobsSpec.MaxTokens*16, false),
			verdict: verdictDivergence,
		},
	}
	for _, tc := range decisive {
		t.Run(tc.name, func(t *testing.T) {
			verdictValue, strength, basis, _ := classify(logprobsSpec, logprobsExp, nil, tc.channel)
			if verdictValue != tc.verdict || strength != strengthStructural {
				t.Fatalf("envelope mark at the cap = (%q, %q) (%s), want (%q, %q): the probe's own token cap cannot explain a missing envelope field",
					verdictValue, strength, basis, tc.verdict, strengthStructural)
			}
			row := probeResult{
				ChannelID: 1, Family: "deepseek-v4", Model: "deepseek-v4",
				Behavior: fitpolicy.BehaviorLogprobsDualPath, Verdict: verdictValue, Strength: strength,
			}
			encoded, ok := resultToPayload(row, false)
			if !ok || encoded.Supported != (tc.verdict == verdictConsistent) {
				t.Fatalf("envelope mark at the cap = (encodable=%v, supported=%v), want (true, %v)",
					ok, encoded.Supported, tc.verdict == verdictConsistent)
			}
		})
	}

	// The one length cut that still downgrades an envelope mark: it emitted no
	// token at all, so there was nothing the upstream could have attached
	// logprobs to. (A cut that did emit a token is decided on that token; R10
	// owns that rule and its ch=21 evidence.)
	cut := []struct {
		name    string
		channel signature
	}{
		{name: "finish_reason=length with no token emitted", channel: answer("length", 0, false)},
	}
	for _, tc := range cut {
		t.Run(tc.name, func(t *testing.T) {
			verdictValue, _, basis, _ := classify(logprobsSpec, logprobsExp, nil, tc.channel)
			if verdictValue != verdictInconclusive {
				t.Fatalf("a length-cut envelope answer = %q (%s), want %q", verdictValue, basis, verdictInconclusive)
			}
			row := probeResult{
				ChannelID: 1, Family: "deepseek-v4", Model: "deepseek-v4",
				Behavior: fitpolicy.BehaviorLogprobsDualPath, Verdict: verdictValue, Strength: strengthStructural,
			}
			if encoded, ok := resultToPayload(row, true); ok {
				t.Fatalf("an inconclusive length cut was encoded as supported=%v", encoded.Supported)
			}
		})
	}

	// Generated-output marks are unchanged: the evidence (tool_calls, reasoning
	// accounting) can only arrive as sampled completion, so a completion that
	// spent the whole budget on empty content is still truncation-explained.
	generated := []struct {
		name    string
		spec    ProbeSpec
		exp     Expectation
		channel signature
	}{
		{
			name: "kimi-k3 tools at the live cap with empty content",
			spec: probeSpecs[fitpolicy.BehaviorToolsChoiceSemantics],
			exp:  expectationFor("kimi-k3", fitpolicy.BehaviorToolsChoiceSemantics),
			channel: func() signature {
				cap := probeSpecs[fitpolicy.BehaviorToolsChoiceSemantics].MaxTokens
				return extractSignature(200, []byte(`{"choices":[{"finish_reason":"stop","message":{"content":""}}],"usage":{"prompt_tokens":23,"completion_tokens":`+itoa(cap)+`}}`))
			}(),
		},
		{
			name: "deepseek-v4 thinking accounting at 8/8 with empty content",
			spec: probeSpecs[fitpolicy.BehaviorThinkingCounting],
			exp:  expectationFor("deepseek-v4", fitpolicy.BehaviorThinkingCounting),
			channel: func() signature {
				return extractSignature(200, []byte(`{"choices":[{"finish_reason":"stop","message":{"content":""}}],"usage":{"prompt_tokens":23,"completion_tokens":8}}`))
			}(),
		},
	}
	for _, tc := range generated {
		t.Run(tc.name, func(t *testing.T) {
			verdictValue, _, basis, _ := classify(tc.spec, tc.exp, nil, tc.channel)
			if verdictValue != verdictInconclusive {
				t.Fatalf("a generated-output absence at the cap = %q (%s), want %q", verdictValue, basis, verdictInconclusive)
			}
		})
	}
}

// R10: the logprobs.dual_path probe asked for max_tokens=1, so every channel
// that accepted the request answered with finish_reason=length at
// completion_tokens=1=max_tokens and the truncation guard had to call the
// absent logprobs field inconclusive. The probe thus manufactured the very
// truncation that made 11 of the 15 stranded supported=false marks undecidable
// by any number of rounds, and the mark could never be asserted again.
//
// The 1-token cap proves nothing about logprobs: a real run observed a
// supporting channel answering the identical request with
// finish_reason=length *and* one logprobs token (ch=21, deepseek-v4-flash and
// deepseek-v4.1-flash, round 3), i.e. a length cut does not prevent an envelope
// field — the field is attached per emitted token, not paid for out of the
// remaining budget. Post-fix the probe asks for logprobsProbeMaxTokens (8) —
// the smallest budget in this table that lets a one-word answer finish
// naturally — and the guard's length clause survives for an envelope mark only
// when the cut produced no token at all, i.e. only when there was nothing the
// upstream could have attached logprobs to.
func TestR10LogprobsProbeBudgetIsNotAGuaranteedCut(t *testing.T) {
	// The expectation is pinned here, not read from the code under test: a test
	// that derives its want from the implementation cannot fail before the fix.
	const wantLogprobsCap = 8
	spec := probeSpecs[fitpolicy.BehaviorLogprobsDualPath]

	// The probe budget itself: above the guaranteed-cut floor of 1, declared
	// once, and actually carried by the request that goes on the wire.
	t.Run("probe budget is above the guaranteed-cut floor", func(t *testing.T) {
		if wantLogprobsCap < 2 {
			t.Fatalf("pinned logprobs probe budget = %d, want above 1: at 1 every accepting channel answers finish_reason=length and the absence is explained by the probe budget", wantLogprobsCap)
		}
		if spec.MaxTokens != wantLogprobsCap {
			t.Fatalf("logprobs.dual_path spec cap = %d, want %d: the classifier reads spec.MaxTokens while the request carries the body's value", spec.MaxTokens, wantLogprobsCap)
		}
		body := spec.Build("deepseek-v4", "deepseek-v4-flash")
		got, ok := body["max_tokens"].(int)
		if !ok || got != wantLogprobsCap {
			t.Fatalf("logprobs.dual_path request max_tokens = %v, want %d: the measured budget must be the sent budget", body["max_tokens"], wantLogprobsCap)
		}
		// Still the minimal envelope probe: the mark's own two fields and nothing
		// else that could raise cost or change semantics.
		if body["logprobs"] != true || body["top_logprobs"] != 1 || body["stream"] != false {
			t.Fatalf("logprobs.dual_path request is no longer the minimal envelope probe: %v", body)
		}
	})

	// answer builds one successful envelope-mark answer: the given finish_reason
	// and completion count, empty content, and optionally the logprobs field.
	answer := func(reason string, completion int, logprobs bool) signature {
		choice := `{"finish_reason":"` + reason + `","message":{"content":""}`
		if logprobs {
			choice += `,"logprobs":{"content":[{"token":"hi","logprob":-0.1}]}`
		}
		choice += `}`
		usage := `,"usage":{"prompt_tokens":5,"completion_tokens":` + itoa(completion) + `}`
		if completion < 0 {
			usage = ""
		}
		return extractSignature(200, []byte(`{"choices":[`+choice+`]`+usage+`}`))
	}
	logprobsExp := expectationFor("deepseek-v4", fitpolicy.BehaviorLogprobsDualPath)

	// A length cut that emitted tokens keeps its evidentiary value: the channel
	// generated a token and did not attach logprobs to it, which is exactly the
	// divergence this mark records. Pre-fix both of these were inconclusive.
	for _, completion := range []int{1, 41} {
		t.Run("length cut with generated tokens still decides (completion="+itoa(completion)+")", func(t *testing.T) {
			verdictValue, strength, basis, _ := classify(spec, logprobsExp, nil, answer("length", completion, false))
			if verdictValue != verdictDivergence || strength != strengthStructural {
				t.Fatalf("a length cut that emitted %d token(s) without logprobs = (%q, %q) (%s), want (%q, %q): the cut did not prevent the field, so the absence is the channel's",
					completion, verdictValue, strength, basis, verdictDivergence, strengthStructural)
			}
			row := probeResult{
				ChannelID: 1, Family: "deepseek-v4", Model: "deepseek-v4",
				Behavior: fitpolicy.BehaviorLogprobsDualPath, Verdict: verdictValue, Strength: strength,
			}
			encoded, ok := resultToPayload(row, false)
			if !ok || encoded.Supported {
				t.Fatalf("a decided envelope divergence was encoded as (encodable=%v, supported=%v), want (true, false)", ok, encoded.Supported)
			}
		})
	}

	// The one cut the budget really can explain: no token was generated, so
	// there was nothing to attach logprobs to. This is also the case where the
	// usage block is missing (-1), because absence of evidence is not evidence.
	for _, completion := range []int{0, -1} {
		t.Run("length cut with no token at all stays inconclusive (completion="+itoa(completion)+")", func(t *testing.T) {
			verdictValue, _, basis, _ := classify(spec, logprobsExp, nil, answer("length", completion, false))
			if verdictValue != verdictInconclusive {
				t.Fatalf("a length cut with no generated token = %q (%s), want %q", verdictValue, basis, verdictInconclusive)
			}
			row := probeResult{
				ChannelID: 1, Family: "deepseek-v4", Model: "deepseek-v4",
				Behavior: fitpolicy.BehaviorLogprobsDualPath, Verdict: verdictValue, Strength: strengthStructural,
			}
			if encoded, ok := resultToPayload(row, true); ok {
				t.Fatalf("an inconclusive length cut was encoded as supported=%v", encoded.Supported)
			}
		})
	}

	// One-directional by construction: the same cut that now decides an absence
	// still counts as evidence *for* the mark when the field is present, and a
	// generated-output mark is untouched (its evidence can only arrive as
	// sampled completion, so a spent budget still explains its absence).
	t.Run("the guard only ever demotes, never promotes", func(t *testing.T) {
		verdictValue, _, basis, _ := classify(spec, logprobsExp, nil, answer("length", 1, true))
		if verdictValue != verdictConsistent {
			t.Fatalf("logprobs present under a length cut = %q (%s), want %q", verdictValue, basis, verdictConsistent)
		}
		toolsSpec := probeSpecs[fitpolicy.BehaviorToolsChoiceSemantics]
		toolsExp := expectationFor("kimi-k3", fitpolicy.BehaviorToolsChoiceSemantics)
		spent := extractSignature(200, []byte(`{"choices":[{"finish_reason":"length","message":{"content":""}}],"usage":{"prompt_tokens":23,"completion_tokens":16}}`))
		verdictValue, _, basis, _ = classify(toolsSpec, toolsExp, nil, spent)
		if verdictValue != verdictInconclusive {
			t.Fatalf("a generated-output absence under a length cut = %q (%s), want %q", verdictValue, basis, verdictInconclusive)
		}
	})
}

// R11: the tools.choice_semantics probe asked for max_tokens=16, but a channel
// that really answers tool_choice=required with a tool call spends far more than
// that: a real doc-only run observed the three channels that did return
// tool_calls needing 60-76 completion tokens, while all eight kimi-k3 rows that
// returned no tool_calls ended exactly at 16/16 with empty content. At a
// 16-token budget the probe cut the answer off before the tool call could be
// emitted, so the absence was an artefact of the probe rather than a fact about
// the channel — the same class of artefact logprobsProbeMaxTokens was raised
// for, and the reason round 1 withheld exactly these rows by hand and a later
// round downgraded them as truncation.
//
// Post-fix the probe asks for toolsChoiceSemanticsProbeMaxTokens, declared once
// and carried by both spec.MaxTokens and the request body, and the two
// behaviours the truncation guard must keep apart are pinned here: a response
// that genuinely ends at the new cap without tool_calls is still inconclusive
// (the budget can explain the absence), while a response that finishes
// normally below the cap without tool_calls is the divergence the mark exists
// to record.
func TestR11ToolsChoiceSemanticsProbeBudgetLetsAToolCallFinish(t *testing.T) {
	// The expectation is pinned here, not read from the code under test: a test
	// that derives its want from the implementation cannot fail before the fix.
	const wantToolsCap = 96
	// The largest tool-call completion count a real run observed (60-76 range).
	const observedMaxToolCallTokens = 76
	spec := probeSpecs[fitpolicy.BehaviorToolsChoiceSemantics]
	exp := expectationFor("kimi-k3", fitpolicy.BehaviorToolsChoiceSemantics)

	// The probe budget itself: above the observed cost of a real tool call,
	// declared once, and actually carried by the request that goes on the wire.
	t.Run("probe budget is above the observed tool-call cost", func(t *testing.T) {
		if wantToolsCap <= observedMaxToolCallTokens {
			t.Fatalf("pinned tools probe budget = %d, want above the observed %d-token tool call: a smaller budget cuts the answer off before the tool call can be emitted",
				wantToolsCap, observedMaxToolCallTokens)
		}
		if spec.MaxTokens != wantToolsCap {
			t.Fatalf("tools.choice_semantics spec cap = %d, want %d: the classifier reads spec.MaxTokens while the request carries the body's value", spec.MaxTokens, wantToolsCap)
		}
		body := spec.Build("kimi-k3", "kimi-k3")
		got, ok := body["max_tokens"].(int)
		if !ok || got != wantToolsCap {
			t.Fatalf("tools.choice_semantics request max_tokens = %v, want %d: the measured budget must be the sent budget", body["max_tokens"], wantToolsCap)
		}
		// Still the minimal tool probe: the mark's own fields and nothing else
		// that could raise cost or change semantics.
		if body["tool_choice"] != "required" || body["stream"] != false || body["tools"] == nil {
			t.Fatalf("tools.choice_semantics request is no longer the minimal tool probe: %v", body)
		}
	})

	// answer builds one successful tool-probe answer at the given completion
	// count, with an optional tool call.
	answer := func(reason string, completion int, content string, toolCall bool) signature {
		message := `"content":"` + content + `"`
		if toolCall {
			message += `,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"ping","arguments":"{}"}}]`
		}
		body := `{"choices":[{"finish_reason":"` + reason + `","message":{` + message + `}}],` +
			`"usage":{"prompt_tokens":23,"completion_tokens":` + itoa(completion) + `}}`
		return extractSignature(200, []byte(body))
	}

	// A response that genuinely ends at the new cap with no tool_calls is still
	// budget-explained whichever finish_reason it carries: the model may have
	// spent the whole budget before the call could be emitted.
	for _, reason := range []string{"length", "stop"} {
		t.Run("a response ending at the cap without tool_calls stays inconclusive ("+reason+")", func(t *testing.T) {
			verdictValue, strength, basis, _ := classify(spec, exp, nil, answer(reason, wantToolsCap, "", false))
			if verdictValue != verdictInconclusive {
				t.Fatalf("a response ending at the probe cap (max_tokens=%d, completion_tokens=%d, finish_reason=%s) = %q (%s), want %q: a spent budget explains the absent tool call",
					wantToolsCap, wantToolsCap, reason, verdictValue, basis, verdictInconclusive)
			}
			row := probeResult{
				ChannelID: 1, Family: "kimi-k3", Model: "kimi-k3",
				Behavior: fitpolicy.BehaviorToolsChoiceSemantics, Verdict: verdictValue, Strength: strength,
			}
			// The widest gate: an inconclusive row must not reach the payload
			// even when acceptance-level marks are allowed.
			if encoded, ok := resultToPayload(row, true); ok {
				t.Fatalf("an inconclusive truncated absence was encoded as supported=%v", encoded.Supported)
			}
		})
	}

	// A response that finishes normally below the cap without tool_calls is the
	// divergence the mark exists to record: the budget cannot explain it.
	t.Run("a response finishing below the cap without tool_calls is a divergence", func(t *testing.T) {
		verdictValue, strength, basis, _ := classify(spec, exp, nil, answer("stop", observedMaxToolCallTokens, "I cannot call functions.", false))
		if verdictValue != verdictDivergence || strength != strengthStructural {
			t.Fatalf("a response that finished below the cap without tool_calls = (%q, %q) (%s), want (%q, %q)",
				verdictValue, strength, basis, verdictDivergence, strengthStructural)
		}
		row := probeResult{
			ChannelID: 1, Family: "kimi-k3", Model: "kimi-k3",
			Behavior: fitpolicy.BehaviorToolsChoiceSemantics, Verdict: verdictValue, Strength: strength,
		}
		encoded, ok := resultToPayload(row, false)
		if !ok || encoded.Supported {
			t.Fatalf("a decided tool-call divergence = (encodable=%v, supported=%v), want (true, false)", ok, encoded.Supported)
		}
	})

	// Positive evidence stays positive at the new cap: a tool call that arrived
	// within budget is the consistency the mark records.
	t.Run("a tool call within the new budget is consistent", func(t *testing.T) {
		verdictValue, strength, basis, _ := classify(spec, exp, nil, answer("tool_calls", observedMaxToolCallTokens, "", true))
		if verdictValue != verdictConsistent || strength != strengthStructural {
			t.Fatalf("a tool call produced inside the budget = (%q, %q) (%s), want (%q, %q)",
				verdictValue, strength, basis, verdictConsistent, strengthStructural)
		}
	})
}

func testBinding() policyBinding {
	return policyBinding{
		Version:  1,
		Hash:     strings.Repeat("a", 64),
		Baseline: strings.Repeat("b", 64),
	}
}

// ---------------------------------------------------------------------------
// R12-R19: --rounds (majority measurement) and --payload-in (a curated payload
// written by the tool itself).
//
// These tests drive the real flag surface through parseFlags and the real
// orchestration through run(), against httptest fakes only: no production
// endpoint is contacted and no channel is probed outside the fakes. The new
// fields are read generically from the JSON output rather than through their Go
// types, so this file still compiles against the pre-change package — which is
// what the recorded pre-change failure text needs.
// ---------------------------------------------------------------------------

// scriptedAnswer is one fake relay answer: a status and a body.
type scriptedAnswer struct {
	status int
	body   string
}

// The three answers the logprobs probe classifies deterministically:
//   - consistent: the channel returns token-level logprobs (structural evidence)
//   - divergence: it accepts the probe and returns no logprobs at all
//   - inconclusive: it rejects the probe without structurally naming the field
//
// answerPlainOK is the acceptance-only answer of the glm-5.3 family.whole
// probe: a plain 200 the mark can only confirm at acceptance strength.
var (
	answerConsistent   = scriptedAnswer{status: http.StatusOK, body: `{"choices":[{"finish_reason":"stop","message":{"content":"ok"},"logprobs":{"content":[{"token":"hi","logprob":-0.1}]}}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`}
	answerDivergence   = scriptedAnswer{status: http.StatusOK, body: `{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`}
	answerInconclusive = scriptedAnswer{status: http.StatusBadRequest, body: `{"error":{"message":"upstream failed","code":"upstream_error"}}`}
	answerRetryable    = scriptedAnswer{status: http.StatusServiceUnavailable, body: `{"error":{"message":"relay busy","code":"upstream_error"}}`}
	answerPlainOK      = scriptedAnswer{status: http.StatusOK, body: `{"choices":[{"finish_reason":"stop","message":{"content":"hi"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`}
)

// scriptedRelay answers the nth request with the nth scripted answer and
// repeats the last one, so a test can script a per-round verdict. served counts
// the requests it answered, which is how a test pins the cost of --rounds.
func scriptedRelay(answers []scriptedAnswer, served *int32) *httptest.Server {
	var mu sync.Mutex
	next := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		position := next
		if position >= len(answers) {
			position = len(answers) - 1
		}
		next++
		mu.Unlock()
		atomic.AddInt32(served, 1)
		answer := answers[position]
		status := answer.status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer.body)
	}))
}

// postRecorder records the report bodies a fake admin endpoint received.
type postRecorder struct {
	mu     sync.Mutex
	bodies []string
}

func (r *postRecorder) record(body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, string(body))
}

func (r *postRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...)
}

// scopedAdminMux serves the two admin reads for one channel advertising the
// given model ids, and records every report POST body.
func scopedAdminMux(channelID int, models string, posts *postRecorder) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/fit-policy", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"source":             "test",
				"effective_document": "",
				"live": map[string]any{
					"installed": true, "version": 1, "enabled": true, "shadow": false,
					"hash": testBinding().Hash, "baseline": testBinding().Baseline,
				},
			},
		})
	})
	mux.HandleFunc("/api/channel/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"items": []any{map[string]any{
					"id": channelID, "name": "fake", "type": 1, "status": 1, "models": models,
				}},
				"total": 1,
			},
		})
	})
	mux.HandleFunc("/api/fit-capability/report", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		posts.record(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	return mux
}

// roundProbeArgs is the flag set the multi-round tests run with: exactly one
// deepseek-v4 logprobs row on the fake channel, so one probe is sent per round
// and the scripted answers map one-to-one onto rounds. The report id and run id
// are pinned so two runs can be compared byte for byte.
func roundProbeArgs(adminURL, relayURL string, extra ...string) []string {
	args := []string{
		"--dry-run=false",
		"--base-url", adminURL,
		"--relay-base-url", relayURL,
		"--admin-token-env", "FIT_PROBE_TEST_ADMIN_TOKEN",
		"--relay-token-env", "FIT_PROBE_TEST_RELAY_TOKEN",
		"--channel-status", "1",
		"--timeout", "5s",
		"--suite", "fit-probe-regression",
		"--report-id", "report-1",
		"--run-id", "run-1",
		"--family", "deepseek-v4",
		"--model", "deepseek-v4-pro",
		"--behaviors", fitpolicy.BehaviorLogprobsDualPath,
	}
	return append(args, extra...)
}

// withoutDryRun removes the --dry-run=false pair from a flag set so a test can
// exercise the default dry-run gate.
func withoutDryRun(args []string) []string {
	out := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		if strings.HasPrefix(args[index], "--dry-run=") {
			continue
		}
		out = append(out, args[index])
	}
	return out
}

// flaggedOptions parses the flags the way an operator would, so the test drives
// the real flag surface instead of a hand-built options value.
func flaggedOptions(t *testing.T, args []string) options {
	t.Helper()
	opts, err := parseFlags(args)
	if err != nil {
		t.Fatalf("parseFlags(%v): %v", args, err)
	}
	return opts
}

// runProbe runs the tool against the fakes and returns stdout, stderr and the
// error.
func runProbe(t *testing.T, opts options) (string, string, error) {
	t.Helper()
	var stdout, stderr strings.Builder
	err := run(context.Background(), opts, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

// decodeObject decodes one JSON document into a generic object, which is how
// these tests read fields a pre-change build does not have.
func decodeObject(t *testing.T, raw, what string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("%s is not a JSON object: %v\n%s", what, err, raw)
	}
	return decoded
}

// decodeObjects decodes the array at key into objects.
func decodeObjects(t *testing.T, object map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := object[key].([]any)
	if !ok {
		t.Fatalf("%q is missing or not an array in %v", key, object)
	}
	objects := make([]map[string]any, 0, len(raw))
	for index, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("%s[%d] is not an object", key, index)
		}
		objects = append(objects, item)
	}
	return objects
}

// payloadOf extracts the embedded payload object from an --explain document.
func payloadOf(t *testing.T, full map[string]any) (map[string]any, []map[string]any) {
	t.Helper()
	payload, ok := full["payload"].(map[string]any)
	if !ok {
		t.Fatalf("the explain document has no payload object: %T", full["payload"])
	}
	return payload, decodeObjects(t, payload, "results")
}

// normalizedPayload drops the one field two runs cannot pin (generated_at), so
// their payloads can be compared exactly.
func normalizedPayload(t *testing.T, raw, what string) string {
	t.Helper()
	payload := decodeObject(t, raw, what)
	delete(payload, "generated_at")
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("normalizing %s: %v", what, err)
	}
	return string(encoded)
}

// curatedFile writes a curated payload file and returns its path.
func curatedFile(t *testing.T, report fitpolicy.SuiteReport) string {
	t.Helper()
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshalling the curated payload: %v", err)
	}
	path := filepath.Join(t.TempDir(), "curated.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("writing the curated payload: %v", err)
	}
	return path
}

// curatedPayload builds the file an operator would hand to --payload-in: the
// rows and their supported flags. The envelope is the tool's own schema, so the
// shape check is exercised too.
func curatedPayload(rounds int, results ...fitpolicy.SuiteResult) fitpolicy.SuiteReport {
	return fitpolicy.SuiteReport{
		ReportID:      "curated-report",
		RunID:         "curated-run",
		Suite:         "curated-suite",
		PolicyVersion: 1,
		PolicyHash:    testBinding().Hash,
		BaselineHash:  testBinding().Baseline,
		GeneratedAt:   1700000000,
		Rounds:        rounds,
		Results:       results,
	}
}

// deepseekLogprobsRow is the one row the multi-round fixtures probe.
func deepseekLogprobsRow(supported bool) fitpolicy.SuiteResult {
	return fitpolicy.SuiteResult{
		ChannelID: 7, Family: "deepseek-v4", Model: "deepseek-v4-pro",
		Behavior: fitpolicy.BehaviorLogprobsDualPath, Supported: supported, Cases: "1/1",
	}
}

// R12: the single-round run is the untouched default. One sample decides, the
// relay is asked exactly once, the payload still claims rounds 1 with cases
// 1/1, and the row carries no round bookkeeping. The three scripted answers
// would produce a different verdict — and three requests — if a round count
// leaked into the default path.
func TestR12SingleRoundIsUnchangedAndIsTheDefault(t *testing.T) {
	t.Setenv("FIT_PROBE_TEST_ADMIN_TOKEN", "admin-token")
	t.Setenv("FIT_PROBE_TEST_RELAY_TOKEN", "relay-token")

	// The first answer decides; the other two must never be consumed.
	answers := []scriptedAnswer{answerConsistent, answerDivergence, answerInconclusive}

	var servedDefault int32
	relayDefault := scriptedRelay(answers, &servedDefault)
	defer relayDefault.Close()
	adminDefault := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", &postRecorder{}))
	defer adminDefault.Close()
	stdoutDefault, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(adminDefault.URL, relayDefault.URL)))
	if err != nil {
		t.Fatalf("default run: %v", err)
	}

	var servedExplicit int32
	relayExplicit := scriptedRelay(answers, &servedExplicit)
	defer relayExplicit.Close()
	adminExplicit := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", &postRecorder{}))
	defer adminExplicit.Close()
	stdoutExplicit, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(adminExplicit.URL, relayExplicit.URL, "--rounds", "1")))
	if err != nil {
		t.Fatalf("--rounds 1 run: %v", err)
	}

	if got := atomic.LoadInt32(&servedDefault); got != 1 {
		t.Fatalf("the default run sent %d probe requests, want 1: the default must stay the single-sample run", got)
	}
	if got := atomic.LoadInt32(&servedExplicit); got != 1 {
		t.Fatalf("--rounds 1 sent %d probe requests, want 1", got)
	}

	payload := decodeObject(t, stdoutDefault, "default payload")
	rows := decodeObjects(t, payload, "results")
	if got := payload["rounds"]; got != float64(1) {
		t.Errorf("payload rounds = %v, want 1", got)
	}
	if len(rows) != 1 {
		t.Fatalf("payload carries %d rows, want 1:\n%s", len(rows), stdoutDefault)
	}
	if rows[0]["supported"] != true || rows[0]["cases"] != "1/1" {
		t.Errorf("single-round row = %v, want supported=true and cases=1/1", rows[0])
	}
	if _, present := rows[0]["round_records"]; present {
		t.Error("a single-round row carries round_records: the single-round JSON shape changed")
	}
	if got, want := normalizedPayload(t, stdoutExplicit, "--rounds 1 output"), normalizedPayload(t, stdoutDefault, "default output"); got != want {
		t.Errorf("--rounds 1 produced a different payload than the default:\n%s\n%s", got, want)
	}

	// The explain row is the first sample, verbatim. Under any majority rule the
	// other two scripted answers would have changed the verdict and left round
	// records behind.
	var servedExplain int32
	relayExplain := scriptedRelay(answers, &servedExplain)
	defer relayExplain.Close()
	adminExplain := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", &postRecorder{}))
	defer adminExplain.Close()
	stdoutExplain, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(adminExplain.URL, relayExplain.URL, "--explain")))
	if err != nil {
		t.Fatalf("explain run: %v", err)
	}
	explainRows := decodeObjects(t, decodeObject(t, stdoutExplain, "explain output"), "results")
	if len(explainRows) != 1 {
		t.Fatalf("the explain output carries %d rows, want 1", len(explainRows))
	}
	if explainRows[0]["verdict"] != "consistent" || explainRows[0]["http_status"] != float64(http.StatusOK) {
		t.Errorf("single-round row = %v/%v, want the first sample (consistent/200)", explainRows[0]["verdict"], explainRows[0]["http_status"])
	}
	if _, present := explainRows[0]["round_records"]; present {
		t.Error("a single-round explain row carries round_records")
	}
}

// R13: three agreeing rounds are encoded as the run's own payload, each round
// is exactly one single-shot probe, and the pre-existing retry stays inside its
// round instead of becoming a round of its own.
func TestR13ThreeRoundMajorityIsEncoded(t *testing.T) {
	t.Setenv("FIT_PROBE_TEST_ADMIN_TOKEN", "admin-token")
	t.Setenv("FIT_PROBE_TEST_RELAY_TOKEN", "relay-token")

	t.Run("three consistent rounds encode supported=true 3/3", func(t *testing.T) {
		var served int32
		relay := scriptedRelay([]scriptedAnswer{answerConsistent}, &served)
		defer relay.Close()
		admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", &postRecorder{}))
		defer admin.Close()

		stdout, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(admin.URL, relay.URL, "--rounds", "3", "--explain")))
		if err != nil {
			t.Fatalf("--rounds 3 run: %v", err)
		}
		if got := atomic.LoadInt32(&served); got != 3 {
			t.Fatalf("--rounds 3 sent %d probe requests, want 3: rounds are sequential and single-shot", got)
		}
		full := decodeObject(t, stdout, "explain output")
		if full["rounds"] != float64(3) {
			t.Errorf("explain rounds = %v, want 3", full["rounds"])
		}
		payload, rows := payloadOf(t, full)
		if payload["rounds"] != float64(3) {
			t.Errorf("payload rounds = %v, want 3", payload["rounds"])
		}
		if len(rows) != 1 || rows[0]["supported"] != true || rows[0]["cases"] != "3/3" {
			t.Fatalf("majority payload = %v, want one supported=true row with cases 3/3", rows)
		}
		records := decodeObjects(t, decodeObjects(t, full, "results")[0], "round_records")
		if len(records) != 3 {
			t.Fatalf("3 rounds produced %d round records, want 3", len(records))
		}
		for index, record := range records {
			if record["round"] != float64(index+1) || record["verdict"] != "consistent" {
				t.Errorf("round record %d = %v, want round %d consistent", index, record, index+1)
			}
			if record["attempts"] != float64(1) {
				t.Errorf("round %d took %v attempts, want 1: no round may retry", index+1, record["attempts"])
			}
		}
		if summary, ok := full["summary"].(map[string]any); !ok || summary["probe_requests"] != float64(3) {
			t.Errorf("summary = %v, want probe_requests=3", full["summary"])
		}
	})

	// The run-4 operator case: consistent / inconclusive / consistent was
	// written true by majority (2 of 3) although one round measured nothing.
	t.Run("two consistent rounds and one abstention encode 2/3", func(t *testing.T) {
		var served int32
		relay := scriptedRelay([]scriptedAnswer{answerConsistent, answerInconclusive, answerConsistent}, &served)
		defer relay.Close()
		admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", &postRecorder{}))
		defer admin.Close()

		stdout, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(admin.URL, relay.URL, "--rounds", "3")))
		if err != nil {
			t.Fatalf("--rounds 3 run: %v", err)
		}
		if got := atomic.LoadInt32(&served); got != 3 {
			t.Fatalf("3 rounds sent %d probe requests, want 3", got)
		}
		rows := decodeObjects(t, decodeObject(t, stdout, "run payload"), "results")
		if len(rows) != 1 || rows[0]["supported"] != true || rows[0]["cases"] != "2/3" {
			t.Fatalf("2-of-3 majority payload = %v, want one supported=true row with cases 2/3", rows)
		}
	})

	// The existing one retry is a second request inside the same round: it must
	// not be counted as another measurement round.
	t.Run("a retry stays inside its round", func(t *testing.T) {
		var served int32
		relay := scriptedRelay([]scriptedAnswer{answerRetryable, answerConsistent, answerConsistent}, &served)
		defer relay.Close()
		admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", &postRecorder{}))
		defer admin.Close()

		stdout, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(admin.URL, relay.URL, "--rounds", "2", "--explain")))
		if err != nil {
			t.Fatalf("--rounds 2 run: %v", err)
		}
		if got := atomic.LoadInt32(&served); got != 3 {
			t.Fatalf("the relay saw %d requests, want 3: round 1 retried once, round 2 did not", got)
		}
		full := decodeObject(t, stdout, "explain output")
		records := decodeObjects(t, decodeObjects(t, full, "results")[0], "round_records")
		if len(records) != 2 {
			t.Fatalf("2 rounds produced %d round records, want 2", len(records))
		}
		if records[0]["attempts"] != float64(2) || records[1]["attempts"] != float64(1) {
			t.Errorf("round attempts = %v/%v, want 2/1", records[0]["attempts"], records[1]["attempts"])
		}
		_, rows := payloadOf(t, full)
		if len(rows) != 1 || rows[0]["supported"] != true || rows[0]["cases"] != "2/2" {
			t.Fatalf("payload = %v, want one supported=true row with cases 2/2", rows)
		}
	})
}

// R14: a 2-of-3 split is decided by the majority, not by the last or the first
// sample, and the majority payload is what the single write path posts.
func TestR14TwoOfThreeMajorityIsEncodedPerMajority(t *testing.T) {
	t.Setenv("FIT_PROBE_TEST_ADMIN_TOKEN", "admin-token")
	t.Setenv("FIT_PROBE_TEST_RELAY_TOKEN", "relay-token")

	cases := []struct {
		name          string
		answers       []scriptedAnswer
		wantSupported bool
	}{
		{
			name:          "divergence wins 2 of 3 although the last round was consistent",
			answers:       []scriptedAnswer{answerDivergence, answerDivergence, answerConsistent},
			wantSupported: false,
		},
		{
			name:          "consistency wins 2 of 3 although the last round diverged",
			answers:       []scriptedAnswer{answerConsistent, answerConsistent, answerDivergence},
			wantSupported: true,
		},
		{
			name:          "consistency wins with the split in the middle",
			answers:       []scriptedAnswer{answerConsistent, answerDivergence, answerConsistent},
			wantSupported: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var served int32
			relay := scriptedRelay(tc.answers, &served)
			defer relay.Close()
			admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", &postRecorder{}))
			defer admin.Close()

			stdout, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(admin.URL, relay.URL, "--rounds", "3")))
			if err != nil {
				t.Fatalf("--rounds 3 run: %v", err)
			}
			if got := atomic.LoadInt32(&served); got != 3 {
				t.Fatalf("3 rounds sent %d probe requests, want 3", got)
			}
			rows := decodeObjects(t, decodeObject(t, stdout, "run payload"), "results")
			if len(rows) != 1 {
				t.Fatalf("payload carries %d rows, want 1 (a 2-of-3 split is decided): %v", len(rows), rows)
			}
			if rows[0]["supported"] != tc.wantSupported || rows[0]["cases"] != "2/3" {
				t.Fatalf("2-of-3 majority = supported=%v cases=%v, want supported=%v cases=2/3",
					rows[0]["supported"], rows[0]["cases"], tc.wantSupported)
			}
		})
	}

	t.Run("the majority payload is what the single write path posts", func(t *testing.T) {
		answers := []scriptedAnswer{answerDivergence, answerDivergence, answerConsistent}

		posts := &postRecorder{}
		var served int32
		relay := scriptedRelay(answers, &served)
		defer relay.Close()
		admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", posts))
		defer admin.Close()

		stdout, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(admin.URL, relay.URL, "--rounds", "3", "--write")))
		if err != nil {
			t.Fatalf("--rounds 3 --write run: %v", err)
		}
		bodies := posts.all()
		if len(bodies) != 1 {
			t.Fatalf("the report endpoint received %d bodies, want exactly 1", len(bodies))
		}
		if strings.TrimSpace(bodies[0]) != strings.TrimSpace(stdout) {
			t.Errorf("the POST body is not the payload the run printed:\n--- posted\n%s\n--- printed\n%s", bodies[0], stdout)
		}
		posted := decodeObject(t, bodies[0], "posted body")
		if posted["rounds"] != float64(3) {
			t.Errorf("posted rounds = %v, want 3", posted["rounds"])
		}
		rows := decodeObjects(t, posted, "results")
		if len(rows) != 1 || rows[0]["supported"] != false || rows[0]["cases"] != "2/3" {
			t.Fatalf("posted rows = %v, want one supported=false row with cases 2/3", rows)
		}
		if posted["policy_hash"] != testBinding().Hash || posted["baseline_hash"] != testBinding().Baseline {
			t.Errorf("posted binding = %v/%v, want the live binding", posted["policy_hash"], posted["baseline_hash"])
		}
	})
}

// R15: a 1-1-1 split has no majority. The row is undecided, which means it is
// omitted from the payload, reported with its per-round verdicts, and the armed
// write refuses because there is nothing decisive to report.
func TestR15SplitVoteIsUndecidedAndOmitted(t *testing.T) {
	t.Setenv("FIT_PROBE_TEST_ADMIN_TOKEN", "admin-token")
	t.Setenv("FIT_PROBE_TEST_RELAY_TOKEN", "relay-token")

	answers := []scriptedAnswer{answerConsistent, answerDivergence, answerInconclusive}

	var served int32
	relay := scriptedRelay(answers, &served)
	defer relay.Close()
	admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", &postRecorder{}))
	defer admin.Close()

	stdout, stderr, err := runProbe(t, flaggedOptions(t, roundProbeArgs(admin.URL, relay.URL, "--rounds", "3", "--explain")))
	if err != nil {
		t.Fatalf("--rounds 3 run: %v", err)
	}
	full := decodeObject(t, stdout, "explain output")
	payload, rows := payloadOf(t, full)
	if len(rows) != 0 {
		t.Fatalf("an undecided row reached the payload: %v", rows)
	}
	if payload["rounds"] != float64(3) {
		t.Errorf("payload rounds = %v, want 3", payload["rounds"])
	}
	results := decodeObjects(t, full, "results")
	if len(results) != 1 {
		t.Fatalf("explain carries %d rows, want 1", len(results))
	}
	if results[0]["verdict"] != "inconclusive" {
		t.Fatalf("split-vote verdict = %v, want inconclusive", results[0]["verdict"])
	}
	records := decodeObjects(t, results[0], "round_records")
	if len(records) != 3 {
		t.Fatalf("split vote produced %d round records, want 3", len(records))
	}
	for index, want := range []string{"consistent", "divergence", "inconclusive"} {
		if records[index]["round"] != float64(index+1) || records[index]["verdict"] != want {
			t.Errorf("round record %d = %v, want round %d %s", index, records[index], index+1, want)
		}
	}
	if summary, ok := full["summary"].(map[string]any); !ok || summary["undecided"] != float64(1) {
		t.Errorf("summary = %v, want undecided=1", full["summary"])
	}
	for _, marker := range []string{"undecided", "r1 consistent", "r2 divergence", "r3 inconclusive"} {
		if !strings.Contains(stderr, marker) {
			t.Errorf("stderr does not report the undecided row with its per-round verdicts (%q missing):\n%s", marker, stderr)
		}
	}

	t.Run("the armed write refuses an undecided-only run", func(t *testing.T) {
		posts := &postRecorder{}
		var servedWrite int32
		relayWrite := scriptedRelay(answers, &servedWrite)
		defer relayWrite.Close()
		adminWrite := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", posts))
		defer adminWrite.Close()

		_, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(adminWrite.URL, relayWrite.URL, "--rounds", "3", "--write")))
		if err == nil || !strings.Contains(err.Error(), "no decisive measurements") {
			t.Fatalf("--write over a 1-1-1 split = %v, want the no-decisive-measurements refusal", err)
		}
		if got := len(posts.all()); got != 0 {
			t.Fatalf("an undecided run posted %d report(s)", got)
		}
		if got := atomic.LoadInt32(&servedWrite); got != 3 {
			t.Errorf("the refused run sent %d probe requests, want 3", got)
		}
	})
}

// R16: --payload-in refuses a row outside the run's probed scope, naming it,
// before anything is posted.
func TestR16PayloadInRejectsARowOutsideTheProbedScope(t *testing.T) {
	t.Setenv("FIT_PROBE_TEST_ADMIN_TOKEN", "admin-token")
	t.Setenv("FIT_PROBE_TEST_RELAY_TOKEN", "relay-token")

	cases := []struct {
		name string
		row  fitpolicy.SuiteResult
		want string
	}{
		{
			name: "another channel",
			row: fitpolicy.SuiteResult{ChannelID: 99, Family: "deepseek-v4", Model: "deepseek-v4-pro",
				Behavior: fitpolicy.BehaviorLogprobsDualPath, Supported: true},
			want: "channel 99",
		},
		{
			name: "a mark this run did not probe",
			row: fitpolicy.SuiteResult{ChannelID: 7, Family: "deepseek-v4", Model: "deepseek-v4-pro",
				Behavior: fitpolicy.BehaviorImageParts, Supported: true},
			want: "image.parts",
		},
		{
			name: "a model this channel does not serve",
			row: fitpolicy.SuiteResult{ChannelID: 7, Family: "deepseek-v4", Model: "deepseek-v4-flash",
				Behavior: fitpolicy.BehaviorLogprobsDualPath, Supported: true},
			want: "deepseek-v4-flash",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := curatedFile(t, curatedPayload(3, tc.row))
			posts := &postRecorder{}
			var served int32
			relay := scriptedRelay([]scriptedAnswer{answerConsistent}, &served)
			defer relay.Close()
			admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", posts))
			defer admin.Close()

			_, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(admin.URL, relay.URL, "--rounds", "3", "--write", "--payload-in", path)))
			if err == nil {
				t.Fatal("--payload-in accepted an out-of-scope row")
			}
			if !strings.Contains(err.Error(), "outside this run's probed scope") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal = %v, want it to name the offending row (%q) as outside the probed scope", err, tc.want)
			}
			if got := len(posts.all()); got != 0 {
				t.Fatalf("%d report(s) were posted despite the refused payload", got)
			}
		})
	}
}

// R17: --payload-in validates the supported flag against this run's own
// measurement instead of trusting the file; the agreeing payload is then posted
// through the same write path as --write, with this run's envelope and binding.
func TestR17PayloadInRejectsASupportedThatContradictsTheMajority(t *testing.T) {
	t.Setenv("FIT_PROBE_TEST_ADMIN_TOKEN", "admin-token")
	t.Setenv("FIT_PROBE_TEST_RELAY_TOKEN", "relay-token")

	t.Run("a true row against a measured false majority is refused", func(t *testing.T) {
		path := curatedFile(t, curatedPayload(3, deepseekLogprobsRow(true)))
		posts := &postRecorder{}
		var served int32
		relay := scriptedRelay([]scriptedAnswer{answerDivergence, answerDivergence, answerConsistent}, &served)
		defer relay.Close()
		admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", posts))
		defer admin.Close()

		_, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(admin.URL, relay.URL, "--rounds", "3", "--write", "--payload-in", path)))
		if err == nil {
			t.Fatal("--payload-in wrote a supported=true row this run measured false")
		}
		for _, marker := range []string{"--payload-in row 0", "channel 7", "supported=true", "supported=false"} {
			if !strings.Contains(err.Error(), marker) {
				t.Fatalf("refusal = %v, want it to name the row and both values (%q missing)", err, marker)
			}
		}
		if got := len(posts.all()); got != 0 {
			t.Fatalf("%d report(s) were posted despite the contradiction", got)
		}
	})

	t.Run("a false row against a measured false majority is posted", func(t *testing.T) {
		answers := []scriptedAnswer{answerDivergence, answerDivergence, answerConsistent}
		path := curatedFile(t, curatedPayload(3, deepseekLogprobsRow(false)))

		posts := &postRecorder{}
		var served int32
		relay := scriptedRelay(answers, &served)
		defer relay.Close()
		admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", posts))
		defer admin.Close()

		stdout, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(admin.URL, relay.URL, "--rounds", "3", "--write", "--payload-in", path)))
		if err != nil {
			t.Fatalf("--payload-in with an agreeing row: %v", err)
		}
		bodies := posts.all()
		if len(bodies) != 1 {
			t.Fatalf("the report endpoint received %d bodies, want exactly 1", len(bodies))
		}
		posted := decodeObject(t, bodies[0], "posted body")
		rows := decodeObjects(t, posted, "results")
		if len(rows) != 1 || rows[0]["supported"] != false || rows[0]["cases"] != "2/3" {
			t.Fatalf("posted rows = %v, want one supported=false row with cases 2/3", rows)
		}
		if posted["rounds"] != float64(3) || posted["report_id"] != "report-1" || posted["suite"] != "fit-probe-regression" {
			t.Errorf("posted envelope = %v/%v/%v, want this run's rounds/report_id/suite", posted["rounds"], posted["report_id"], posted["suite"])
		}
		if posted["policy_hash"] != testBinding().Hash {
			t.Errorf("posted policy_hash = %v, want the live binding", posted["policy_hash"])
		}

		// The curated body is the run's own body: the same measurement without
		// --payload-in produces the same payload (generated_at aside).
		var plainServed int32
		relayPlain := scriptedRelay(answers, &plainServed)
		defer relayPlain.Close()
		adminPlain := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", &postRecorder{}))
		defer adminPlain.Close()
		stdoutPlain, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(adminPlain.URL, relayPlain.URL, "--rounds", "3")))
		if err != nil {
			t.Fatalf("plain --rounds 3 run: %v", err)
		}
		if got, want := normalizedPayload(t, bodies[0], "posted body"), normalizedPayload(t, stdoutPlain, "run payload"); got != want {
			t.Errorf("the curated write is not this run's own payload:\n%s\n%s", got, want)
		}
		if strings.TrimSpace(stdout) == "" {
			t.Error("the --payload-in run printed nothing")
		}
	})

	t.Run("a row this run left undecided is refused", func(t *testing.T) {
		// One row is decided (so the run has something decisive to report) and
		// a second row splits 1-1-1: the curated payload must not be able to
		// write the undecided one alongside it. Round-major order: the pro row
		// is probed first in every round, then the flash row.
		path := curatedFile(t, curatedPayload(3,
			fitpolicy.SuiteResult{ChannelID: 7, Family: "deepseek-v4", Model: "deepseek-v4-pro",
				Behavior: fitpolicy.BehaviorLogprobsDualPath, Supported: true, Cases: "1/1"},
			fitpolicy.SuiteResult{ChannelID: 7, Family: "deepseek-v4", Model: "deepseek-v4-flash",
				Behavior: fitpolicy.BehaviorLogprobsDualPath, Supported: false, Cases: "1/1"},
		))
		posts := &postRecorder{}
		var served int32
		relay := scriptedRelay([]scriptedAnswer{
			answerConsistent, answerConsistent, // round 1: pro, flash
			answerConsistent, answerDivergence, // round 2: pro, flash
			answerConsistent, answerInconclusive, // round 3: pro, flash
		}, &served)
		defer relay.Close()
		admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro,deepseek-v4-flash", posts))
		defer admin.Close()

		args := roundProbeArgs(admin.URL, relay.URL, "--rounds", "3", "--write", "--payload-in", path)
		args = append(args, "--model", "deepseek-v4-pro,deepseek-v4-flash")
		_, _, err := runProbe(t, flaggedOptions(t, args))
		if err == nil || !strings.Contains(err.Error(), "did not decide it") {
			t.Fatalf("--payload-in over an undecided row = %v, want a refusal naming the row", err)
		}
		if !strings.Contains(err.Error(), "deepseek-v4-flash") {
			t.Fatalf("refusal = %v, want it to name the undecided row", err)
		}
		if got := len(posts.all()); got != 0 {
			t.Fatalf("%d report(s) were posted despite the undecided row", got)
		}
	})

	t.Run("a curated subset is written without touching the rows it leaves out", func(t *testing.T) {
		// The run decides two rows; the curated payload keeps only the flash
		// one. That is the shape operators used to hand-POST: a named write
		// scope smaller than what the run measured.
		path := curatedFile(t, curatedPayload(3,
			fitpolicy.SuiteResult{ChannelID: 7, Family: "deepseek-v4", Model: "deepseek-v4-flash",
				Behavior: fitpolicy.BehaviorLogprobsDualPath, Supported: true, Cases: "1/1"},
		))
		posts := &postRecorder{}
		var served int32
		relay := scriptedRelay([]scriptedAnswer{answerConsistent}, &served)
		defer relay.Close()
		admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro,deepseek-v4-flash", posts))
		defer admin.Close()

		args := roundProbeArgs(admin.URL, relay.URL, "--rounds", "3", "--write", "--payload-in", path, "--explain")
		args = append(args, "--model", "deepseek-v4-pro,deepseek-v4-flash")
		stdout, _, err := runProbe(t, flaggedOptions(t, args))
		if err != nil {
			t.Fatalf("--payload-in with a curated subset: %v", err)
		}
		if got := atomic.LoadInt32(&served); got != 6 {
			t.Fatalf("2 rows x 3 rounds sent %d probe requests, want 6", got)
		}
		bodies := posts.all()
		if len(bodies) != 1 {
			t.Fatalf("the report endpoint received %d bodies, want exactly 1", len(bodies))
		}
		posted := decodeObjects(t, decodeObject(t, bodies[0], "posted body"), "results")
		if len(posted) != 1 || posted[0]["model"] != "deepseek-v4-flash" || posted[0]["supported"] != true {
			t.Fatalf("posted rows = %v, want only the curated flash row", posted)
		}
		full := decodeObject(t, stdout, "explain output")
		runRows := decodeObjects(t, full, "results")
		if len(runRows) != 2 || runRows[0]["verdict"] != "consistent" || runRows[1]["verdict"] != "consistent" {
			t.Fatalf("the run's own results = %v, want the two decided rows", runRows)
		}
		if _, embedded := payloadOf(t, full); len(embedded) != 1 {
			t.Fatalf("the embedded payload carries %d rows, want the curated 1", len(embedded))
		}
		block, ok := full["payload_in"].(map[string]any)
		if !ok {
			t.Fatalf("the explain output has no payload_in block: %v", full["payload_in"])
		}
		if block["rows"] != float64(1) || block["omitted_decided_rows"] != float64(1) {
			t.Errorf("payload_in = %v, want rows=1 and omitted_decided_rows=1", block)
		}
	})

	t.Run("an acceptance-tier row cannot bypass the --acceptance-marks gate", func(t *testing.T) {
		// glm-5.3 family.whole is acceptance-only, so without
		// --acceptance-marks the run itself would never encode it, and a
		// curated payload must not be able to smuggle it in. The deepseek row
		// keeps the run decisive, so the refusal is the gate and not an empty
		// payload.
		glmRow := fitpolicy.SuiteResult{ChannelID: 7, Family: "glm-5.3", Model: "glm-5.3",
			Behavior: fitpolicy.BehaviorFamilyWhole, Supported: true, Cases: "1/1"}
		answers := []scriptedAnswer{
			answerConsistent, answerPlainOK, // round 1: deepseek logprobs, glm whole
			answerConsistent, answerPlainOK, // round 2
			answerConsistent, answerPlainOK, // round 3
		}
		scope := []string{"--family", "deepseek-v4,glm-5.3",
			"--behaviors", fitpolicy.BehaviorLogprobsDualPath + "," + fitpolicy.BehaviorFamilyWhole,
			"--model", "deepseek-v4-pro,glm-5.3"}

		posts := &postRecorder{}
		var served int32
		relay := scriptedRelay(answers, &served)
		defer relay.Close()
		admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro,glm-5.3", posts))
		defer admin.Close()

		path := curatedFile(t, curatedPayload(3, glmRow))
		gated := append([]string{"--rounds", "3", "--write", "--payload-in", path}, scope...)
		_, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(admin.URL, relay.URL, gated...)))
		if err == nil || !strings.Contains(err.Error(), "not encodable") || !strings.Contains(err.Error(), "acceptance-marks") {
			t.Fatalf("--payload-in over an acceptance-tier row = %v, want a refusal pointing at --acceptance-marks", err)
		}
		if got := len(posts.all()); got != 0 {
			t.Fatalf("%d report(s) were posted despite the gated row", got)
		}

		// With the gate open the same curated row is written, carrying this
		// run's own tally (three agreeing rounds).
		postsOpen := &postRecorder{}
		var servedOpen int32
		relayOpen := scriptedRelay(answers, &servedOpen)
		defer relayOpen.Close()
		adminOpen := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro,glm-5.3", postsOpen))
		defer adminOpen.Close()

		open := append([]string{"--rounds", "3", "--write", "--payload-in", path, "--acceptance-marks"}, scope...)
		if _, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(adminOpen.URL, relayOpen.URL, open...))); err != nil {
			t.Fatalf("--payload-in with --acceptance-marks: %v", err)
		}
		bodies := postsOpen.all()
		if len(bodies) != 1 {
			t.Fatalf("the report endpoint received %d bodies, want exactly 1", len(bodies))
		}
		rows := decodeObjects(t, decodeObject(t, bodies[0], "posted body"), "results")
		if len(rows) != 1 || rows[0]["family"] != "glm-5.3" || rows[0]["supported"] != true || rows[0]["cases"] != "3/3" {
			t.Fatalf("posted rows = %v, want only the curated glm-5.3 row at 3/3", rows)
		}
	})

	t.Run("a misspelled field is refused instead of read as its zero value", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "typo.json")
		body := `{"report_id":"r","run_id":"r","suite":"s","rounds":3,"results":[{"channel_id":7,"family":"deepseek-v4","model":"deepseek-v4-pro","behavior":"logprobs.dual_path","suported":true,"cases":"1/1"}]}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("writing the curated payload: %v", err)
		}
		posts := &postRecorder{}
		var served int32
		relay := scriptedRelay([]scriptedAnswer{answerConsistent}, &served)
		defer relay.Close()
		admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", posts))
		defer admin.Close()

		_, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(admin.URL, relay.URL, "--rounds", "3", "--write", "--payload-in", path)))
		if err == nil || !strings.Contains(err.Error(), "suported") {
			t.Fatalf("--payload-in over a misspelled field = %v, want a refusal naming the field", err)
		}
		if got := len(posts.all()); got != 0 {
			t.Fatalf("%d report(s) were posted despite the malformed payload", got)
		}
	})
}

// R18: --payload-in is behind the same write gate as --write: dry-run refuses
// it without probing, it is meaningless without --write, and --rounds itself is
// bounded by the report schema.
func TestR18PayloadInStaysBehindTheWriteGate(t *testing.T) {
	t.Setenv("FIT_PROBE_TEST_ADMIN_TOKEN", "admin-token")
	t.Setenv("FIT_PROBE_TEST_RELAY_TOKEN", "relay-token")

	path := curatedFile(t, curatedPayload(3, deepseekLogprobsRow(false)))

	t.Run("the dry-run default refuses it without probing", func(t *testing.T) {
		posts := &postRecorder{}
		var served int32
		relay := scriptedRelay([]scriptedAnswer{answerConsistent}, &served)
		defer relay.Close()
		admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", posts))
		defer admin.Close()

		// --dry-run stays at its default true: only the write gate decides.
		args := withoutDryRun(roundProbeArgs(admin.URL, relay.URL, "--rounds", "3", "--write", "--payload-in", path))
		_, _, err := runProbe(t, flaggedOptions(t, args))
		if err == nil || !strings.Contains(err.Error(), "--dry-run") {
			t.Fatalf("--payload-in under dry-run = %v, want the --dry-run refusal", err)
		}
		if got := atomic.LoadInt32(&served); got != 0 {
			t.Fatalf("the dry-run refusal still probed %d time(s)", got)
		}
		if got := len(posts.all()); got != 0 {
			t.Fatalf("the dry-run refusal still posted %d report(s)", got)
		}
	})

	t.Run("it is refused without --write", func(t *testing.T) {
		posts := &postRecorder{}
		var served int32
		relay := scriptedRelay([]scriptedAnswer{answerConsistent}, &served)
		defer relay.Close()
		admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", posts))
		defer admin.Close()

		_, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(admin.URL, relay.URL, "--rounds", "3", "--payload-in", path)))
		if err == nil || !strings.Contains(err.Error(), "--payload-in requires --write") {
			t.Fatalf("--payload-in without --write = %v, want a --write requirement", err)
		}
		if got := atomic.LoadInt32(&served); got != 0 {
			t.Fatalf("--payload-in without --write probed %d time(s)", got)
		}
	})

	t.Run("the rounds flag itself is bounded", func(t *testing.T) {
		if _, err := parseFlags([]string{"--rounds", "0"}); err == nil {
			t.Error("--rounds 0 parsed; a run needs at least one round")
		}
		if _, err := parseFlags([]string{"--rounds", "-3"}); err == nil {
			t.Error("--rounds -3 parsed")
		}
		if _, err := parseFlags([]string{"--rounds", "1001"}); err == nil {
			t.Error("--rounds 1001 parsed; the report schema caps rounds at fitpolicy.MaxSuiteReportRounds")
		}
		if _, err := parseFlags([]string{"--rounds", "2"}); err != nil {
			t.Errorf("--rounds 2 was rejected: %v", err)
		}
	})
}

// R19: a run with zero decisive rows is refused exactly as before, with and
// without a curated payload, and the report endpoint is never reached.
func TestR19ZeroDecisiveRowsStillRefused(t *testing.T) {
	t.Setenv("FIT_PROBE_TEST_ADMIN_TOKEN", "admin-token")
	t.Setenv("FIT_PROBE_TEST_RELAY_TOKEN", "relay-token")

	t.Run("without a curated payload", func(t *testing.T) {
		posts := &postRecorder{}
		var served int32
		relay := scriptedRelay([]scriptedAnswer{answerInconclusive}, &served)
		defer relay.Close()
		admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", posts))
		defer admin.Close()

		_, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(admin.URL, relay.URL, "--rounds", "3", "--write")))
		if err == nil || !strings.Contains(err.Error(), "no decisive measurements") {
			t.Fatalf("--write over three inconclusive rounds = %v, want the no-decisive-measurements refusal", err)
		}
		if got := atomic.LoadInt32(&served); got != 3 {
			t.Errorf("the refused run sent %d probe requests, want 3", got)
		}
		if got := len(posts.all()); got != 0 {
			t.Fatalf("a run with zero decisive rows posted %d report(s)", got)
		}
	})

	t.Run("with a curated payload", func(t *testing.T) {
		path := curatedFile(t, curatedPayload(3, deepseekLogprobsRow(false)))
		posts := &postRecorder{}
		var served int32
		relay := scriptedRelay([]scriptedAnswer{answerInconclusive}, &served)
		defer relay.Close()
		admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", posts))
		defer admin.Close()

		_, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(admin.URL, relay.URL, "--rounds", "3", "--write", "--payload-in", path)))
		if err == nil || !strings.Contains(err.Error(), "no decisive measurements") {
			t.Fatalf("--payload-in over three inconclusive rounds = %v, want the no-decisive-measurements refusal", err)
		}
		if got := len(posts.all()); got != 0 {
			t.Fatalf("a curated payload with nothing decisive posted %d report(s)", got)
		}
	})
}

// R20: the deepseek-v4 probes must pin the thinking state they measure, because
// an unpinned thinking state makes the official reference non-reproducible.
//
// logprobs.dual_path pinned no thinking flag at all (probe.go:120-125 pre-fix)
// and DeepSeek V4 defaults to thinking-on, so at this probe's 8-token budget the
// model usually spent the whole budget on reasoning and never emitted a content
// token. The official endpoint attaches logprobs to the tokens it does emit, so
// whether the reference carried logprobs was decided by that coin flip rather
// than by the channel: on byte-identical requests the official channel returned
// logprobs in 1 of 3 rounds, and in one round the official baseline leg and the
// identically pinned official sample leg disagreed with each other (ch5
// diverging from ch5). Every presence-derived verdict therefore rested on a
// reference that could not be reproduced, which is why the deepseek-v4 rows
// decided by logprobs_present / reasoning_content_present were withheld.
//
// Post-fix the logprobs probe pins thinking.type=disabled — the exact mechanism
// the kimi probes already use — so the answer is content, which is the path the
// official endpoint attaches logprobs to. The deepseek thinking probe keeps
// pinning thinking.type=enabled: its mark is the thinking-expected shape
// (pkg/fitpolicy/builtin.go:39 requires it when !ThinkingDisabled()), its
// evidence is reasoning output, and disabling thinking there would both probe a
// shape the policy never consults and compare reasoning-token accounting that
// the official endpoint omits for disabled-thinking requests
// (relay/channel/openai/deepseek_v4_fit.go:129-130). The expectations are pinned
// here, never read from the constant the implementation uses, and the planned
// request is checked as well as the probe spec, so the test fails before the fix.
func TestR20DeepSeekProbesPinTheThinkingState(t *testing.T) {
	// thinkingType reads the pinned state out of a request body. An absent or
	// malformed thinking object yields "", which no assertion below accepts.
	thinkingType := func(body map[string]any) string {
		object, ok := body["thinking"].(map[string]any)
		if !ok {
			return ""
		}
		value, _ := object["type"].(string)
		return value
	}

	t.Run("deepseek logprobs probe pins thinking off", func(t *testing.T) {
		spec := probeSpecs[fitpolicy.BehaviorLogprobsDualPath]
		for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4.1-flash"} {
			body := spec.Build("deepseek-v4", model)
			if got := thinkingType(body); got != "disabled" {
				t.Fatalf("deepseek-v4 %s logprobs.dual_path pins thinking.type=%q, want %q: with the model's default thinking-on the 8-token budget is spent on reasoning, no content token is emitted, and the official reference's logprobs presence flips between rounds", model, got, "disabled")
			}
			// The fix must not move anything else about the probe.
			if body["logprobs"] != true || body["top_logprobs"] != 1 || body["stream"] != false {
				t.Fatalf("deepseek-v4 %s logprobs.dual_path is no longer the minimal envelope probe: %v", model, body)
			}
			if got, ok := body["max_tokens"].(int); !ok || got != spec.MaxTokens {
				t.Fatalf("deepseek-v4 %s logprobs.dual_path max_tokens = %v, want %d: the sent budget must stay the measured budget", model, body["max_tokens"], spec.MaxTokens)
			}
		}
	})

	t.Run("the flag is the kimi probes' mechanism, and the thinking probe is untouched", func(t *testing.T) {
		kimi := probeSpecs[fitpolicy.BehaviorThinkingCounting].Build("kimi-k3", "kimi-k3")
		if got := thinkingType(kimi); got != "disabled" {
			t.Fatalf("kimi-k3 usage.thinking_counting pins thinking.type=%q, want %q: the determinism mechanism the deepseek logprobs probe reuses", got, "disabled")
		}
		deepseek := probeSpecs[fitpolicy.BehaviorThinkingCounting].Build("deepseek-v4", "deepseek-v4-flash")
		if got := thinkingType(deepseek); got != "enabled" {
			t.Fatalf("deepseek-v4 usage.thinking_counting pins thinking.type=%q, want %q: that mark is required for thinking-expected requests (pkg/fitpolicy/builtin.go:39) and its evidence is reasoning output, so its shape must not change", got, "enabled")
		}
	})

	t.Run("the planned request carries the flag", func(t *testing.T) {
		t.Setenv("FIT_PROBE_TEST_ADMIN_TOKEN", "admin-token")
		t.Setenv("FIT_PROBE_TEST_RELAY_TOKEN", "relay-token")
		admin := httptest.NewServer(scopedAdminMux(7, "deepseek-v4-pro", &postRecorder{}))
		defer admin.Close()

		stdout, _, err := runProbe(t, flaggedOptions(t, roundProbeArgs(admin.URL, admin.URL, "--plan", "--explain")))
		if err != nil {
			t.Fatalf("--plan --explain: %v", err)
		}
		doc := decodeObject(t, stdout, "--plan --explain output")
		plans := decodeObjects(t, doc, "planned_probes")
		if len(plans) == 0 {
			t.Fatal("the plan carries no probes, so this assertion would pass vacuously")
		}
		for _, plan := range plans {
			if plan["behavior"] != fitpolicy.BehaviorLogprobsDualPath {
				continue
			}
			request, ok := plan["minimal_request"].(map[string]any)
			if !ok {
				t.Fatalf("planned_probes entry has no minimal_request object: %v", plan)
			}
			if got := thinkingType(request); got != "disabled" {
				t.Fatalf("the planned deepseek-v4 logprobs.dual_path request pins thinking.type=%q, want %q: the flag must reach the request that is actually sent, not only the spec", got, "disabled")
			}
		}
	})
}
