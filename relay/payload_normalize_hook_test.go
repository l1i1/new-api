package relay

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/pkg/payloadnorm"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// payloadNormalizeFixture builds the customer shape the rewrite targets: one short system
// instruction plus one user message that carries a long document followed by a varying tail.
func payloadNormalizeFixture() (systemPrompt, tail, document string, request *dto.GeneralOpenAIRequest) {
	systemPrompt = "你是一个专业的文档助手，总结文章并输出32token左右的内容。"
	tail = "RAG/工作流真实对话场景"
	document = strings.Repeat("The lake remembers every winter it has seen. ", 200)

	systemMessage := dto.Message{Role: "system"}
	systemMessage.SetStringContent(systemPrompt)
	userMessage := dto.Message{Role: "user"}
	userMessage.SetStringContent(document + tail)
	return systemPrompt, tail, document, &dto.GeneralOpenAIRequest{
		Model:    "glm-5.3-flash",
		Messages: []dto.Message{systemMessage, userMessage},
	}
}

func payloadNormalizeContext(t *testing.T) *gin.Context {
	t.Helper()
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return ctx
}

// TestNormalizePayloadForUpstreamAppliesInstalledScope is the wiring contract for the
// channel-agnostic hook: a scoped token+channel gets the cache-friendly layout, every other
// caller keeps the client's own layout byte for byte.
func TestNormalizePayloadForUpstreamAppliesInstalledScope(t *testing.T) {
	systemPrompt, tail, document, request := payloadNormalizeFixture()
	require.NoError(t, payloadnorm.InstallSettingJSON(
		`{"enabled":true,"token_ids":[88],"channel_ids":[7],"min_user_chars":1000,"document_chars":`+strconv.Itoa(len(document))+`}`))
	t.Cleanup(payloadnorm.InstallDisabled)
	ctx := payloadNormalizeContext(t)

	scoped := normalizePayloadForUpstream(ctx, &relaycommon.RelayInfo{
		TokenId:     88,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 7},
	}, request)
	require.Len(t, scoped.Messages, 2)
	require.Equal(t, systemPrompt+"\n\n"+document, scoped.Messages[0].Content)
	require.Equal(t, tail, scoped.Messages[1].Content)

	// The input request itself must stay untouched: the client's own layout is what the
	// request log and the capture pipeline report.
	require.Len(t, request.Messages, 2)
	require.Equal(t, systemPrompt, request.Messages[0].Content)
	require.Equal(t, document+tail, request.Messages[1].Content)

	unscoped := normalizePayloadForUpstream(ctx, &relaycommon.RelayInfo{
		TokenId:     99,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 7},
	}, request)
	require.Len(t, unscoped.Messages, 2)
	require.Equal(t, systemPrompt, unscoped.Messages[0].Content)
	require.Equal(t, document+tail, unscoped.Messages[1].Content)
}

// TestNormalizePayloadForUpstreamIsChannelAgnostic is the property this hook was moved for:
// the rewrite does not depend on the channel being the official one — any scoped channel id
// gets the same body, so routing to an aggregator cannot silently drop the cache benefit.
func TestNormalizePayloadForUpstreamIsChannelAgnostic(t *testing.T) {
	_, tail, document, request := payloadNormalizeFixture()
	require.NoError(t, payloadnorm.InstallSettingJSON(
		`{"enabled":true,"token_ids":[88],"min_user_chars":1000,"document_chars":`+strconv.Itoa(len(document))+`}`))
	t.Cleanup(payloadnorm.InstallDisabled)
	ctx := payloadNormalizeContext(t)

	for _, channelID := range []int{7, 78, 79, 1024} {
		out := normalizePayloadForUpstream(ctx, &relaycommon.RelayInfo{
			TokenId:     88,
			ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID},
		}, request)
		require.Len(t, out.Messages, 2, "channel %d", channelID)
		systemContent, ok := out.Messages[0].Content.(string)
		require.True(t, ok, "channel %d: system content is not a string", channelID)
		require.True(t, strings.HasSuffix(systemContent, document), "channel %d must carry the document in the system message", channelID)
		require.Equal(t, tail, out.Messages[1].Content, "channel %d", channelID)
	}
}

// TestNormalizePayloadForUpstreamIsANoOpWithoutScope covers the disabled option and the
// defensive nil guard: neither may panic, and neither may rewrite the request.
func TestNormalizePayloadForUpstreamIsANoOpWithoutScope(t *testing.T) {
	systemPrompt, tail, document, request := payloadNormalizeFixture()
	ctx := payloadNormalizeContext(t)

	payloadnorm.InstallDisabled()
	unchanged := normalizePayloadForUpstream(ctx, &relaycommon.RelayInfo{
		TokenId:     88,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 7},
	}, request)
	require.Equal(t, systemPrompt, unchanged.Messages[0].Content)

	noChannelMeta := normalizePayloadForUpstream(ctx, &relaycommon.RelayInfo{TokenId: 88}, request)
	require.Equal(t, document+tail, noChannelMeta.Messages[1].Content)

	nilRequest := normalizePayloadForUpstream(ctx, &relaycommon.RelayInfo{
		TokenId:     88,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 7},
	}, nil)
	require.Nil(t, nilRequest)
}
