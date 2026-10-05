package main

// fit-probe: measure the channel-fit capability marks through the relay.
//
// Modes:
//   --dry-run  the default: no network at all. Print the report payload shape
//              (with --explain, also the exact minimal request that would be
//              sent for each planned dimension). Every row is marked
//              placeholder. Requires --dry-run=false to leave.
//   --offline  same as --dry-run, kept for callers that already pass it.
//   --plan     read the admin API and print the plan; send no probe.
//   default    read the admin API, probe the relay (and, when configured, a real
//              official endpoint), classify and print the report payload.
//   --write    additionally POST the payload to /api/fit-capability/report.
//              Nothing is ever written without it, and it is refused outright
//              while --dry-run is in effect.
//   --rounds N measure every row N times, sequentially, and decide it by a
//              strict majority of the rounds; undecided rows are omitted and
//              reported with their per-round verdicts. N=1 (the default) is the
//              original single-sample run, unchanged.
//   --payload-in FILE
//              write the curated payload in FILE through the same write path
//              instead of this run's own payload. Every row must be inside this
//              run's probed scope and match this run's own decision; the
//              envelope, binding, rounds, cases and expiry are regenerated here.
//
// Credentials are read only from environment variables named by
// --admin-token-env / --relay-token-env. They are never printed, and no request
// or response body is written to disk.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/officialfit"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
)

const (
	defaultAdminTokenEnv = "FIT_PROBE_ADMIN_TOKEN"
	defaultRelayTokenEnv = "FIT_PROBE_RELAY_TOKEN"
	defaultSuite         = "fit-probe-minimal-v1"
)

type options struct {
	baseURL        string
	relayBaseURL   string
	adminTokenEnv  string
	relayTokenEnv  string
	channels       string
	models         string
	behaviors      string
	families       string
	officialURL    stringList
	officialKeyEnv stringList
	dryRun         bool
	reportID       string
	runID          string
	suite          string
	offline        bool
	plan           bool
	explain        bool
	write          bool
	acceptance     bool
	rounds         int
	payloadIn      string
	timeout        time.Duration
	status         string
}

func parseFlags(args []string) (options, error) {
	var opts options
	fs := flag.NewFlagSet("fit-probe", flag.ContinueOnError)
	fs.StringVar(&opts.baseURL, "base-url", "", "gateway root URL, e.g. https://n.example.dev (admin API at /api, relay at /v1)")
	fs.StringVar(&opts.relayBaseURL, "relay-base-url", "", "override the relay origin (defaults to --base-url)")
	fs.StringVar(&opts.adminTokenEnv, "admin-token-env", defaultAdminTokenEnv, "environment variable holding the admin API credential")
	fs.StringVar(&opts.relayTokenEnv, "relay-token-env", defaultRelayTokenEnv, "environment variable holding the relay user credential")
	fs.StringVar(&opts.channels, "channel", "", "comma-separated channel ids (default: every channel)")
	fs.StringVar(&opts.models, "model", "", "comma-separated model ids (default: every official-family model on the channel)")
	fs.StringVar(&opts.behaviors, "behaviors", "", "comma-separated capability marks (default: every mark the live policy can require)")
	fs.StringVar(&opts.families, "family", "", "comma-separated family ids (default: every registered family)")
	fs.Var(&opts.officialURL, "official-base-url", "official endpoint, optionally family=URL (repeatable)")
	fs.Var(&opts.officialKeyEnv, "official-key-env", "environment variable holding the official key, optionally family=ENV (repeatable)")
	fs.BoolVar(&opts.dryRun, "dry-run", true, "send nothing over the network and print the report payload only (default; leave with --dry-run=false)")
	fs.StringVar(&opts.reportID, "report-id", "", "report_id to encode (default: a UTC timestamp id)")
	fs.StringVar(&opts.runID, "run-id", "", "run_id to encode (default: the report id)")
	fs.StringVar(&opts.suite, "suite", "", "suite name to encode (default: "+defaultSuite+")")
	fs.BoolVar(&opts.offline, "offline", false, "send no request at all; print the payload shape and the planned minimal requests")
	fs.BoolVar(&opts.plan, "plan", false, "read the admin API and print the plan without sending any probe")
	fs.BoolVar(&opts.explain, "explain", false, "print the full comparison report instead of the bare report payload")
	fs.BoolVar(&opts.write, "write", false, "POST the payload to /api/fit-capability/report (off by default)")
	fs.BoolVar(&opts.acceptance, "acceptance-marks", false, "also encode acceptance-strength verdicts, consistent and divergent; without it only structural-strength evidence is written")
	fs.IntVar(&opts.rounds, "rounds", 1, "independent measurement rounds (default 1); above 1 each row is decided by a strict majority of the rounds, undecided rows are omitted and never written, and every probe — including a configured official baseline — is repeated N times, sequentially")
	fs.StringVar(&opts.payloadIn, "payload-in", "", "write the curated suite report in this file through the same write path instead of this run's own payload (requires --write); every row must be inside the run's probed scope and match the run's own decision, and the envelope, binding, rounds and cases are regenerated by this run")
	fs.DurationVar(&opts.timeout, "timeout", 60*time.Second, "per-request timeout")
	fs.StringVar(&opts.status, "channel-status", "1", "channel status filter for the admin list (1=enabled, 0=disabled, -1=all)")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if opts.rounds < 1 {
		return opts, fmt.Errorf("--rounds must be at least 1, got %d", opts.rounds)
	}
	if opts.rounds > fitpolicy.MaxSuiteReportRounds {
		return opts, fmt.Errorf("--rounds %d exceeds the report limit of %d", opts.rounds, fitpolicy.MaxSuiteReportRounds)
	}
	if opts.relayBaseURL == "" {
		opts.relayBaseURL = opts.baseURL
	}
	return opts, nil
}

// stringList is a repeatable flag that also accepts "key=value".
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

// lookupFamilyValue resolves a family=value list, falling back to a single bare
// value that applies to every family.
func lookupFamilyValue(values []string, family string) string {
	bare := ""
	for _, entry := range values {
		if key, value, found := strings.Cut(entry, "="); found {
			if strings.EqualFold(strings.TrimSpace(key), family) {
				return strings.TrimSpace(value)
			}
			continue
		}
		bare = strings.TrimSpace(entry)
	}
	return bare
}

func splitList(value string) []string {
	out := make([]string, 0)
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func main() {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := run(context.Background(), opts, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "fit-probe:", err)
		os.Exit(1)
	}
}

// fullReport is the enriched, --explain output. The payload is embedded so the
// comparison and the exact bytes to be posted are in one document.
type fullReport struct {
	Tool          string            `json:"tool"`
	SchemaVersion int               `json:"schema_version"`
	GeneratedAt   int64             `json:"generated_at"`
	Mode          string            `json:"mode"`
	AdminBaseURL  string            `json:"admin_base_url"`
	RelayBaseURL  string            `json:"relay_base_url"`
	Credentials   credentialsReport `json:"credentials"`
	Baseline      baselineReport    `json:"baseline"`
	MarkMapping   []markMappingRow  `json:"mark_mapping"`
	Plan          []planRow         `json:"planned_probes"`
	Results       []probeResult     `json:"results"`
	Summary       summaryReport     `json:"summary"`
	SelfCheck     string            `json:"self_check"`
	Postable      bool              `json:"postable"`
	PostResult    string            `json:"post_result,omitempty"`
	// Rounds is set only by a multi-round run, so the single-round report keeps
	// exactly its previous shape.
	Rounds int `json:"rounds,omitempty"`
	// PayloadIn records the --payload-in validation when a curated payload is
	// what would be posted.
	PayloadIn *payloadInReport `json:"payload_in,omitempty"`
	Payload   json.RawMessage  `json:"payload"`
}

type credentialsReport struct {
	AdminTokenPresent bool `json:"admin_token_present"`
	RelayTokenPresent bool `json:"relay_token_present"`
}

type baselineReport struct {
	Source      string   `json:"source"`
	OfficialURL string   `json:"official_url,omitempty"`
	Note        string   `json:"note"`
	Families    []string `json:"measured_families,omitempty"`
}

type summaryReport struct {
	Channels     int `json:"channels"`
	Models       int `json:"models"`
	Probes       int `json:"probes"`
	Consistent   int `json:"consistent"`
	Divergence   int `json:"divergence"`
	Inconclusive int `json:"inconclusive"`
	Encoded      int `json:"encoded_results"`
	Omitted      int `json:"omitted_results"`
	// Rounds, ProbeRequests and Undecided are set only by a multi-round run.
	// ProbeRequests is the number of HTTP requests the rounds actually sent to
	// the relay (a probe retried once counts twice); the official-baseline
	// requests a configured baseline costs per round are not included.
	Rounds        int `json:"rounds,omitempty"`
	ProbeRequests int `json:"probe_requests,omitempty"`
	Undecided     int `json:"undecided,omitempty"`
}

// planRow is one planned probe: the dimension and the exact request that will
// be sent to the relay (or already was).
type planRow struct {
	ChannelID        int            `json:"channel_id"`
	Family           string         `json:"family"`
	Behavior         string         `json:"behavior"`
	Model            string         `json:"model"`
	Path             string         `json:"path"`
	MaxTokens        int            `json:"max_tokens"`
	MinimalRequest   map[string]any `json:"minimal_request"`
	ExpectedOfficial string         `json:"expected_official"`
	Discriminators   []string       `json:"discriminators"`
	Basis            []string       `json:"basis"`
	// Rounds is set only when the planned run repeats this probe, so the plan
	// shows the cost --rounds adds before anything is sent.
	Rounds int `json:"rounds,omitempty"`
}

// run is the whole program: mode selection, planning, probing, encoding and the
// single optional write.
func run(ctx context.Context, opts options, stdout, stderr io.Writer) error {
	now := time.Now()
	adminToken := strings.TrimSpace(os.Getenv(opts.adminTokenEnv))
	relayToken := strings.TrimSpace(os.Getenv(opts.relayTokenEnv))

	rounds := opts.rounds
	if rounds < 1 {
		rounds = 1
	}

	dryRun := opts.dryRun
	offline := dryRun || opts.offline || opts.baseURL == "" || adminToken == ""
	mode := "probe"
	switch {
	case dryRun:
		mode = "dry-run"
	case opts.offline:
		mode = "offline"
	case offline:
		mode = "offline (no --base-url or no admin credential)"
	case opts.plan:
		mode = "plan"
	}

	// The write gate's static refusals come first. --write is the only thing
	// that changes state outside this process, so a POST that cannot happen
	// must not cost a single probe, and it must not depend on what the run
	// measured. The measurement-dependent refusal (no decisive row) still comes
	// after the run, because only the run can know it.
	if opts.write {
		if dryRun {
			return fmt.Errorf("--write refused: --dry-run is in effect (pass --dry-run=false to allow the POST)")
		}
		if offline {
			return fmt.Errorf("--write requires --base-url and an admin credential")
		}
	}
	if opts.payloadIn != "" && !opts.write {
		return fmt.Errorf("--payload-in requires --write: a curated payload is validated against this run's own measurements and posted through the same single write path")
	}

	reportID := firstNonEmpty(opts.reportID, newReportID(now))
	runID := firstNonEmpty(opts.runID, reportID)
	suite := firstNonEmpty(opts.suite, defaultSuite)
	curated, err := loadCuratedPayload(opts.payloadIn, reportID, runID, suite, rounds)
	if err != nil {
		return err
	}

	policy, binding, policyNote, err := loadPolicy(ctx, opts, offline, adminToken)
	if err != nil {
		return err
	}

	families := selectedFamilies(policy, opts)
	behaviorFilter := splitList(opts.behaviors)
	behaviorFilter = filterToPolicy(policy, behaviorFilter, opts, stderr)

	rows := make([]probeResult, 0)
	plan := make([]planRow, 0)

	channels := []channelInfo{{Id: 0, Name: "placeholder"}}
	if !offline {
		api := newClient(opts.timeout, opts.baseURL, adminToken, opts.relayBaseURL, relayToken)
		channels, err = api.listChannels(ctx, opts.status)
		if err != nil {
			return err
		}
		channels = selectChannels(channels, splitList(opts.channels))
	}

	targets := buildTargets(policy, channels, families, behaviorFilter, opts)
	for _, target := range targets {
		spec, known := probeSpecs[target.Behavior]
		if !known {
			continue
		}
		body := spec.Build(target.Family, target.Model)
		plan = append(plan, planRow{
			ChannelID:        target.ChannelID,
			Family:           target.Family,
			Behavior:         target.Behavior,
			Model:            target.Model,
			Path:             spec.Path,
			MaxTokens:        spec.MaxTokens,
			MinimalRequest:   body,
			ExpectedOfficial: expectationFor(target.Family, target.Behavior).Official,
			Discriminators:   expectationFor(target.Family, target.Behavior).Discriminators,
			Basis:            expectationFor(target.Family, target.Behavior).Basis,
			Rounds:           multiRounds(rounds),
		})
	}

	measuredBaseline := false
	baselineFamilies := make([]string, 0)
	api := newClient(opts.timeout, opts.baseURL, adminToken, opts.relayBaseURL, relayToken)

	if offline || opts.plan {
		for _, target := range targets {
			spec, known := probeSpecs[target.Behavior]
			if !known {
				rows = append(rows, unmeasurableRow(target))
				continue
			}
			rows = append(rows, probeResult{
				ChannelID:      target.ChannelID,
				ChannelName:    target.ChannelName,
				ChannelType:    target.ChannelType,
				Family:         target.Family,
				Model:          target.Model,
				Behavior:       target.Behavior,
				Verdict:        verdictInconclusive,
				Strength:       strengthAcceptance,
				Basis:          "no probe was sent in " + mode + " mode",
				Path:           spec.Path,
				MinimalRequest: spec.Build(target.Family, target.Model),
				Placeholder:    true,
			})
		}
	} else {
		// Every probed row starts as a static skeleton; the rounds below fill
		// the measurement fields. A mark this tool cannot probe at all keeps its
		// placeholder row and is never counted as a round, because it is not a
		// sample of anything.
		plans := make([]probePlan, len(targets))
		probeable := make([]bool, len(targets))
		rows = make([]probeResult, len(targets))
		for index, target := range targets {
			spec, known := probeSpecs[target.Behavior]
			if !known {
				rows[index] = unmeasurableRow(target)
				continue
			}
			body, marshalErr := probeBody(spec, target)
			if marshalErr != nil {
				rows[index] = unencodableRow(target, spec, marshalErr)
				continue
			}
			plans[index] = probePlan{spec: spec, body: body}
			probeable[index] = true
			rows[index] = probeResult{
				ChannelID:      target.ChannelID,
				ChannelName:    target.ChannelName,
				ChannelType:    target.ChannelType,
				Family:         target.Family,
				Model:          target.Model,
				Behavior:       target.Behavior,
				Path:           spec.Path,
				MinimalRequest: spec.Build(target.Family, target.Model),
			}
		}

		// Rounds are independent and strictly sequential: round 1 sweeps every
		// target, then round 2 re-probes the same requests, and so on. There is
		// no concurrency, and each probe keeps its single-shot shape — one
		// request, plus at most the existing one retry on a retryable status,
		// which stays inside its round rather than becoming another round. A
		// round re-measures the official baseline too when one is configured, so
		// a multi-round run costs N times the single-round request count: N
		// times the relay probes and N times the official baseline probes.
		samples := make([][]roundRecord, len(targets))
		for round := 1; round <= rounds; round++ {
			baselines := map[string]*signature{}
			for index, target := range targets {
				if !probeable[index] {
					continue
				}
				spec, body := plans[index].spec, plans[index].body
				// One baseline per distinct official request, per round. The key
				// carries the full request signature (family, model, behaviour
				// and the marshalled body), and the identical bytes below go to
				// both the relay and the official endpoint. A mark whose own
				// baseline request is missing therefore stays doc-only instead
				// of being compared with another mark's response.
				key := baselineKey(target.Family, target.Model, target.Behavior, body)
				baseline, seen := baselines[key]
				if !seen {
					baseline = probeOfficialBaseline(ctx, api, opts, target, body)
					baselines[key] = baseline
					if baseline != nil {
						baselineFamilies = appendUnique(baselineFamilies, target.Family)
					}
				}
				sig, attempts, transport := probeChannel(ctx, api, target, spec, body)
				annotateErrorField(&sig, spec.Fields)
				exp := expectationFor(target.Family, target.Behavior)
				verdictValue, strength, basis, shape := classify(spec, exp, baseline, sig)
				samples[index] = append(samples[index], roundRecord{
					Round:      round,
					Verdict:    verdictValue,
					Strength:   strength,
					Basis:      basis,
					Shape:      shape,
					HTTPStatus: sig.Status,
					Attempts:   attempts,
					Transport:  transport,
					Signature:  sig,
					Baseline:   baseline,
				})
			}
		}
		for index := range targets {
			if !probeable[index] {
				continue
			}
			rows[index] = aggregateRounds(rows[index], samples[index], rounds)
		}
		measuredBaseline = len(baselineFamilies) > 0
		reportUndecided(rows, stderr)
	}

	payload, omitted, err := buildSuiteReportRounds(reportID, runID, suite, binding, now.Unix(), rounds, rows, opts.acceptance)
	if err != nil {
		return fmt.Errorf("refusing to print an invalid payload: %w", err)
	}
	if opts.write && len(payload.Results) == 0 {
		return fmt.Errorf("--write refused: no decisive measurements to report")
	}
	encoded, err := marshalPayload(payload)
	if err != nil {
		return err
	}
	omittedRows := len(omitted)
	postable := payload

	// --payload-in narrows what is posted to the curated row set, after every
	// curated row has been validated against this run's own measurement. The
	// validator returns before anything is sent, so a rejected payload costs no
	// write; the body is then re-encoded by the same encoder as --write, so the
	// endpoint, the admin credential and the body shape cannot differ.
	var payloadIn *payloadInReport
	if curated != nil {
		curatedPayload, curatedErr := curatedReportFrom(*curated, rows, reportID, runID, suite, binding, now.Unix(), rounds, opts.acceptance)
		if curatedErr != nil {
			return curatedErr
		}
		postable = curatedPayload
		encoded, err = marshalPayload(postable)
		if err != nil {
			return err
		}
		omittedRows = len(rows) - len(postable.Results)
		payloadIn = &payloadInReport{
			File:           opts.payloadIn,
			Rows:           len(postable.Results),
			Validation:     "every row is inside this run's probed scope and matches this run's own decision; the envelope, binding, rounds and cases are this run's, not the file's",
			OmittedDecided: decidedRowCount(rows) - len(postable.Results),
		}
	}

	selfCheck := "passed: the payload satisfies fitpolicy.ValidateSuiteReport against the given binding"
	if payloadIn != nil {
		selfCheck = "passed: the curated payload satisfies fitpolicy.ValidateSuiteReport against the given binding, and every row matches this run's own decision"
	}
	if len(postable.Results) == 0 {
		selfCheck = "skipped: no decisive measurements, so the payload carries no results (the endpoint rejects an empty report)"
	}

	baselineInfo := baselineReport{
		Source: "docs",
		Note:   "baseline comes from the repository expectation table, not from a measurement",
	}
	if measuredBaseline {
		baselineInfo.Source = "measured"
		baselineInfo.OfficialURL = redactURL(lookupFamilyValue(opts.officialURL, baselineFamilies[0]))
		baselineInfo.Note = "baseline measured against the configured official endpoint; where it disagrees with the table the measurement wins"
		baselineInfo.Families = baselineFamilies
	}
	if policyNote != "" {
		baselineInfo.Note += "; " + policyNote
	}

	summary := summarize(rows, len(postable.Results), omittedRows)
	if rounds > 1 {
		summary.Rounds = rounds
		summary.ProbeRequests = probeRequests(rows)
		summary.Undecided = undecidedRows(rows)
	}

	full := fullReport{
		Tool:          "fit-probe",
		SchemaVersion: 1,
		GeneratedAt:   now.Unix(),
		Mode:          mode,
		AdminBaseURL:  redactURL(opts.baseURL),
		RelayBaseURL:  redactURL(opts.relayBaseURL),
		Credentials: credentialsReport{
			AdminTokenPresent: adminToken != "",
			RelayTokenPresent: relayToken != "",
		},
		Baseline:    baselineInfo,
		MarkMapping: markMapping(behaviorsOf(targets)),
		Plan:        plan,
		Results:     rows,
		Summary:     summary,
		SelfCheck:   selfCheck,
		Postable:    len(postable.Results) > 0,
		Rounds:      multiRounds(rounds),
		PayloadIn:   payloadIn,
		Payload:     encoded,
	}

	if opts.write {
		code, responseBody, err := api.postReport(ctx, encoded)
		if err != nil {
			return fmt.Errorf("POST /api/fit-capability/report: %w", err)
		}
		full.PostResult = fmt.Sprintf("HTTP %d %s", code, redactText(string(responseBody), 400))
		if code != 200 {
			return fmt.Errorf("POST /api/fit-capability/report answered %d: %s", code, redactText(string(responseBody), 400))
		}
		fmt.Fprintln(stderr, "fit-probe: report accepted:", redactText(string(responseBody), 400))
	}

	if opts.explain {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(full)
	}
	_, err = stdout.Write(append(encoded, '\n'))
	return err
}

// target is one planned channel/model/family/behaviour combination.
type target struct {
	ChannelID   int
	ChannelName string
	ChannelType int
	Family      string
	Model       string
	Behavior    string
}

// probePlan is one target's marshalled request, built once and re-sent by every
// round so each round measures exactly the same bytes.
type probePlan struct {
	spec ProbeSpec
	body []byte
}

// loadPolicy reads the live policy document and falls back to the built-in one.
func loadPolicy(ctx context.Context, opts options, offline bool, adminToken string) (fitpolicy.Policy, policyBinding, string, error) {
	if offline || opts.plan {
		return fitpolicy.DefaultPolicy(), policyBinding{}, "offline: the shipped default policy was used for the mark list", nil
	}
	api := newClient(opts.timeout, opts.baseURL, adminToken, opts.relayBaseURL, "")
	view, err := api.fitPolicy(ctx)
	if err != nil {
		return fitpolicy.Policy{}, policyBinding{}, "", err
	}
	if !view.Live.Installed {
		return fitpolicy.Policy{}, policyBinding{}, "", fmt.Errorf("no fit policy is installed; the report endpoint would answer 409 (controller/channel_fit_capability.go:392-399)")
	}
	policy := fitpolicy.DefaultPolicy()
	note := "live policy unreadable; the shipped default document was used for the mark list"
	if strings.TrimSpace(view.EffectiveDocument) != "" {
		parsed, parseErr := fitpolicy.ParsePolicy([]byte(view.EffectiveDocument))
		if parseErr != nil {
			note = "live policy did not parse (" + parseErr.Error() + "); the shipped default was used"
		} else {
			policy = parsed
			note = ""
		}
	}
	return policy, view.binding(), note, nil
}

// selectedFamilies intersects the requested families with the registry.
func selectedFamilies(policy fitpolicy.Policy, opts options) []string {
	requested := splitList(opts.families)
	if len(requested) == 0 {
		out := make([]string, 0, len(policy.Families))
		for _, family := range policy.Families {
			out = append(out, family.ID)
		}
		if len(out) > 0 {
			return out
		}
		for _, family := range officialfit.Families {
			out = append(out, family.ID)
		}
		return out
	}
	return requested
}

// filterToPolicy warns when a requested mark is not required by any family and
// drops it.
func filterToPolicy(policy fitpolicy.Policy, requested []string, opts options, stderr io.Writer) []string {
	if len(requested) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, behaviorList := range requiredBehaviors(policy) {
		for _, behavior := range behaviorList {
			known[behavior] = true
		}
	}
	out := make([]string, 0, len(requested))
	for _, behavior := range requested {
		normalized := strings.ToLower(strings.TrimSpace(behavior))
		if known[normalized] {
			out = append(out, normalized)
			continue
		}
		fmt.Fprintln(stderr, "fit-probe: ignoring unknown mark", normalized)
	}
	return out
}

func selectChannels(channels []channelInfo, requested []string) []channelInfo {
	if len(requested) == 0 {
		return channels
	}
	wanted := map[int]bool{}
	for _, id := range requested {
		var parsed int
		if _, err := fmt.Sscanf(id, "%d", &parsed); err == nil {
			wanted[parsed] = true
		}
	}
	out := make([]channelInfo, 0, len(channels))
	for _, channel := range channels {
		if wanted[channel.Id] {
			out = append(out, channel)
		}
	}
	return out
}

// buildTargets expands channels and models into the probe plan.
func buildTargets(policy fitpolicy.Policy, channels []channelInfo, families, behaviorFilter []string, opts options) []target {
	requestedModels := splitList(opts.models)
	modelFilter := map[string]bool{}
	for _, model := range requestedModels {
		modelFilter[strings.ToLower(model)] = true
	}
	familyFilter := map[string]bool{}
	for _, family := range families {
		familyFilter[strings.ToLower(strings.TrimSpace(family))] = true
	}
	var out []target
	for _, channel := range channels {
		for _, model := range channel.modelList() {
			family := officialfit.FamilyOf(model)
			if family == "" || !familyFilter[family] {
				continue
			}
			if len(modelFilter) > 0 && !modelFilter[strings.ToLower(model)] {
				continue
			}
			for _, behavior := range requiredBehaviors(policy)[family] {
				if len(behaviorFilter) > 0 && !containsString(behaviorFilter, behavior) {
					continue
				}
				out = append(out, target{
					ChannelID:   channel.Id,
					ChannelName: channel.Name,
					ChannelType: channel.Type,
					Family:      family,
					Model:       model,
					Behavior:    behavior,
				})
			}
		}
	}
	if len(out) == 0 && len(channels) == 1 && channels[0].Id == 0 {
		// Offline placeholder: one row per registered family and mark.
		for _, family := range officialfit.Families {
			if !familyFilter[family.ID] || len(family.OfficialModelNames) == 0 {
				continue
			}
			for _, behavior := range requiredBehaviors(policy)[family.ID] {
				if len(behaviorFilter) > 0 && !containsString(behaviorFilter, behavior) {
					continue
				}
				out = append(out, target{
					ChannelID: 0,
					Family:    family.ID,
					Model:     family.OfficialModelNames[0],
					Behavior:  behavior,
				})
			}
		}
	}
	return out
}

// probeBody marshals the exact bytes of one probe request. The relay probe and
// the official baseline of a mark share these bytes, so the comparison is
// always between two identical requests.
func probeBody(spec ProbeSpec, tgt target) ([]byte, error) {
	return json.Marshal(spec.Build(tgt.Family, tgt.Model))
}

// baselineKey identifies one measured baseline by the full request signature:
// family, model, behaviour and the marshalled body. Keying on (family, model)
// alone reused one mark's response as the baseline for every other mark of the
// same pair, which compared different request bodies and invented divergences.
func baselineKey(family, model, behavior string, body []byte) string {
	return strings.Join([]string{family, model, behavior, string(body)}, "\x00")
}

// unmeasurableRow records a required policy mark this tool has no minimal probe
// for. It is reported as inconclusive — and can therefore never be encoded —
// instead of silently disappearing from the output.
func unmeasurableRow(tgt target) probeResult {
	reason := unmeasurableBehaviors[tgt.Behavior]
	if reason == "" {
		reason = "no minimal probe is defined for this mark"
	}
	return probeResult{
		ChannelID:   tgt.ChannelID,
		ChannelName: tgt.ChannelName,
		ChannelType: tgt.ChannelType,
		Family:      tgt.Family,
		Model:       tgt.Model,
		Behavior:    tgt.Behavior,
		Verdict:     verdictInconclusive,
		Strength:    strengthAcceptance,
		Basis:       "not measured: " + reason,
		Placeholder: true,
	}
}

// unencodableRow reports a probe request that could not be serialised. It is
// inconclusive and therefore never encoded.
func unencodableRow(tgt target, spec ProbeSpec, err error) probeResult {
	message := redactText("request was not encodable: "+err.Error(), 200)
	return probeResult{
		ChannelID:   tgt.ChannelID,
		ChannelName: tgt.ChannelName,
		ChannelType: tgt.ChannelType,
		Family:      tgt.Family,
		Model:       tgt.Model,
		Behavior:    tgt.Behavior,
		Verdict:     verdictInconclusive,
		Strength:    strengthAcceptance,
		Basis:       "probe was not executed (" + message + ")",
		Path:        spec.Path,
		Transport:   message,
	}
}

// probeChannel sends one probe with at most one retry, for a transport error or
// a retryable status. Deterministic 4xx answers are never retried.
func probeChannel(ctx context.Context, api *client, tgt target, spec ProbeSpec, body []byte) (signature, int, string) {
	attempts := 0
	var lastErr string
	for attempt := 1; attempt <= 2; attempt++ {
		attempts = attempt
		status, payload, err := api.probeRelay(ctx, tgt.ChannelID, spec.Path, body)
		if err != nil {
			lastErr = redactText(err.Error(), 200)
			continue
		}
		sig := extractSignature(status, payload)
		if retryableStatus(status) && attempt == 1 {
			lastErr = "retryable status " + itoa(status)
			continue
		}
		return sig, attempts, ""
	}
	return signature{Status: 0}, attempts, lastErr
}

func probeOfficialBaseline(ctx context.Context, api *client, opts options, tgt target, body []byte) *signature {
	base := lookupFamilyValue(opts.officialURL, tgt.Family)
	keyEnv := lookupFamilyValue(opts.officialKeyEnv, tgt.Family)
	if base == "" || keyEnv == "" {
		return nil
	}
	key := strings.TrimSpace(os.Getenv(keyEnv))
	if key == "" {
		return nil
	}
	spec, known := probeSpecs[tgt.Behavior]
	if !known {
		return nil
	}
	status, payload, err := api.probeOfficial(ctx, base, key, spec.Path, body)
	if err != nil {
		return nil
	}
	sig := extractSignature(status, payload)
	return &sig
}

func retryableStatus(status int) bool {
	switch status {
	case 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

func expectationFor(family, behavior string) Expectation {
	if entry, ok := expectationIndex()[expectationKey(family, behavior)]; ok {
		return entry
	}
	return Expectation{Family: family, Behavior: behavior, Official: expectUndefined, BasisKind: "none"}
}

func behaviorsOf(targets []target) []string {
	seen := map[string]bool{}
	out := make([]string, 0)
	for _, tgt := range targets {
		if seen[tgt.Behavior] {
			continue
		}
		seen[tgt.Behavior] = true
		out = append(out, tgt.Behavior)
	}
	sort.Strings(out)
	return out
}

func summarize(rows []probeResult, encoded, omitted int) summaryReport {
	summary := summaryReport{Probes: len(rows), Encoded: encoded, Omitted: omitted}
	channels := map[int]bool{}
	models := map[string]bool{}
	for _, row := range rows {
		channels[row.ChannelID] = true
		models[row.Family+"\x00"+row.Model] = true
		switch row.Verdict {
		case verdictConsistent:
			summary.Consistent++
		case verdictDivergence:
			summary.Divergence++
		default:
			summary.Inconclusive++
		}
	}
	summary.Channels = len(channels)
	summary.Models = len(models)
	return summary
}

// probeRequests is the number of HTTP requests the rounds actually sent to the
// relay: one per executed round, twice when a probe used the existing retry.
func probeRequests(rows []probeResult) int {
	requests := 0
	for _, row := range rows {
		for _, record := range row.RoundRecords {
			requests += record.Attempts
		}
	}
	return requests
}

// undecidedRows counts the rows a multi-round majority left undecided.
func undecidedRows(rows []probeResult) int {
	count := 0
	for _, row := range rows {
		if len(row.RoundRecords) > 0 && row.Verdict == verdictInconclusive {
			count++
		}
	}
	return count
}

func appendUnique(values []string, value string) []string {
	if containsString(values, value) {
		return values
	}
	return append(values, value)
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
