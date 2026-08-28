package openai

import (
	"encoding/json"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func maskedInfo(origin, upstream string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		OriginModelName: origin,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: upstream},
	}
}

func TestMaskUpstreamModelNameDisabledByDefault(t *testing.T) {
	// Global setting default is false: no rewrite even when names differ.
	body := []byte(`{"model":"@cf/deepseek-ai/deepseek-v4-flash-0731","choices":[]}`)
	got, ok := maskModelNameInResponse(body, maskedInfo("deepseek-v4-flash", "@cf/deepseek-ai/deepseek-v4-flash-0731"))
	assert.False(t, ok)
	assert.Equal(t, body, got)
}

func TestMaskUpstreamModelNameRewritesModel(t *testing.T) {
	model_setting.GetGlobalSettings().MaskUpstreamModelName = true
	defer func() { model_setting.GetGlobalSettings().MaskUpstreamModelName = false }()

	body := []byte(`{"model":"@cf/deepseek-ai/deepseek-v4-flash-0731","usage":{"prompt_tokens":1}}`)
	got, ok := maskModelNameInResponse(body, maskedInfo("deepseek-v4-flash", "@cf/deepseek-ai/deepseek-v4-flash-0731"))
	require.True(t, ok)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(got, &payload))
	assert.Equal(t, "deepseek-v4-flash", payload["model"])
	// Byte-level rewrite keeps everything else intact.
	assert.Contains(t, string(got), `"usage":{"prompt_tokens":1}`)
}

func TestMaskUpstreamModelNameSkipsSameName(t *testing.T) {
	model_setting.GetGlobalSettings().MaskUpstreamModelName = true
	defer func() { model_setting.GetGlobalSettings().MaskUpstreamModelName = false }()

	body := []byte(`{"model":"deepseek-v4-flash","choices":[]}`)
	got, ok := maskModelNameInResponse(body, maskedInfo("deepseek-v4-flash", "deepseek-v4-flash"))
	assert.False(t, ok)
	assert.Equal(t, body, got)
}

func TestMaskUpstreamModelNameStreamData(t *testing.T) {
	model_setting.GetGlobalSettings().MaskUpstreamModelName = true
	defer func() { model_setting.GetGlobalSettings().MaskUpstreamModelName = false }()

	chunk := `{"id":"chatcmpl-x","object":"chat.completion.chunk","model":"@cf/deepseek-ai/deepseek-v4-flash-0731","choices":[{"delta":{"content":"hi"}}]}`
	got := maskModelNameInStreamData(chunk, maskedInfo("deepseek-v4-flash", "@cf/deepseek-ai/deepseek-v4-flash-0731"))
	assert.Contains(t, got, `"model":"deepseek-v4-flash"`)
	assert.NotContains(t, got, "@cf/deepseek-ai")
}
