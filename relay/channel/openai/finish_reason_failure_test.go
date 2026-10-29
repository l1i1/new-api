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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOaiStreamHandlerTreatsAbnormalFinishReasonAsFailure pins the A fix: a
// stream that ends with a provider failure marker instead of a terminal
// finish_reason ("error" from aggregators, "engine_overloaded" from the
// official Moonshot endpoint) reached [DONE] but did not deliver a finished
// answer, so the handler must return an error (the controller then refunds
// instead of settling it as a success).
func TestOaiStreamHandlerTreatsAbnormalFinishReasonAsFailure(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	for _, reason := range []string{"error", "engine_overloaded"} {
		t.Run(reason, func(t *testing.T) {
			body := strings.Join([]string{
				`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"reasoning_content":"thinking"},"finish_reason":null}]}`,
				`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"` + reason + `"}]}`,
				`data: [DONE]`,
				``,
			}, "\n")

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
				IsStream:    true,
				RelayMode:   relayconstant.RelayModeChatCompletions,
				RelayFormat: types.RelayFormatOpenAI,
				DisablePing: true,
			}
			info.SetEstimatePromptTokens(10)

			usage, err := OaiStreamHandler(c, info, &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			})

			require.NotNil(t, err, "an abnormal finish reason must not be reported as success")
			require.Equal(t, http.StatusBadGateway, err.StatusCode)
			require.NotNil(t, usage)
			assert.Zero(t, usage.TotalTokens, "a failed generation must return zero usage to the caller")
			assert.Equal(t, relaycommon.UndeliveredAbnormalFinish, info.UndeliveredReason)
			require.Equal(t, reason, info.StreamFinishReason)
			require.NotNil(t, info.StreamStatus)
			require.True(t, info.StreamStatus.ResponseFailed())
			require.False(t, info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors())
			require.True(t, err.ShouldEvictChannelAffinity(), "an abnormal finish must drop the affinity binding")
			require.False(t, err.SettlesObservedUsageAfterError(), "an abnormal finish is refunded, not settled")
			// The failure row has to explain the cut without container logs.
			require.NotNil(t, info.StreamCutDiagnostics, "an undelivered cut must carry diagnostics for the error row")
			assert.Equal(t, reason, info.StreamCutDiagnostics.FinishReason)
			assert.False(t, info.StreamCutDiagnostics.MissingFinish)
			assert.Zero(t, info.StreamCutDiagnostics.VisibleContentChars)
			assert.Zero(t, info.StreamCutDiagnostics.ReasoningChars, "reasoning is only captured when a continuation mode is configured")
			assert.False(t, info.StreamCutDiagnostics.ContinuationConfigured, "this request had no continuation configured")
			assert.False(t, info.StreamCutDiagnostics.RepairAttempted)
		})
	}
}

// TestOpenaiHandlerTreatsAbnormalFinishReasonAsFailure is the non-stream
// sibling: the body must not be forwarded as a successful completion.
func TestOpenaiHandlerTreatsAbnormalFinishReasonAsFailure(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	body := `{"id":"chat_1","object":"chat.completion","created":1710000000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"engine_overloaded"}],"usage":{"prompt_tokens":10,"completion_tokens":16000,"total_tokens":16010}}`

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
	}
	info.SetEstimatePromptTokens(10)

	_, err := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	})

	require.NotNil(t, err)
	require.Equal(t, http.StatusBadGateway, err.StatusCode)
	require.NotContains(t, recorder.Body.String(), "engine_overloaded")
	require.True(t, err.ShouldEvictChannelAffinity())
	require.False(t, err.SettlesObservedUsageAfterError())
}

func TestIsAbnormalFinishReason(t *testing.T) {
	normal := []string{"stop", "length", "tool_calls", "function_call", "content_filter", " STOP "}
	for _, r := range normal {
		require.False(t, relaycommon.IsAbnormalFinishReason(r), r)
	}
	abnormal := []string{"error", "engine_overloaded", "insufficient_system_resource", "timeout"}
	for _, r := range abnormal {
		require.True(t, relaycommon.IsAbnormalFinishReason(r), r)
	}
}

func newMissingFinishTestContext(t *testing.T) (*gin.Context, *relaycommon.RelayInfo, *httptest.ResponseRecorder) {
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
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3.1-pro"},
		IsStream:    true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
		DisablePing: true,
	}
	info.SetEstimatePromptTokens(10)
	return c, info, recorder
}

// TestOaiStreamHandlerTreatsMissingFinishReasonAsFailure pins the second half of
// the A contract. The reported 2026-10-27 case: an aggregator channel (ch77)
// ended the stream at 60s with a usage-only event and [DONE], while the reasoning
// text stopped mid-sentence and no finish_reason appeared at all. [DONE] is not
// proof of a finished answer, so the handler must fail the request (the
// controller refunds instead of settling) rather than bill the truncated output.
func TestOaiStreamHandlerTreatsMissingFinishReasonAsFailure(t *testing.T) {
	cases := []struct {
		name string
		body []string
		// A cut that delivered answer text is closed honestly as a truncated
		// answer (finish_reason=length, zero usage) instead of an error frame; a
		// reasoning-only cut delivered no answer and keeps the error.
		expectError bool
	}{
		{
			name:        "done_with_usage_and_no_finish",
			expectError: true,
			body: []string{
				`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"I need to respond"},"finish_reason":null}]}`,
				`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[],"usage":{"prompt_tokens":167674,"completion_tokens":237,"total_tokens":167911}}`,
				`data: [DONE]`,
				``,
			},
		},
		{
			name:        "eof_without_done_and_no_finish",
			expectError: false,
			body: []string{
				`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"partial answer"},"finish_reason":null}]}`,
				``,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, info, recorder := newMissingFinishTestContext(t)
			body := strings.Join(tc.body, "\n")

			usage, err := OaiStreamHandler(c, info, &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			})

			require.NotNil(t, usage)
			assert.Zero(t, usage.PromptTokens, "a truncated stream must return zero usage to the caller")
			assert.Zero(t, usage.CompletionTokens)
			assert.Equal(t, relaycommon.UndeliveredMissingFinish, info.UndeliveredReason)
			require.NotNil(t, info.StreamCutDiagnostics, "a missing terminal frame must carry diagnostics for the error row")
			assert.True(t, info.StreamCutDiagnostics.MissingFinish)
			assert.Empty(t, info.StreamCutDiagnostics.FinishReason)
			assert.NotContains(t, recorder.Body.String(), "167674", "the upstream usage count must never reach the caller unzeroed")
			require.NotNil(t, info.StreamStatus)
			require.True(t, info.StreamStatus.ResponseFailed())
			if tc.expectError {
				require.NotNil(t, err, "a silent cut with no delivered answer must not be reported as success")
				require.Equal(t, http.StatusBadGateway, err.StatusCode)
				require.Empty(t, info.StreamFinishReason)
				require.True(t, err.ShouldEvictChannelAffinity(), "a truncated stream must drop the affinity binding")
				require.False(t, err.SettlesObservedUsageAfterError(), "a truncated stream is refunded, not settled")
				// The client must be able to tell that nothing was delivered: a bare
				// [DONE] after zero content reads as a successful empty answer.
				assert.Contains(t, recorder.Body.String(), "truncated_stream", "an unrecoverable silent cut must be surfaced as an error frame")
			} else {
				require.Nil(t, err, "a cut after delivered answer text is closed, not failed")
				require.Equal(t, "length", info.StreamFinishReason)
				assert.Contains(t, recorder.Body.String(), `"finish_reason":"length"`, "the caller must be told the answer was cut")
				assert.NotContains(t, recorder.Body.String(), "truncated_stream")
			}
		})
	}
}

// TestOaiStreamHandlerAcceptsTerminalFinishReason is the control: the same shape
// with a real terminal finish_reason must still succeed.
func TestOaiStreamHandlerAcceptsTerminalFinishReason(t *testing.T) {
	c, info, _ := newMissingFinishTestContext(t)
	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")

	_, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	require.Nil(t, err)
	require.Equal(t, "stop", info.StreamFinishReason)
	require.False(t, info.StreamStatus.ResponseFailed())
}
