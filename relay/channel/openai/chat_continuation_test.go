package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
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
	require.Equal(t, 5, usage.PromptTokens, "input is billed once, not per continuation round")
	require.Equal(t, 9, usage.TotalTokens)
	// A successful continuation is an ordinary delivered response: settlement
	// bills the merged usage, so no undelivered marker may be set.
	require.Empty(t, info.UndeliveredReason)
	require.True(t, info.StreamStatus.IsNormalEnd())

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
	require.Equal(t, 5, usage.PromptTokens, "input is billed once across capped rounds")
	require.Equal(t, 8, usage.TotalTokens)

	body := recorder.Body.String()
	require.Contains(t, body, "A")
	require.Contains(t, body, "B")
	require.Contains(t, body, "C")
	require.Contains(t, body, `"id":"chat_1"`)
	require.NotContains(t, body, `"id":"chat_2"`, "capped rounds keep the first round's id")
	require.NotContains(t, body, `"id":"chat_3"`)
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

// TestOaiStreamHandlerKeepsBaseUsageBillableWhenCallerLeaves pins the billing
// interaction: a caller-side disconnect during a continuation round is not an
// upstream fault. The write to the gone caller fails, so the continuation round
// stops before its usage event is read; the first round's observed usage stays
// billable (the same rule as client_gone on a normal stream) and no undelivered
// marker is recorded.
func TestOaiStreamHandlerKeepsBaseUsageBillableWhenCallerLeaves(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"content":"1、2、"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"content":"3、4、"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c, info, _ := newContinuationTestContext(t, func(string, int) (*http.Response, error) {
		cancel() // the caller walks away while the continuation round is running
		return continuationSSEResponse(secondRound), nil
	}, 2, 20)
	c.Request = c.Request.WithContext(ctx)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err, "a caller-side continuation disconnect is not an upstream failure")
	require.NotNil(t, usage)
	require.Equal(t, 7, usage.TotalTokens, "the first round's observed usage stays billable")
	require.Empty(t, info.UndeliveredReason, "client_gone is caller-side, not an undelivered upstream result")
}

func TestBuildChatThinkingContinuationBody(t *testing.T) {
	base := []byte(`{"model":"kimi-k3.1-pro","stream":true,"stream_options":{"include_usage":true},"max_tokens":98304,"messages":[{"role":"user","content":"solve"}]}`)
	out, err := BuildChatThinkingContinuationBody(base, "已经想到第 81 步", 90000)
	require.NoError(t, err)

	messages := gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 3)
	require.Equal(t, "user", messages[0].Get("role").String())
	require.Equal(t, "assistant", messages[1].Get("role").String())
	require.Equal(t, "已经想到第 81 步", messages[1].Get("content").String())
	require.NotContains(t, messages[1].Raw, "reasoning_content")
	require.Equal(t, "user", messages[2].Get("role").String())
	require.Contains(t, messages[2].Get("content").String(), "继续思考")
	require.Equal(t, int64(90000), gjson.GetBytes(out, "max_tokens").Int())
	require.False(t, gjson.GetBytes(base, "messages").Get("1").Exists(), "base body is not mutated")
}

func TestMergeContinuationUsageBillsInputOnce(t *testing.T) {
	base := &dto.Usage{PromptTokens: 703, CompletionTokens: 34013, TotalTokens: 34716,
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 34013}}
	round := &dto.Usage{PromptTokens: 27780, CompletionTokens: 9142, TotalTokens: 36922,
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 37523}}
	base.BillingUsage = dto.NewOpenAIChatBillingUsage(base)
	merged := mergeContinuationUsage(base, round)
	require.Equal(t, 703, merged.PromptTokens, "input is billed once")
	require.Equal(t, 43155, merged.CompletionTokens, "output of both rounds is billed")
	require.Equal(t, 43858, merged.TotalTokens)
	require.Equal(t, 71536, merged.CompletionTokenDetails.ReasoningTokens)
	require.NotNil(t, merged.BillingUsage)
	require.NotNil(t, merged.BillingUsage.OpenAIUsage)
	require.Equal(t, 703, merged.BillingUsage.OpenAIUsage.PromptTokens)
	require.Equal(t, 43155, merged.BillingUsage.OpenAIUsage.CompletionTokens)
}

func newAbnormalContinuationTestContext(t *testing.T, sender func(string, int) (*http.Response, error), maxSteps, clientMaxTokens, maxReasoningBytes int) (*gin.Context, *relaycommon.RelayInfo, *httptest.ResponseRecorder) {
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
		ChatContinuation: &relaycommon.ChatContinuationConfig{
			Abnormal: &relaycommon.ChatAbnormalContinuationConfig{
				MaxSteps:          maxSteps,
				Send:              sender,
				MaxReasoningBytes: maxReasoningBytes,
				MinReasoningChars: 1,
			},
		},
	}
	info.SetEstimatePromptTokens(5)
	return c, info, recorder
}

// TestOaiStreamHandlerContinuesThinkingAfterAbnormalFinish pins the 900s shape:
// the first round emitted only reasoning and died with finish_reason=error;
// the gateway must not surface that failure, but replay the reasoning as the
// assistant body and let the model finish, with one stop frame and input
// billed exactly once.
func TestOaiStreamHandlerContinuesThinkingAfterAbnormalFinish(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"思考一"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"思考二"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"继续思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"最终答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":3,"total_tokens":28}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	calls := 0
	var gotReasoning string
	var gotRemaining int
	c, info, recorder := newAbnormalContinuationTestContext(t, func(reasoning string, remaining int) (*http.Response, error) {
		calls++
		gotReasoning, gotRemaining = reasoning, remaining
		return continuationSSEResponse(secondRound), nil
	}, 1, 100, 1<<20)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 1, calls)
	require.Equal(t, "思考一思考二", gotReasoning)
	require.Equal(t, 90, gotRemaining)
	require.Equal(t, 5, usage.PromptTokens, "input is billed once")
	require.Equal(t, 13, usage.CompletionTokens, "reasoning from round one + round two output are billed")
	require.Equal(t, 18, usage.TotalTokens)
	require.True(t, info.StreamStatus.IsNormalEnd())
	require.False(t, info.StreamStatus.ResponseFailed())

	body := recorder.Body.String()
	require.Contains(t, body, "最终答案")
	require.Contains(t, body, `"id":"chat_1"`)
	require.NotContains(t, body, `"id":"chat_2"`, "continuation deltas keep the first round's id")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`), "one terminal frame")
	require.Equal(t, 1, strings.Count(body, "[DONE]"), "one [DONE]")
	require.NotContains(t, body, `"finish_reason":"error"`, "the failed round's terminal frame is suppressed")
}

// TestOaiStreamHandlerSkipsThinkingContinuationWhenReasoningTooLarge ensures
// the capture cap turns into the ordinary failure path rather than replaying a
// truncated reasoning prefix.
func TestOaiStreamHandlerSkipsThinkingContinuationWhenReasoningTooLarge(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"1234567890"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	calls := 0
	c, info, _ := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		calls++
		return continuationSSEResponse(secondContinuationRoundForTest()), nil
	}, 1, 100, 8)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.NotNil(t, err, "a truncated reasoning capture must fall back to the failure path")
	require.Zero(t, usage.TotalTokens)
	require.Equal(t, 0, calls)
}

// TestOaiStreamHandlerFallsBackWhenThinkingContinuationFails keeps the
// customer-visible contract for an unsuccessful resume: no success terminal,
// zero billable usage from the handler, and the ordinary abnormal-finish error
// so the controller refunds the reservation.
func TestOaiStreamHandlerFallsBackWhenThinkingContinuationFails(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	failedRound := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"reasoning_content":"仍然失败"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	calls := 0
	c, info, _ := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		calls++
		return continuationSSEResponse(failedRound), nil
	}, 1, 100, 1<<20)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadGateway, err.StatusCode)
	require.NotNil(t, usage)
	require.Zero(t, usage.TotalTokens)
	require.Equal(t, 1, calls)
	require.Equal(t, relaycommon.UndeliveredAbnormalFinish, info.UndeliveredReason)
}

// TestOaiStreamHandlerRejectsThinkingContinuationWithoutVisibleAnswer keeps a
// stop frame from being reported as a delivered answer when the resumed round
// produced only reasoning and no visible content.
func TestOaiStreamHandlerRejectsThinkingContinuationWithoutVisibleAnswer(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	reasoningOnlyRound := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"reasoning_content":"还在想"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, info, _ := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return continuationSSEResponse(reasoningOnlyRound), nil
	}, 1, 100, 1<<20)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.NotNil(t, err, "reasoning-only continuation is not a delivered answer")
	require.Zero(t, usage.TotalTokens)
	require.Equal(t, relaycommon.UndeliveredAbnormalFinish, info.UndeliveredReason)
}

// TestOaiStreamHandlerBillsObservedUsageWhenCallerLeavesDuringThinkingContinuation
// keeps the client_gone contract for the new mode: if the caller disconnects
// while the resumed round is in flight, observed upstream usage stays billable.
func TestOaiStreamHandlerBillsObservedUsageWhenCallerLeavesDuringThinkingContinuation(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	partialRound := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"reasoning_content":"继续"},"finish_reason":null}]}`,
		``,
	}, "\n")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	calls := 0
	c, info, _ := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		calls++
		cancel() // caller leaves while the resumed round is still in flight
		return continuationSSEResponse(partialRound), nil
	}, 1, 100, 1<<20)
	c.Request = c.Request.WithContext(ctx)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err, "client disconnect is not an upstream error")
	require.NotNil(t, usage)
	require.Equal(t, 1, calls)
	require.Equal(t, 5, usage.PromptTokens)
	require.Equal(t, 10, usage.CompletionTokens, "the first round's observed output stays billable")
}

// TestOaiStreamHandlerSkipsThinkingContinuationWithoutReasoning is the negative
// control: an abnormal finish with no captured reasoning keeps the ordinary
// failure path (zero usage, 502).
func TestOaiStreamHandlerSkipsThinkingContinuationWithoutReasoning(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	calls := 0
	c, info, _ := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		calls++
		return continuationSSEResponse(secondContinuationRoundForTest()), nil
	}, 1, 100, 1<<20)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.NotNil(t, err)
	require.Zero(t, usage.TotalTokens)
	require.Equal(t, 0, calls)
	require.Equal(t, relaycommon.UndeliveredAbnormalFinish, info.UndeliveredReason)
}

func TestRewriteContinuationChunkMeta(t *testing.T) {
	meta := chatContinuationMeta{ID: "chat_1", Created: 111, Model: "kimi-k3.1-pro", SystemFingerprint: "fp_1"}
	out := rewriteContinuationChunkMeta(`{"id":"chat_2","created":222,"model":"other","system_fingerprint":"fp_2","choices":[]}`, meta)
	require.Contains(t, out, `"id":"chat_1"`)
	require.Contains(t, out, `"created":111`)
	require.Contains(t, out, `"model":"kimi-k3.1-pro"`)
	require.Contains(t, out, `"system_fingerprint":"fp_1"`)
	require.NotContains(t, out, "chat_2")

	noFP := rewriteContinuationChunkMeta(`{"id":"chat_2","created":222,"model":"other","system_fingerprint":"fp_2"}`, chatContinuationMeta{ID: "chat_1", Created: 111, Model: "m"})
	require.NotContains(t, noFP, "system_fingerprint", "fingerprint is removed when the first round had none")
}
