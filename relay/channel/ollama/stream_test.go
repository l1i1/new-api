package ollama

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOllamaChatHandlerNonStreamToolCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name   string
		raw    string
		wantID string
	}{
		{
			name:   "compact json per-line parse path",
			raw:    `{"model":"llama3.1","created_at":"2026-05-27T12:00:00Z","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_upstream","function":{"name":"get_weather","arguments":{"city":"Paris","days":0}}}]},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":7}`,
			wantID: "call_upstream",
		},
		{
			name: "pretty json fallback parse path",
			raw: `{
  "model": "llama3.1",
  "created_at": "2026-05-27T12:00:00Z",
  "message": {
    "role": "assistant",
    "content": "",
    "tool_calls": [
      {
        "function": {
          "name": "get_weather",
          "arguments": {
            "city": "Paris",
            "days": 0
          }
        }
      }
    ]
  },
  "done": true,
  "done_reason": "stop",
  "prompt_eval_count": 5,
  "eval_count": 7
}`,
			wantID: "call_0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(tt.raw)),
			}

			usage, apiErr := ollamaChatHandler(c, &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "fallback-model"},
			}, resp)
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			assert.Equal(t, 12, usage.TotalTokens)

			var out dto.OpenAITextResponse
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &out))
			require.Len(t, out.Choices, 1)
			assert.Equal(t, constant.FinishReasonToolCalls, out.Choices[0].FinishReason)

			var toolCalls []dto.ToolCallResponse
			require.NoError(t, common.Unmarshal(out.Choices[0].Message.ToolCalls, &toolCalls))
			require.Len(t, toolCalls, 1)
			assert.Equal(t, tt.wantID, toolCalls[0].ID)
			assert.Equal(t, "function", toolCalls[0].Type)
			assert.Equal(t, "get_weather", toolCalls[0].Function.Name)
			assert.Nil(t, toolCalls[0].Index)

			var args map[string]any
			require.NoError(t, common.Unmarshal([]byte(toolCalls[0].Function.Arguments), &args))
			assert.Equal(t, "Paris", args["city"])
			assert.Equal(t, float64(0), args["days"])
		})
	}
}

func TestOllamaChatHandlerNonStreamEstimationIsClientSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	raw := `{"model":"llama3","created_at":"2026-05-27T12:00:00Z","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":200,"eval_count":1}`

	first := openAIRequest(msgs("user", "hello"), "session-1")
	firstWriter := httptest.NewRecorder()
	firstContext, _ := gin.CreateTestContext(firstWriter)
	usage, apiErr := ollamaChatHandler(firstContext, infoWith(1, 10, "llama3", setting, first), &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(raw)),
	})
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	second := openAIRequest(msgs("user", "hello", "assistant", "ok"), "session-1")
	secondWriter := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondWriter)
	usage, apiErr = ollamaChatHandler(secondContext, infoWith(1, 10, "llama3", setting, second), &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(raw)),
	})
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 200, usage.PromptTokensDetails.CachedTokens)
	assert.Contains(t, secondWriter.Body.String(), `"cached_tokens":200`)
	assert.NotContains(t, secondWriter.Body.String(), `billing_usage`)
}

func TestOllamaChatHandlerResponseCandidateMatchesFinalThinkingAndTools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}

	firstInfo := infoWith(9, 10, "llama3", setting, openAIRequest(msgs("user", "hello"), "structured-response"))
	firstWriter := httptest.NewRecorder()
	firstContext, _ := gin.CreateTestContext(firstWriter)
	firstBody, firstCloser, err := relaycommon.NewOutboundJSONBody([]byte(
		`{"model":"llama3","messages":[{"role":"user","content":"hello"}]}`,
	))
	require.NoError(t, err)
	defer firstCloser.Close()
	captureOllamaPromptCacheIdentity(firstContext, firstInfo, firstBody)
	_, apiErr := ollamaChatHandler(firstContext, firstInfo, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"model":"llama3","message":{"role":"assistant","content":"answer","thinking":"internal","tool_calls":[{"id":"call_1","function":{"name":"lookup","arguments":{"city":"Paris"}}}]},"done":true,"prompt_eval_count":100,"eval_count":20}`,
		)),
	})
	require.Nil(t, apiErr)

	secondInfo := infoWith(9, 10, "llama3", setting, openAIRequest(msgs("user", "hello"), "structured-response"))
	secondWriter := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondWriter)
	secondBody, secondCloser, err := relaycommon.NewOutboundJSONBody([]byte(
		`{"model":"llama3","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"answer","thinking":"internal","tool_calls":[{"id":"call_1","function":{"name":"lookup","arguments":{"city":"Paris"}}}]}]}`,
	))
	require.NoError(t, err)
	defer secondCloser.Close()
	captureOllamaPromptCacheIdentity(secondContext, secondInfo, secondBody)
	usage, apiErr := ollamaChatHandler(secondContext, secondInfo, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"model":"llama3","message":{"role":"assistant","content":"next"},"done":true,"prompt_eval_count":150,"eval_count":1}`,
		)),
	})
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 120, usage.PromptTokensDetails.CachedTokens)
}

func TestOllamaChatHandlerRealZeroAllowsEstimation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	withoutCache := `{"model":"llama3","created_at":"2026-05-27T12:00:00Z","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":200,"eval_count":1}`
	withRealZero := `{"model":"llama3","created_at":"2026-05-27T12:00:00Z","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":300,"eval_count":1,"prompt_tokens_details":{"cached_tokens":0}}`

	firstWriter := httptest.NewRecorder()
	firstContext, _ := gin.CreateTestContext(firstWriter)
	_, apiErr := ollamaChatHandler(firstContext, infoWith(2, 10, "llama3", setting, openAIRequest(msgs("user", "hello"), "session-2")), &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(withoutCache)),
	})
	require.Nil(t, apiErr)

	secondWriter := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondWriter)
	usage, apiErr := ollamaChatHandler(secondContext, infoWith(2, 10, "llama3", setting, openAIRequest(msgs("user", "hello", "assistant", "ok"), "session-2")), &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(withRealZero)),
	})
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 201, usage.PromptTokensDetails.CachedTokens)
	assert.NotContains(t, secondWriter.Body.String(), `billing_usage`)
}

func TestOllamaStreamHandlerEstimationIsClientSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetPromptCache()
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	raw := "{" + `"model":"llama3","created_at":"2026-05-27T12:00:00Z","message":{"role":"assistant","content":"ok"}}` + "\n" +
		"{" + `"model":"llama3","created_at":"2026-05-27T12:00:00Z","done":true,"prompt_eval_count":200,"eval_count":1}` + "\n"

	firstWriter := httptest.NewRecorder()
	firstContext, _ := gin.CreateTestContext(firstWriter)
	firstInfo := infoWith(3, 10, "llama3", setting, openAIRequest(msgs("user", "hello"), "session-3"))
	firstInfo.ShouldIncludeUsage = true
	_, apiErr := ollamaStreamHandler(firstContext, firstInfo, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(raw)),
	})
	require.Nil(t, apiErr)

	secondWriter := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondWriter)
	secondInfo := infoWith(3, 10, "llama3", setting, openAIRequest(msgs("user", "hello", "assistant", "ok"), "session-3"))
	secondInfo.ShouldIncludeUsage = true
	usage, apiErr := ollamaStreamHandler(secondContext, secondInfo, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(raw)),
	})
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 200, usage.PromptTokensDetails.CachedTokens)
	assert.Contains(t, secondWriter.Body.String(), `"cached_tokens":200`)
	assert.NotContains(t, secondWriter.Body.String(), `billing_usage`)
}

func TestOllamaStreamHandlerRecordsFirstRealResponseBeforeDownstreamTTFT(t *testing.T) {
	gin.SetMode(gin.TestMode)
	start := time.Now()
	info := &relaycommon.RelayInfo{
		StartTime:        start,
		AttemptStartTime: start,
		IsStream:         true,
		ChannelMeta:      &relaycommon.ChannelMeta{UpstreamModelName: "llama3"},
	}
	writer := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(writer)
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"model":"llama3","created_at":"2026-05-27T12:00:00Z","message":{"role":"assistant","content":"ok"}}` + "\n" + `{"model":"llama3","created_at":"2026-05-27T12:00:00Z","done":true,"prompt_eval_count":2,"eval_count":1}` + "\n",
		)),
	}

	_, apiErr := ollamaStreamHandler(context, info, response)
	require.Nil(t, apiErr)
	require.False(t, info.AttemptFirstResponseTime.IsZero())
}

func TestOllamaUsageNormalizesUpstreamCounts(t *testing.T) {
	assert.Equal(t, 0, normalizeOllamaTokenCount(-1))
	assert.Equal(t, 100, normalizeOllamaCachedTokens(200, 100))
	assert.Equal(t, 0, normalizeOllamaCachedTokens(-1, 100))
	maxInt := int(^uint(0) >> 1)
	assert.Equal(t, maxInt, saturatingOllamaTokenAdd(maxInt, 1))
	assert.Equal(t, maxInt, normalizeOllamaUsage(maxInt, maxInt).TotalTokens)
}

func TestOllamaChatHandlerRejectsErrorAndIncompleteResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name string
		raw  string
	}{
		{name: "top level error", raw: `{"error":"model failed"}`},
		{name: "missing done", raw: `{"model":"llama3","response":"partial","done":false}`},
		{name: "bad ndjson line", raw: "{\"model\":\"llama3\",\"done\":false}\nnot-json\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(writer)
			usage, apiErr := ollamaChatHandler(c, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3"}}, &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(tt.raw)),
			})
			require.Nil(t, usage)
			require.NotNil(t, apiErr)
			assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
		})
	}
}

func TestOllamaChatHandlerSupportsCompletionsAndTopLevelThinking(t *testing.T) {
	gin.SetMode(gin.TestMode)
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	info := &relaycommon.RelayInfo{
		RelayMode:      relayconstant.RelayModeCompletions,
		RequestURLPath: "/v1/completions",
		ChannelMeta:    &relaycommon.ChannelMeta{UpstreamModelName: "llama3"},
	}
	usageValue, apiErr := (&Adaptor{}).DoResponse(c, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"model":"llama3","response":"answer","thinking":"reason","done":true,"prompt_eval_count":3,"eval_count":2}`,
		)),
	}, info)
	require.Nil(t, apiErr)
	usage := usageValue.(*dto.Usage)
	assert.Equal(t, 5, usage.TotalTokens)
	assert.Contains(t, writer.Body.String(), `"object":"text_completion"`)
	assert.Contains(t, writer.Body.String(), `"text":"answer"`)
	assert.NotContains(t, writer.Body.String(), `"message"`)
}

func TestOllamaChatHandlerReturnsClaudeMessagesProtocol(t *testing.T) {
	gin.SetMode(gin.TestMode)
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3"},
	}
	usageValue, apiErr := (&Adaptor{}).DoResponse(c, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"model":"llama3","message":{"role":"assistant","content":"answer"},"done":true,"prompt_eval_count":3,"eval_count":2}`,
		)),
	}, info)
	require.Nil(t, apiErr)
	usage := usageValue.(*dto.Usage)
	require.NotNil(t, usage)

	var response dto.ClaudeResponse
	require.NoError(t, common.Unmarshal(writer.Body.Bytes(), &response))
	assert.Equal(t, "message", response.Type)
	assert.Equal(t, "assistant", response.Role)
	require.Len(t, response.Content, 1)
	assert.Equal(t, "text", response.Content[0].Type)
	assert.Equal(t, "answer", response.Content[0].GetText())
	assert.NotContains(t, writer.Body.String(), `"choices"`)
}

func TestOllamaStreamHandlerRejectsErrorIncompleteAndScannerFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	makeInfo := func() *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{
			IsStream:    true,
			RelayFormat: types.RelayFormatOpenAI,
			ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3"},
		}
	}
	tests := []struct {
		name string
		body io.Reader
	}{
		{name: "top level error", body: strings.NewReader(`{"error":"model failed"}`)},
		{name: "missing done", body: strings.NewReader(`{"model":"llama3","response":"partial","done":false}` + "\n")},
		{name: "bad ndjson line", body: strings.NewReader(`{"model":"llama3","done":false}` + "\nnot-json\n")},
		{name: "scanner error", body: &ollamaErrorReader{data: []byte(`{"model":"llama3","done":false}` + "\n")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(writer)
			usage, apiErr := ollamaStreamHandler(c, makeInfo(), &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(tt.body),
			})
			require.NotNil(t, apiErr)
			assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
			assert.NotContains(t, writer.Body.String(), "[DONE]")
			if tt.name == "top level error" {
				assert.Empty(t, writer.Body.String(), "an error frame must not be preceded by a synthetic success chunk")
				assert.False(t, types.IsSkipRetryError(apiErr), "an error before any client response can still fail over")
			} else {
				assert.True(t, types.IsSkipRetryError(apiErr), "a partial stream must never be followed by a retry response")
			}
			assert.NotNil(t, usage)
		})
	}
}

func TestOllamaStreamHandlerMasksAndBoundsMalformedFrameLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldDebug := common.DebugEnabled
	common.DebugEnabled = false
	defer func() { common.DebugEnabled = oldDebug }()

	var logs bytes.Buffer
	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logs
	common.LogWriterMu.Unlock()
	defer func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
	}()

	secret := "sk-ollama-stream-log-secret-123456"
	line := `not-json api_key=` + secret + strings.Repeat("x", 4000)
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	_, apiErr := ollamaStreamHandler(c, &relaycommon.RelayInfo{
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3"},
	}, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(line + "\n")),
	})

	require.NotNil(t, apiErr)
	assert.NotContains(t, logs.String(), secret)
	assert.Less(t, logs.Len(), common.LocalLogContentLimit+512)
}

func TestOllamaStreamHandlerHonorsUsageAndCompletionsProtocol(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw := `{"model":"llama3","response":"answer","done":false}` + "\n" + `{"model":"llama3","response":" tail","thinking":"reason","done":true,"prompt_eval_count":3,"eval_count":2}` + "\n"
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	info := &relaycommon.RelayInfo{
		IsStream:           true,
		ShouldIncludeUsage: false,
		RelayMode:          relayconstant.RelayModeCompletions,
		RequestURLPath:     "/v1/completions",
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "llama3"},
	}
	usageValue, apiErr := (&Adaptor{}).DoResponse(c, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(raw)),
	}, info)
	require.Nil(t, apiErr)
	usage := usageValue.(*dto.Usage)
	assert.Equal(t, 5, usage.TotalTokens)
	body := writer.Body.String()
	assert.Equal(t, 5, strings.Count(body, "data: "))
	assert.Contains(t, body, `"object":"text_completion"`)
	assert.Contains(t, body, `"text":"answer"`)
	assert.Contains(t, body, `"text":" tail"`, "done=true payload must be forwarded before the finish chunk")
	assert.NotContains(t, body, `"delta"`)
	assert.NotContains(t, body, `"usage":`, "include_usage=false must omit the usage-only frame")
}

func TestOllamaStreamHandlerOmitsUsageForChatCompletions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	info := &relaycommon.RelayInfo{
		IsStream:           true,
		ShouldIncludeUsage: false,
		RelayFormat:        types.RelayFormatOpenAI,
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "llama3"},
	}
	usage, apiErr := ollamaStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"model":"llama3","message":{"content":"answer"},"done":true,"prompt_eval_count":3,"eval_count":2}` + "\n",
		)),
	})

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.TotalTokens)
	assert.NotContains(t, writer.Body.String(), `"choices":[],"usage":{`, "include_usage=false must omit the usage-only frame")
	assert.NotContains(t, writer.Body.String(), `"prompt_tokens":3`)
}

func TestOllamaStreamHandlerClaudeProtocolAndToolBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	operation_setting.SetToolPriceForTest("lookup_weather", 1)
	defer operation_setting.DeleteToolPriceForTest("lookup_weather")
	raw := `{"model":"llama3","message":{"content":"","tool_calls":[{"id":"call_1","function":{"name":"lookup_weather","arguments":{"city":"Paris"}}}]},"done":false}` + "\n" +
		`{"model":"llama3","message":{"content":"","tool_calls":[{"id":"call_1","function":{"name":"lookup_weather","arguments":{"city":"Paris"}}}]},"done":true,"prompt_eval_count":3,"eval_count":2}` + "\n"
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		RelayFormat: types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3"},
	}
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	usageValue, apiErr := (&Adaptor{}).DoResponse(c, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(raw)),
	}, info)
	require.Nil(t, apiErr)
	usage := usageValue.(*dto.Usage)
	require.NotNil(t, usage)
	assert.Contains(t, writer.Body.String(), "event: message_start")
	assert.Contains(t, writer.Body.String(), "event: message_stop")
	assert.Equal(t, 1, strings.Count(writer.Body.String(), "event: content_block_start"))
	assert.Equal(t, 1, strings.Count(writer.Body.String(), `"id":"call_1"`))
	assert.NotContains(t, writer.Body.String(), "data: [DONE]")
	require.NotNil(t, info.ResponsesUsageInfo)
	require.NotNil(t, info.ResponsesUsageInfo.BuiltInTools["lookup_weather"])
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools["lookup_weather"].CallCount)
}

func TestOllamaClaudeStreamDoesNotCommitAfterDownstreamWriteFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	operation_setting.SetToolPriceForTest("lookup_weather", 1)
	defer operation_setting.DeleteToolPriceForTest("lookup_weather")

	info := &relaycommon.RelayInfo{
		IsStream:    true,
		RelayFormat: types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3"},
	}
	baseWriter := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(baseWriter)
	c.Writer = &ollamaFailingWriter{ResponseWriter: c.Writer, failOn: "event: message_stop"}
	_, apiErr := ollamaStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"model":"llama3","message":{"tool_calls":[{"id":"call_1","function":{"name":"lookup_weather","arguments":{"city":"Paris"}}}]},"done":true,"prompt_eval_count":3,"eval_count":2}` + "\n",
		)),
	})

	require.NotNil(t, apiErr)
	assert.True(t, types.IsSkipRetryError(apiErr))
	if info.ResponsesUsageInfo != nil {
		assert.NotContains(t, info.ResponsesUsageInfo.BuiltInTools, "lookup_weather")
	}
}

func TestOllamaChatHandlerDoesNotBillIncompleteToolResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	operation_setting.SetToolPriceForTest("lookup_weather", 1)
	defer operation_setting.DeleteToolPriceForTest("lookup_weather")

	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3"}}
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	_, apiErr := ollamaChatHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"model":"llama3","message":{"tool_calls":[{"id":"call_1","function":{"name":"lookup_weather","arguments":{"city":"Paris"}}}]},"done":false}`,
		)),
	})

	require.NotNil(t, apiErr)
	if info.ResponsesUsageInfo != nil {
		assert.NotContains(t, info.ResponsesUsageInfo.BuiltInTools, "lookup_weather")
	}
}

func TestOllamaStreamHandlerDoesNotCommitToolBillingBeforeSuccessfulTerminalFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)
	operation_setting.SetToolPriceForTest("lookup_weather", 1)
	defer operation_setting.DeleteToolPriceForTest("lookup_weather")

	info := &relaycommon.RelayInfo{
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3"},
	}
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	_, apiErr := ollamaStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"model":"llama3","message":{"tool_calls":[{"id":"call_1","function":{"name":"lookup_weather","arguments":{"city":"Paris"}}}]},"done":false}` + "\n",
		)),
	})
	require.NotNil(t, apiErr)
	assert.True(t, types.IsSkipRetryError(apiErr))
	if info.ResponsesUsageInfo != nil {
		assert.NotContains(t, info.ResponsesUsageInfo.BuiltInTools, "lookup_weather")
	}
}

func TestOllamaStreamHandlerDoesNotCommitAfterDownstreamWriteFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetPromptCache()
	operation_setting.SetToolPriceForTest("lookup_weather", 1)
	defer operation_setting.DeleteToolPriceForTest("lookup_weather")

	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	info := infoWith(7, 10, "llama3", setting, openAIRequest(msgs("user", "hello"), "write-failure"))
	info.IsStream = true
	info.RelayFormat = types.RelayFormatOpenAI
	baseWriter := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(baseWriter)
	c.Writer = &ollamaFailingWriter{ResponseWriter: c.Writer, failOn: "[DONE]"}

	_, apiErr := ollamaStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"model":"llama3","message":{"content":"answer","tool_calls":[{"id":"call_1","function":{"name":"lookup_weather","arguments":{"city":"Paris"}}}]},"done":true,"prompt_eval_count":100,"eval_count":5}` + "\n",
		)),
	})
	require.NotNil(t, apiErr)
	assert.True(t, types.IsSkipRetryError(apiErr))
	if info.ResponsesUsageInfo != nil {
		assert.NotContains(t, info.ResponsesUsageInfo.BuiltInTools, "lookup_weather")
	}
	entry, found, err := getPromptCache().Get(buildPromptCacheKey(info))
	require.NoError(t, err)
	require.True(t, found)
	assert.Len(t, promptCacheCandidates(entry), 1, "failed response must not add an assistant cache candidate")
}

func TestOllamaChatHandlerDoesNotCommitAfterDownstreamWriteFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetPromptCache()
	operation_setting.SetToolPriceForTest("lookup_weather", 1)
	defer operation_setting.DeleteToolPriceForTest("lookup_weather")

	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	info := infoWith(8, 10, "llama3", setting, openAIRequest(msgs("user", "hello"), "write-failure"))
	baseWriter := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(baseWriter)
	c.Writer = &ollamaFailingWriter{ResponseWriter: c.Writer, failOn: `"object":"chat.completion"`}

	_, apiErr := ollamaChatHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"model":"llama3","message":{"content":"answer","tool_calls":[{"id":"call_1","function":{"name":"lookup_weather","arguments":{"city":"Paris"}}}]},"done":true,"prompt_eval_count":100,"eval_count":5}`,
		)),
	})
	require.NotNil(t, apiErr)
	assert.True(t, types.IsSkipRetryError(apiErr))
	if info.ResponsesUsageInfo != nil {
		assert.NotContains(t, info.ResponsesUsageInfo.BuiltInTools, "lookup_weather")
	}
	entry, found, err := getPromptCache().Get(buildPromptCacheKey(info))
	require.NoError(t, err)
	require.True(t, found)
	assert.Len(t, promptCacheCandidates(entry), 1, "failed response must not add an assistant cache candidate")
}

func TestOllamaHandlersKeepDistinctNoIDToolCallsWithSameName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	operation_setting.SetToolPriceForTest("lookup_weather", 1)
	defer operation_setting.DeleteToolPriceForTest("lookup_weather")
	raw := `{"model":"llama3","message":{"tool_calls":[{"function":{"name":"lookup_weather","arguments":{"city":"Paris"}}}]},"done":false}` + "\n" +
		`{"model":"llama3","message":{"tool_calls":[{"function":{"name":"lookup_weather","arguments":{"city":"Tokyo"}}}]},"done":true,"prompt_eval_count":3,"eval_count":2}` + "\n"

	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				IsStream:    stream,
				RelayFormat: types.RelayFormatOpenAI,
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3"},
			}
			writer := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(writer)
			response := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(raw)),
			}
			var apiErr *types.NewAPIError
			if stream {
				_, apiErr = ollamaStreamHandler(c, info, response)
			} else {
				_, apiErr = ollamaChatHandler(c, info, response)
			}
			require.Nil(t, apiErr)
			assert.Equal(t, 2, strings.Count(writer.Body.String(), `"name":"lookup_weather"`))
			require.NotNil(t, info.ResponsesUsageInfo)
			require.NotNil(t, info.ResponsesUsageInfo.BuiltInTools["lookup_weather"])
			assert.Equal(t, 2, info.ResponsesUsageInfo.BuiltInTools["lookup_weather"].CallCount)
		})
	}
}

type ollamaErrorReader struct {
	data []byte
	done bool
}

type ollamaFailingWriter struct {
	gin.ResponseWriter
	failOn string
	once   sync.Once
}

func (w *ollamaFailingWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.failOn) {
		failed := false
		w.once.Do(func() { failed = true })
		if failed {
			written, _ := w.ResponseWriter.Write(p[:len(p)/2])
			return written, errors.New("synthetic downstream write failure")
		}
	}
	return w.ResponseWriter.Write(p)
}

func (w *ollamaFailingWriter) WriteString(value string) (int, error) {
	return w.Write([]byte(value))
}

func (r *ollamaErrorReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		n := copy(p, r.data)
		return n, nil
	}
	return 0, errors.New("synthetic upstream read failure")
}
