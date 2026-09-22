package openai

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type streamErrorReader struct {
	err error
}

func (r streamErrorReader) Read([]byte) (int, error) {
	return 0, r.err
}

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
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"gemini-3.7-flash","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`,
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
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`,
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

func TestOaiStreamHandlerForwardsEmptyFinalOutput(t *testing.T) {
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
	require.NotNil(t, usage)
	require.Zero(t, usage.CompletionTokens)
	require.Equal(t, 151, usage.TotalTokens)
	require.False(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
	require.Contains(t, recorder.Body.String(), `"finish_reason":"stop"`)
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
	require.Nil(t, err)

	var payload struct {
		Usage map[string]json.RawMessage `json:"usage"`
	}
	require.NoError(t, common.UnmarshalJsonStr(patched, &payload))
	assert.NotContains(t, payload.Usage, "billing_usage")
	assert.JSONEq(t, `{"cache_tier":"ephemeral"}`, string(payload.Usage["provider_extension"]))
	assert.Equal(t, "10", string(payload.Usage["prompt_tokens"]))
}

func TestOpenaiHandlerStripsBillingUsageFromNormalResponse(t *testing.T) {
	body := `{"id":"chatcmpl_1","object":"chat.completion","created":1710000000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5,"provider_extension":{"tier":"standard"},"billing_usage":{"source":"internal"}}}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		RelayMode:   relayconstant.RelayModeChatCompletions,
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
	body := `{"id":"chatcmpl_1","object":"chat.completion","created":1710000000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5,"billing_usage":{"source":"internal"}}}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
			ChannelSetting:    dto.ChannelSettings{ForceFormat: true},
		},
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
	}

	_, err := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
	})

	require.Nil(t, err)
	assert.NotContains(t, recorder.Body.String(), `billing_usage`)
}

func TestOpenaiHandlerDeepSeekV4FitsForceFormattedUsage(t *testing.T) {
	body := `{"id":"chatcmpl_1","object":"chat.completion","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":99,"input_tokens":2,"output_tokens":3,"claude_cache_creation_1_h_tokens":1}}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "deepseek-v4-flash",
			ChannelSetting:    dto.ChannelSettings{ForceFormat: true},
		},
		OriginModelName: "deepseek-v4-flash",
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     types.RelayFormatOpenAI,
		UserSetting: dto.UserSetting{OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"deepseek-v4-": {Validate: true, Shape: true},
		}}},
	}

	usage, err := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
	})

	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 99, usage.TotalTokens, "client response fitting must not rewrite billing usage")
	responseBody := recorder.Body.String()
	assert.Contains(t, responseBody, `"total_tokens":5`)
	assert.Contains(t, responseBody, `"prompt_cache_hit_tokens"`)
	assert.Contains(t, responseBody, `"completion_tokens_details"`)
	assert.NotContains(t, responseBody, `"input_tokens"`)
	assert.NotContains(t, responseBody, `"output_tokens"`)
	assert.NotContains(t, responseBody, `"claude_cache_creation"`)
	assert.NotContains(t, responseBody, `"system_fingerprint"`, "fingerprint is never fabricated")
}

func TestOpenaiHandlerForwardsEmptyFinalOutput(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
	}

	usage, err := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":256,"total_tokens":266}}`)),
	})

	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 10, usage.PromptTokens)
	assert.Equal(t, 256, usage.CompletionTokens)
	assert.True(t, recorder.Flushed)
	assert.Contains(t, recorder.Body.String(), `"content":""`)
}

func TestOpenaiHandlerAcceptsValidFunctionToolCallWithoutContent(t *testing.T) {
	body := `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`
	var parsed dto.OpenAITextResponse
	require.NoError(t, common.Unmarshal([]byte(body), &parsed))
	require.Len(t, parsed.Choices, 1)
	require.Len(t, parsed.Choices[0].Message.ParseToolCalls(), 1)
	assert.Equal(t, "lookup", parsed.Choices[0].Message.ParseToolCalls()[0].Function.Name)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
	}

	usage, err := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
	})

	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Contains(t, recorder.Body.String(), `"finish_reason":"tool_calls"`)
	assert.Contains(t, recorder.Body.String(), `"name":"lookup"`)
}

func TestOpenaiHandlerAcceptsReasoningOnlyStopOutput(t *testing.T) {
	// A reasoning model can match a stop word while still thinking and end
	// with content empty + reasoning non-empty + finish_reason=stop; the
	// official endpoints return 200 for that shape (e.g. GLM-5.3 stop
	// probes), so the empty-output guard must not turn it into a 502.
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
		OriginModelName: "deepseek-v4-flash",
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     types.RelayFormatOpenAI,
	}

	usage, err := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_content":"internal reasoning"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":256,"total_tokens":266}}`)),
	})

	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Contains(t, recorder.Body.String(), `"reasoning_content":"internal reasoning"`)
	assert.Contains(t, recorder.Body.String(), `"finish_reason":"stop"`)
}

func TestOpenaiHandlerAcceptsDeepSeekV4ReasoningOnlyLengthOutput(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
		OriginModelName: "deepseek-v4-flash",
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     types.RelayFormatOpenAI,
	}

	usage, err := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":"internal reasoning"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":256,"total_tokens":266}}`)),
	})

	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Contains(t, recorder.Body.String(), `"reasoning_content":"internal reasoning"`)
	assert.Contains(t, recorder.Body.String(), `"finish_reason":"length"`)
}

func TestOpenaiHandlerForwardsDeepSeekV4ReasoningOnlyLengthWhenThinkingDisabled(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
		OriginModelName: "deepseek-v4-flash",
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     types.RelayFormatOpenAI,
		Request:         &dto.GeneralOpenAIRequest{THINKING: json.RawMessage(`{"type":"disabled"}`)},
	}

	usage, err := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":"internal reasoning"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":256,"total_tokens":266}}`)),
	})

	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 10, usage.PromptTokens)
	assert.Equal(t, 256, usage.CompletionTokens)
	assert.NotContains(t, recorder.Body.String(), "reasoning_content")
	assert.Contains(t, recorder.Body.String(), `"finish_reason":"length"`)
}

func TestOpenaiHandlerSuppressesReasoningWhenDisabled(t *testing.T) {
	body := `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"answer","reasoning_content":"internal reasoning"},"logprobs":{"content":[{"token":"answer"}],"reasoning_content":[{"token":"internal"}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
		Request:     &dto.GeneralOpenAIRequest{THINKING: json.RawMessage(`{"type":"disabled"}`)},
	}
	_, err := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
	})

	require.Nil(t, err)
	assert.Contains(t, recorder.Body.String(), `"content":"answer"`)
	assert.Contains(t, recorder.Body.String(), `"token":"answer"`)
	assert.NotContains(t, recorder.Body.String(), "reasoning_content")
}

func TestOaiStreamHandlerForwardsReasoningOnlyOutputAfterCommit(t *testing.T) {
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
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
		IsStream:    true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
		Request:     &dto.GeneralOpenAIRequest{THINKING: json.RawMessage(`{"type":"disabled"}`)},
		DisablePing: true,
	}
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"internal reasoning"},"logprobs":{"content":[{"token":"answer"}],"reasoning_content":[{"token":"internal"}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	usage, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	require.NotNil(t, usage)
	require.Nil(t, err)
	assert.True(t, recorder.Flushed)
	assert.Contains(t, recorder.Body.String(), `"token":"answer"`)
	assert.NotContains(t, recorder.Body.String(), "reasoning_content")
}

func TestOaiStreamHandlerAcceptsEOFWithoutDone(t *testing.T) {
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
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
		IsStream:    true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
		DisablePing: true,
	}
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" tail"},"finish_reason":null}]}`,
		``,
	}, "\n")

	usage, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	require.NotNil(t, usage)
	require.Nil(t, err)
	assert.Equal(t, relaycommon.StreamEndReasonEOF, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.IsNormalEnd())
	assert.Contains(t, recorder.Body.String(), `data: [DONE]`)
}

func TestOaiStreamHandlerRejectsScannerErrorAfterPartialData(t *testing.T) {
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
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
		OriginModelName:    "test-model",
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
	}
	info.SetEstimatePromptTokens(12)
	operation_setting.SetToolPriceForTest("lookup_partial", 5)
	t.Cleanup(func() { operation_setting.DeleteToolPriceForTest("lookup_partial") })
	readerErr := errors.New("stream reset by upstream CDN")
	body := io.MultiReader(
		strings.NewReader(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup_partial","arguments":"{}"}}]},"finish_reason":null}]}`+"\n"),
		streamErrorReader{err: readerErr},
	)

	usage, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(body),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	require.NotNil(t, usage)
	require.Error(t, err)
	assert.Equal(t, 12, usage.PromptTokens)
	assert.Greater(t, usage.CompletionTokens, 0)
	assert.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	assert.Equal(t, types.ErrorCode("server_error"), err.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(err))
	assert.Equal(t, relaycommon.StreamEndReasonScannerErr, info.StreamStatus.EndReason)
	assert.Contains(t, err.Error(), "stream reset by upstream CDN")
	assert.NotContains(t, recorder.Body.String(), `data: [DONE]`)
	require.NotNil(t, info.ResponsesUsageInfo)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, "lookup_partial")
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools["lookup_partial"].CallCount)
}

func TestOaiStreamHandlerAllowsRetryBeforeAnyUpstreamData(t *testing.T) {
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
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
		IsStream:    true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
		DisablePing: true,
	}
	readerErr := errors.New("upstream connection failed before response")

	usage, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(streamErrorReader{err: readerErr}),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	require.NotNil(t, usage)
	require.Error(t, err)
	assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	assert.Equal(t, types.ErrorCode("server_error"), err.GetErrorCode())
	assert.False(t, types.IsSkipRetryError(err))
	assert.Zero(t, info.ReceivedResponseCount)
	assert.Equal(t, relaycommon.StreamEndReasonScannerErr, info.StreamStatus.EndReason)
	assert.Contains(t, err.Error(), "upstream connection failed before response")
	assert.NotContains(t, recorder.Body.String(), `data: [DONE]`)
}

func TestOaiStreamHandlerTreatsClientGoneAsNonError(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
		OriginModelName:    "deepseek-v4-flash",
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
	}
	info.SetEstimatePromptTokens(11)
	body := io.MultiReader(
		strings.NewReader(`data: {"id":"chat_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"par"},"finish_reason":null}]}`+"\n"),
		readerFunc(func(p []byte) (int, error) {
			// The caller abandoned the request mid-stream: the cancelled request
			// context closes the upstream body, so the next read reports it.
			cancel()
			return 0, context.Canceled
		}),
	)

	usage, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(body),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	require.Nil(t, err, "a caller that disconnected mid-stream must not surface a gateway error")
	require.NotNil(t, usage)
	assert.Equal(t, 11, usage.PromptTokens)
	assert.Greater(t, usage.CompletionTokens, 0, "observed partial output stays settled")
	assert.NotContains(t, recorder.Body.String(), `data: [DONE]`, "an abandoned stream never earns a success marker")
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestOaiStreamHandlerAcceptsValidFunctionToolCallWithoutContent(t *testing.T) {
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
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-flash"},
		IsStream:    true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
		DisablePing: true,
	}
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		``,
	}, "\n\n")
	var parsed dto.ChatCompletionsStreamResponse
	require.NoError(t, common.UnmarshalJsonStr(strings.TrimPrefix(strings.Split(body, "\n\n")[0], "data: "), &parsed))
	require.Len(t, parsed.Choices, 1)
	require.Len(t, parsed.Choices[0].Delta.ToolCalls, 1)
	require.True(t, isValidStreamFunctionToolCall(parsed.Choices[0].Delta.ToolCalls[0]))

	usage, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Contains(t, recorder.Body.String(), `"name":"lookup"`)
	assert.Contains(t, recorder.Body.String(), `data: [DONE]`)
}

func TestOpenaiHandlerDoesNotApplyChatOutputCheckToCompletions(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "text-model"},
		RelayMode:   relayconstant.RelayModeCompletions,
		RelayFormat: types.RelayFormatOpenAI,
	}

	usage, err := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"id":"cmpl_1","object":"text_completion","choices":[{"index":0,"text":"answer","finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)),
	})

	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Contains(t, recorder.Body.String(), `"text":"answer"`)
}

func TestDeepSeekThinkingLogprobsRequireBothOutputStreams(t *testing.T) {
	logprobs := any(map[string]any{"content": []any{map[string]any{"token": "2"}}})
	choices := []dto.OpenAITextResponseChoice{{Logprobs: &logprobs}}
	info := &relaycommon.RelayInfo{
		OriginModelName: "deepseek-v4-flash",
		RelayMode:       relayconstant.RelayModeChatCompletions,
		Request:         &dto.GeneralOpenAIRequest{LogProbs: boolPtr(true)},
		UserSetting: dto.UserSetting{OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"deepseek-v4-": {Validate: true},
		}}},
	}

	assert.True(t, requiresDeepSeekV4ReasoningLogprobs(info))
	assert.False(t, hasBothChatLogprobs(choices))
	assert.Equal(t, types.ErrorCodeChannelUnsupportedFeature, missingReasoningLogprobsError().GetErrorCode())

	logprobs = map[string]any{
		"content":           []any{map[string]any{"token": "2"}},
		"reasoning_content": []any{map[string]any{"token": "="}},
	}
	assert.True(t, hasBothChatLogprobs([]dto.OpenAITextResponseChoice{{Logprobs: &logprobs}}))

	info.Request = &dto.GeneralOpenAIRequest{LogProbs: boolPtr(true), ReasoningEffort: "none"}
	assert.False(t, requiresDeepSeekV4ReasoningLogprobs(info))
	info.Request = &dto.GeneralOpenAIRequest{LogProbs: boolPtr(true), THINKING: []byte(`{"type":"disabled"}`)}
	assert.False(t, requiresDeepSeekV4ReasoningLogprobs(info))
	info.OriginModelName = "deepseek-v4-flash-none"
	info.Request = &dto.GeneralOpenAIRequest{LogProbs: boolPtr(true)}

	// The official upstream is exempt from the dual-path gate: it legitimately
	// returns content-path-only logprobs (e.g. max_tokens exhausted during
	// thinking), and rejecting it would 502 the contract-defining channel.
	assert.False(t, isOfficialDeepSeekV4Upstream(nil))
	assert.False(t, isOfficialDeepSeekV4Upstream(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI}}))
	assert.True(t, isOfficialDeepSeekV4Upstream(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeDeepSeek}}))
	assert.False(t, requiresDeepSeekV4ReasoningLogprobs(info))
}

func boolPtr(value bool) *bool { return &value }

// TestOaiStreamHandlerK3EmitsExactlyOneTopLevelUsageFrame pins the interlock
// that keeps K3's official usage-only event coming from exactly one writer.
// Official puts the counts on choices[0] of the terminal chunk and then, when
// the client asked for stream usage, repeats them in a choices:[] event. Both
// upstream shapes end up emitted by the K3 fit, which is the point of the
// gate in HandleFinalResponse:
//
//   - upstream carried usage  -> the fit renders those counts into the terminal
//     chunk and lifts the emitted object from it verbatim, so the two events
//     cannot disagree;
//   - upstream carried none   -> the relay derives the counts from the answer,
//     the fit renders those into the terminal chunk, and the same lift runs;
//     the client sees real counts, never the placeholder the fit injected.
//
// The gate is what makes that true: the generic HandleFinalResponse injection
// is gated on ShouldIncludeUsage (forced on for billing) rather than on the
// client's ask, and it serializes the platform's own usage struct. Letting it
// also fire hands the client two top-level events, the second in a shape
// official never sends.
func TestOaiStreamHandlerK3EmitsExactlyOneTopLevelUsageFrame(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	terminal := func(withUsage bool) string {
		chunk := `{"id":"chatcmpl-k3","object":"chat.completion.chunk","created":1790053753,"model":"kimi-k3",` +
			`"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
		if !withUsage {
			return chunk
		}
		return `{"id":"chatcmpl-k3","object":"chat.completion.chunk","created":1790053753,"model":"kimi-k3",` +
			`"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":93,"completion_tokens":55,"total_tokens":148,` +
			`"prompt_token_details":{},"prompt_cache_write_tokens":17,"credit":0.2}}`
	}
	body := func(withUsage bool) string {
		return strings.Join([]string{
			`data: {"id":"chatcmpl-k3","object":"chat.completion.chunk","created":1790053753,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null,"logprobs":null}]}`,
			`data: ` + terminal(withUsage),
			`data: [DONE]`,
			``,
		}, "\n")
	}

	for _, tc := range []struct {
		name      string
		withUsage bool
	}{
		{name: "upstream reports usage", withUsage: true},
		{name: "upstream reports none", withUsage: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{
				ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3"},
				OriginModelName:    "kimi-k3",
				IsStream:           true,
				RelayMode:          relayconstant.RelayModeChatCompletions,
				RelayFormat:        types.RelayFormatOpenAI,
				ShouldIncludeUsage: true,
				ClientIncludeUsage: true,
				DisablePing:        true,
				UserSetting: dto.UserSetting{
					OfficialFit: &dto.OfficialFitConfig{
						Profile: map[string]dto.OfficialFitProfile{"kimi-k3": {Shape: true}},
					},
				},
			}
			info.SetEstimatePromptTokens(40)

			_, err := OaiStreamHandler(c, info, &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body(tc.withUsage))),
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			})
			require.Nil(t, err)

			// Collect the usage objects by where they appeared. The values are
			// decoded loosely: a real usage carries nested detail objects, so
			// only the counts are read back as numbers.
			var choiceUsage, topUsage []map[string]any
			var topKeys map[string]any
			count := func(usage map[string]any, key string) int {
				value, ok := usage[key].(float64)
				require.Truef(t, ok, "%s missing or not a number in %v", key, usage)
				return int(value)
			}
			for _, line := range strings.Split(recorder.Body.String(), "\n") {
				if !strings.HasPrefix(line, "data:") {
					continue
				}
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if payload == "" || payload == "[DONE]" {
					continue
				}
				var chunk map[string]any
				require.NoError(t, json.Unmarshal([]byte(payload), &chunk))
				choices, _ := chunk["choices"].([]any)
				if len(choices) > 0 {
					if choice, ok := choices[0].(map[string]any); ok {
						if usage, ok := choice["usage"].(map[string]any); ok {
							choiceUsage = append(choiceUsage, usage)
						}
					}
				}
				if usage, ok := chunk["usage"].(map[string]any); ok {
					topUsage = append(topUsage, usage)
					topKeys = chunk
				}
			}

			require.Len(t, choiceUsage, 1, "the terminal chunk carries the counts on choices[0]")
			require.Len(t, topUsage, 1, "exactly one top-level usage event, never two")
			for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
				assert.Equalf(t, count(choiceUsage[0], key), count(topUsage[0], key),
					"the two events must not disagree on %s", key)
			}

			// The event's field set is the discriminator between the two writers
			// that could produce it. Official's is exactly these six keys, and
			// the K3 fit never invents a backend identity — whereas
			// GenerateFinalUsageResponse unconditionally sets system_fingerprint
			// (to "" when it knows none), which would leak an extra key. So this
			// assertion is what keeps the fit as the writer in both branches.
			// Measured: the generic frame serializes 7 top-level keys against the
			// fit's 6, and its usage object can render 16 keys where the official
			// usage has 6.
			var keys []string
			for key := range topKeys {
				keys = append(keys, key)
			}
			assert.ElementsMatch(t,
				[]string{"id", "object", "created", "model", "choices", "usage"}, keys,
				"the usage-only event must carry exactly the official field set")

			// The counts must be real, whichever writer produced them: the
			// upstream's own values, or values derived from the answer — never
			// the placeholder the fit injected.
			assert.Greater(t, count(topUsage[0], "prompt_tokens"), 0, "a zeroed usage means the wrong writer won the interlock")
			assert.Greater(t, count(topUsage[0], "completion_tokens"), 0, "a zeroed usage means the wrong writer won the interlock")
			if tc.withUsage {
				assert.Equal(t, 93, count(topUsage[0], "prompt_tokens"), "the upstream's own counts are preserved")
				assert.Equal(t, 55, count(topUsage[0], "completion_tokens"), "the upstream's own counts are preserved")
			}
		})
	}
}
