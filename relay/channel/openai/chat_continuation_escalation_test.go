package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// newEscalationTestContext builds a RelayInfo with BOTH repair layers wired: the
// abnormal reasoning resume (which cuts after delivering text) and the content
// continuation (which replays the delivered prefix and can hop).
func newEscalationTestContext(t *testing.T, reasoningSender, contentSender func(string, int) (*http.Response, error)) (*gin.Context, *relaycommon.RelayInfo, *httptest.ResponseRecorder) {
	t.Helper()
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("token_id", 1)
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3.1-pro"},
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
		ClientMaxTokens:    100,
		ChatContinuation: &relaycommon.ChatContinuationConfig{
			MaxSteps:  2,
			MaxRounds: 2,
			Send:      contentSender,
			Abnormal: &relaycommon.ChatAbnormalContinuationConfig{
				MaxSteps:          1,
				Send:              reasoningSender,
				MaxReasoningBytes: 1 << 20,
				MinReasoningChars: 1,
			},
		},
	}
	info.SetEstimatePromptTokens(5)
	return c, info, recorder
}

// TestOaiStreamHandlerEscalatesReasoningCutToContent pins the 2026-10-30 shape: the
// abnormal reasoning repair delivers a partial answer and then cuts. The reasoning
// resume cannot hop again, but the content continuation can replay the prefix and
// finish the answer, so the caller must get one complete answer, not a 502.
func TestOaiStreamHandlerEscalatesReasoningCutToContent(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	// The reasoning repair delivers visible text and then dies on an abnormal finish.
	reasoningRepair := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"content":"半句答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	contentRepair := strings.Join([]string{
		`data: {"id":"chat_3","choices":[{"index":0,"delta":{"content":"，完整答案。"},"finish_reason":null}]}`,
		`data: {"id":"chat_3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	var contentPrefix string
	c, info, recorder := newEscalationTestContext(t,
		func(string, int) (*http.Response, error) { return continuationSSEResponse(reasoningRepair), nil },
		func(prefix string, _ int) (*http.Response, error) {
			contentPrefix = prefix
			return continuationSSEResponse(contentRepair), nil
		})

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	body := recorder.Body.String()
	require.Nil(t, err, "the escalated repair must finish the answer instead of failing: %s", body)
	require.NotNil(t, usage)
	require.Equal(t, "半句答案", contentPrefix, "the content hand-off must replay exactly the delivered prefix")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`), "one terminal frame")
	require.Equal(t, 1, strings.Count(body, "半句答案"), "the caller must not read the delivered prefix twice")
	require.Contains(t, body, "完整答案")
	require.NotContains(t, body, "truncated_stream")
	require.Greater(t, usage.CompletionTokens, 0, "a resumed answer is billed on the merged usage")
}

// TestOaiStreamHandlerEscalationFailureStaysHonest pins the guard: when the content
// hand-off also dies, the caller still gets the honest truncated-stream 502 and the
// usage stays zeroed (nothing was billed for the repair rounds).
func TestOaiStreamHandlerEscalationFailureStaysHonest(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	reasoningRepair := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"content":"半句答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	contentRepairCut := strings.Join([]string{
		`data: {"id":"chat_3","choices":[{"index":0,"delta":{"content":"又断了"},"finish_reason":null}]}`,
		`data: {"id":"chat_3","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, info, recorder := newEscalationTestContext(t,
		func(string, int) (*http.Response, error) { return continuationSSEResponse(reasoningRepair), nil },
		func(string, int) (*http.Response, error) { return continuationSSEResponse(contentRepairCut), nil })

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	body := recorder.Body.String()
	require.NotNil(t, err, "a failed hand-off keeps the honest failure")
	require.Equal(t, http.StatusBadGateway, err.StatusCode)
	require.NotNil(t, usage)
	require.Zero(t, usage.CompletionTokens, "an undelivered repair must not bill completion tokens")
	require.NotContains(t, body, `"finish_reason":"stop"`, "a failed hand-off must not fake a terminal frame")
}

// TestOaiStreamHandlerEscalationKeepsToolCallAnswer pins the blocking shape the
// code comments call dominant (66/68 visible-cut resumes end in a tool call): the
// hand-off round finishes the answer by emitting a complete tool-call set and then
// loses its terminal frame. That result is usable only when toolCallsDelivered
// survives the copy, or the caller would 502 after the tool_calls event already
// reached the client.
func TestOaiStreamHandlerEscalationKeepsToolCallAnswer(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	reasoningRepair := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"content":"半句答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	toolRepair := strings.Join([]string{
		`data: {"id":"chat_3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expr\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"1+1\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_3","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, info, recorder := newEscalationTestContext(t,
		func(string, int) (*http.Response, error) { return continuationSSEResponse(reasoningRepair), nil },
		func(string, int) (*http.Response, error) { return continuationSSEResponse(toolRepair), nil })

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	body := recorder.Body.String()
	require.Nil(t, err, "a delivered tool-call set must not surface as a 502: %s", body)
	require.NotNil(t, usage)
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"tool_calls"`))
	require.Equal(t, 1, strings.Count(body, `"delta":{"tool_calls"`), "the call set must be delivered once")
	require.NotContains(t, body, "truncated_stream")
}
