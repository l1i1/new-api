package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSupportsVideoForModel pins the model-level capability rule: the
// channel-wide declaration is necessary, and a listed model is excluded from it
// case-insensitively. This is the shape a channel needs when its upstream reads
// video for one model but answers from text alone for another.
func TestSupportsVideoForModel(t *testing.T) {
	var nilSettings *ChannelOtherSettings
	assert.False(t, nilSettings.SupportsVideoForModel("kimi-k3"), "nil settings cannot be video capable")

	undeclared := &ChannelOtherSettings{}
	assert.False(t, undeclared.SupportsVideoForModel("kimi-k3"))

	capable := &ChannelOtherSettings{SupportsVideo: true}
	assert.True(t, capable.SupportsVideoForModel("kimi-k3"))
	assert.True(t, capable.SupportsVideoForModel(""), "an unknown model keeps the channel-wide answer")

	withException := &ChannelOtherSettings{
		SupportsVideo:          true,
		VideoUnsupportedModels: []string{"kimi-k3.1-pro"},
	}
	assert.True(t, withException.SupportsVideoForModel("kimi-k3"))
	assert.True(t, withException.SupportsVideoForModel("glm-5.3-flash"))
	assert.False(t, withException.SupportsVideoForModel("kimi-k3.1-pro"))
	assert.False(t, withException.SupportsVideoForModel("Kimi-K3.1-Pro"), "matching must be case-insensitive")
	assert.False(t, withException.SupportsVideoForModel("  kimi-k3.1-pro  "), "matching must trim the request model")

	// The exception never overrides the channel-wide declaration: an undeclared
	// channel stays incapable for every model.
	mixed := &ChannelOtherSettings{VideoUnsupportedModels: []string{"kimi-k3.1-pro"}}
	assert.False(t, mixed.SupportsVideoForModel("kimi-k3"))
}

// TestNormalizeVideoUnsupportedModels covers the canonical form written by the
// capability marker: no empty entries, no case duplicates, first spelling kept.
func TestNormalizeVideoUnsupportedModels(t *testing.T) {
	assert.Nil(t, NormalizeVideoUnsupportedModels(nil))
	assert.Nil(t, NormalizeVideoUnsupportedModels([]string{"", "   "}))
	assert.Equal(t,
		[]string{"kimi-k3.1-pro", "glm-5.3-flash"},
		NormalizeVideoUnsupportedModels([]string{" kimi-k3.1-pro ", "", "Kimi-K3.1-Pro", "glm-5.3-flash", "  "}),
	)
}

// TestValidateVideoUnsupportedModels pins the save-time rejections so a typo
// cannot leave a channel that silently keeps routing video to a blind upstream,
// and so an exception without the declaration it belongs to is refused.
func TestValidateVideoUnsupportedModels(t *testing.T) {
	require.NoError(t, (&ChannelOtherSettings{}).ValidateVideoUnsupportedModels())
	require.NoError(t, (&ChannelOtherSettings{SupportsVideo: true, VideoUnsupportedModels: []string{"kimi-k3.1-pro"}}).ValidateVideoUnsupportedModels())

	err := (&ChannelOtherSettings{VideoUnsupportedModels: []string{"kimi-k3.1-pro"}}).ValidateVideoUnsupportedModels()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires supports_video")

	err = (&ChannelOtherSettings{SupportsVideo: true, VideoUnsupportedModels: []string{""}}).ValidateVideoUnsupportedModels()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty model name")

	err = (&ChannelOtherSettings{SupportsVideo: true, VideoUnsupportedModels: []string{"kimi-k3.1-pro", "Kimi-K3.1-Pro"}}).ValidateVideoUnsupportedModels()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")

	tooMany := make([]string, 0, MaxVideoUnsupportedModels+1)
	for i := 0; i <= MaxVideoUnsupportedModels; i++ {
		tooMany = append(tooMany, "model-"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	err = (&ChannelOtherSettings{SupportsVideo: true, VideoUnsupportedModels: tooMany}).ValidateVideoUnsupportedModels()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "limit is")
}

// TestVideoUnsupportedModelsSerialization keeps the exclusion list on the wire
// only when it is set, so untouched channels keep the exact settings JSON they
// had before this field existed (the config surface diffs cleanly and a channel
// with no exceptions is byte-identical).
func TestVideoUnsupportedModelsSerialization(t *testing.T) {
	encoded, err := json.Marshal(&ChannelOtherSettings{SupportsVideo: true})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "video_unsupported_models")

	encoded, err = json.Marshal(&ChannelOtherSettings{SupportsVideo: true, VideoUnsupportedModels: []string{"kimi-k3.1-pro"}})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"video_unsupported_models":["kimi-k3.1-pro"]`)

	var decoded ChannelOtherSettings
	require.NoError(t, json.Unmarshal([]byte(`{"supports_video":true,"video_unsupported_models":["Kimi-K3.1-Pro"]}`), &decoded))
	assert.True(t, decoded.SupportsVideo)
	assert.False(t, decoded.SupportsVideoForModel("kimi-k3.1-pro"))
}
