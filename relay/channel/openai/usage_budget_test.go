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

// TestOaiStreamHandlerClampsUsageToClientBudget pins the Phase-B rule on the
// streaming path: an upstream that accumulated internal rounds (completion 500
// against a declared max_tokens of 100) must reach the client capped, and the
// handler must hand the same capped usage to settlement. The raw upstream count
// stays on RelayInfo for the alarm-only deviation marker.
func TestOaiStreamHandlerClampsUsageToClientBudget(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[],"usage":{"prompt_tokens":50,"completion_tokens":500,"total_tokens":550}}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
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
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
		ClientMaxTokens:    100,
	}
	info.SetEstimatePromptTokens(50)

	usage, err := OaiStreamHandler(c, info, resp)

	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 100, usage.CompletionTokens, "settled usage must be capped")
	require.Equal(t, 150, usage.TotalTokens)
	require.Equal(t, 500, info.UpstreamRawCompletionTokens, "raw count must stay available to the deviation guard")
	require.Contains(t, recorder.Body.String(), `"completion_tokens":100`)
	require.NotContains(t, recorder.Body.String(), `"completion_tokens":500`)
}

// TestOpenaiHandlerClampsUsageToClientBudget is the non-stream sibling: the
// forwarded body and the returned usage must both carry the capped count.
func TestOpenaiHandlerClampsUsageToClientBudget(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	body := `{"id":"chat_1","object":"chat.completion","created":1710000000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":500,"total_tokens":550}}`

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     types.RelayFormatOpenAI,
		ClientMaxTokens: 100,
	}
	info.SetEstimatePromptTokens(50)

	usage, err := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	})

	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 100, usage.CompletionTokens)
	require.Equal(t, 150, usage.TotalTokens)
	require.Equal(t, 500, info.UpstreamRawCompletionTokens)
	require.Contains(t, recorder.Body.String(), `"completion_tokens":100`)
	require.NotContains(t, recorder.Body.String(), `"completion_tokens":500`)
}
