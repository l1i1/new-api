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

			_, err := OaiStreamHandler(c, info, &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			})

			require.NotNil(t, err, "an abnormal finish reason must not be reported as success")
			require.Equal(t, http.StatusBadGateway, err.StatusCode)
			require.Equal(t, reason, info.StreamFinishReason)
			require.NotNil(t, info.StreamStatus)
			require.True(t, info.StreamStatus.ResponseFailed())
			require.False(t, info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors())
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
