package openai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestOaiStreamHandlerRejectsTopLevelUpstreamErrorEvent(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	for _, test := range []struct {
		name     string
		payload  string
		wantCode types.ErrorCode
		wantBody string
	}{
		{
			name:     "dflash capability",
			payload:  `{"error":{"message":"DFLASH speculative decoding does not support return_logprob yet.","type":"invalid_request_error"}}`,
			wantCode: types.ErrorCodeChannelUnsupportedFeature,
			wantBody: "",
		},
		{
			name:     "generic upstream error",
			payload:  `{"error":{"message":"upstream stream failed","type":"server_error","code":"server_error"}}`,
			wantCode: types.ErrorCode("server_error"),
			wantBody: "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
				IsStream:    true,
				RelayMode:   relayconstant.RelayModeChatCompletions,
				RelayFormat: types.RelayFormatOpenAI,
				DisablePing: true,
			}
			usage, err := OaiStreamHandler(c, info, &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("data: " + test.payload + "\n\ndata: [DONE]\n\n")),
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			})

			require.NotNil(t, usage)
			require.NotNil(t, err)
			require.Equal(t, test.wantCode, err.GetErrorCode())
			require.Empty(t, test.wantBody)
			require.Equal(t, http.StatusBadGateway, c.Writer.Status())
			require.Empty(t, recorder.Body.String())
		})
	}
}

func TestOaiStreamHandlerFormatsCyberPolicyForClaudeClients(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("data: {\"error\":{\"code\":\"cyber_policy\",\"message\":\"blocked\"},\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		IsStream:    true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatClaude,
		DisablePing: true,
	}

	usage, err := OaiStreamHandler(c, info, resp)
	require.NotNil(t, err)
	require.Equal(t, types.ErrorCode("cyber_policy"), err.GetErrorCode())
	require.Equal(t, 2, usage.PromptTokens)
	require.Contains(t, recorder.Body.String(), "event: error")
	require.Contains(t, recorder.Body.String(), `"type":"cyber_policy"`)
	require.NotContains(t, recorder.Body.String(), `"code":"cyber_policy"`)
}

func TestOaiStreamHandlerFormatsCyberPolicyForGeminiClients(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/generateContent", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("data: {\"error\":{\"code\":\"cyber_policy\",\"message\":\"blocked\"}}\n\ndata: [DONE]\n\n")),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		IsStream:    true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatGemini,
		DisablePing: true,
	}

	_, err := OaiStreamHandler(c, info, resp)
	require.NotNil(t, err)
	require.Contains(t, recorder.Body.String(), `"status":"CYBER_POLICY"`)
	require.Contains(t, recorder.Body.String(), `"code":200`)
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

func TestOaiStreamHandlerEstimatesCompletionForPromptOnlyUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"gemini-3.7-flash","choices":[{"index":0,"delta":{"content":"partial streamed answer before the final usage event"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"gemini-3.7-flash","choices":[],"usage":{"prompt_tokens":151,"completion_tokens":0,"total_tokens":151,"billing_usage":{"source":"oai_chat","semantic":"openai","openai_usage":{"prompt_tokens":151,"completion_tokens":0,"total_tokens":151}}}}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"gemini-3.7-flash","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
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

	usage, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 151, usage.PromptTokens)
	require.Greater(t, usage.CompletionTokens, 0)
	require.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	require.NotNil(t, usage.BillingUsage)
	require.NotNil(t, usage.BillingUsage.OpenAIUsage)
	require.Equal(t, usage.CompletionTokens, usage.BillingUsage.OpenAIUsage.CompletionTokens)
	require.Equal(t, usage.TotalTokens, usage.BillingUsage.OpenAIUsage.TotalTokens)
	require.True(t, usage.BillingUsage.Estimated)
	require.True(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
	require.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
	require.Equal(t, "length", info.StreamFinishReason)
	assert.Contains(t, recorder.Body.String(), `partial streamed answer before the final usage event`)
	assert.Contains(t, recorder.Body.String(), `"finish_reason":"length"`)
	assert.Contains(t, recorder.Body.String(), `"completion_tokens":`+strconv.Itoa(usage.CompletionTokens))
	assert.NotContains(t, recorder.Body.String(), `"billing_usage"`)
}

func TestOpenaiHandlerEstimatesCompletionForPromptOnlyUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3.7-flash",
		},
		RelayFormat: types.RelayFormatOpenAI,
	}
	body := `{"id":"chat_1","object":"chat.completion","created":1710000000,"model":"gemini-3.7-flash","choices":[{"index":0,"message":{"role":"assistant","content":"complete buffered answer"},"finish_reason":"length"}],"usage":{"prompt_tokens":151,"completion_tokens":0,"total_tokens":151}}`

	usage, err := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	})

	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Greater(t, usage.CompletionTokens, 0)
	require.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	require.NotNil(t, usage.BillingUsage)
	require.NotNil(t, usage.BillingUsage.OpenAIUsage)
	require.True(t, usage.BillingUsage.Estimated)
	require.Equal(t, usage.CompletionTokens, usage.BillingUsage.OpenAIUsage.CompletionTokens)
	assert.Contains(t, recorder.Body.String(), `"finish_reason":"length"`)
	assert.Contains(t, recorder.Body.String(), `"completion_tokens":`+strconv.Itoa(usage.CompletionTokens))
	assert.NotContains(t, recorder.Body.String(), `"billing_usage"`)
}

func TestOaiStreamHandlerPromotesExactCompletionFromUpstreamUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chat_1","choices":[],"usage":{"prompt_tokens":151,"completion_tokens":0,"total_tokens":151,"billing_usage":{"source":"oai_chat","semantic":"openai","openai_usage":{"prompt_tokens":151,"completion_tokens":0,"output_tokens":7,"total_tokens":158}}}}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.7-flash"},
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
	}
	usage, err := OaiStreamHandler(c, info, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))})
	require.Nil(t, err)
	require.Equal(t, 7, usage.CompletionTokens)
	require.Equal(t, 158, usage.TotalTokens)
	require.False(t, usage.BillingUsage.Estimated)
	require.False(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
}

func TestOaiStreamHandlerPromotesCompletionFromUpstreamTotal(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":null}],"usage":{"prompt_tokens":151,"completion_tokens":0,"total_tokens":158}}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.7-flash"},
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
	}
	usage, err := OaiStreamHandler(c, info, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))})
	require.Nil(t, err)
	require.Equal(t, 7, usage.CompletionTokens)
	require.Equal(t, 158, usage.TotalTokens)
	require.False(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
}

func TestOaiStreamHandlerPromotesTopLevelOutputTokens(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":null}],"usage":{"prompt_tokens":151,"completion_tokens":0,"output_tokens":9,"total_tokens":160}}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.7-flash"},
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
	}
	usage, err := OaiStreamHandler(c, info, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))})
	require.Nil(t, err)
	require.Equal(t, 9, usage.CompletionTokens)
	require.Equal(t, 160, usage.TotalTokens)
	require.False(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
}

func TestOaiStreamHandlerKeepsZeroCompletionWithoutOutput(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chat_1","choices":[],"usage":{"prompt_tokens":151,"completion_tokens":0,"total_tokens":151}}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.7-flash"},
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
	}
	usage, err := OaiStreamHandler(c, info, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))})
	require.Nil(t, err)
	require.Zero(t, usage.CompletionTokens)
	require.Equal(t, 151, usage.TotalTokens)
	require.False(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
}

func TestOaiStreamHandlerEmitsUsageOnlyWhenRequestedAndPreservesChoices(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	tests := []struct {
		name                  string
		shouldIncludeUsage    bool
		body                  string
		wantUsageEvents       int
		wantFinishBeforeUsage bool
	}{
		{
			name:               "usage disabled",
			shouldIncludeUsage: false,
			body: strings.Join([]string{
				`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"test-model","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null}]}`,
				`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"test-model","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
				`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				`data: [DONE]`,
				``,
			}, "\n"),
			wantUsageEvents: 0,
		},
		{
			name:               "usage alongside finish choices",
			shouldIncludeUsage: true,
			body: strings.Join([]string{
				`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"test-model","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null}]}`,
				`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
				`data: [DONE]`,
				``,
			}, "\n"),
			wantUsageEvents:       1,
			wantFinishBeforeUsage: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			}
			info := &relaycommon.RelayInfo{
				ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
				IsStream:           true,
				RelayMode:          relayconstant.RelayModeChatCompletions,
				RelayFormat:        types.RelayFormatOpenAI,
				ShouldIncludeUsage: tt.shouldIncludeUsage,
				DisablePing:        true,
			}

			usage, err := OaiStreamHandler(c, info, resp)
			require.Nil(t, err)
			require.NotNil(t, usage)
			assert.Equal(t, 10, usage.PromptTokens)
			assert.Equal(t, tt.wantUsageEvents, strings.Count(recorder.Body.String(), `"usage"`))
			assert.Contains(t, recorder.Body.String(), `"finish_reason":"stop"`)
			assert.Equal(t, 1, strings.Count(recorder.Body.String(), `"finish_reason":"stop"`))
			if tt.wantFinishBeforeUsage {
				assert.Greater(t, strings.LastIndex(recorder.Body.String(), `"usage"`), strings.Index(recorder.Body.String(), `"finish_reason":"stop"`))
			}
		})
	}
}

func TestOaiStreamHandlerDoesNotDuplicateChoicesWhenUsageSharesFinalChunk(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"test-model","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
	}

	usage, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})
	require.Nil(t, err)
	require.NotNil(t, usage)

	got := recorder.Body.String()
	assert.Equal(t, 1, strings.Count(got, `"content":"hello"`))
	assert.Equal(t, 1, strings.Count(got, `"finish_reason":"stop"`))
	assert.Equal(t, 1, strings.Count(got, `"usage"`))
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

func TestOpenaiHandlerStripsBillingUsageFromNormalResponse(t *testing.T) {
	body := `{"id":"chatcmpl_1","object":"chat.completion","created":1710000000,"model":"gpt-test","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5,"provider_extension":{"tier":"standard"},"billing_usage":{"source":"internal"}}}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		RelayFormat: types.RelayFormatOpenAI,
	}

	usage, err := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
	})

	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.TotalTokens)
	assert.NotContains(t, recorder.Body.String(), `billing_usage`)
	assert.Contains(t, recorder.Body.String(), `"provider_extension":{"tier":"standard"}`)
}

func TestOpenaiHandlerStripsBillingUsageWhenForceFormatting(t *testing.T) {
	body := `{"id":"chatcmpl_1","object":"chat.completion","created":1710000000,"model":"gpt-test","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5,"billing_usage":{"source":"internal"}}}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
			ChannelSetting:    dto.ChannelSettings{ForceFormat: true},
		},
		RelayFormat: types.RelayFormatOpenAI,
	}

	_, err := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
	})

	require.Nil(t, err)
	assert.NotContains(t, recorder.Body.String(), `billing_usage`)
}
