package openai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOaiStreamHandlerKeepsUsageBeforeFinalEvent(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":11877,"completion_tokens":8,"total_tokens":11885,"prompt_tokens_details":{"cached_tokens":11776}}}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
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
			UpstreamModelName: "deepseek-v4-flash",
		},
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
	}
	info.SetEstimatePromptTokens(4672)

	usage, err := OaiStreamHandler(c, info, resp)

	require.Nil(t, err)
	require.Equal(t, 11877, usage.PromptTokens)
	require.Equal(t, 11776, usage.PromptTokensDetails.CachedTokens)
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `"usage"`))
	require.NotContains(t, recorder.Body.String(), `"prompt_tokens":4672`)
	require.Greater(t, strings.LastIndex(recorder.Body.String(), `"usage"`), strings.Index(recorder.Body.String(), `"finish_reason":"stop"`))
	require.Contains(t, recorder.Body.String(), `data: [DONE]`)
}

func TestOaiStreamHandlerRetainsCacheAcrossUsageEvents(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"gemini-3.7-flash","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_tokens_details":{"cached_tokens":80}}}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"gemini-3.7-flash","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110}}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"gemini-3.7-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
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
			UpstreamModelName: "gemini-3.7-flash",
		},
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
	}

	usage, err := OaiStreamHandler(c, info, resp)

	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 10, usage.CompletionTokens)
	require.Equal(t, 80, usage.PromptTokensDetails.CachedTokens)
	usageEvents := make([]string, 0, 2)
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"usage"`) {
			usageEvents = append(usageEvents, line)
		}
	}
	require.NotEmpty(t, usageEvents)
	require.Len(t, usageEvents, 1)
	assert.Contains(t, usageEvents[len(usageEvents)-1], `"cached_tokens":80`)
}

func TestPatchStreamUsageDataPreservesExtensionsAndStripsBillingUsage(t *testing.T) {
	data := `{"usage":{"prompt_tokens":1,"provider_extension":{"cache_tier":"ephemeral"},"billing_usage":{"source":"internal"}}}`
	usage := &dto.Usage{
		PromptTokens: 10,
		BillingUsage: dto.NewOpenAIChatBillingUsage(&dto.Usage{
			PromptTokens: 10,
		}),
	}

	patched, err := patchStreamUsageData(data, usage)
	require.NoError(t, err)

	var payload struct {
		Usage map[string]json.RawMessage `json:"usage"`
	}
	require.NoError(t, common.UnmarshalJsonStr(patched, &payload))
	assert.NotContains(t, payload.Usage, "billing_usage")
	assert.JSONEq(t, `{"cache_tier":"ephemeral"}`, string(payload.Usage["provider_extension"]))
	assert.Equal(t, "10", string(payload.Usage["prompt_tokens"]))
}
