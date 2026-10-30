package helper

import (
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The official K3 chat endpoint accepts a video only as `video_url` carrying an
// inline data URI. Measured 2026-10-30: a `video_base64` part answers 400
// "Invalid request: the message at position 0 with role 'user' contains an
// invalid part type: video_base64", and a bare base64 value in `video_url`
// answers 400 "Invalid request: unsupported video url: <value>". The local
// check mirrors both so the verdict never depends on which channel is picked.
func TestValidateKimiK3VideoSources(t *testing.T) {
	requestWith := func(parts ...any) *dto.GeneralOpenAIRequest {
		return &dto.GeneralOpenAIRequest{
			Model:    "kimi-k3.1-pro",
			Messages: []dto.Message{{Role: "user", Content: parts}},
		}
	}
	const dataURI = "data:video/mp4;base64,AAAAIGZ0eXBpc29t"

	t.Run("inline data URI passes", func(t *testing.T) {
		err := validateKimiK3VideoSources(requestWith(
			map[string]any{"type": "video_url", "video_url": map[string]any{"url": dataURI}},
			map[string]any{"type": "text", "text": "描述视频"},
		))
		assert.NoError(t, err)
	})

	t.Run("bare base64 in video_url is rejected with the official text", func(t *testing.T) {
		err := validateKimiK3VideoSources(requestWith(
			map[string]any{"type": "video_url", "video_url": map[string]any{"url": "AAAAIGZ0eXBpc29t"}},
		))
		require.Error(t, err)
		newAPIErr, ok := err.(*types.NewAPIError)
		require.True(t, ok)
		assert.Equal(t, http.StatusBadRequest, newAPIErr.StatusCode)
		assert.Contains(t, err.Error(), "unsupported video url")
	})

	t.Run("string spelling of video_url is covered too", func(t *testing.T) {
		err := validateKimiK3VideoSources(requestWith(
			map[string]any{"type": "video_url", "video_url": "AAAAIGZ0eXBpc29t"},
		))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported video url")
	})

	t.Run("video_base64 part type is rejected as an invalid part type", func(t *testing.T) {
		err := validateKimiK3VideoSources(requestWith(
			map[string]any{"type": "video_base64", "video_base64": "AAAAIGZ0eXBpc29t"},
		))
		require.Error(t, err)
		msg := err.Error()
		assert.True(t, strings.Contains(msg, "the message at position 0 with role 'user'"), msg)
		assert.True(t, strings.Contains(msg, "invalid part type: video_base64"), msg)
	})

	t.Run("text-only request passes", func(t *testing.T) {
		assert.NoError(t, validateKimiK3VideoSources(requestWith(map[string]any{"type": "text", "text": "hi"})))
		assert.NoError(t, validateKimiK3VideoSources(&dto.GeneralOpenAIRequest{Model: "kimi-k3.1-pro"}))
	})
}
