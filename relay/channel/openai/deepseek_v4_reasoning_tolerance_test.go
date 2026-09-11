package openai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression (2026-09-11): a thinking-expected official-fit request served by a
// non-official channel whose response carries content but no reasoning_content
// must be DELIVERED, not rejected. Only the official upstream guarantees the
// reasoning-before-content order; demanding it of an aggregator turned every
// dropped-reasoning response into a 502 ("upstream did not return
// reasoning_content in thinking mode") and, on the stream path, into a retry
// storm across every candidate channel.
func TestNonStreamThinkingWithoutReasoningIsDelivered(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-pro", ChannelType: 1},
		OriginModelName: "deepseek-v4-pro",
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     types.RelayFormatOpenAI,
		// Shape enabled (fit), no Route pin, thinking expected: the exact
		// production configuration that triggered the incident.
		UserSetting: dto.UserSetting{OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"deepseek-v4": {Validate: true, Errors: true, Shape: true},
		}}},
	}
	info.Request = &dto.GeneralOpenAIRequest{Model: "deepseek-v4-pro", THINKING: json.RawMessage(`{"type":"enabled"}`)}

	// Content present, reasoning_content absent entirely (aggregator shape).
	body := `{"id":"c1","object":"chat.completion","created":1710000000,"model":"deepseek-v4-pro",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12}}`

	usage, apiErr := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	})
	require.Nil(t, apiErr, "content-only thinking response must be delivered, not rejected")
	require.NotNil(t, usage)
	assert.Equal(t, "Hello", extractContent(t, recorder.Body.String()))
}

// Empty output is still a failure (nothing for the client to act on), but the
// upstream-reported usage must travel back with the error so the relay can
// settle the provider charge instead of refunding it. This is the billing gap
// observed on 2026-09-11: aborted/empty completions were recorded with
// prompt=0/completion=0 while the upstream had already billed.
func TestEmptyFinalContentReturnsObservedUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-pro", ChannelType: 1},
		OriginModelName: "deepseek-v4-pro",
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     types.RelayFormatOpenAI,
	}

	// Content empty, but the upstream billed 400 prompt + 120 reasoning tokens.
	body := `{"id":"c1","object":"chat.completion","created":1710000000,"model":"deepseek-v4-pro",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":400,"completion_tokens":120,"total_tokens":520}}`

	usage, apiErr := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	})
	require.NotNil(t, apiErr, "empty completion must still fail")
	assert.True(t, apiErr.IsEmptyOutput())
	assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
	require.NotNil(t, usage, "observed usage must be returned so the relay can settle it")
	assert.Equal(t, 400, usage.PromptTokens)
	assert.Equal(t, 120, usage.CompletionTokens)
}

// extractContent pulls the assistant content out of a chat.completion body.
func extractContent(t *testing.T, body string) string {
	t.Helper()
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
	if len(resp.Choices) == 0 {
		return ""
	}
	return resp.Choices[0].Message.Content
}

// Stream twin of the tolerance regression: an aggregator that emits content
// before (or without) reasoning must stream through. Before the gate removal
// this was aborted with zero bytes and surfaced as "upstream returned empty
// final content", driving a retry storm across every candidate channel.
func TestStreamContentWithoutReasoningIsDelivered(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"c1","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}],"usage":null}`,
		`data: {"id":"c1","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}],"usage":null}`,
		`data: {"id":"c1","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	recorder, hres := newDeepSeekV4StreamTestContext(t, body)
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	usage, apiErr := OaiStreamHandler(c, deepSeekV4RelayInfo(), hres)
	require.Nil(t, apiErr, "content-before-reasoning must be delivered after the gate removal")
	require.NotNil(t, usage)
	assert.Contains(t, recorder.Body.String(), `"content":"Hello"`)
}

// Stream twin of the billing regression: an empty terminal completion still
// fails, but the upstream-reported usage must come back with the error so the
// relay settles it rather than refunding the provider charge.
func TestStreamEmptyFinalContentReturnsUsage(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"c1","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}],"usage":null}`,
		`data: {"id":"c1","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":300,"completion_tokens":80,"total_tokens":380}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	recorder, hres := newDeepSeekV4StreamTestContext(t, body)
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	usage, apiErr := OaiStreamHandler(c, deepSeekV4RelayInfo(), hres)
	require.NotNil(t, apiErr, "empty completion must still fail")
	assert.True(t, apiErr.IsEmptyOutput())
	require.NotNil(t, usage, "observed usage must be returned so the relay can settle it")
	assert.Equal(t, 300, usage.PromptTokens)
	assert.Equal(t, 80, usage.CompletionTokens)
}
