package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func bufferedStreamContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "kimi-k3",
		// UpstreamModelName is promoted from the embedded ChannelMeta, so it can
		// only be set through the embedded struct in a composite literal.
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3"},
	}
	return ctx, recorder, info
}

func sseResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// The success path: a full upstream stream becomes one non-streaming JSON body.
func TestOaiChatBufferedStreamHandlerWritesSingleJSONBody(t *testing.T) {
	ctx, recorder, info := bufferedStreamContext(t)
	sse := "data: {\"id\":\"chatcmpl-9\",\"object\":\"chat.completion.chunk\",\"created\":1700000001,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"你\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-9\",\"object\":\"chat.completion.chunk\",\"created\":1700000001,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"好\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-9\",\"object\":\"chat.completion.chunk\",\"created\":1700000001,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" +
		"data: [DONE]\n\n"

	usage, err := OaiChatBufferedStreamHandler(ctx, info, sseResponse(sse))
	require.Nil(t, err, "a complete stream must not error")
	require.NotNil(t, usage)
	require.Equal(t, 5, usage.TotalTokens)

	body := recorder.Body.String()
	require.Contains(t, body, `"object":"chat.completion"`, "client must receive a non-streaming body")
	require.NotContains(t, body, "chat.completion.chunk")
	require.NotContains(t, body, "data:", "no SSE framing may reach a non-streaming client")
	require.Contains(t, body, `"content":"你好"`)
}

// A stream that stops without [DONE] and without a finish_reason is truncated.
// Answering with the partial body would hide a channel failure behind a 200, so
// the handler must fail - uncommitted, which is what lets the retry loop fail over.
func TestOaiChatBufferedStreamHandlerRejectsTruncatedStream(t *testing.T) {
	ctx, recorder, info := bufferedStreamContext(t)
	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"半\"}}]}\n\n"

	_, err := OaiChatBufferedStreamHandler(ctx, info, sseResponse(sse))
	require.NotNil(t, err, "a truncated stream must be an error, not a partial 200")
	require.Zero(t, recorder.Body.Len(), "nothing may be written when the stream is truncated")
}

// An error payload carried inside the stream is the request's failure.
func TestOaiChatBufferedStreamHandlerSurfacesInStreamError(t *testing.T) {
	ctx, _, info := bufferedStreamContext(t)
	sse := "data: {\"error\":{\"message\":\"upstream exploded\",\"type\":\"server_error\"}}\n\n"

	_, err := OaiChatBufferedStreamHandler(ctx, info, sseResponse(sse))
	require.NotNil(t, err)
	require.Contains(t, err.Error(), "upstream exploded")
}

// A usage-only terminal chunk (include_usage) still counts as a complete answer
// when a finish_reason was seen earlier; and the choice-level usage K3 sends is
// picked up when the top level carries none.
func TestOaiChatBufferedStreamHandlerReadsChoiceLevelUsage(t *testing.T) {
	ctx, recorder, info := bufferedStreamContext(t)
	sse := "data: {\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\",\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":4,\"total_tokens\":11}}]}\n\n" +
		"data: [DONE]\n\n"

	usage, err := OaiChatBufferedStreamHandler(ctx, info, sseResponse(sse))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 11, usage.TotalTokens, "choice-level usage must survive the buffering")
	require.Contains(t, recorder.Body.String(), `"total_tokens":11`)
}
