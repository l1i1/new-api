package controller

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClassifyVideoProbe pins the verdict rules that decide whether a channel
// keeps receiving video requests for one model.
//
// The signal is the prompt-token delta between two otherwise identical arms: a
// capable upstream charges the media, a media-blind one reports the baseline
// count because the part never reached tokenisation. Only an exact zero delta
// proves blindness — an ambiguous small delta must not rewrite capability —
// and a rejected video arm proves the upstream inspected the media, so it can
// never be read as blindness.
func TestClassifyVideoProbe(t *testing.T) {
	cases := []struct {
		name      string
		baseline  int
		video     int
		status    int
		verdict   videoProbeVerdict
		rationale string
	}{
		{"media charged", 40, 7168, http.StatusOK, videoProbeVerdictCapable, "the upstream read the clip"},
		{"exact baseline", 122, 122, http.StatusOK, videoProbeVerdictBlind, "the measured 2026-10-28 signature: media dropped before tokenisation"},
		{"small delta", 122, 130, http.StatusOK, videoProbeVerdictInconclusive, "ambiguous, must not rewrite capability"},
		{"video rejected", 40, 0, http.StatusBadRequest, videoProbeVerdictInconclusive, "an explicit rejection means the media was inspected, not ignored"},
		{"video failed", 40, 0, 0, videoProbeVerdictInconclusive, "a transport failure proves nothing"},
		{"no usage", 0, 7168, http.StatusOK, videoProbeVerdictInconclusive, "a baseline without usage cannot be compared"},
		{"negative delta", 500, 40, http.StatusOK, videoProbeVerdictInconclusive, "a shrinking prompt is not evidence of capability"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.verdict, classifyVideoProbe(tc.baseline, tc.video, tc.status), tc.rationale)
		})
	}
}

// TestVideoProbeRequestArmsDifferOnlyByTheMedia pins the experiment's
// precondition: both arms carry the same model and prompt text, so the delta is
// the clip alone rather than a difference in the text.
func TestVideoProbeRequestArmsDifferOnlyByTheMedia(t *testing.T) {
	baseline := videoProbeRequest("kimi-k3.1-pro", false)
	video := videoProbeRequest("kimi-k3.1-pro", true)

	baselineRaw, err := json.Marshal(baseline)
	require.NoError(t, err)
	videoRaw, err := json.Marshal(video)
	require.NoError(t, err)

	var baselineBody, videoBody map[string]any
	require.NoError(t, json.Unmarshal(baselineRaw, &baselineBody))
	require.NoError(t, json.Unmarshal(videoRaw, &videoBody))

	assert.Equal(t, "kimi-k3.1-pro", baselineBody["model"])
	assert.Equal(t, false, baselineBody["stream"])
	assert.EqualValues(t, videoProbeMaxTokens, baselineBody["max_tokens"])

	baselineParts := probeContentParts(t, baselineBody)
	videoParts := probeContentParts(t, videoBody)
	require.Len(t, baselineParts, 1)
	require.Len(t, videoParts, 2)

	assert.Equal(t, "text", baselineParts[0]["type"])
	assert.Equal(t, "text", videoParts[0]["type"])
	assert.Equal(t, baselineParts[0]["text"], videoParts[0]["text"], "the prompt text must be identical in both arms")
	assert.Equal(t, videoProbePrompt, videoParts[0]["text"])

	assert.Equal(t, "video_url", videoParts[1]["type"])
	videoURL, ok := videoParts[1]["video_url"].(map[string]any)
	require.True(t, ok, "the video part must use the OpenAI video_url shape")
	assert.Equal(t, videoProbeSampleURL, videoURL["url"])
}

// probeContentParts extracts the first message's content parts from a marshalled
// probe request.
func probeContentParts(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	messages, ok := body["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1)
	message, ok := messages[0].(map[string]any)
	require.True(t, ok)
	parts, ok := message["content"].([]any)
	require.True(t, ok)
	typed := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		partMap, ok := part.(map[string]any)
		require.True(t, ok)
		typed = append(typed, partMap)
	}
	return typed
}

// TestChannelVideoProbeModelsSkipsNonChatModels pins the probe scope: only the
// channel's chat models are probed, because a chat-completions probe against an
// embedding or image model can only produce an inconclusive error.
func TestChannelVideoProbeModelsSkipsNonChatModels(t *testing.T) {
	channel := &model.Channel{
		Models: "kimi-k3.1-pro, kimi-k3 ,text-embedding-3-large,gpt-image-2,rerank-v3, kimi-k2.7-code",
	}
	assert.Equal(t, []string{"kimi-k3.1-pro", "kimi-k3", "kimi-k2.7-code"}, channelVideoProbeModels(channel))
}

func TestIsNonChatModelName(t *testing.T) {
	for _, name := range []string{"text-embedding-3-large", "bge-rerank", "gpt-image-2", "dall-e-3", "whisper-1", "tts-1", "stable-diffusion-xl", "flux-pro"} {
		assert.True(t, isNonChatModelName(name), name)
	}
	for _, name := range []string{"kimi-k3.1-pro", "glm-5.3-flash", "deepseek-v4.1-flash", "qwen3.8-max"} {
		assert.False(t, isNonChatModelName(name), name)
	}
}

// TestVideoProbeEnabledByEnvKeepsTheSweepDefaultOn documents the kill switch: the
// sweep is on unless VIDEO_CAPABILITY_PROBE_ENABLED=false is set, so an operator
// can stop the outbound probes without a rebuild.
func TestVideoProbeEnabledByEnvKeepsTheSweepDefaultOn(t *testing.T) {
	previous := common.GetEnvOrDefaultBool("VIDEO_CAPABILITY_PROBE_ENABLED", true)
	assert.True(t, previous)
	assert.True(t, (videoProbeHandler{}).Enabled())
	assert.Equal(t, videoProbeInterval, (videoProbeHandler{}).Interval())
	assert.Equal(t, model.SystemTaskTypeVideoCapabilityProbe, (videoProbeHandler{}).Type())
}
