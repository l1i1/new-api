package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/officialfit"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

// Fit capability calibration.
//
// A capability mark is a promise about an upstream, and nothing re-checks it
// after the external applier writes it. Measured 2026-10-28: channel 10's
// kimi-k3 marks (written 2026-10-05) contradicted live behaviour in every
// dimension that mattered — the accounting false negatives plus the upstream's
// own drift — and no traffic ever failed loudly enough for anyone to notice.
//
// This task re-measures the marks it can decide in-process and reports
// contradictions. It deliberately does NOT replace the applier
// (scripts/fit-probe): that tool carries the long-argued judgement rules, and a
// second opinion that silently rewrote marks would be worse than no opinion.
// The engine here therefore covers only the dimensions whose evidence is
// unambiguous (structure present/absent, or a prompt-accounting delta against a
// bare reference), reports everything else as not covered, and leaves writes
// off unless FIT_CAPABILITY_CALIBRATION_WRITE=true is set: the alarm is the
// product, the write is an opt-in after the alarm has been reviewed.
const (
	// fitCalibrationInterval re-checks every covered mark once a day.
	fitCalibrationInterval = 24 * time.Hour
	// fitCalibrationRequestTimeout bounds one probe request.
	fitCalibrationRequestTimeout = 90 * time.Second
	// fitCalibrationMaxTokens is the budget of the reference and accounting
	// probes; the structural probes need room for thinking-first upstreams, the
	// same reason the external tool uses larger caps for them.
	fitCalibrationThinkingMaxTokens = 128
	fitCalibrationToolsMaxTokens    = 96
	fitCalibrationFormatMaxTokens   = 64
	fitCalibrationMinTokens         = 8
	fitCalibrationLogprobsMaxTokens = 16
	fitCalibrationReferenceMaxToken = 1
)

// fitCalibrationVerdict is the calibration outcome for one mark.
type fitCalibrationVerdict string

const (
	// fitCalibrationConsistent: live behaviour agrees with the stored mark.
	fitCalibrationConsistent fitCalibrationVerdict = "consistent"
	// fitCalibrationDrift: live behaviour contradicts the stored mark.
	fitCalibrationDrift fitCalibrationVerdict = "drift"
	// fitCalibrationUncovered: the sweep has no in-process probe for this mark
	// (the two Responses-API dimensions), or the channel stored an operator
	// mark the sweep must not second-guess.
	fitCalibrationUncovered fitCalibrationVerdict = "uncovered"
	// fitCalibrationInconclusive: neither side produced usable evidence.
	fitCalibrationInconclusive fitCalibrationVerdict = "inconclusive"
)

// fitCalibrationSignature is the redacted structural view of one probe answer.
type fitCalibrationSignature struct {
	status           int
	accepted         bool
	content          bool
	contentJSON      bool
	toolCalls        bool
	logprobs         bool
	reasoning        bool
	finishReason     string
	promptTokens     int
	completionTokens int
}

// fitCalibrationOutcome reports one (channel, model, behaviour) result.
type fitCalibrationOutcome struct {
	ChannelID   int                      `json:"channel_id"`
	ChannelName string                   `json:"channel_name"`
	Model       string                   `json:"model"`
	Behavior    string                   `json:"behavior"`
	Verdict     fitCalibrationVerdict    `json:"verdict"`
	Stored      *bool                    `json:"stored_supported,omitempty"`
	Measured    *bool                    `json:"measured_supported,omitempty"`
	Applied     bool                     `json:"applied,omitempty"`
	Reason      string                   `json:"reason,omitempty"`
	SigChannel  *fitCalibrationSignature `json:"channel_signature,omitempty"`
	SigOfficial *fitCalibrationSignature `json:"official_signature,omitempty"`
}

// fitCalibrationSweepResult is what the admin endpoint and the task row carry.
type fitCalibrationSweepResult struct {
	StartedAt  int64                   `json:"started_at"`
	FinishedAt int64                   `json:"finished_at"`
	Pairs      int                     `json:"pairs"`
	Drift      int                     `json:"drift"`
	Applied    int                     `json:"applied"`
	Write      bool                    `json:"write_enabled"`
	Outcomes   []fitCalibrationOutcome `json:"outcomes"`
	Skipped    []string                `json:"skipped,omitempty"`
}

// fitCalibrationShape describes one covered mark: the request sent to both the
// channel and the official reference, and what the official answer must look
// like.
type fitCalibrationShape struct {
	behavior string
	// evidence is "structure" when presence/absence of a structural field
	// decides, "accounting" when the prompt-token delta against a bare
	// reference decides.
	evidence string
	// expectPresent is what the official reference must show for the mark to
	// mean "supported".
	expectPresent bool
	build         func(modelName string) map[string]any
}

var (
	fitCalibrationStructure  = "structure"
	fitCalibrationAccounting = "accounting"
)

func fitCalibrationBase(modelName string, maxTokens int) map[string]any {
	return map[string]any{
		"model":      modelName,
		"max_tokens": maxTokens,
		"stream":     false,
	}
}

// fitCalibrationShapes lists the marks the in-process sweep can decide. The two
// Responses-API dimensions are absent on purpose: probing them needs a
// different relay format, which the external applier already covers with manual
// functional probes.
func fitCalibrationShapes() []fitCalibrationShape {
	return []fitCalibrationShape{
		{
			behavior:      "usage.thinking_counting",
			evidence:      fitCalibrationStructure,
			expectPresent: false, // the official endpoint emits no reasoning when thinking is disabled
			build: func(modelName string) map[string]any {
				body := fitCalibrationBase(modelName, fitCalibrationThinkingMaxTokens)
				body["messages"] = []map[string]any{{"role": "user", "content": "hi"}}
				body["thinking"] = map[string]any{"type": "disabled"}
				return body
			},
		},
		{
			behavior:      "tools.choice_semantics",
			evidence:      fitCalibrationStructure,
			expectPresent: true,
			build: func(modelName string) map[string]any {
				body := fitCalibrationBase(modelName, fitCalibrationToolsMaxTokens)
				body["messages"] = []map[string]any{{"role": "user", "content": "Call the ping function."}}
				body["tools"] = []map[string]any{{
					"type": "function",
					"function": map[string]any{
						"name":        "ping",
						"description": "probe",
						"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
					},
				}}
				body["tool_choice"] = "required"
				return body
			},
		},
		{
			behavior:      "response_format.json",
			evidence:      fitCalibrationStructure,
			expectPresent: true,
			build: func(modelName string) map[string]any {
				body := fitCalibrationBase(modelName, fitCalibrationFormatMaxTokens)
				body["messages"] = []map[string]any{{"role": "user", "content": `Reply with json: {"ok":true}`}}
				body["response_format"] = map[string]any{"type": "json_object"}
				return body
			},
		},
		{
			behavior:      "logprobs.present",
			evidence:      fitCalibrationStructure,
			expectPresent: true,
			build: func(modelName string) map[string]any {
				body := fitCalibrationBase(modelName, fitCalibrationLogprobsMaxTokens)
				body["messages"] = []map[string]any{{"role": "user", "content": "hi"}}
				body["logprobs"] = true
				body["top_logprobs"] = 1
				return body
			},
		},
		{
			behavior:      "history.assistant_first",
			evidence:      fitCalibrationAccounting,
			expectPresent: true,
			build: func(modelName string) map[string]any {
				body := fitCalibrationBase(modelName, fitCalibrationMinTokens)
				body["messages"] = []map[string]any{
					{"role": "assistant", "content": "prior turn"},
					{"role": "user", "content": "hi"},
				}
				return body
			},
		},
		{
			behavior:      "tools.dynamic_names",
			evidence:      fitCalibrationAccounting,
			expectPresent: true,
			build: func(modelName string) map[string]any {
				body := fitCalibrationBase(modelName, fitCalibrationMinTokens)
				body["messages"] = []map[string]any{
					{"role": "system", "content": "", "tools": []map[string]any{{
						"type": "function",
						"function": map[string]any{
							"name":        "ping",
							"description": "probe",
							"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
						},
					}}},
					{"role": "user", "content": "hi"},
				}
				return body
			},
		},
	}
}

// fitCalibrationReference is the bare request the accounting marks compare
// against: the delta isolates the shape under test from a channel's own
// tokenizer/billing offset.
func fitCalibrationReference(modelName string) map[string]any {
	body := fitCalibrationBase(modelName, fitCalibrationReferenceMaxToken)
	body["messages"] = []map[string]any{{"role": "user", "content": "hi"}}
	return body
}

// fitCalibrationRequestBody turns a probe body into a relay request.
func fitCalibrationRequestBody(body map[string]any) (dto.Request, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var request dto.GeneralOpenAIRequest
	if err := common.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	return &request, nil
}

// fitCalibrationSignatureOf extracts the structural fields a calibration can
// compare. It never keeps the answer text.
func fitCalibrationSignatureOf(result testResult) fitCalibrationSignature {
	sig := fitCalibrationSignature{status: result.upstreamStatus}
	if result.localErr != nil || result.newAPIError != nil || len(result.body) == 0 {
		return sig
	}
	var payload struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Logprobs     any    `json:"logprobs"`
			Message      struct {
				Content          any    `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []any  `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := common.Unmarshal(result.body, &payload); err != nil || len(payload.Choices) == 0 {
		return sig
	}
	choice := payload.Choices[0]
	sig.accepted = result.upstreamStatus == http.StatusOK
	sig.finishReason = choice.FinishReason
	sig.toolCalls = len(choice.Message.ToolCalls) > 0
	sig.logprobs = choice.Logprobs != nil
	text := ""
	if value, ok := choice.Message.Content.(string); ok {
		text = strings.TrimSpace(value)
	}
	sig.content = text != ""
	sig.contentJSON = sig.content && strings.HasPrefix(text, "{") && strings.HasSuffix(text, "}")
	reasoning := strings.TrimSpace(choice.Message.ReasoningContent)
	if result.usage != nil {
		sig.promptTokens = result.usage.PromptTokens
		sig.completionTokens = result.usage.CompletionTokens
		if result.usage.CompletionTokenDetails.ReasoningTokens > 0 {
			reasoning = "reasoning-tokens"
		}
	}
	sig.reasoning = reasoning != ""
	return sig
}

// fitCalibrationProbe runs one probe body against a channel and returns its
// signature, reusing the channel-test engine so the measurement sees exactly
// what the relay would send.
func fitCalibrationProbe(ctx context.Context, channel *model.Channel, testUserID int, modelName string, body map[string]any) fitCalibrationSignature {
	request, err := fitCalibrationRequestBody(body)
	if err != nil {
		return fitCalibrationSignature{}
	}
	probeCtx, cancel := context.WithTimeout(ctx, fitCalibrationRequestTimeout)
	defer cancel()
	return fitCalibrationSignatureOf(testChannelWithOptionsAndRequest(
		probeCtx, channel, testUserID, modelName, "", false, nil, false, false, request,
	))
}

// fitCalibrationPromptTolerance mirrors the external tool: an official delta of
// N tokens tolerates N/8 of accounting noise, at least one token.
func fitCalibrationPromptTolerance(officialDelta int) int {
	if officialDelta < 0 {
		officialDelta = -officialDelta
	}
	tolerance := officialDelta / 8
	if tolerance < 1 {
		tolerance = 1
	}
	return tolerance
}

// classifyFitCalibration compares one mark's live evidence with the stored
// mark. It returns the verdict and, when decisive, what the measurement says
// the mark should be.
//
// Rules are deliberately narrow: a difference is only called drift when the
// official reference itself produced usable evidence for the same shape, the
// channel answered, and the answer was not cut short at the probe's own budget
// (a cut answer says nothing about the channel).
func classifyFitCalibration(
	shape fitCalibrationShape,
	storedSupported bool,
	channelSig fitCalibrationSignature,
	officialSig fitCalibrationSignature,
	channelRef fitCalibrationSignature,
	officialRef fitCalibrationSignature,
) (fitCalibrationVerdict, *bool, string) {
	if !officialSig.accepted {
		return fitCalibrationInconclusive, nil, "the official reference did not answer this shape, so there is nothing to compare"
	}
	if !channelSig.accepted {
		// The channel rejected a shape the official endpoint serves: the mark
		// claims capability it does not have.
		measured := false
		return fitCalibrationDrift, &measured, "the official reference accepted the shape, the channel did not (status " + itoaForCalibration(channelSig.status) + ")"
	}
	if (shape.evidence == fitCalibrationStructure) && shape.expectPresent && channelSig.finishReason == "length" && !fitCalibrationStructurePresent(shape.behavior, channelSig) {
		return fitCalibrationInconclusive, nil, "the channel's answer was cut at the probe's own token budget, so the missing structure cannot be attributed to the channel"
	}
	if shape.evidence == fitCalibrationStructure {
		if shape.behavior == "usage.thinking_counting" {
			// The mark means "matches the official accounting": official emits no
			// reasoning for a thinking-disabled request.
			if officialSig.reasoning {
				return fitCalibrationInconclusive, nil, "the official reference itself reasoned on the thinking-disabled shape, so the probe cannot discriminate"
			}
			measured := !channelSig.reasoning
			if measured != storedSupported {
				if channelSig.reasoning {
					return fitCalibrationDrift, &measured, "the channel reasoned on a thinking-disabled request where the official endpoint does not"
				}
				return fitCalibrationDrift, &measured, "the channel stopped reasoning on the thinking-disabled shape but the stored mark says it does"
			}
			return fitCalibrationConsistent, &measured, "reasoning accounting matches the official reference"
		}
		present := fitCalibrationStructurePresent(shape.behavior, channelSig)
		officialPresent := fitCalibrationStructurePresent(shape.behavior, officialSig)
		if !officialPresent {
			return fitCalibrationInconclusive, nil, "the official reference did not produce the dimension's structure, so the probe does not discriminate"
		}
		if present != storedSupported {
			reason := "the official reference produced the dimension's structure and the channel did not"
			if present && !storedSupported {
				reason = "the channel now produces the dimension's structure while the stored mark denies it"
			}
			return fitCalibrationDrift, &present, reason
		}
		return fitCalibrationConsistent, &present, "the dimension's structure matches the stored mark"
	}
	// Accounting mark: compare the prompt-token delta against the bare
	// reference on both legs, which cancels a constant tokenizer offset.
	if channelRef.accepted != true || officialRef.accepted != true ||
		channelRef.promptTokens <= 0 || officialRef.promptTokens <= 0 || channelSig.promptTokens <= 0 || officialSig.promptTokens <= 0 {
		return fitCalibrationInconclusive, nil, "the bare reference arm did not answer on both legs, so the accounting delta cannot be computed"
	}
	officialDelta := officialSig.promptTokens - officialRef.promptTokens
	channelDelta := channelSig.promptTokens - channelRef.promptTokens
	if fitCalibrationAbs(channelDelta-officialDelta) > fitCalibrationPromptTolerance(officialDelta) {
		measured := false
		return fitCalibrationDrift, &measured, "the prompt-token delta differs from the official reference (official " + signedItoaForCalibration(officialDelta) + ", channel " + signedItoaForCalibration(channelDelta) + ")"
	}
	measured := true
	return fitCalibrationConsistent, &measured, "the prompt-token delta matches the official reference"
}

// fitCalibrationStructurePresent reports whether the response shows the
// structure the mark is about.
func fitCalibrationStructurePresent(behavior string, sig fitCalibrationSignature) bool {
	switch behavior {
	case "tools.choice_semantics":
		return sig.toolCalls
	case "response_format.json":
		return sig.contentJSON
	case "logprobs.present":
		return sig.logprobs
	case "usage.thinking_counting":
		return sig.reasoning
	default:
		return false
	}
}

func fitCalibrationAbs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func itoaForCalibration(value int) string {
	return fmt.Sprintf("%d", value)
}

func signedItoaForCalibration(value int) string {
	if value >= 0 {
		return "+" + fmt.Sprintf("%d", value)
	}
	return fmt.Sprintf("%d", value)
}

// fitCalibrationTargets lists the (channel, model) pairs to re-check: a channel
// that appears in the capability table for a kimi-k3 family model. A pair with
// no marks has nothing to calibrate — the applier owns the initial measurement,
// and an unmarked pair is already routed conservatively.
func fitCalibrationTargets() ([]*model.Channel, map[int][]string, error) {
	channels, err := loadAllChannelsForCalibration()
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[int]*model.Channel, len(channels))
	for _, channel := range channels {
		byID[channel.Id] = channel
	}
	rows, err := model.ListAllChannelFitCapabilities()
	if err != nil {
		return nil, nil, err
	}
	seen := make(map[int]map[string]struct{})
	for i := range rows {
		row := &rows[i]
		if row.Family != "kimi-k3" {
			continue
		}
		if officialfit.FamilyOf(row.Model) != officialfit.FamilyKimiK3 {
			continue
		}
		if _, ok := byID[row.ChannelId]; !ok {
			continue
		}
		if seen[row.ChannelId] == nil {
			seen[row.ChannelId] = make(map[string]struct{})
		}
		seen[row.ChannelId][row.Model] = struct{}{}
	}
	targets := make(map[int][]string, len(seen))
	out := make([]*model.Channel, 0, len(seen))
	for channelID, models := range seen {
		if byID[channelID].Status != common.ChannelStatusEnabled {
			continue
		}
		out = append(out, byID[channelID])
		list := make([]string, 0, len(models))
		for modelName := range models {
			list = append(list, modelName)
		}
		sortStringsForCalibration(list)
		targets[channelID] = list
	}
	return out, targets, nil
}

// loadAllChannelsForCalibration pages every channel from the database.
func loadAllChannelsForCalibration() ([]*model.Channel, error) {
	var all []*model.Channel
	for startIdx := 0; ; startIdx += 100 {
		channels, err := model.GetAllChannels(startIdx, 100, true, true)
		if err != nil {
			return nil, err
		}
		if len(channels) == 0 {
			break
		}
		all = append(all, channels...)
		if len(channels) < 100 {
			break
		}
	}
	return all, nil
}

// sortStringsForCalibration keeps the sweep order stable so two runs' reports
// can be diffed.
func sortStringsForCalibration(values []string) {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
}

// findFitCalibrationReference returns an enabled channel of the official type
// for the model: the measurement's reference leg.
func findFitCalibrationReference(channels []*model.Channel, modelName string) *model.Channel {
	officialType := model.OfficialFitChannelType(modelName)
	if officialType == 0 {
		return nil
	}
	for _, channel := range channels {
		if channel.Status != common.ChannelStatusEnabled {
			continue
		}
		if channel.Type != officialType {
			continue
		}
		for _, declared := range channel.GetModels() {
			if strings.EqualFold(strings.TrimSpace(declared), modelName) {
				return channel
			}
		}
	}
	return nil
}

// runFitCapabilityCalibration sweeps the covered marks and reports drift.
// Writes happen only when the sweep contradicts a stored mark, the mark came
// from the applier (not an operator), and the write switch is on.
func runFitCapabilityCalibration(ctx context.Context) (fitCalibrationSweepResult, error) {
	result := fitCalibrationSweepResult{StartedAt: common.GetTimestamp()}
	result.Write = common.GetEnvOrDefaultBool("FIT_CAPABILITY_CALIBRATION_WRITE", false)

	testUserID, err := resolveChannelTestUserID(nil)
	if err != nil {
		return result, err
	}
	channels, targets, err := fitCalibrationTargets()
	if err != nil {
		return result, err
	}
	allChannels := channels
	if len(allChannels) == 0 {
		result.FinishedAt = common.GetTimestamp()
		return result, nil
	}
	// The reference lookup needs every enabled channel, not only the targets.
	everything, err := loadAllChannelsForCalibration()
	if err != nil {
		return result, err
	}

	shapes := fitCalibrationShapes()
	snapshot := fitpolicy.Current()
	for _, channel := range allChannels {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		for _, modelName := range targets[channel.Id] {
			reference := findFitCalibrationReference(everything, modelName)
			if reference == nil {
				result.Skipped = append(result.Skipped, fmt.Sprintf("channel %d model %s: no enabled official reference", channel.Id, modelName))
				continue
			}
			for _, shape := range shapes {
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
				row, found, getErr := model.GetChannelFitCapability(channel.Id, "kimi-k3", modelName, shape.behavior)
				if getErr != nil || !found || row == nil {
					continue
				}
				result.Pairs++
				outcome := fitCalibrationOutcome{
					ChannelID:   channel.Id,
					ChannelName: channel.Name,
					Model:       modelName,
					Behavior:    shape.behavior,
					Stored:      &row.Supported,
				}
				if row.Source == model.FitCapabilitySourceManual {
					outcome.Verdict = fitCalibrationUncovered
					outcome.Reason = "the mark is an operator assertion; the sweep does not second-guess it"
					result.Outcomes = append(result.Outcomes, outcome)
					continue
				}
				body := shape.build(modelName)
				channelSig := fitCalibrationProbe(ctx, channel, testUserID, modelName, body)
				officialSig := fitCalibrationProbe(ctx, reference, testUserID, modelName, body)
				outcome.SigChannel = &channelSig
				outcome.SigOfficial = &officialSig
				var channelRef, officialRef fitCalibrationSignature
				if shape.evidence == fitCalibrationAccounting {
					refBody := fitCalibrationReference(modelName)
					channelRef = fitCalibrationProbe(ctx, channel, testUserID, modelName, refBody)
					officialRef = fitCalibrationProbe(ctx, reference, testUserID, modelName, refBody)
				}
				verdict, measured, reason := classifyFitCalibration(shape, row.Supported, channelSig, officialSig, channelRef, officialRef)
				outcome.Verdict = verdict
				outcome.Measured = measured
				outcome.Reason = reason
				if verdict == fitCalibrationDrift {
					result.Drift++
					if measured != nil && result.Write {
						_, applyErr := model.ApplyChannelFitCapability(model.FitCapabilityWrite{
							ChannelId:        channel.Id,
							Family:           "kimi-k3",
							Model:            modelName,
							Behavior:         shape.behavior,
							Supported:        *measured,
							Source:           model.FitCapabilitySourceSuite,
							Suite:            "gateway-calibration",
							Cases:            "1/1",
							Rounds:           1,
							At:               common.GetTimestamp(),
							PolicyVersion:    snapshot.Version(),
							PolicyHash:       snapshot.Hash(),
							BaselineHash:     snapshot.Baseline(),
							ReportId:         "gateway-calibration",
							RunId:            "gateway-calibration",
							ExpectedRevision: row.Revision,
						}, common.GetTimestamp())
						if applyErr != nil {
							outcome.Reason = reason + "; write failed: " + applyErr.Error()
						} else {
							outcome.Applied = true
							result.Applied++
						}
					}
					common.SysLog(fmt.Sprintf(
						"fit capability calibration: channel=%d model=%s behavior=%s DRIFT stored=%v measured=%v applied=%v",
						channel.Id, modelName, shape.behavior, row.Supported, measured != nil && *measured, outcome.Applied,
					))
				}
				result.Outcomes = append(result.Outcomes, outcome)
			}
		}
	}
	result.FinishedAt = common.GetTimestamp()
	return result, nil
}

// RunFitCapabilityCalibration is the admin endpoint that runs one sweep.
func RunFitCapabilityCalibration(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()
	result, err := runFitCapabilityCalibration(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// fitCalibrationHandler runs the calibration as a scheduled system task so a DB
// lease dedups it across nodes and every run leaves one task row.
type fitCalibrationHandler struct{}

func (fitCalibrationHandler) Type() string { return model.SystemTaskTypeFitCapabilityCalibration }

// Enabled reports whether the sweep runs at all. It defaults to on because the
// alarm is read-only; FIT_CAPABILITY_CALIBRATION_ENABLED=false stops it.
func (fitCalibrationHandler) Enabled() bool {
	return common.GetEnvOrDefaultBool("FIT_CAPABILITY_CALIBRATION_ENABLED", true)
}

func (fitCalibrationHandler) Interval() time.Duration { return fitCalibrationInterval }

func (fitCalibrationHandler) NewPayload() any { return nil }

func (fitCalibrationHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	result, err := runFitCapabilityCalibration(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			err = errors.New("fit capability calibration timed out")
		} else {
			// Channel/provider text must not be persisted in system-task records.
			err = errors.New("fit capability calibration failed")
		}
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	common.SysLog(fmt.Sprintf(
		"fit capability calibration finished: pairs=%d drift=%d applied=%d write=%v",
		result.Pairs, result.Drift, result.Applied, result.Write,
	))
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, result, nil)
}
