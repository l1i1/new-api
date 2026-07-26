package xai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestXAIStreamHandlerPreservesCachedTokenUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"grok-4.5","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"grok-4.5","choices":[],"usage":{"prompt_tokens":7424,"completion_tokens":5,"total_tokens":7432,"prompt_tokens_details":{"cached_tokens":7296},"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "grok-4.5",
		},
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
	}

	usage, err := xAIStreamHandler(c, info, resp)

	require.Nil(t, err)
	require.Equal(t, 7424, usage.PromptTokens)
	require.Equal(t, 7296, usage.PromptTokensDetails.CachedTokens)
	require.Equal(t, 8, usage.CompletionTokens)
	require.Equal(t, 2, usage.CompletionTokenDetails.ReasoningTokens)
	require.Equal(t, 6, usage.CompletionTokenDetails.TextTokens)
	require.Contains(t, recorder.Body.String(), `"cached_tokens":7296`)
	require.Contains(t, recorder.Body.String(), `"completion_tokens":8`)
	require.Contains(t, recorder.Body.String(), `data: [DONE]`)
}

func TestXAIStreamHandlerClampsInvalidUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"grok-4.5","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":99,"total_tokens":5,"prompt_tokens_details":{"cached_tokens":20},"completion_tokens_details":{"reasoning_tokens":7}}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "grok-4.5"},
		IsStream:    true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
		DisablePing: true,
	}

	usage, err := xAIStreamHandler(c, info, resp)

	require.Nil(t, err)
	require.Equal(t, 10, usage.PromptTokens)
	require.Equal(t, 10, usage.TotalTokens)
	require.Zero(t, usage.CompletionTokens)
	require.Zero(t, usage.CompletionTokenDetails.ReasoningTokens)
	require.Zero(t, usage.CompletionTokenDetails.TextTokens)
	require.Equal(t, 10, usage.PromptTokensDetails.CachedTokens)
}

func TestXAIHandlerClampsInvalidUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chat_1","object":"chat.completion","created":1710000000,"model":"grok-4.5","choices":[],"usage":{"prompt_tokens":-3,"completion_tokens":99,"total_tokens":-8,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":2}}}`,
		)),
		Header: http.Header{"Content-Type": []string{"application/json"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "grok-4.5"},
	}

	usage, err := xAIHandler(c, info, resp)

	require.Nil(t, err)
	require.Zero(t, usage.PromptTokens)
	require.Zero(t, usage.TotalTokens)
	require.Zero(t, usage.CompletionTokens)
	require.Zero(t, usage.CompletionTokenDetails.ReasoningTokens)
	require.Zero(t, usage.CompletionTokenDetails.TextTokens)
	require.Zero(t, usage.PromptTokensDetails.CachedTokens)
	require.Contains(t, recorder.Body.String(), `"total_tokens":0`)
}
