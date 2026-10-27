package openai

import (
	"io"
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
	"github.com/tidwall/gjson"
)

func TestBuildChatContinuationBody(t *testing.T) {
	base := []byte(`{"model":"kimi-k3.1-pro","stream":true,"stream_options":{"include_usage":true},"max_completion_tokens":131072,"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`)
	out, err := BuildChatContinuationBody(base, "9152、", 64000)
	require.NoError(t, err)

	messages := gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 4)
	require.Equal(t, "system", messages[0].Get("role").String())
	require.Equal(t, "user", messages[1].Get("role").String())
	require.Equal(t, "assistant", messages[2].Get("role").String())
	require.Equal(t, "9152、", messages[2].Get("content").String())
	require.Equal(t, "user", messages[3].Get("role").String())
	require.NotEmpty(t, messages[3].Get("content").String())
	require.Equal(t, int64(64000), gjson.GetBytes(out, "max_completion_tokens").Int())
	require.True(t, gjson.GetBytes(out, "stream").Bool())
	require.True(t, gjson.GetBytes(out, "stream_options.include_usage").Bool())

	// The base request must not be mutated: every continuation round rebuilds
	// from the same final upstream body snapshot.
	require.Len(t, gjson.GetBytes(base, "messages").Array(), 2)
	require.Equal(t, int64(131072), gjson.GetBytes(base, "max_completion_tokens").Int())
}

func TestBuildChatContinuationBodyHonorsMaxTokensField(t *testing.T) {
	base := []byte(`{"model":"m","stream":true,"max_tokens":131072,"messages":[{"role":"user","content":"hi"}]}`)
	out, err := BuildChatContinuationBody(base, "partial", 4096)
	require.NoError(t, err)
	require.Equal(t, int64(4096), gjson.GetBytes(out, "max_tokens").Int())
	require.False(t, gjson.GetBytes(out, "max_completion_tokens").Exists())
}

func newContinuationTestContext(t *testing.T, sender func(string, int) (*http.Response, error), maxSteps, clientMaxTokens int) (*gin.Context, *relaycommon.RelayInfo, *httptest.ResponseRecorder) {
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
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3.1-pro"},
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
		ClientMaxTokens:    clientMaxTokens,
		ChatContinuation:   &relaycommon.ChatContinuationConfig{MaxSteps: maxSteps, Send: sender},
	}
	info.SetEstimatePromptTokens(5)
	return c, info, recorder
}

func continuationSSEResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
}

// TestOaiStreamHandlerStitchesTransparentContinuation pins the transparent
// continuation contract at the handler boundary: the first round ends at an
// upstream output cap (finish_reason=length) with client budget left, a
// continuation round resumes the answer, and the client sees one continuous
// stream with exactly one terminal frame and one merged usage event.
func TestOaiStreamHandlerStitchesTransparentContinuation(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"role":"assistant","content":"1、2、"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"3、4、"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	calls := 0
	var gotPartial string
	var gotRemaining int
	c, info, recorder := newContinuationTestContext(t, func(partial string, remaining int) (*http.Response, error) {
		calls++
		gotPartial, gotRemaining = partial, remaining
		return continuationSSEResponse(secondRound), nil
	}, 2, 20)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 1, calls)
	require.Contains(t, gotPartial, "1、2、")
	require.Equal(t, 18, gotRemaining)
	require.Equal(t, 4, usage.CompletionTokens)
	require.Equal(t, 14, usage.PromptTokens)
	require.Equal(t, 18, usage.TotalTokens)

	body := recorder.Body.String()
	require.Contains(t, body, "1、2、")
	require.Contains(t, body, "3、4、")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`), "one terminal frame")
	require.Equal(t, 1, strings.Count(body, "[DONE]"), "one [DONE]")
	require.NotContains(t, body, `"finish_reason":"length"`, "the capped round's terminal frame is suppressed")
	require.Contains(t, body, `"completion_tokens":4`, "client sees the merged usage")
}

// TestOaiStreamHandlerRepeatsContinuationUntilFinish covers the multi-round
// path: two capped rounds and a final stop, still exactly one terminal frame.
func TestOaiStreamHandlerRepeatsContinuationUntilFinish(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"content":"A"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	cappedRound := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"content":"B"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	finalRound := strings.Join([]string{
		`data: {"id":"chat_3","choices":[{"index":0,"delta":{"content":"C"},"finish_reason":null}]}`,
		`data: {"id":"chat_3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	calls := 0
	c, info, recorder := newContinuationTestContext(t, func(string, int) (*http.Response, error) {
		calls++
		if calls == 1 {
			return continuationSSEResponse(cappedRound), nil
		}
		return continuationSSEResponse(finalRound), nil
	}, 2, 20)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 2, calls)
	require.Equal(t, 3, usage.CompletionTokens)

	body := recorder.Body.String()
	require.Contains(t, body, "A")
	require.Contains(t, body, "B")
	require.Contains(t, body, "C")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`))
	require.Equal(t, 1, strings.Count(body, "[DONE]"))
}

// TestOaiStreamHandlerDoesNotContinueWhenClientBudgetExhausted is the negative
// control: finish_reason=length at the client's own max_tokens is an ordinary
// truncation, not an upstream cap, and must not spend more of the provider's
// output budget.
func TestOaiStreamHandlerDoesNotContinueWhenClientBudgetExhausted(t *testing.T) {
	round := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"content":"1、2、"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	calls := 0
	c, info, recorder := newContinuationTestContext(t, func(string, int) (*http.Response, error) {
		calls++
		return continuationSSEResponse(secondContinuationRoundForTest()), nil
	}, 2, 2)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(round))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 0, calls)
	require.Equal(t, 2, usage.CompletionTokens)
	require.Contains(t, recorder.Body.String(), `"finish_reason":"length"`)
}

func secondContinuationRoundForTest() string {
	return strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"content":"3、4、"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
}
