package relay

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
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
