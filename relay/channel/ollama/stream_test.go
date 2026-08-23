package ollama

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

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
	_, apiErr := ollamaStreamHandler(firstContext, infoWith(3, 10, "llama3", setting, openAIRequest(msgs("user", "hello"), "session-3")), &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(raw)),
	})
	require.Nil(t, apiErr)

	secondWriter := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondWriter)
	usage, apiErr := ollamaStreamHandler(secondContext, infoWith(3, 10, "llama3", setting, openAIRequest(msgs("user", "hello", "assistant", "ok"), "session-3")), &http.Response{
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

func TestOllamaUsageNormalizesUpstreamCounts(t *testing.T) {
	assert.Equal(t, 0, normalizeOllamaTokenCount(-1))
	assert.Equal(t, 100, normalizeOllamaCachedTokens(200, 100))
	assert.Equal(t, 0, normalizeOllamaCachedTokens(-1, 100))
}
