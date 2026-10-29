package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

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

// TestOaiStreamHandlerFlushesCoalescedContinuationBeforeTerminal pins the
// early-return flush: channels with stream coalescing must not let the terminal
// frame overtake (or drop) the resumed round's buffered content.
func TestOaiStreamHandlerFlushesCoalescedContinuationBeforeTerminal(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"content":"最终"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"content":"答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return continuationSSEResponse(secondRound), nil
	}, 1, 100, 1<<20)
	info.ChannelSetting.StreamCoalesceMs = 1

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	body := recorder.Body.String()
	terminalAt := strings.Index(body, `"finish_reason":"stop"`)
	require.NotEqual(t, -1, terminalAt)
	// The first content delta flushes immediately; the second one is buffered by
	// the coalescer and must be flushed before the terminal frame.
	secondAt := strings.Index(body, "答案")
	require.NotEqual(t, -1, secondAt, "buffered continuation content must be flushed")
	require.Less(t, secondAt, terminalAt, "buffered content must precede the terminal frame")
}

// TestOaiStreamHandlerWritesPingWhileWaitingForContinuation proves the header
// wait keeps the SSE connection alive; the short ticker override keeps the test
// fast and the race detector can verify the writer-only ping.
func TestOaiStreamHandlerWritesPingWhileWaitingForContinuation(t *testing.T) {
	oldInterval := thinkingContinuationPingInterval
	thinkingContinuationPingInterval = 5 * time.Millisecond
	t.Cleanup(func() { thinkingContinuationPingInterval = oldInterval })

	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"content":"答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		time.Sleep(20 * time.Millisecond)
		return continuationSSEResponse(secondRound), nil
	}, 1, 100, 1<<20)
	info.DisablePing = false

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Contains(t, recorder.Body.String(), ": PING")
}

func newMissingContinuationTestContext(t *testing.T, sender func(string, int) (*http.Response, error), maxSteps, clientMaxTokens, maxReasoningBytes int) (*gin.Context, *relaycommon.RelayInfo, *httptest.ResponseRecorder) {
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
			Missing: &relaycommon.ChatAbnormalContinuationConfig{
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

// TestOaiStreamHandlerContinuesMissingFinishStream pins the 60s silent-cut
// shape: reasoning, usage and [DONE], but no finish_reason at all. The stream
// must resume and deliver one visible answer with one stop frame.
func TestOaiStreamHandlerContinuesMissingFinishStream(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考一"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考二"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"reasoning_content":"继续思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"content":"最终答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	calls := 0
	var gotReasoning string
	c, info, recorder := newMissingContinuationTestContext(t, func(reasoning string, remaining int) (*http.Response, error) {
		calls++
		gotReasoning = reasoning
		return continuationSSEResponse(secondRound), nil
	}, 1, 100, 1<<20)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 1, calls)
	require.Equal(t, "思考一思考二", gotReasoning)
	require.Equal(t, 5, usage.PromptTokens, "input is billed once")
	require.Equal(t, 13, usage.CompletionTokens, "output of both rounds is billed")
	require.True(t, info.StreamStatus.IsNormalEnd())

	body := recorder.Body.String()
	require.Contains(t, body, "最终答案")
	require.Contains(t, body, `"id":"chat_1"`)
	require.NotContains(t, body, `"id":"chat_2"`)
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`))
	require.Equal(t, 1, strings.Count(body, "[DONE]"))
}

// TestOaiStreamHandlerFallsBackWhenMissingFinishContinuationFails keeps the
// refund path when the resumed round also ends without a terminal frame.
func TestOaiStreamHandlerFallsBackWhenMissingFinishContinuationFails(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	stillTruncated := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"reasoning_content":"继续"},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, info, _ := newMissingContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return continuationSSEResponse(stillTruncated), nil
	}, 1, 100, 1<<20)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.NotNil(t, err)
	require.Zero(t, usage.TotalTokens)
	require.Equal(t, relaycommon.UndeliveredMissingFinish, info.UndeliveredReason)
}

// TestOaiStreamHandlerSkipsMissingFinishContinuationWithoutReasoning is the
// negative control for empty/short reasoning.
func TestOaiStreamHandlerSkipsMissingFinishContinuationWithoutReasoning(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	calls := 0
	c, info, _ := newMissingContinuationTestContext(t, func(string, int) (*http.Response, error) {
		calls++
		return continuationSSEResponse(secondContinuationRoundForTest()), nil
	}, 1, 100, 1<<20)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.NotNil(t, err)
	require.Zero(t, usage.TotalTokens)
	require.Equal(t, 0, calls)
	require.Equal(t, relaycommon.UndeliveredMissingFinish, info.UndeliveredReason)
}

// TestOaiStreamHandlerDedupesRepeatedRoundUsage protects billing from providers
// that repeat the same cumulative usage snapshot inside one continuation round.
func TestOaiStreamHandlerDedupesRepeatedRoundUsage(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"content":"答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, info, _ := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return continuationSSEResponse(secondRound), nil
	}, 1, 100, 1<<20)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.Equal(t, 5, usage.PromptTokens)
	require.Equal(t, 13, usage.CompletionTokens, "repeated cumulative usage must count once")
}

// TestOaiStreamHandlerSuppressesFinishOnDeltaChunk ensures a round-2 chunk that
// carries both content and finish_reason does not produce a second terminal frame.
func TestOaiStreamHandlerSuppressesFinishOnDeltaChunk(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"content":"最终答案"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return continuationSSEResponse(secondRound), nil
	}, 1, 100, 1<<20)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	body := recorder.Body.String()
	require.Contains(t, body, "最终答案")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`), "exactly one terminal frame")
}

// TestSendThinkingContinuationSuppressesUpstreamPinger pins the single-writer
// contract: while the second attempt runs, doRequest must not start its own SSE
// pinger, and the original DisablePing value is restored afterwards.
func TestSendThinkingContinuationSuppressesUpstreamPinger(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{DisablePing: false}
	seen := false
	cfg := &relaycommon.ChatAbnormalContinuationConfig{
		MaxSteps: 1,
		Send: func(string, int) (*http.Response, error) {
			seen = info.DisablePing
			return continuationSSEResponse("data: [DONE]\n\n"), nil
		},
	}
	resp, err := sendThinkingContinuationWithKeepAlive(c, info, cfg.Send, "思考", 100)
	require.Nil(t, err)
	require.NotNil(t, resp)
	_ = resp.Body.Close()
	require.True(t, seen, "doRequest pinger must be suppressed during the second attempt")
	require.False(t, info.DisablePing, "the original DisablePing value must be restored")
}

type cappedDeadlineWriter struct {
	gin.ResponseWriter
	max time.Duration
}

func (w *cappedDeadlineWriter) SetWriteDeadline(t time.Time) error {
	if limit := time.Now().Add(w.max); t.After(limit) {
		t = limit
	}
	return http.NewResponseController(w.ResponseWriter).SetWriteDeadline(t)
}

// TestContinuationKeepsWriteDeadlineAlive is the real-server regression for the
// 30s client write deadline: pings must extend the deadline while round two is
// still waiting, otherwise the resumed answer is silently dropped.
func TestContinuationKeepsWriteDeadlineAlive(t *testing.T) {
	oldInterval := thinkingContinuationPingInterval
	thinkingContinuationPingInterval = 100 * time.Millisecond
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		thinkingContinuationPingInterval = oldInterval
		constant.StreamingTimeout = oldTimeout
	})

	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"content":"最终答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/", func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("handler panic: %v", r)
				panic(r)
			}
		}()
		c.Header("Content-Type", "text/event-stream")
		c.Writer = &cappedDeadlineWriter{ResponseWriter: c.Writer, max: 500 * time.Millisecond}
		info := &relaycommon.RelayInfo{
			ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3.1-pro"},
			IsStream:           true,
			RelayMode:          relayconstant.RelayModeChatCompletions,
			RelayFormat:        types.RelayFormatOpenAI,
			ShouldIncludeUsage: true,
			ClientMaxTokens:    100,
			ChatContinuation: &relaycommon.ChatContinuationConfig{
				Abnormal: &relaycommon.ChatAbnormalContinuationConfig{
					MaxSteps:          1,
					MaxReasoningBytes: 1 << 20,
					MinReasoningChars: 1,
					Send: func(string, int) (*http.Response, error) {
						time.Sleep(900 * time.Millisecond)
						return continuationSSEResponse(secondRound), nil
					},
				},
			},
		}
		info.SetEstimatePromptTokens(5)
		_, _ = OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	})
	server := httptest.NewServer(router)
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(server.URL + "/")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "最终答案")
	require.Contains(t, string(body), "[DONE]")
}

// TestChatMissingContinuationRejectsMultipleChoices pins the n<=1 gate; a
// multi-choice stream must not be resumed by a single-choice continuation.
func TestChatMissingContinuationRejectsMultipleChoices(t *testing.T) {
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3.1-pro"},
		IsStream:        true,
		RelayFormat:     types.RelayFormatOpenAI,
		ClientMaxTokens: 100,
		StreamStatus:    relaycommon.NewStreamStatus(),
		ChatContinuation: &relaycommon.ChatContinuationConfig{
			Missing: &relaycommon.ChatAbnormalContinuationConfig{
				MaxSteps:          1,
				Send:              func(string, int) (*http.Response, error) { return nil, nil },
				MinReasoningChars: 1,
			},
		},
	}
	require.False(t, chatMissingContinuationCandidate(info, &dto.Usage{}, 0, 100, 0, 2, false))
	require.True(t, chatMissingContinuationCandidate(info, &dto.Usage{}, 0, 100, 0, 1, false))
}

// TestContinuationCountsEachToolCallOnce proves the resumed round uses the same
// identity dedupe as round one instead of counting every streamed chunk.
func TestContinuationCountsEachToolCallOnce(t *testing.T) {
	operation_setting.SetToolPriceForTest("get_weather", 0.01)
	t.Cleanup(func() { operation_setting.DeleteToolPriceForTest("get_weather") })

	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"content":"查一下","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, info, _ := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return continuationSSEResponse(secondRound), nil
	}, 1, 100, 1<<20)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.NotNil(t, info.ResponsesUsageInfo)
	tool, ok := info.ResponsesUsageInfo.BuiltInTools["get_weather"]
	require.True(t, ok)
	require.Equal(t, 1, tool.CallCount, "one tool call split over two chunks must be billed once")
}

// TestOaiStreamHandlerContinuesThinkingAfterLengthCap covers the 32k-cap shape
// where the provider stops on its output cap while the model is still thinking:
// there is no visible content to replay, so the thinking continuation resumes
// from the captured reasoning and the client still sees one normal completion.
func TestOaiStreamHandlerContinuesThinkingAfterLengthCap(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"思考一"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"思考二"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
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
	require.Equal(t, 1, calls, "the length cap with no visible content resumes the thinking round")
	require.Equal(t, "思考一思考二", gotReasoning)
	require.Equal(t, 90, gotRemaining)
	require.Equal(t, 13, usage.CompletionTokens, "reasoning from both rounds is billed once")
	require.True(t, info.StreamStatus.IsNormalEnd())
	require.False(t, info.StreamStatus.ResponseFailed())

	body := recorder.Body.String()
	require.Contains(t, body, "最终答案")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`), "one terminal frame")
	require.Equal(t, 1, strings.Count(body, "[DONE]"), "one [DONE]")
	require.NotContains(t, body, `"finish_reason":"length"`, "the capped round's terminal frame is suppressed")
}

// TestOaiStreamHandlerLengthThinkingContinuationFailureKeepsTruncation checks
// that a failed continuation leaves the ordinary output-cap behaviour intact:
// one length terminal, first-round usage billed once, no recovered status.
func TestOaiStreamHandlerLengthThinkingContinuationFailureKeepsTruncation(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"思考一"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	calls := 0
	c, info, recorder := newAbnormalContinuationTestContext(t, func(reasoning string, remaining int) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("upstream refused the continuation")
	}, 2, 100, 1<<20)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 1, calls)
	require.Equal(t, 10, usage.CompletionTokens, "a failed continuation still bills the first round")
	body := recorder.Body.String()
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"length"`), "one terminal frame")
	require.Equal(t, 1, strings.Count(body, "[DONE]"), "one [DONE]")
}

// TestOaiStreamHandlerSkipsLengthThinkingContinuationWithoutReasoning ensures a
// cap with no captured reasoning keeps the plain truncation path.
func TestOaiStreamHandlerSkipsLengthThinkingContinuationWithoutReasoning(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":""},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	calls := 0
	c, info, recorder := newAbnormalContinuationTestContext(t, func(reasoning string, remaining int) (*http.Response, error) {
		calls++
		return continuationSSEResponse(`data: [DONE]` + "\n"), nil
	}, 2, 100, 1<<20)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.Equal(t, 0, calls, "no reasoning to resume")
	require.Equal(t, 10, usage.CompletionTokens)
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `"finish_reason":"length"`))
}

// TestOaiStreamHandlerSkipsLengthThinkingContinuationWhenBudgetExhausted covers
// a client that already spent its whole budget: nothing left to ask for.
func TestOaiStreamHandlerSkipsLengthThinkingContinuationWhenBudgetExhausted(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":100,"total_tokens":105}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	calls := 0
	c, info, _ := newAbnormalContinuationTestContext(t, func(reasoning string, remaining int) (*http.Response, error) {
		calls++
		return continuationSSEResponse(`data: [DONE]` + "\n"), nil
	}, 2, 100, 1<<20)

	_, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.Equal(t, 0, calls)
}

// TestOaiStreamHandlerLengthThinkingContinuationPartialRoundEndsLength covers a
// continuation round that forwards content and then dies: the client already
// read that content, so the response must end on a length terminal with the
// merged usage instead of the first round's frame.
func TestOaiStreamHandlerLengthThinkingContinuationPartialRoundEndsLength(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"思考一"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"部分答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":25,"completion_tokens":3,"total_tokens":28}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, info, recorder := newAbnormalContinuationTestContext(t, func(reasoning string, remaining int) (*http.Response, error) {
		return continuationSSEResponse(secondRound), nil
	}, 2, 100, 1<<20)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 13, usage.CompletionTokens, "both rounds are billed once")
	body := recorder.Body.String()
	require.Contains(t, body, "部分答案")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"length"`), "delivered content ends on a length terminal")
	require.NotContains(t, body, `"finish_reason":"error"`)
	require.Equal(t, 1, strings.Count(body, "[DONE]"))
}

// enableKimiK3OfficialFit makes the relay info look like a Kimi K3 caller whose
// user enabled the official-fit Shape dimension — the production shape of user
// 1 (admin) and user 3 (the customer), see MEMORY 2026-10-16/17 and 2026-10-28.
// Before this work that setting silently disabled every continuation mode.
func enableKimiK3OfficialFit(info *relaycommon.RelayInfo) {
	info.OriginModelName = "kimi-k3.1-pro"
	info.RelayMode = relayconstant.RelayModeChatCompletions
	info.RelayFormat = types.RelayFormatOpenAI
	info.ClientIncludeUsage = true
	info.UserSetting = dto.UserSetting{OfficialFit: &dto.OfficialFitConfig{
		Profile: map[string]dto.OfficialFitProfile{
			"kimi-k3": {Shape: true},
		},
	}}
}

func continuationDataPayloads(body string) []string {
	payloads := make([]string, 0, 8)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

// assertKimiK3OfficialContinuationBody pins the official-form contract for a
// transparently continued K3 stream: one terminal frame, one [DONE], the first
// round's envelope everywhere, no repeated assistant role, no forwarded
// top-level usage, the merged usage inside choices[0].usage of the terminal
// chunk, and (only when the client asked) exactly one choices:[] usage-only
// event whose usage is byte-identical to the terminal's.
func assertKimiK3OfficialContinuationBody(t *testing.T, body string, wantCompletion int, wantUsageOnly bool) {
	t.Helper()

	require.Equal(t, 1, strings.Count(body, "[DONE]"), "one [DONE]")
	require.Contains(t, body, `"id":"chat_1"`)
	require.NotContains(t, body, `"id":"chat_2"`, "continuation deltas keep the first round's id")
	require.Equal(t, 1, strings.Count(body, `"role":"assistant"`), "no repeated role delta")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`), "one terminal frame")

	officialTopLevel := map[string]struct{}{"id": {}, "object": {}, "created": {}, "model": {}, "choices": {}, "system_fingerprint": {}}
	officialChoice := map[string]struct{}{"index": {}, "delta": {}, "finish_reason": {}, "usage": {}}
	officialDelta := map[string]struct{}{"role": {}, "content": {}, "reasoning_content": {}, "tool_calls": {}}
	officialUsage := map[string]struct{}{"prompt_tokens": {}, "completion_tokens": {}, "total_tokens": {}, "cached_tokens": {}, "completion_tokens_details": {}, "prompt_tokens_details": {}}

	var terminal string
	terminalCount := 0
	usageOnlyCount := 0
	var usageOnlyUsage string
	for _, payload := range continuationDataPayloads(body) {
		event := gjson.Parse(payload)
		choices := event.Get("choices").Array()
		if len(choices) == 0 {
			require.True(t, event.Get("usage").Exists(), "an empty-choices event must be the usage-only event: %s", payload)
			usageOnlyCount++
			usageOnlyUsage = event.Get("usage").Raw
			for key := range event.Map() {
				if key == "usage" {
					continue
				}
				_, ok := officialTopLevel[key]
				require.True(t, ok, "unexpected top-level key %q in usage-only event: %s", key, payload)
			}
			continue
		}
		require.False(t, event.Get("usage").Exists(), "official K3 stream carries no top-level usage: %s", payload)
		for key := range event.Map() {
			_, ok := officialTopLevel[key]
			require.True(t, ok, "unexpected top-level key %q in continuation chunk: %s", key, payload)
		}
		for _, choice := range choices {
			for key := range choice.Map() {
				_, ok := officialChoice[key]
				require.True(t, ok, "unexpected choice key %q in continuation chunk: %s", key, payload)
			}
			delta := choice.Get("delta")
			if delta.Exists() {
				for key := range delta.Map() {
					_, ok := officialDelta[key]
					require.True(t, ok, "unexpected delta key %q in continuation chunk: %s", key, payload)
				}
			}
			finish := choice.Get("finish_reason")
			if finish.Type == gjson.String && finish.String() != "" {
				terminal = payload
				terminalCount++
			}
		}
	}
	require.Equal(t, 1, terminalCount, "exactly one terminal frame")
	require.NotEmpty(t, terminal)

	terminalUsage := gjson.Get(terminal, "choices.0.usage")
	require.True(t, terminalUsage.Exists(), "official K3 terminal chunk carries usage inside choices[0]: %s", terminal)
	for key := range terminalUsage.Map() {
		_, ok := officialUsage[key]
		require.True(t, ok, "unexpected usage key %q: %s", key, terminal)
	}
	require.Equal(t, int64(5), terminalUsage.Get("prompt_tokens").Int(), "input is billed once")
	require.Equal(t, int64(wantCompletion), terminalUsage.Get("completion_tokens").Int(), "terminal carries the merged completion")
	require.Equal(t, int64(5+wantCompletion), terminalUsage.Get("total_tokens").Int())

	if wantUsageOnly {
		require.Equal(t, 1, usageOnlyCount, "one choices:[] usage-only event when the client asked for usage")
		require.Equal(t, terminalUsage.Raw, usageOnlyUsage, "usage-only event repeats the terminal usage verbatim")
	} else {
		require.Equal(t, 0, usageOnlyCount, "no usage-only event when the client did not ask")
	}
}

// TestOaiStreamHandlerKimiK3FitLengthContinuationKeepsOfficialShape covers the
// 32k-cap shape for a fit caller: the resumed segment and the merged usage must
// stay inside the official K3 stream form.
func TestOaiStreamHandlerKimiK3FitLengthContinuationKeepsOfficialShape(t *testing.T) {
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
	c, info, recorder := newContinuationTestContext(t, func(string, int) (*http.Response, error) {
		calls++
		return continuationSSEResponse(secondRound), nil
	}, 2, 20)
	enableKimiK3OfficialFit(info)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 1, calls, "a fit caller must still be continued")
	require.Equal(t, 4, usage.CompletionTokens)
	require.Equal(t, 5, usage.PromptTokens, "input is billed once")
	require.Empty(t, info.UndeliveredReason)

	body := recorder.Body.String()
	require.Contains(t, body, "1、2、")
	require.Contains(t, body, "3、4、")
	assertKimiK3OfficialContinuationBody(t, body, 4, true)
}

// TestOaiStreamHandlerKimiK3FitAbnormalContinuationKeepsOfficialShape covers
// the 900s cut-off shape for a fit caller (reasoning only, finish_reason=error).
func TestOaiStreamHandlerKimiK3FitAbnormalContinuationKeepsOfficialShape(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"思考一"},"finish_reason":null}]}`,
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
	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		calls++
		return continuationSSEResponse(secondRound), nil
	}, 1, 100, 1<<20)
	enableKimiK3OfficialFit(info)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 1, calls)
	require.Equal(t, 13, usage.CompletionTokens)
	require.Equal(t, 5, usage.PromptTokens, "input is billed once")
	require.True(t, info.StreamStatus.IsNormalEnd())
	require.False(t, info.StreamStatus.ResponseFailed())

	body := recorder.Body.String()
	require.Contains(t, body, "最终答案")
	require.NotContains(t, body, `"finish_reason":"error"`, "the failed round's terminal frame is suppressed")
	assertKimiK3OfficialContinuationBody(t, body, 13, true)
}

// TestOaiStreamHandlerKimiK3FitMissingFinishContinuationKeepsOfficialShape
// covers the 60s/600s silent-cut shape for a fit caller.
func TestOaiStreamHandlerKimiK3FitMissingFinishContinuationKeepsOfficialShape(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"思考一"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"思考二"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"最终答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	calls := 0
	c, info, recorder := newMissingContinuationTestContext(t, func(string, int) (*http.Response, error) {
		calls++
		return continuationSSEResponse(secondRound), nil
	}, 1, 100, 1<<20)
	enableKimiK3OfficialFit(info)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 1, calls)
	require.Equal(t, 13, usage.CompletionTokens)
	require.True(t, info.StreamStatus.IsNormalEnd())

	assertKimiK3OfficialContinuationBody(t, recorder.Body.String(), 13, true)
}

// TestOaiStreamHandlerKimiK3FitLengthThinkingContinuationKeepsOfficialShape
// covers the output cap landing while the model is still thinking.
func TestOaiStreamHandlerKimiK3FitLengthThinkingContinuationKeepsOfficialShape(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"思考一"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"思考二"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
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
	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		calls++
		return continuationSSEResponse(secondRound), nil
	}, 1, 100, 1<<20)
	enableKimiK3OfficialFit(info)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 1, calls)
	require.Equal(t, 13, usage.CompletionTokens)
	require.True(t, info.StreamStatus.IsNormalEnd())

	body := recorder.Body.String()
	require.NotContains(t, body, `"finish_reason":"length"`, "the capped round's terminal frame is suppressed")
	assertKimiK3OfficialContinuationBody(t, body, 13, true)
}

// TestOaiStreamHandlerKimiK3FitContinuationWithoutClientUsageAsk keeps the
// official contract that the choices:[] usage-only event exists only when the
// client's own stream_options asked for usage.
func TestOaiStreamHandlerKimiK3FitContinuationWithoutClientUsageAsk(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"最终答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return continuationSSEResponse(secondRound), nil
	}, 1, 100, 1<<20)
	enableKimiK3OfficialFit(info)
	info.ClientIncludeUsage = false

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 13, usage.CompletionTokens)

	assertKimiK3OfficialContinuationBody(t, recorder.Body.String(), 13, false)
}

// TestOaiStreamHandlerKimiK3FitContinuationFailureKeepsFailurePath keeps the
// no-fit failure contract for a fit caller: a continuation round that never
// produces a visible answer must not be reported as a delivered success.
func TestOaiStreamHandlerKimiK3FitContinuationFailureKeepsFailurePath(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	reasoningOnlyRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"还在想"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, info, _ := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return continuationSSEResponse(reasoningOnlyRound), nil
	}, 1, 100, 1<<20)
	enableKimiK3OfficialFit(info)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.NotNil(t, err)
	require.Zero(t, usage.TotalTokens)
	require.Equal(t, relaycommon.UndeliveredAbnormalFinish, info.UndeliveredReason)
}

// TestOaiStreamHandlerContinuesAnswerAfterAbnormalCutWithContent pins the 900s
// wall hit mid-answer: the caller already received a partial answer, and the
// gateway used to discard it and surface finish_reason=error. It must instead
// continue the visible prefix, keep one terminal frame and bill the merged usage
// exactly once.
func TestOaiStreamHandlerContinuesAnswerAfterAbnormalCutWithContent(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"前半段答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"后半段答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":3,"total_tokens":28}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	var gotPartial string
	sender := func(partial string, remaining int) (*http.Response, error) {
		gotPartial = partial
		return continuationSSEResponse(secondRound), nil
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3.1-pro"},
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
		ClientMaxTokens:    100,
		ChatContinuation: &relaycommon.ChatContinuationConfig{
			MaxSteps: 2,
			Send:     sender,
			Abnormal: &relaycommon.ChatAbnormalContinuationConfig{MaxSteps: 1, Send: sender, MaxReasoningBytes: 1 << 20, MinReasoningChars: 1},
		},
	}
	info.SetEstimatePromptTokens(5)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err, "a repaired answer must not surface the cut as an error")
	require.NotNil(t, usage)
	require.Equal(t, "前半段答案", gotPartial, "the continuation replays the visible prefix")
	require.Equal(t, 13, usage.CompletionTokens, "both rounds are billed")

	body := recorder.Body.String()
	require.Contains(t, body, "后半段答案")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`), "one terminal frame")
	require.Equal(t, 1, strings.Count(body, "[DONE]"), "one [DONE]")
	require.NotContains(t, body, `"finish_reason":"error"`, "the cut's terminal frame is suppressed")
}

// TestChatMissingVisibleContinuationCandidateGuards pins the guards on the
// truncate-mid-answer repair: it may only run when a visible prefix exists, the
// request can still be continued (budget, one choice, no tool calls) and the cut
// was not the caller's own disconnect.
func TestChatMissingVisibleContinuationCandidateGuards(t *testing.T) {
	send := func(string, int) (*http.Response, error) { return nil, nil }
	newInfo := func() *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{
			ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3.1-pro"},
			IsStream:        true,
			RelayFormat:     types.RelayFormatOpenAI,
			ClientMaxTokens: 100,
			StreamStatus:    relaycommon.NewStreamStatus(),
			ChatContinuation: &relaycommon.ChatContinuationConfig{
				MaxSteps: 1,
				Send:     send,
				Missing:  &relaycommon.ChatAbnormalContinuationConfig{MaxSteps: 1, Send: send, MinReasoningChars: 1},
			},
		}
	}

	info := newInfo()
	require.True(t, chatMissingVisibleContinuationCandidate(info, &dto.Usage{}, 1200, 0, 1, false),
		"a visible prefix with budget left is repairable")

	require.False(t, chatMissingVisibleContinuationCandidate(info, &dto.Usage{}, 0, 0, 1, false),
		"thinking-only cuts belong to the reasoning resume")

	require.False(t, chatMissingVisibleContinuationCandidate(info, &dto.Usage{}, 1200, 1, 1, false),
		"a tool-call stream cannot be continued")

	require.False(t, chatMissingVisibleContinuationCandidate(info, &dto.Usage{}, 1200, 0, 2, false),
		"multiple choices are not repairable")

	require.False(t, chatMissingVisibleContinuationCandidate(info, &dto.Usage{}, 1200, 0, 1, true),
		"a terminal frame means this is not the missing-finish shape")

	flat := newInfo()
	flat.StreamFinishReason = "length"
	require.False(t, chatMissingVisibleContinuationCandidate(flat, &dto.Usage{}, 1200, 0, 1, false))

	// An undeclared max_tokens is not an exhausted budget: the request simply has
	// no client ceiling (2026-10-28 18:21: one kimi cut reached the caller as a 502
	// because this guard read 0 as "no budget left" after 52 output tokens).
	noBudget := newInfo()
	noBudget.ClientMaxTokens = 0
	require.True(t, chatMissingVisibleContinuationCandidate(noBudget, &dto.Usage{CompletionTokens: 100000}, 1200, 0, 1, false),
		"an undeclared budget gets a per-round step instead of being refused")

	spent := newInfo()
	spent.ClientMaxTokens = 10
	require.False(t, chatMissingVisibleContinuationCandidate(spent, &dto.Usage{CompletionTokens: 10}, 1200, 0, 1, false),
		"no budget left to continue")

	gone := newInfo()
	gone.StreamStatus.EndReason = relaycommon.StreamEndReasonClientGone
	require.False(t, chatMissingVisibleContinuationCandidate(gone, &dto.Usage{}, 1200, 0, 1, false),
		"a client disconnect is not a truncation")

	noMissing := newInfo()
	noMissing.ChatContinuation.Missing = nil
	require.False(t, chatMissingVisibleContinuationCandidate(noMissing, &dto.Usage{}, 1200, 0, 1, false))

	noSender := newInfo()
	noSender.ChatContinuation.Send = nil
	require.False(t, chatMissingVisibleContinuationCandidate(noSender, &dto.Usage{}, 1200, 0, 1, false))

	thinkingToContent := newInfo()
	thinkingToContent.ChannelSetting.ThinkingToContent = true
	require.False(t, chatMissingVisibleContinuationCandidate(thinkingToContent, &dto.Usage{}, 1200, 0, 1, false))
}

// TestOaiStreamHandlerContinuesAnswerAfterMissingCutWithContent covers the
// 2026-10-28 production shape: an upstream dies mid-answer with no terminal
// finish_reason at all. The prefix already sent to the client must be continued
// instead of surfacing a 502, with one terminal frame and merged usage.
func TestOaiStreamHandlerContinuesAnswerAfterMissingCutWithContent(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"前半段答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"后半段答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":3,"total_tokens":28}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	var gotPartial string
	c, info, recorder := newMissingContinuationTestContext(t, func(partial string, remaining int) (*http.Response, error) {
		gotPartial = partial
		return continuationSSEResponse(secondRound), nil
	}, 1, 100, 1<<20)
	// 可见正文续写复用输出上限那条 runner，它走顶层 sender。
	info.ChatContinuation.MaxSteps = 1
	info.ChatContinuation.Send = info.ChatContinuation.Missing.Send

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err, "a repaired answer must not surface the cut as an error")
	require.NotNil(t, usage)
	require.Equal(t, "前半段答案", gotPartial, "the continuation replays the visible prefix")
	require.Equal(t, 13, usage.CompletionTokens, "both rounds are billed")
	require.True(t, info.StreamStatus.IsNormalEnd())

	body := recorder.Body.String()
	require.Contains(t, body, "后半段答案")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`), "one terminal frame")
	require.Equal(t, 1, strings.Count(body, "[DONE]"), "one [DONE]")
}

// TestTruncationRepairSkipReason pins the diagnostic reasons: each guard that
// declines a repair must be named, so a new uncovered shape is identifiable from
// one log line instead of a decision-matrix investigation.
func TestTruncationRepairSkipReason(t *testing.T) {
	send := func(string, int) (*http.Response, error) { return nil, nil }
	newInfo := func() *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{
			ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3.1-pro"},
			IsStream:        true,
			RelayFormat:     types.RelayFormatOpenAI,
			ClientMaxTokens: 100,
			StreamStatus:    relaycommon.NewStreamStatus(),
			ChatContinuation: &relaycommon.ChatContinuationConfig{
				MaxSteps: 1,
				Send:     send,
				Missing:  &relaycommon.ChatAbnormalContinuationConfig{MaxSteps: 1, Send: send},
			},
		}
	}

	info := newInfo()
	require.Equal(t, "", truncationRepairSkipReason(info, &dto.Usage{}, 1200, 10, 0, 1),
		"a repairable shape has no skip reason")
	require.Equal(t, "tool_calls_in_progress", truncationRepairSkipReason(info, &dto.Usage{}, 1200, 10, 1, 1))
	require.Equal(t, "multiple_choices", truncationRepairSkipReason(info, &dto.Usage{}, 1200, 10, 0, 2))

	folded := newInfo()
	folded.ChannelSetting.ThinkingToContent = true
	require.Equal(t, "reasoning_suppressed_or_folded", truncationRepairSkipReason(folded, &dto.Usage{}, 1200, 10, 0, 1))

	spent := newInfo()
	spent.ClientMaxTokens = 10
	require.Equal(t, "client_budget_exhausted", truncationRepairSkipReason(spent, &dto.Usage{CompletionTokens: 10}, 1200, 10, 0, 1))

	gone := newInfo()
	gone.StreamStatus.EndReason = relaycommon.StreamEndReasonClientGone
	require.Equal(t, "client_gone", truncationRepairSkipReason(gone, &dto.Usage{}, 1200, 10, 0, 1))

	unconfigured := newInfo()
	unconfigured.ChatContinuation = nil
	require.Equal(t, "continuation_not_configured", truncationRepairSkipReason(unconfigured, &dto.Usage{}, 1200, 10, 0, 1))

	noSender := newInfo()
	noSender.ChatContinuation.Send = nil
	require.Equal(t, "visible_continuation_disabled", truncationRepairSkipReason(noSender, &dto.Usage{}, 1200, 10, 0, 1))
	require.Equal(t, "", truncationRepairSkipReason(noSender, &dto.Usage{}, 0, 10, 0, 1),
		"a thinking-only cut uses the reasoning sender, which is still configured")

	empty := newInfo()
	require.Equal(t, "nothing_to_continue", truncationRepairSkipReason(empty, &dto.Usage{}, 0, 0, 0, 1))

	notStream := newInfo()
	notStream.IsStream = false
	require.Equal(t, "unsupported_format", truncationRepairSkipReason(notStream, &dto.Usage{}, 1200, 10, 0, 1))
}

// TestChatContinuationBudgetSemantics pins the two budget readings: a declared
// max_tokens stays a hard ceiling for the whole response, while an undeclared
// one grants one round of the per-round step no matter how much the first round
// already produced (output that predates the ceiling cannot exhaust it).
func TestChatContinuationBudgetSemantics(t *testing.T) {
	declared := &relaycommon.RelayInfo{ClientMaxTokens: 100}
	require.Equal(t, 100, chatContinuationOutputBudget(declared))
	require.Equal(t, 60, chatContinuationBudgetLeft(declared, &dto.Usage{CompletionTokens: 40}))
	require.Equal(t, 0, chatContinuationBudgetLeft(declared, &dto.Usage{CompletionTokens: 100}))
	require.Equal(t, 0, chatContinuationBudgetLeft(declared, &dto.Usage{CompletionTokens: 140}))

	undeclared := &relaycommon.RelayInfo{}
	require.Equal(t, undeclaredClientOutputBudget, chatContinuationOutputBudget(undeclared))
	require.Equal(t, undeclaredClientOutputBudget, chatContinuationBudgetLeft(undeclared, &dto.Usage{CompletionTokens: 923}))
	require.Equal(t, undeclaredClientOutputBudget, chatContinuationBudgetLeft(undeclared, nil))
}

// TestOaiStreamHandlerContinuesMissingCutWithContentWithoutDeclaredBudget is the
// 2026-10-28 18:21 production shape: the client sent no max_tokens, the upstream
// died mid-answer without a terminal frame, and the repair was refused because
// ClientMaxTokens was 0. The answer must be continued and delivered as one
// stream with one terminal frame.
func TestOaiStreamHandlerContinuesMissingCutWithContentWithoutDeclaredBudget(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"前半段答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"后半段答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":3,"total_tokens":28}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	calls := 0
	var gotPartial string
	var gotRemaining int
	c, info, recorder := newMissingContinuationTestContext(t, func(partial string, remaining int) (*http.Response, error) {
		calls++
		gotPartial = partial
		gotRemaining = remaining
		return continuationSSEResponse(secondRound), nil
	}, 1, 0, 1<<20) // ClientMaxTokens 0 = the request declared no output budget
	info.ChatContinuation.MaxSteps = 1
	info.ChatContinuation.Send = info.ChatContinuation.Missing.Send

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err, "a repaired answer must not surface the cut as an error")
	require.NotNil(t, usage)
	require.Equal(t, 1, calls, "the cut must be repaired without a declared max_tokens")
	require.Equal(t, "前半段答案", gotPartial, "the continuation replays the visible prefix")
	require.Equal(t, undeclaredClientOutputBudget, gotRemaining,
		"an undeclared request spends one bounded round instead of zero")
	require.Equal(t, 13, usage.CompletionTokens, "both rounds are billed once")

	body := recorder.Body.String()
	require.Contains(t, body, "后半段答案")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`), "one terminal frame")
	require.Equal(t, 1, strings.Count(body, "[DONE]"), "one [DONE]")
}

// TestOaiStreamHandlerRetriesContinuationRoundWhenUpstreamRejectsResume pins the
// 2026-10-28 21:21 shape: a 900s cut is followed by a resume request the
// upstream rejects immediately (503). Retrying the round must recover the
// answer instead of surfacing a 502 for the prefix already delivered.
func TestOaiStreamHandlerRetriesContinuationRoundWhenUpstreamRejectsResume(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"前半段答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"后半段答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	calls := 0
	sender := func(string, int) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("busy"))}, nil
		}
		return continuationSSEResponse(secondRound), nil
	}
	c, info, recorder := newAbnormalContinuationTestContext(t, sender, 1, 128000, 1<<20)
	info.ChatContinuation.MaxSteps = 1
	info.ChatContinuation.Send = sender

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err, "a rejected resume request must be retried, not surfaced")
	require.NotNil(t, usage)
	require.Equal(t, 2, calls, "the round is retried once after the rejection")
	require.Equal(t, 13, usage.CompletionTokens, "both rounds are billed once")

	body := recorder.Body.String()
	require.Contains(t, body, "后半段答案")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`), "one terminal frame")
	require.Equal(t, 1, strings.Count(body, "[DONE]"), "one [DONE]")
}

// TestOaiStreamHandlerClosesPartiallyDeliveredAnswerAsTruncated pins the
// fallback: when every repair attempt fails, an answer whose prefix already
// reached the caller is closed with finish_reason=length instead of a 502, so
// the delivered work survives and the caller can continue. Usage stays unbilled.
func TestOaiStreamHandlerClosesPartiallyDeliveredAnswerAsTruncated(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"已经发出的前半段"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	sender := func(string, int) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("busy"))}, nil
	}
	c, info, recorder := newAbnormalContinuationTestContext(t, sender, 1, 128000, 1<<20)
	info.ChatContinuation.MaxSteps = 1
	info.ChatContinuation.Send = sender

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err, "a delivered prefix must not be turned into a 502")
	require.NotNil(t, usage)
	require.Zero(t, usage.TotalTokens, "an unrepairable cut stays unbilled")

	body := recorder.Body.String()
	require.Contains(t, body, "已经发出的前半段", "the delivered prefix stays")
	require.NotContains(t, body, "truncated_stream", "no error frame for a delivered prefix")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"length"`), "closed honestly as truncated")
	require.Equal(t, 1, strings.Count(body, "[DONE]"), "one [DONE]")
}

// TestOaiStreamHandlerDoesNotBillRoundWithoutTerminalFrame pins the robustness
// rule behind the retry work: a resumed round that forwards deltas and then
// hits a clean EOF (no finish_reason, no [DONE]) is NOT a recovered answer. It
// used to be reported as a "length" completion, billed, and its failure erased
// from StreamStatus.
func TestOaiStreamHandlerDoesNotBillRoundWithoutTerminalFrame(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"前半段答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	// A resume round that emits a delta and then closes with no terminal frame.
	truncatedRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"后半段"},"finish_reason":null}]}`,
		``,
	}, "\n")

	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return continuationSSEResponse(truncatedRound), nil
	}, 1, 128000, 1<<20)
	info.ChatContinuation.MaxSteps = 1
	info.ChatContinuation.Send = info.ChatContinuation.Abnormal.Send

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Zero(t, usage.TotalTokens, "an answer cut twice must not be billed")
	require.NotEmpty(t, info.UndeliveredReason, "the cut stays classified as undelivered")

	body := recorder.Body.String()
	require.NotContains(t, body, `"finish_reason":"stop"`, "no clean answer was produced")
}

// TestOaiStreamHandlerKeepsErrorWhenResumeForwardsToolCalls pins the tool-call
// rule across rounds: a resumed round that forwards half a tool call must keep
// the error frame instead of being closed as a truncated success, because a
// client would otherwise execute a truncated argument string.
func TestOaiStreamHandlerKeepsErrorWhenResumeForwardsToolCalls(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"前半段答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	toolCallRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"q\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return continuationSSEResponse(toolCallRound), nil
	}, 1, 128000, 1<<20)
	info.ChatContinuation.MaxSteps = 1
	info.ChatContinuation.Send = info.ChatContinuation.Abnormal.Send

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.NotNil(t, err, "a half tool call must stay an error for the caller")
	require.NotNil(t, usage)
	require.Zero(t, usage.TotalTokens)

	body := recorder.Body.String()
	require.NotContains(t, body, `"finish_reason":"length"`,
		"a half tool call must not be masked as a truncated success")
}

// TestOaiStreamHandlerContinuesAfterMidStreamError pins the 2026-10-28 22:55
// shape: the upstream streamed a visible answer and then sent an in-band error.
// The handler used to return the transport error before any repair path, so the
// caller lost an answer it had already been reading.
func TestOaiStreamHandlerContinuesAfterMidStreamError(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"前半段"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"答案"},"finish_reason":null}]}`,
		`data: {"error":{"message":"Upstream response stream ended before completion","type":"upstream_error"}}`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"后半段答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	calls := 0
	var gotPartial string
	sender := func(partial string, remaining int) (*http.Response, error) {
		calls++
		gotPartial = partial
		return continuationSSEResponse(secondRound), nil
	}
	c, info, recorder := newAbnormalContinuationTestContext(t, sender, 1, 128000, 1<<20)
	info.ChatContinuation.MaxSteps = 1
	info.ChatContinuation.Send = sender

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err, "a delivered prefix must be repaired, not surfaced as an error")
	require.NotNil(t, usage)
	require.Equal(t, 1, calls)
	require.Equal(t, "前半段答案", gotPartial, "the continuation replays the visible prefix")

	body := recorder.Body.String()
	require.Contains(t, body, "后半段答案")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`), "one terminal frame")
	require.Equal(t, 1, strings.Count(body, "[DONE]"), "one [DONE]")
	require.NotContains(t, body, "truncated_stream")
}

// TestOaiStreamHandlerClosesMidStreamErrorAsTruncated is the fallback for the
// same shape when the repair itself fails: the caller gets the delivered answer
// closed with finish_reason=length and no charge, not a raw transport error.
func TestOaiStreamHandlerClosesMidStreamErrorAsTruncated(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"已经发出的"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"内容"},"finish_reason":null}]}`,
		`data: {"error":{"message":"Upstream response stream ended before completion","type":"upstream_error"}}`,
		``,
	}, "\n")

	sender := func(string, int) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("busy"))}, nil
	}
	c, info, recorder := newAbnormalContinuationTestContext(t, sender, 1, 128000, 1<<20)
	info.ChatContinuation.MaxSteps = 1
	info.ChatContinuation.Send = sender

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err, "no raw transport error may reach a caller that already read content")
	require.NotNil(t, usage)
	require.Zero(t, usage.TotalTokens, "an unrepairable cut stays unbilled")

	body := recorder.Body.String()
	require.Contains(t, body, `"content":"已经发出的"`)
	require.Contains(t, body, `"content":"内容"`, "the tail the loop still held must reach the caller")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"length"`), "closed honestly as truncated")
	require.Equal(t, 1, strings.Count(body, "[DONE]"), "one [DONE]")
}

// TestOpenaiHandlerAbnormalFinishIsRetryable pins the non-streaming gap found on
// 2026-10-28 22:54: an upstream answered finish_reason=error after 85s with zero
// content, and the request ended as a customer-visible 502 because the error was
// marked non-retryable. Nothing had been written to a non-streaming caller, so
// another channel must be allowed to serve it.
func TestOpenaiHandlerAbnormalFinishIsRetryable(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3.1-pro"},
		IsStream:    false,
		RelayFormat: types.RelayFormatOpenAI,
	}
	info.SetEstimatePromptTokens(5)

	body := `{"id":"chat_1","object":"chat.completion","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	_, apiErr := OpenaiHandler(c, info, resp)
	require.NotNil(t, apiErr)
	require.False(t, types.IsSkipRetryError(apiErr), "a non-streaming abnormal finish must fall over to another channel")
}

// TestOaiStreamHandlerFallsBackToAnotherChannelWhenContinuationCannotStart pins
// the cross-channel fallback contract: when the owning channel cannot even start
// the resume round, the configured fallback is spent once and the answer is
// stitched from the other channel (2026-10-28 ch77 kept answering "Upstream
// completed without answer text", while other channels could still resume).
func TestOaiStreamHandlerFallsBackToAnotherChannelWhenContinuationCannotStart(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"前半段"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"答案"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	fallbackRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"后半段"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":3,"total_tokens":9}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	sameChannelCalls := 0
	sender := func(string, int) (*http.Response, error) {
		sameChannelCalls++
		return nil, errors.New("dial tcp: connection refused")
	}
	fallbackCalls := 0
	fallback := func(string, int) (*http.Response, error) {
		fallbackCalls++
		return continuationSSEResponse(fallbackRound), nil
	}
	c, info, recorder := newContinuationTestContext(t, sender, 1, 128000)
	info.ChatContinuation.Fallback = fallback

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.GreaterOrEqual(t, sameChannelCalls, 1, "the owning channel is always tried first")
	require.Equal(t, 1, fallbackCalls, "the cross-channel fallback is spent once")

	body := recorder.Body.String()
	require.Contains(t, body, "后半段")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`), "one terminal frame")
	require.Equal(t, 1, strings.Count(body, "[DONE]"), "one [DONE]")
}

// TestOaiStreamHandlerKeepsOneTerminalWhenFinishAndUsageAreSeparateChunks pins the
// standard OpenAI shape (finish chunk followed by a usage-only chunk) that the
// earlier fixtures missed: the provider terminal frame used to be forwarded before
// the repair decided, so the caller received finish_reason=length, then resumed
// content, then finish_reason=stop (2026-10-29 review, BLOCKER).
func TestOaiStreamHandlerKeepsOneTerminalWhenFinishAndUsageAreSeparateChunks(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"前半段"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"后半段"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":3,"total_tokens":9}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	sender := func(string, int) (*http.Response, error) { return continuationSSEResponse(secondRound), nil }
	c, info, recorder := newContinuationTestContext(t, sender, 1, 128000)

	_, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)

	body := recorder.Body.String()
	require.Contains(t, body, "后半段")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`), "one terminal frame")
	require.Zero(t, strings.Count(body, `"finish_reason":"length"`), "the provider terminal frame must be held, not forwarded")
	require.Equal(t, 1, strings.Count(body, "[DONE]"))
}

// TestOaiStreamHandlerDeliversHeldTailBeforeRepair pins the answer fragment the
// stream loop still held when the early return happened: the continuation prompt
// includes it, so dropping it left a silent hole in the caller's answer
// (2026-10-29 review, BLOCKER).
func TestOaiStreamHandlerDeliversHeldTailBeforeRepair(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"第一块"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"尾块"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"续写"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":3,"total_tokens":9}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	sender := func(string, int) (*http.Response, error) { return continuationSSEResponse(secondRound), nil }
	c, info, recorder := newContinuationTestContext(t, sender, 1, 128000)

	_, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)

	body := recorder.Body.String()
	require.Contains(t, body, "第一块")
	require.Contains(t, body, "尾块", "the held answer tail must reach the caller before the resumed round")
	require.Contains(t, body, "续写")
}

// TestOaiStreamHandlerRepairsTransportFailureAfterVisibleContent pins the transport
// fault shape (reset/timeout): the delivered prefix is resumed instead of leaving
// the caller with a bare EOF and no terminal frame (2026-10-29 review, HIGH).
func TestOaiStreamHandlerRepairsTransportFailureAfterVisibleContent(t *testing.T) {
	firstRound := io.MultiReader(
		strings.NewReader(strings.Join([]string{
			`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"第一块"},"finish_reason":null}]}`,
			`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"尾块"},"finish_reason":null}]}`,
			``,
		}, "\n")),
		streamErrorReader{err: errors.New("connection reset by peer")},
	)
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"续写"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":3,"total_tokens":9}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	sender := func(string, int) (*http.Response, error) { return continuationSSEResponse(secondRound), nil }
	c, info, recorder := newContinuationTestContext(t, sender, 1, 128000)

	_, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(firstRound),
	})
	require.Nil(t, err, "a transport cut after delivered content must be repaired or closed, not failed")

	body := recorder.Body.String()
	require.Contains(t, body, "尾块")
	require.Contains(t, body, "续写")
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`), "one terminal frame")
}

// TestOaiStreamHandlerCreditsResumedRoundToItsChannel pins the capability telemetry
// attribution: a round served by another channel must be credited to that channel,
// not to the channel that failed to start it (2026-10-29 review, H5).
func TestOaiStreamHandlerCreditsResumedRoundToItsChannel(t *testing.T) {
	const hopChannelID = 986777
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"前半段"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"后半段"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":3,"total_tokens":9}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	sender := func(string, int) (*http.Response, error) { return nil, errors.New("dial tcp: connection refused") }
	c, info, _ := newContinuationTestContext(t, sender, 1, 128000)
	tracker := &relaycommon.ContinuationRoundChannel{}
	info.ChatContinuation.RoundChannel = tracker
	info.ChatContinuation.Fallback = func(string, int) (*http.Response, error) {
		tracker.Set(hopChannelID)
		return continuationSSEResponse(secondRound), nil
	}

	_, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	stats := common.ContinuationCapabilitySnapshot()[hopChannelID]
	require.GreaterOrEqual(t, stats.Successes, int64(1), "the hop channel that produced the text must be credited")
}

// TestOaiStreamHandlerHopsWhenThinkingRoundFailsWithoutAnswer pins the 2026-10-29
// ch102 shape: the first cut is resumed on its own channel, the resume forwards
// reasoning and then dies, and nothing visible ever reached the caller. A second
// channel must get the chance to finish the answer instead of a 502.
func TestOaiStreamHandlerHopsWhenThinkingRoundFailsWithoutAnswer(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	dyingRound := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"reasoning_content":"继续思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	recoveredRound := strings.Join([]string{
		`data: {"id":"chat_3","choices":[{"index":0,"delta":{"content":"最终答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":4,"total_tokens":34}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	sameCalls, fallbackCalls := 0, 0
	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		sameCalls++
		return continuationSSEResponse(dyingRound), nil
	}, 1, 100, 1<<20)
	info.ChatContinuation.Abnormal.Fallback = func(string, int) (*http.Response, error) {
		fallbackCalls++
		return continuationSSEResponse(recoveredRound), nil
	}

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	t.Logf("sameCalls=%d fallbackCalls=%d err=%v", sameCalls, fallbackCalls, err)
	require.Equal(t, 1, sameCalls, "the dying channel gets one round")
	require.Equal(t, 1, fallbackCalls, "the fallback is spent exactly once")
	require.NotNil(t, usage)
	require.Contains(t, recorder.Body.String(), "最终答案")
	require.Equal(t, 5, usage.PromptTokens, "input is billed once")
	require.Equal(t, 14, usage.CompletionTokens, "reasoning from round one + hop round output")
}

// A round that already forwarded visible content must never hop: the caller is
// reading that answer and a second channel would duplicate it.
func TestOaiStreamHandlerDoesNotHopAfterForwardedContent(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	contentThenDie := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"content":"半句正文"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	sameCalls, fallbackCalls := 0, 0
	c, info, _ := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		sameCalls++
		return continuationSSEResponse(contentThenDie), nil
	}, 1, 100, 1<<20)
	info.ChatContinuation.Abnormal.Fallback = func(string, int) (*http.Response, error) {
		fallbackCalls++
		return continuationSSEResponse(contentThenDie), nil
	}
	_, _ = OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Equal(t, 1, sameCalls)
	require.Zero(t, fallbackCalls, "a forwarded answer prefix must not be resumed elsewhere")
}

// The output-cap resume has the same ch102-shaped gap as the reasoning resume:
// a round that dies after streaming only reasoning left the caller without new
// answer text, so another channel may still finish the answer.
func TestOaiStreamHandlerHopsWhenContentRoundFailsWithoutContent(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"role":"assistant","content":"开头"}}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	dyingRound := strings.Join([]string{
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{"reasoning_content":"继续思考"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	recoveredRound := strings.Join([]string{
		`data: {"id":"chat_3","choices":[{"index":0,"delta":{"content":"续写正文"},"finish_reason":null}]}`,
		`data: {"id":"chat_3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":4,"total_tokens":34}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	sameCalls, fallbackCalls := 0, 0
	c, info, recorder := newContinuationTestContext(t, func(string, int) (*http.Response, error) {
		sameCalls++
		return continuationSSEResponse(dyingRound), nil
	}, 2, 100)
	info.ChatContinuation.Fallback = func(string, int) (*http.Response, error) {
		fallbackCalls++
		return continuationSSEResponse(recoveredRound), nil
	}

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err, "a resume that only streamed reasoning must be retried on another channel")
	require.Equal(t, 1, sameCalls)
	require.Equal(t, 1, fallbackCalls)
	require.Contains(t, recorder.Body.String(), "续写正文")
	require.Equal(t, 5, usage.PromptTokens, "input is billed once")
}

// TestOaiStreamHandlerClosesBudgetCappedThinkingAsLength pins the 2026-10-29
// 20:48 shape (request …3qpGsABu): the caller set max_tokens=50, the model spent
// the whole budget thinking and reached [DONE] with no terminal finish_reason.
// The official endpoint answers this with finish_reason=length, empty content and
// no error (probed live), so the gateway must not turn the caller's own cap into
// a truncated_stream failure. The attempt is billable like any capped answer.
func TestOaiStreamHandlerClosesBudgetCappedThinkingAsLength(t *testing.T) {
	round := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"I'll respond in Chinese "},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"to the user asking about attention."},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":41,"completion_tokens":50,"total_tokens":91}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	calls := 0
	c, info, recorder := newContinuationTestContext(t, func(string, int) (*http.Response, error) {
		calls++
		return continuationSSEResponse(secondContinuationRoundForTest()), nil
	}, 1, 2)
	info.ClientMaxTokens = 50

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(round))
	require.Nil(t, err, "a client output cap is not a truncation")
	require.NotNil(t, usage)
	require.Equal(t, 0, calls, "no continuation: the budget is already spent")
	require.Equal(t, 50, usage.CompletionTokens)
	body := recorder.Body.String()
	require.Contains(t, body, `"finish_reason":"length"`)
	require.NotContains(t, body, "truncated_stream")
}

// TestOaiStreamHandlerDoesNotSendUsageAfterErrorFrame pins the 2026-10-29 20:48
// customer complaint: a truncated stream answered with `truncated_stream` and
// then a usage frame (prompt 41 / completion 54), which reads as "the answer
// failed but you were charged anyway". Once the caller is told the generation
// failed, the only frame that may follow is the end of the stream.
func TestOaiStreamHandlerDoesNotSendUsageAfterErrorFrame(t *testing.T) {
	round := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_content":"thinking but never answering"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":41,"completion_tokens":54,"total_tokens":95}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, info, recorder := newContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return continuationSSEResponse(secondContinuationRoundForTest()), nil
	}, 0, 2000)
	info.ShouldIncludeUsage = true
	info.ClientIncludeUsage = true

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(round))
	require.NotNil(t, usage)
	require.Zero(t, usage.CompletionTokens, "an undelivered answer settles at zero")
	require.NotNil(t, err, "the caller must still get a failure for settlement")
	body := recorder.Body.String()
	require.Contains(t, body, `"code":"truncated_stream"`)
	require.NotContains(t, body, `"usage"`, "no usage frame may follow the error frame")
	require.Contains(t, body, "[DONE]")
}

// TestOaiStreamHandlerHopsFirstAfterProviderWall pins the 2026-10-29 wall fix:
// when the cut being repaired is the provider wall (the request already ran most
// of a wall), the resume must go to another channel FIRST. All 106 abnormal
// failures in 30 minutes that night were >=890s, and same-channel resume retries
// never rescued one of them.
func TestOaiStreamHandlerHopsFirstAfterProviderWall(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"` + strings.Repeat("思考", 300) + `"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	answerRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"墙后续写答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":3,"total_tokens":28}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	sameChannelCalls := 0
	crossChannelCalls := 0
	c, info, recorder := newAbnormalContinuationTestContext(t, func(reasoning string, remaining int) (*http.Response, error) {
		sameChannelCalls++
		return continuationSSEResponse(answerRound), nil
	}, 1, 100, 1<<20)
	info.StartTime = time.Now().Add(-continuationHopFirstAfter - 30*time.Second)
	info.ChatContinuation.Abnormal.Fallback = func(reasoning string, remaining int) (*http.Response, error) {
		crossChannelCalls++
		return continuationSSEResponse(answerRound), nil
	}

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 1, crossChannelCalls, "the wall resume goes to another channel first")
	require.Equal(t, 0, sameChannelCalls, "the channel that just walled is not retried first")
	require.Contains(t, recorder.Body.String(), "墙后续写答案")
}

// A resume that was not triggered by the wall keeps the same-channel-first order
// (the channel that owns the prefix hits its prompt cache).
func TestOaiStreamHandlerKeepsSameChannelFirstBeforeWall(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"` + strings.Repeat("思考", 300) + `"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	answerRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"答卷"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":3,"total_tokens":28}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	sameChannelCalls := 0
	crossChannelCalls := 0
	c, info, recorder := newAbnormalContinuationTestContext(t, func(reasoning string, remaining int) (*http.Response, error) {
		sameChannelCalls++
		return continuationSSEResponse(answerRound), nil
	}, 1, 100, 1<<20)
	info.StartTime = time.Now().Add(-30 * time.Second)
	info.ChatContinuation.Abnormal.Fallback = func(reasoning string, remaining int) (*http.Response, error) {
		crossChannelCalls++
		return continuationSSEResponse(answerRound), nil
	}

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 1, sameChannelCalls)
	require.Equal(t, 0, crossChannelCalls)
	require.Contains(t, recorder.Body.String(), "答卷")
}
