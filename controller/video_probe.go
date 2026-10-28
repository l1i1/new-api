package controller

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
	// videoProbeSampleURL is a short public MP4 that the probe embeds as an
	// inline data URI. The inline form is the one customers send, and it is the
	// only form the official Moonshot endpoint accepts: a URL-sourced video part
	// is rejected with 400 "unsupported video url" there, so probing with a URL
	// could never confirm capability on the official channel and would leave
	// every URL-rejecting upstream permanently unclassified.
	//
	// VIDEO_CAPABILITY_PROBE_SAMPLE_URL overrides the download source without a
	// rebuild (an operator can point it at a local mirror when the public CDN is
	// unreachable from the node).
	videoProbeSampleURL = "https://interactive-examples.mdn.mozilla.net/media/cc0-videos/flower.mp4"
	// videoProbeSampleMaxBytes bounds the download; the sample is ~1.1MB.
	videoProbeSampleMaxBytes = 16 << 20
	// videoProbeSampleTimeout bounds the one-off download.
	videoProbeSampleTimeout = 60 * time.Second
	// videoProbeSampleRetryDelay is how long a failed download is remembered
	// before the next pair retries it.
	videoProbeSampleRetryDelay = 30 * time.Minute
	// videoProbePrompt is identical in both arms so the prompt-token delta is
	// the media alone.
	videoProbePrompt = "Describe this video in one short sentence."
	// videoProbeMaxTokens keeps the probe cheap; the answer text is ignored.
	videoProbeMaxTokens = 16
	// videoProbeCapableMinDelta is the smallest prompt-token increase that
	// proves the upstream charged the media. The sample clip costs thousands of
	// prompt tokens wherever video is actually read (measured 2026-10-28: 7168
	// on the official channel, 3549 on a GLM channel, 7168 on the aggregator that
	// serves kimi-k3), so a four-digit floor cannot be reached by a placeholder
	// or a reminder sentence injected in place of the media.
	videoProbeCapableMinDelta = 1024
	// videoProbeBlindMaxDelta is the largest increase still read as "the media
	// never reached tokenisation". It is not zero because a blind upstream may
	// substitute the part with a short reminder: the measured blind failure
	// answered 200 with 122 prompt tokens for a 1.13MB clip, and any handful of
	// substituted tokens must still count as blind rather than as ambiguous.
	videoProbeBlindMaxDelta = 128
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
// Only a successful video arm can prove anything: an explicit rejection proves
// the upstream inspects the media (the official endpoint rejects URL-sourced
// video with 400 instead of ignoring it), and a transport failure proves
// nothing at all.
//
// The two thresholds are deliberately far apart. A capable upstream charges a
// four-digit number of tokens for the sample clip, a blind one charges the
// baseline (or a few substituted tokens), and everything in between is left
// alone: a mis-marked channel either strands video traffic or silently serves
// blind answers, so only a clear signal may rewrite capability.
func classifyVideoProbe(baselineTokens int, videoTokens int, videoStatus int) videoProbeVerdict {
	if videoStatus != http.StatusOK {
		return videoProbeVerdictInconclusive
	}
	if baselineTokens <= 0 || videoTokens <= 0 {
		return videoProbeVerdictInconclusive
	}
	delta := videoTokens - baselineTokens
	switch {
	case delta >= videoProbeCapableMinDelta:
		return videoProbeVerdictCapable
	case delta <= videoProbeBlindMaxDelta:
		// Includes a shrinking prompt: a video arm that costs no more than the
		// text-only arm never had the media charged.
		return videoProbeVerdictBlind
	default:
		return videoProbeVerdictInconclusive
	}
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

// videoProbeSampleDataURL returns the inline video part shared by every probe
// arm, downloading and validating the sample once and reusing it for the life
// of the process.
//
// The inline form is what customers send and what the official endpoint
// accepts; see videoProbeSampleURL. A download failure is reported and turns
// the sweep inconclusive rather than guessing capability; the failure is
// retried after videoProbeSampleRetryDelay so a transient CDN outage does not
// disable probing until the next restart.
func videoProbeSampleDataURL(ctx context.Context) (string, error) {
	videoProbeSampleMu.Lock()
	defer videoProbeSampleMu.Unlock()
	if videoProbeSample != "" {
		return videoProbeSample, nil
	}
	if videoProbeSampleErr != nil && time.Now().Before(videoProbeSampleRetryAfter) {
		return "", videoProbeSampleErr
	}
	sample, err := downloadVideoProbeSample(ctx)
	if err != nil {
		videoProbeSampleErr = err
		videoProbeSampleRetryAfter = time.Now().Add(videoProbeSampleRetryDelay)
		return "", err
	}
	videoProbeSample = sample
	videoProbeSampleErr = nil
	return videoProbeSample, nil
}

// downloadVideoProbeSample performs the one-off download and inline encoding.
func downloadVideoProbeSample(ctx context.Context) (string, error) {
	source := strings.TrimSpace(common.GetEnvOrDefaultString("VIDEO_CAPABILITY_PROBE_SAMPLE_URL", videoProbeSampleURL))
	downloadCtx, cancel := context.WithTimeout(ctx, videoProbeSampleTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(downloadCtx, http.MethodGet, source, nil)
	if err != nil {
		return "", fmt.Errorf("invalid video sample url: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("failed to download video sample: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("video sample download answered %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, videoProbeSampleMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("failed to read video sample: %w", err)
	}
	if err := validateVideoProbeSample(raw); err != nil {
		return "", err
	}
	return "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// validateVideoProbeSample rejects payloads that are clearly not an MP4 so a
// proxy error page does not become the "video" every probe measures.
func validateVideoProbeSample(raw []byte) error {
	if len(raw) == 0 {
		return errors.New("video sample is empty")
	}
	if len(raw) > videoProbeSampleMaxBytes {
		return fmt.Errorf("video sample exceeds %d bytes", videoProbeSampleMaxBytes)
	}
	// ISO base media files carry an "ftyp" box within the first 64 bytes.
	header := raw
	if len(header) > 64 {
		header = header[:64]
	}
	if !strings.Contains(string(header), "ftyp") {
		return errors.New("video sample is not an mp4 container")
	}
	return nil
}

// Package-level cache for the sample bytes: one download per process, shared by
// the scheduled sweep and the admin endpoints. A failed download is remembered
// for videoProbeSampleRetryDelay rather than forever.
var (
	videoProbeSampleMu         sync.Mutex
	videoProbeSample           string
	videoProbeSampleErr        error
	videoProbeSampleRetryAfter time.Time
)

// videoProbeRequest builds the probe chat request. An empty videoPartURL yields
// the baseline arm (identical prompt, no media), so the two arms differ only by
// the media part.
func videoProbeRequest(modelName string, videoPartURL string) dto.Request {
	content := []any{map[string]any{"type": "text", "text": videoProbePrompt}}
	if videoPartURL != "" {
		content = append(content, map[string]any{
			"type":      "video_url",
			"video_url": map[string]any{"url": videoPartURL},
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

	sample, err := videoProbeSampleDataURL(ctx)
	if err != nil {
		outcome.Verdict = videoProbeVerdictInconclusive
		outcome.Error = "video sample unavailable: " + common.DebugLogPreview(err.Error())
		return outcome
	}

	pairCtx, cancel := context.WithTimeout(ctx, videoProbePairTimeout)
	defer cancel()

	baseline := testChannelWithOptionsAndRequest(pairCtx, channel, testUserID, modelName, "", false, nil, false, false, videoProbeRequest(modelName, ""))
	if baseline.usage == nil {
		outcome.Verdict = videoProbeVerdictInconclusive
		outcome.Error = "baseline probe failed: " + channelTestFailureText(baseline)
		return outcome
	}
	outcome.BaselineTokens = baseline.usage.PromptTokens

	video := testChannelWithOptionsAndRequest(pairCtx, channel, testUserID, modelName, "", false, nil, false, false, videoProbeRequest(modelName, sample))
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
