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

// Review-driven: the upstream answered SSE, so IOCopyBytesGracefully would copy
// its content type verbatim; a non-streaming client must still see JSON.
func TestOaiChatBufferedStreamHandlerSendsJSONContentType(t *testing.T) {
	ctx, recorder, info := bufferedStreamContext(t)
	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	_, err := OaiChatBufferedStreamHandler(ctx, info, sseResponse(sse))
	require.Nil(t, err)
	require.Contains(t, recorder.Header().Get("Content-Type"), "application/json",
		"the client asked for JSON and must not inherit text/event-stream")
}

// Review-driven: the caller dereferences the usage pointer, so a successful
// buffered answer must never hand back nil.
func TestOaiChatBufferedStreamHandlerNeverReturnsNilUsage(t *testing.T) {
	ctx, _, info := bufferedStreamContext(t)
	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	usage, err := OaiChatBufferedStreamHandler(ctx, info, sseResponse(sse))
	require.Nil(t, err)
	require.NotNil(t, usage, "a nil usage would be dereferenced by the caller")
}

// Review-driven: [DONE] with no chunks is a legitimate empty answer and must come
// back as a well-formed shape, not an empty choices array.
func TestOaiChatBufferedStreamHandlerAnswersEmptyCompletionWithOneChoice(t *testing.T) {
	ctx, recorder, info := bufferedStreamContext(t)
	_, err := OaiChatBufferedStreamHandler(ctx, info, sseResponse("data: [DONE]\n\n"))
	require.Nil(t, err, "an empty completion signalled with [DONE] is not a failure")
	body := recorder.Body.String()
	require.Contains(t, body, `"choices":[{"index":0`)
	require.Contains(t, body, `"role":"assistant"`)
}

// Review-driven: with no [DONE] and no choices at all the stream was cut short.
func TestOaiChatBufferedStreamHandlerRejectsNoChoiceStreamWithoutDone(t *testing.T) {
	ctx, recorder, info := bufferedStreamContext(t)
	_, err := OaiChatBufferedStreamHandler(ctx, info, sseResponse(": keep-alive\n\n"))
	require.NotNil(t, err)
	require.Zero(t, recorder.Body.Len())
}

// Review-driven: without [DONE], an unfinished second choice means the answer is
// incomplete even though the first choice finished.
func TestOaiChatBufferedStreamHandlerRejectsPartiallyFinishedChoices(t *testing.T) {
	ctx, recorder, info := bufferedStreamContext(t)
	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[{\"index\":1,\"delta\":{\"content\":\"b\"}}]}\n\n"
	_, err := OaiChatBufferedStreamHandler(ctx, info, sseResponse(sse))
	require.NotNil(t, err, "choice 1 never terminated, so the stream was cut short")
	require.Zero(t, recorder.Body.Len())
}

// A single choice that terminated is a complete answer even without [DONE]:
// some compatible upstreams close right after the terminal chunk.
func TestOaiChatBufferedStreamHandlerAcceptsSingleFinishedChoiceWithoutDone(t *testing.T) {
	ctx, recorder, info := bufferedStreamContext(t)
	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"},\"finish_reason\":\"stop\"}]}\n\n"
	_, err := OaiChatBufferedStreamHandler(ctx, info, sseResponse(sse))
	require.Nil(t, err)
	require.Contains(t, recorder.Body.String(), `"content":"a"`)
}

// Review-driven: system_fingerprint must survive into the buffered body.
func TestOaiChatBufferedStreamHandlerKeepsSystemFingerprint(t *testing.T) {
	ctx, recorder, info := bufferedStreamContext(t)
	sse := "data: {\"system_fingerprint\":\"fp_zzz\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	_, err := OaiChatBufferedStreamHandler(ctx, info, sseResponse(sse))
	require.Nil(t, err)
	require.Contains(t, recorder.Body.String(), `"system_fingerprint":"fp_zzz"`)
}

// Second-review-driven: [DONE] alone must not excuse an unterminated choice. An
// upstream can close the stream early, and a half-finished answer behind a 200
// would hide exactly the channel failure this mode exists to make retryable.
func TestOaiChatBufferedStreamHandlerRejectsUnfinishedChoiceEvenWithDone(t *testing.T) {
	ctx, recorder, info := bufferedStreamContext(t)
	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"}}]}\n\ndata: [DONE]\n\n"
	_, err := OaiChatBufferedStreamHandler(ctx, info, sseResponse(sse))
	require.NotNil(t, err, "[DONE] with an unfinished choice is still an incomplete answer")
	require.Zero(t, recorder.Body.Len())
}

// A buffered answer that the upstream cut mid-stream must be resumed inside the
// same body instead of failing the caller: this is the non-streaming face of the
// 900s provider wall.
func TestOaiChatBufferedStreamHandlerContinuesCutAnswer(t *testing.T) {
	ctx, recorder, info := bufferedStreamContext(t)
	sends := 0
	info.ChatContinuation = &relaycommon.ChatContinuationConfig{
		MaxSteps: 2,
		Send: func(partial string, remaining int) (*http.Response, error) {
			sends++
			require.Equal(t, "你好", partial, "the resumed round must replay the visible prefix")
			require.Positive(t, remaining)
			return sseResponse("data: {\"id\":\"chatcmpl-9\",\"object\":\"chat.completion.chunk\",\"created\":1700000002,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"世界\"},\"finish_reason\":null}]}\n\n" +
				"data: {\"id\":\"chatcmpl-9\",\"object\":\"chat.completion.chunk\",\"created\":1700000002,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" +
				"data: [DONE]\n\n"), nil
		},
	}
	cut := "data: {\"id\":\"chatcmpl-9\",\"object\":\"chat.completion.chunk\",\"created\":1700000001,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"你\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-9\",\"object\":\"chat.completion.chunk\",\"created\":1700000001,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"好\"}}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n"

	usage, err := OaiChatBufferedStreamHandler(ctx, info, sseResponse(cut))
	require.Nil(t, err, "a repairable cut must not surface an error")
	require.Equal(t, 1, sends)
	body := recorder.Body.String()
	require.Contains(t, body, `"content":"你好世界"`, "the resumed tail must join the cut prefix")
	require.Contains(t, body, `"finish_reason":"stop"`)
	require.Equal(t, 7, usage.TotalTokens, "input is billed once, output across rounds")
}

// A round that died inside thinking has no prefix to replay: the captured
// reasoning goes back as the answer-mode prompt and its text becomes the body.
func TestOaiChatBufferedStreamHandlerContinuesThinkingOnlyCut(t *testing.T) {
	ctx, recorder, info := bufferedStreamContext(t)
	sends := 0
	info.ChatContinuation = &relaycommon.ChatContinuationConfig{
		MaxSteps: 0,
		Abnormal: &relaycommon.ChatAbnormalContinuationConfig{
			MaxSteps:          1,
			MinReasoningChars: 8,
			Send: func(reasoning string, remaining int) (*http.Response, error) {
				sends++
				require.Contains(t, reasoning, "thinking")
				return sseResponse("data: {\"id\":\"chatcmpl-9\",\"object\":\"chat.completion.chunk\",\"created\":1700000002,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"答案\"},\"finish_reason\":null}]}\n\n" +
					"data: {\"id\":\"chatcmpl-9\",\"object\":\"chat.completion.chunk\",\"created\":1700000002,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
					"data: [DONE]\n\n"), nil
			},
		},
	}
	thinkingOnly := "data: {\"id\":\"chatcmpl-9\",\"object\":\"chat.completion.chunk\",\"created\":1700000001,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"thinking about it\"}}]}\n\n"

	_, err := OaiChatBufferedStreamHandler(ctx, info, sseResponse(thinkingOnly))
	require.Nil(t, err, "a thinking-only cut must be answered from the captured trace")
	require.Equal(t, 1, sends)
	require.Contains(t, recorder.Body.String(), `"content":"答案"`)
}

// Without a continuation sender the cut stays an error, so the caller can retry.
func TestOaiChatBufferedStreamHandlerKeepsCutAsErrorWithoutContinuation(t *testing.T) {
	ctx, _, info := bufferedStreamContext(t)
	cut := "data: {\"id\":\"chatcmpl-9\",\"object\":\"chat.completion.chunk\",\"created\":1700000001,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"半\"}}]}\n\n"
	_, err := OaiChatBufferedStreamHandler(ctx, info, sseResponse(cut))
	require.NotNil(t, err)
	require.False(t, types.IsSkipRetryError(err), "a short cut is still retryable")
}
