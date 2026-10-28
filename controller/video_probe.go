package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

// Video capability probe.
//
// A channel's video declaration (dto.ChannelOtherSettings.SupportsVideo) is a
// promise about its upstream that nobody re-checks: capability is declared per
// channel, but the truth is per model, and an upstream can lose it for a single
// model without any error surface. Measured 2026-10-28: on one channel
// kimi-k3 reported 7168 prompt tokens for a clip while kimi-k3.1-pro reported
// 122 and answered "the current model does not support videos" — HTTP 200, no
// error, a plausible answer written from text alone.
//
// The probe answers "does this upstream actually read the media for this
// model?" without trusting the answer text. It sends the same chat prompt twice
// against the same channel and model: once text-only, once with a video part.
// A capable upstream charges the media (prompt tokens jump by the video's
// token count); a media-blind upstream reports the baseline count unchanged,
// because the part was dropped before tokenisation. The verdict drives the
// per-model exclusion list (dto.ChannelOtherSettings.VideoUnsupportedModels),
// so routing stops sending that model's video requests to a blind upstream.
const (
	// videoProbeSampleURL is a short public MP4 used as the video part. It only
	// has to be a fetchable clip: blindness is detected from the usage delta, not
	// from the answer, so an upstream that rejects the URL (the official Moonshot
	// endpoint answers 400 "unsupported video url") is reported as inconclusive
	// instead of being marked blind.
	videoProbeSampleURL = "https://interactive-examples.mdn.mozilla.net/media/cc0-videos/flower.mp4"
	// videoProbePrompt is identical in both arms so the prompt-token delta is
	// the media alone.
	videoProbePrompt = "Describe this video in one short sentence."
	// videoProbeMaxTokens keeps the probe cheap; the answer text is ignored.
	videoProbeMaxTokens = 16
	// videoProbeMinVideoTokens is the smallest media contribution that counts as
	// "the upstream read it". Anything between zero and this is ambiguous and
	// reported as inconclusive rather than written into the exclusion list.
	videoProbeMinVideoTokens = 32
	// videoProbeInterval is how often the scheduled sweep re-checks every
	// (channel, model) pair whose channel declares video.
	videoProbeInterval = 6 * time.Hour
	// videoProbeStartDelay lets a freshly started node settle before spending
	// upstream capacity on a sweep.
	videoProbeStartDelay = 10 * time.Minute
	// videoProbePairTimeout bounds one (channel, model) pair so a hanging
	// upstream cannot stall the sweep.
	videoProbePairTimeout = 90 * time.Second
)

// videoProbeVerdict is the conclusion of one probe pair.
type videoProbeVerdict string

const (
	// videoProbeVerdictCapable: the upstream charged the media, so video
	// requests for this channel+model can be routed here.
	videoProbeVerdictCapable videoProbeVerdict = "capable"
	// videoProbeVerdictBlind: the upstream answered 200 with the baseline prompt
	// count, i.e. it silently dropped the video part.
	videoProbeVerdictBlind videoProbeVerdict = "blind"
	// videoProbeVerdictInconclusive: no evidence either way (upstream rejected
	// the sample, transport failure, baseline failed, ambiguous delta). The
	// stored exclusion list is left untouched.
	videoProbeVerdictInconclusive videoProbeVerdict = "inconclusive"
)

// classifyVideoProbe maps one probe pair to a verdict.
//
// videoStatus is the HTTP status of the video arm (0 when it never answered).
// Only a successful video arm can prove blindness: an explicit rejection proves
// the upstream inspects the media (the official endpoint rejects URL-sourced
// video with 400 instead of ignoring it), and a transport failure proves
// nothing at all.
func classifyVideoProbe(baselineTokens int, videoTokens int, videoStatus int) videoProbeVerdict {
	if videoStatus != http.StatusOK {
		return videoProbeVerdictInconclusive
	}
	if baselineTokens <= 0 || videoTokens <= 0 {
		return videoProbeVerdictInconclusive
	}
	delta := videoTokens - baselineTokens
	if delta == 0 {
		return videoProbeVerdictBlind
	}
	if delta >= videoProbeMinVideoTokens {
		return videoProbeVerdictCapable
	}
	return videoProbeVerdictInconclusive
}

// videoProbeOutcome reports one (channel, model) probe result.
type videoProbeOutcome struct {
	ChannelID      int               `json:"channel_id"`
	ChannelName    string            `json:"channel_name"`
	Model          string            `json:"model"`
	Verdict        videoProbeVerdict `json:"verdict"`
	BaselineTokens int               `json:"baseline_prompt_tokens"`
	VideoTokens    int               `json:"video_prompt_tokens"`
	VideoStatus    int               `json:"video_status"`
	Error          string            `json:"error,omitempty"`
	Applied        bool              `json:"applied,omitempty"`
}

// videoProbeRequest builds the probe chat request. withVideo=false yields the
// baseline arm (identical prompt, no media), so the two arms differ only by the
// media part.
func videoProbeRequest(modelName string, withVideo bool) dto.Request {
	content := []any{map[string]any{"type": "text", "text": videoProbePrompt}}
	if withVideo {
		content = append(content, map[string]any{
			"type":      "video_url",
			"video_url": map[string]any{"url": videoProbeSampleURL},
		})
	}
	maxTokens := uint(videoProbeMaxTokens)
	stream := false
	return &dto.GeneralOpenAIRequest{
		Model:     modelName,
		Messages:  []dto.Message{{Role: "user", Content: content}},
		MaxTokens: &maxTokens,
		Stream:    &stream,
	}
}

// probeChannelModelVideo runs the two arms against one channel and model.
func probeChannelModelVideo(ctx context.Context, channel *model.Channel, testUserID int, modelName string) videoProbeOutcome {
	outcome := videoProbeOutcome{ChannelID: channel.Id, ChannelName: channel.Name, Model: modelName}

	pairCtx, cancel := context.WithTimeout(ctx, videoProbePairTimeout)
	defer cancel()

	baseline := testChannelWithOptionsAndRequest(pairCtx, channel, testUserID, modelName, "", false, nil, false, false, videoProbeRequest(modelName, false))
	if baseline.usage == nil {
		outcome.Verdict = videoProbeVerdictInconclusive
		outcome.Error = "baseline probe failed: " + channelTestFailureText(baseline)
		return outcome
	}
	outcome.BaselineTokens = baseline.usage.PromptTokens

	video := testChannelWithOptionsAndRequest(pairCtx, channel, testUserID, modelName, "", false, nil, false, false, videoProbeRequest(modelName, true))
	outcome.VideoStatus = video.upstreamStatus
	if video.usage != nil {
		outcome.VideoTokens = video.usage.PromptTokens
	} else if video.upstreamStatus == 0 {
		outcome.Error = "video probe failed: " + channelTestFailureText(video)
	}
	outcome.Verdict = classifyVideoProbe(outcome.BaselineTokens, outcome.VideoTokens, outcome.VideoStatus)
	return outcome
}

// channelTestFailureText flattens a failed probe result for reporting.
func channelTestFailureText(result testResult) string {
	switch {
	case result.localErr != nil:
		return common.DebugLogPreview(result.localErr.Error())
	case result.newAPIError != nil:
		return common.DebugLogPreview(result.newAPIError.Error())
	default:
		return "no usage in response"
	}
}

// videoProbeSweepResult is what the admin endpoint returns and the scheduled
// task logs.
type videoProbeSweepResult struct {
	StartedAt  int64               `json:"started_at"`
	FinishedAt int64               `json:"finished_at"`
	Outcomes   []videoProbeOutcome `json:"outcomes"`
	Marked     int                 `json:"marked_blind"`
	Cleared    int                 `json:"cleared"`
	Skipped    []string            `json:"skipped,omitempty"`
}

// runVideoCapabilityProbeSweep probes every enabled channel that declares video
// for each model it serves, and applies the verdict to the channel's per-model
// exclusion list. It only writes on evidence: blind marks the model, capable
// clears an existing mark, inconclusive changes nothing.
func runVideoCapabilityProbeSweep(ctx context.Context) (videoProbeSweepResult, error) {
	result := videoProbeSweepResult{StartedAt: common.GetTimestamp()}

	testUserID, err := resolveChannelTestUserID(nil)
	if err != nil {
		return result, err
	}

	channels, err := loadChannelsDeclaringVideo()
	if err != nil {
		return result, err
	}
	for _, channel := range channels {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		for _, modelName := range channelVideoProbeModels(channel) {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			outcome := probeChannelModelVideo(ctx, channel, testUserID, modelName)
			switch outcome.Verdict {
			case videoProbeVerdictBlind:
				changed, applyErr := model.SetChannelVideoUnsupportedModel(channel.Id, modelName, true)
				if applyErr != nil {
					outcome.Error = "failed to record blind verdict: " + applyErr.Error()
				} else if changed {
					outcome.Applied = true
					result.Marked++
				}
				common.SysLog(fmt.Sprintf(
					"video capability probe: channel=%d model=%s BLIND baseline=%d video=%d applied=%v err=%q",
					channel.Id, modelName, outcome.BaselineTokens, outcome.VideoTokens, outcome.Applied, outcome.Error,
				))
			case videoProbeVerdictCapable:
				changed, applyErr := model.SetChannelVideoUnsupportedModel(channel.Id, modelName, false)
				if applyErr != nil {
					outcome.Error = "failed to clear exclusion: " + applyErr.Error()
				} else if changed {
					outcome.Applied = true
					result.Cleared++
					common.SysLog(fmt.Sprintf(
						"video capability probe: channel=%d model=%s capable again (baseline=%d video=%d), exclusion cleared",
						channel.Id, modelName, outcome.BaselineTokens, outcome.VideoTokens,
					))
				}
			case videoProbeVerdictInconclusive:
				common.SysLog(fmt.Sprintf(
					"video capability probe: channel=%d model=%s inconclusive baseline=%d video=%d status=%d err=%q",
					channel.Id, modelName, outcome.BaselineTokens, outcome.VideoTokens, outcome.VideoStatus, outcome.Error,
				))
			}
			result.Outcomes = append(result.Outcomes, outcome)
		}
	}
	result.FinishedAt = common.GetTimestamp()
	return result, nil
}

// loadChannelsDeclaringVideo returns every enabled channel whose settings
// declare video capability.
func loadChannelsDeclaringVideo() ([]*model.Channel, error) {
	var declaring []*model.Channel
	for startIdx := 0; ; startIdx += 100 {
		channels, err := model.GetAllChannels(startIdx, 100, true, true)
		if err != nil {
			return nil, err
		}
		if len(channels) == 0 {
			break
		}
		for _, channel := range channels {
			if channel.Status != common.ChannelStatusEnabled {
				continue
			}
			if channel.GetOtherSettings().SupportsVideo {
				declaring = append(declaring, channel)
			}
		}
		if len(channels) < 100 {
			break
		}
	}
	return declaring, nil
}

// channelVideoProbeModels lists the model names worth probing on a video
// declaring channel: the channel's own model list, minus obviously non-chat
// models (a chat-completions probe against an embedding or image model can only
// produce noise).
func channelVideoProbeModels(channel *model.Channel) []string {
	models := channel.GetModels()
	probeModels := make([]string, 0, len(models))
	for _, modelName := range models {
		trimmed := strings.TrimSpace(modelName)
		if trimmed == "" || isNonChatModelName(trimmed) {
			continue
		}
		probeModels = append(probeModels, trimmed)
	}
	return probeModels
}

// isNonChatModelName filters model families that cannot answer a chat
// completion, so the probe does not spend capacity on requests that are
// guaranteed to fail.
func isNonChatModelName(modelName string) bool {
	lowered := strings.ToLower(modelName)
	for _, marker := range []string{
		"embedding", "embed-", "rerank", "whisper", "tts", "moderation",
		"dall-e", "gpt-image", "stable-diffusion", "flux", "sora",
	} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// RunVideoCapabilityProbe is the admin endpoint that runs one sweep on demand.
func RunVideoCapabilityProbe(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Minute)
	defer cancel()
	result, err := runVideoCapabilityProbeSweep(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// RunVideoCapabilityProbeForChannel probes one channel (all of its probe-able
// models, or a single model when the caller names one).
func RunVideoCapabilityProbeForChannel(c *gin.Context) {
	channelID, _ := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if channelID <= 0 {
		channelID = c.GetInt("channel_id")
	}
	modelName := strings.TrimSpace(c.Query("model"))
	if channelID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "channel id is required"})
		return
	}
	channel, err := model.GetChannelById(channelID, true)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": err.Error()})
		return
	}
	if !channel.GetOtherSettings().SupportsVideo {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "channel does not declare video capability"})
		return
	}
	testUserID, err := resolveChannelTestUserID(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	probeModels := channelVideoProbeModels(channel)
	if modelName != "" {
		probeModels = []string{modelName}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()
	outcomes := make([]videoProbeOutcome, 0, len(probeModels))
	for _, name := range probeModels {
		outcome := probeChannelModelVideo(ctx, channel, testUserID, name)
		if outcome.Verdict == videoProbeVerdictBlind {
			if changed, applyErr := model.SetChannelVideoUnsupportedModel(channel.Id, name, true); applyErr != nil {
				outcome.Error = applyErr.Error()
			} else {
				outcome.Applied = changed
			}
		}
		outcomes = append(outcomes, outcome)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": outcomes})
}

// videoProbeHandler runs the video capability sweep as a scheduled system task
// so a DB lease dedups it across master nodes and every run leaves one task row.
type videoProbeHandler struct{}

func (videoProbeHandler) Type() string { return model.SystemTaskTypeVideoCapabilityProbe }

// Enabled reports whether the sweep should run at all. Defaults to on because
// the whole point is catching an upstream that silently stopped reading media;
// VIDEO_CAPABILITY_PROBE_ENABLED=false turns it off without a rebuild.
func (videoProbeHandler) Enabled() bool {
	return common.GetEnvOrDefaultBool("VIDEO_CAPABILITY_PROBE_ENABLED", true)
}

func (videoProbeHandler) Interval() time.Duration { return videoProbeInterval }

func (videoProbeHandler) NewPayload() any { return nil }

func (videoProbeHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	result, err := runVideoCapabilityProbeSweep(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			err = errors.New("video capability probe timed out")
		} else {
			// Upstream/provider text must not be persisted in system-task records.
			err = errors.New("video capability probe failed")
		}
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	common.SysLog(fmt.Sprintf(
		"video capability probe sweep finished: pairs=%d marked=%d cleared=%d",
		len(result.Outcomes), result.Marked, result.Cleared,
	))
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, result, nil)
}
