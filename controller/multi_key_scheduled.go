package controller

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// multiKeyScheduledTestMaxKeys bounds one channel's sweep so a huge pool
// cannot monopolize the runner; the next scheduled cycle continues it.
const multiKeyScheduledTestMaxKeys = 500

// multiKeyScheduledTestProbeTimeout bounds one credential probe; scheduled
// sweeps should finish well within the smallest configured interval.
const multiKeyScheduledTestProbeTimeout = 45 * time.Second

// multiKeyScheduledTestConcurrency matches the manual credential-test default
// so a scheduled sweep never hits upstream harder than an admin-driven one.
const multiKeyScheduledTestConcurrency = 4

// multiKeyLastRunOtherKey is the other_info marker recording each channel's
// last scheduled sweep time.
const multiKeyLastRunOtherKey = "multi_key_test_last_run"

// multiKeyScheduledTestSummary is the persisted result of one scheduled run.
type multiKeyScheduledTestSummary struct {
	ChannelsTested int      `json:"channels_tested"`
	KeysTested     int      `json:"keys_tested"`
	KeysSucceeded  int      `json:"keys_succeeded"`
	KeysFailed     int      `json:"keys_failed"`
	KeysDisabled   int      `json:"keys_disabled"`
	KeysReenabled  int      `json:"keys_reenabled"`
	Errors         []string `json:"errors,omitempty"`
}

// multiKeyScheduledTestHandler is a low-frequency scheduler pass: Enabled()
// folds in "is any multi-key channel enrolled" so an idle system schedules no
// rows, and Interval() is the scan cadence, not the per-channel cadence.
type multiKeyScheduledTestHandler struct{}

func (multiKeyScheduledTestHandler) Type() string { return model.SystemTaskTypeMultiKeyScheduledTest }

func (multiKeyScheduledTestHandler) Enabled() bool {
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		return false
	}
	for _, channel := range channels {
		if channel.Status == common.ChannelStatusManuallyDisabled || !channel.ChannelInfo.IsMultiKey {
			continue
		}
		if channel.GetSetting().NormalizedMultiKeyTest() != nil {
			return true
		}
	}
	return false
}

// Interval is the scan cadence: due channels are found on every pass, each
// judged by its own configured interval against other_info's last-run marker.
func (multiKeyScheduledTestHandler) Interval() time.Duration { return 5 * time.Minute }

func (multiKeyScheduledTestHandler) NewPayload() any { return nil }

func (multiKeyScheduledTestHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	summary, err := runMultiKeyScheduledTestTask(ctx, service.NewSystemTaskProgressReporter(task, runnerID))
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

// multiKeyTestDueChannels selects enrolled multi-key channels whose own
// interval has elapsed since their last sweep marker.
func multiKeyTestDueChannels(now time.Time) ([]*model.Channel, error) {
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		return nil, err
	}
	due := make([]*model.Channel, 0)
	for _, channel := range channels {
		if channel.Status == common.ChannelStatusManuallyDisabled || !channel.ChannelInfo.IsMultiKey {
			continue
		}
		mk := channel.GetSetting().NormalizedMultiKeyTest()
		if mk == nil {
			continue
		}
		lastRun := int64(0)
		if v, ok := channel.GetOtherInfo()[multiKeyLastRunOtherKey].(float64); ok {
			lastRun = int64(v)
		}
		if now.Unix()-lastRun < int64(mk.IntervalMinutes)*60 {
			continue
		}
		due = append(due, channel)
	}
	return due, nil
}

// runMultiKeyScheduledTestTask sweeps every due multi-key channel and applies
// the outcome: failed keys are auto-disabled, recovered keys are re-enabled.
// Re-enabling skips manual_disabled keys unless the channel opted in.
func runMultiKeyScheduledTestTask(ctx context.Context, report func(processed, total int)) (multiKeyScheduledTestSummary, error) {
	summary := multiKeyScheduledTestSummary{}
	targets, err := multiKeyTestDueChannels(time.Now())
	if err != nil {
		return summary, err
	}
	if len(targets) == 0 {
		return summary, nil
	}

	for _, channel := range targets {
		if ctx.Err() != nil {
			return summary, ctx.Err()
		}
		mk := channel.GetSetting().NormalizedMultiKeyTest()
		channelSummary, err := testMultiKeyChannelCredentials(ctx, channel, mk.Model, mk.ReenableManual)
		if err != nil {
			summary.Errors = append(summary.Errors, fmt.Sprintf("channel %d: %s", channel.Id, err.Error()))
			continue
		}
		if recordErr := model.SetChannelOtherInfoKey(channel.Id, multiKeyLastRunOtherKey, time.Now().Unix()); recordErr != nil {
			common.SysError(fmt.Sprintf("failed to record multi-key test last-run: channel_id=%d error=%v", channel.Id, recordErr))
		}
		summary.ChannelsTested++
		summary.KeysTested += channelSummary.KeysTested
		summary.KeysSucceeded += channelSummary.KeysSucceeded
		summary.KeysFailed += channelSummary.KeysFailed
		summary.KeysDisabled += channelSummary.KeysDisabled
		summary.KeysReenabled += channelSummary.KeysReenabled
		if report != nil {
			report(summary.ChannelsTested, len(targets))
		}
	}
	return summary, nil
}

// multiKeyChannelTestOutcome counts one channel's scheduled probe.
type multiKeyChannelTestOutcome struct {
	KeysTested    int
	KeysSucceeded int
	KeysFailed    int
	KeysDisabled  int
	KeysReenabled int
}

// testMultiKeyChannelCredentials probes the active credentials of one
// multi-key channel and applies enable/disable decisions in one pass. Disabled
// keys are probed too so recovered keys can rejoin the rotation; manual
// disabled keys only rejoin when the channel opted in.
func testMultiKeyChannelCredentials(ctx context.Context, channel *model.Channel, probeModel string, reenableManual bool) (multiKeyChannelTestOutcome, error) {
	outcome := multiKeyChannelTestOutcome{}
	credentials, err := model.ListChannelCredentials(model.DB, channel.Id)
	if err != nil {
		return outcome, err
	}
	ids := make([]int, 0, len(credentials))
	for _, credential := range credentials {
		if credential.Position < 0 {
			continue
		}
		if credential.Status == common.ChannelStatusManuallyDisabled && !reenableManual {
			continue
		}
		ids = append(ids, credential.Id)
	}
	if len(ids) == 0 {
		return outcome, nil
	}
	if len(ids) > multiKeyScheduledTestMaxKeys {
		common.SysLog(fmt.Sprintf("scheduled multi-key test truncated: channel_id=%d keys=%d cap=%d", channel.Id, len(ids), multiKeyScheduledTestMaxKeys))
		ids = ids[:multiKeyScheduledTestMaxKeys]
	}
	// Per-key outcomes come from the shared credential-test runner; a
	// channel-level probe would rotate keys and hide per-key results.
	result, err := runMultiKeyCredentialTests(ctx, channelCredentialTestTaskPayload{
		ChannelID:       channel.Id,
		CredentialIDs:   ids,
		Model:           probeModel,
		IncludeDisabled: true,
		Concurrency:     multiKeyScheduledTestConcurrency,
		TimeoutSeconds:  int(multiKeyScheduledTestProbeTimeout.Seconds()),
	}, nil)
	if err != nil {
		return outcome, err
	}
	for _, probe := range result.Results {
		if probe.Status == "success" {
			outcome.KeysSucceeded++
		} else {
			outcome.KeysFailed++
		}
	}

	disabledIDs, reenabledIDs := multiKeyScheduledTestStatusChanges(channel.Id, result.Results, reenableManual)
	if len(disabledIDs) > 0 || len(reenabledIDs) > 0 {
		if err := applyMultiKeyScheduledStatusChanges(channel.Id, disabledIDs, reenabledIDs); err != nil {
			return outcome, err
		}
		model.InitChannelCache()
	}
	outcome.KeysTested = len(result.Results)
	outcome.KeysDisabled = len(disabledIDs)
	outcome.KeysReenabled = len(reenabledIDs)
	return outcome, nil
}

// multiKeyScheduledTestStatusChanges classifies probe results into disable and
// re-enable credential ID lists. A failed key is auto-disabled immediately; a
// disabled key is re-enabled only when the probe succeeded and the previous
// failure was auto-disable (or manual keys are explicitly opted in).
func multiKeyScheduledTestStatusChanges(channelID int, probes []multiKeyTestResult, reenableManual bool) (disabledIDs, reenabledIDs []int) {
	disabledIDs = make([]int, 0)
	reenabledIDs = make([]int, 0)
	failedIDs := make([]int, 0)
	succeededIDs := make([]int, 0)
	for _, probe := range probes {
		if probe.CredentialID <= 0 {
			continue
		}
		if probe.Status == "success" {
			succeededIDs = append(succeededIDs, probe.CredentialID)
		} else if probe.Status == "failed" {
			failedIDs = append(failedIDs, probe.CredentialID)
		}
	}
	if len(failedIDs) > 0 {
		disabledIDs = failedIDs
	}
	if len(succeededIDs) == 0 {
		return disabledIDs, reenabledIDs
	}
	// Re-enable only keys that are actually down and eligible.
	current, err := model.ListChannelCredentials(model.DB, channelID)
	if err != nil {
		common.SysError(fmt.Sprintf("scheduled multi-key test skipped re-enable lookup: channel_id=%d error=%v", channelID, err))
		return disabledIDs, reenabledIDs
	}
	statusByID := make(map[int]model.ChannelCredential, len(current))
	for _, credential := range current {
		statusByID[credential.Id] = credential
	}
	for _, id := range succeededIDs {
		credential, ok := statusByID[id]
		if !ok || credential.Status == common.ChannelStatusEnabled {
			continue
		}
		if credential.Status == common.ChannelStatusManuallyDisabled && !reenableManual {
			continue
		}
		reenabledIDs = append(reenabledIDs, id)
	}
	return disabledIDs, reenabledIDs
}

// applyMultiKeyScheduledStatusChanges persists disable and re-enable decisions.
// Each call bumps the channel's credential revision once per transition.
func applyMultiKeyScheduledStatusChanges(channelID int, disabledIDs, reenabledIDs []int) error {
	apply := func(ids []int, status int, reason string) error {
		if len(ids) == 0 {
			return nil
		}
		_, err := model.UpdateChannelCredentialStatuses(model.DB, model.ChannelCredentialStatusUpdate{
			ChannelID:     channelID,
			CredentialIDs: ids,
			Status:        status,
			Reason:        reason,
		})
		return err
	}
	if err := apply(disabledIDs, common.ChannelStatusAutoDisabled, "scheduled test failed"); err != nil {
		return err
	}
	return apply(reenabledIDs, common.ChannelStatusEnabled, "")
}

// ScheduleMultiKeyTest enqueues a manual sweep that probes every enrolled
// channel regardless of its due time. It shares the scheduled task type so the
// per-type dedup also blocks a concurrent scheduled run.
func ScheduleMultiKeyTest(c *gin.Context) {
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	enrolled := 0
	for _, channel := range channels {
		if channel.ChannelInfo.IsMultiKey && channel.GetSetting().NormalizedMultiKeyTest() != nil {
			enrolled++
		}
	}
	if enrolled == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "no multi-key channel has scheduled testing enabled"})
		return
	}
	task, created, err := service.EnqueueSystemTask(model.SystemTaskTypeMultiKeyScheduledTest, multiKeyScheduledTestHandler{}.NewPayload())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !created {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "a multi-key scheduled test task is already running", "data": task.ToResponse()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": task.ToResponse()})
}
