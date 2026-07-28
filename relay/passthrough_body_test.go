package relay

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/gemini"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGetPassThroughRequestBodyPreservesBytesAndSize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	expected := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"stable prefix"}]}`)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(expected))
	ctx.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{OriginModelName: "deepseek-v4-flash"}
	t.Cleanup(func() { common.CleanupBodyStorage(ctx) })

	body, err := getPassThroughRequestBody(ctx, info)
	require.NoError(t, err)
	actual, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, expected, actual)
	require.Equal(t, int64(len(expected)), info.UpstreamRequestBodySize)
}

func TestShouldPassThroughClaudeRequestBodyRejectsOpenAICrossProtocol(t *testing.T) {
	channelSetting := dto.ChannelSettings{PassThroughBodyEnabled: true}

	require.False(t, shouldPassThroughClaudeRequestBody(&relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiType:        constant.APITypeOpenAI,
			ChannelSetting: channelSetting,
		},
	}))
	require.True(t, shouldPassThroughClaudeRequestBody(&relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiType:        constant.APITypeAnthropic,
			ChannelSetting: channelSetting,
		},
	}))
}

func TestShouldPassThroughOpenAIRequestBodyRequiresOpenAIUpstream(t *testing.T) {
	original := model_setting.GetGlobalSettings().PassThroughRequestEnabled
	model_setting.GetGlobalSettings().PassThroughRequestEnabled = true
	t.Cleanup(func() {
		model_setting.GetGlobalSettings().PassThroughRequestEnabled = original
	})

	require.True(t, shouldPassThroughOpenAIRequestBody(&relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ApiType: constant.APITypeOpenAI},
	}))
	require.False(t, shouldPassThroughOpenAIRequestBody(&relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ApiType: constant.APITypeGemini},
	}))
	require.False(t, shouldPassThroughOpenAIRequestBody(&relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiType:        constant.APITypeVertexAi,
			ChannelSetting: dto.ChannelSettings{PassThroughBodyEnabled: true},
		},
	}))
}

func TestGeminiOpenAIRequestStillConvertsWhenPassThroughIsEnabled(t *testing.T) {
	original := model_setting.GetGlobalSettings().PassThroughRequestEnabled
	model_setting.GetGlobalSettings().PassThroughRequestEnabled = true
	t.Cleanup(func() {
		model_setting.GetGlobalSettings().PassThroughRequestEnabled = original
	})

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ApiType: constant.APITypeGemini},
	}
	require.False(t, shouldPassThroughOpenAIRequestBody(info))
	converted, err := (&gemini.Adaptor{}).ConvertOpenAIRequest(nil, info, &dto.GeneralOpenAIRequest{
		Model: "gemini-3.1-pro",
		Messages: []dto.Message{
			{Role: "system", Content: "Follow the user instruction exactly."},
			{Role: "user", Content: "Reply with TOKENESS_GEMINI_CHAT_OK."},
		},
	})
	require.NoError(t, err)
	body, err := common.Marshal(converted)
	require.NoError(t, err)
	var outbound map[string]any
	require.NoError(t, json.Unmarshal(body, &outbound))
	require.NotContains(t, outbound, "messages")
	require.Contains(t, outbound, "contents")
	require.Contains(t, outbound, "systemInstruction")
}
