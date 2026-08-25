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

func newDeepSeekV4StreamTestContext(t *testing.T, body string) (*httptest.ResponseRecorder, *http.Response) {
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
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	return recorder, resp
}

func deepSeekV4RelayInfo() *relaycommon.RelayInfo {
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "deepseek-v4-flash",
		},
		OriginModelName:    "deepseek-v4-flash",
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
	}
	return info
}

func dataEvents(recorder *httptest.ResponseRecorder) []string {
	var events []string
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		line = strings.TrimPrefix(line, "data: ")
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			events = append(events, line)
		}
	}
	return events
}

// An official-shaped upstream (usage attached to the final finish_reason
// chunk, fingerprint on every chunk) must reach the client unchanged, with no
// synthetic usage-only event.
func TestOaiStreamHandlerDeepSeekV4ForwardsOfficialShapeVerbatim(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-flash","system_fingerprint":"a26a7955944dc5c60445bff77fac9c8e","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":""},"logprobs":null,"finish_reason":null}],"usage":null}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-flash","system_fingerprint":"a26a7955944dc5c60445bff77fac9c8e","choices":[{"index":0,"delta":{"content":null,"reasoning_content":"We"},"logprobs":null,"finish_reason":null}],"usage":null}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-flash","system_fingerprint":"a26a7955944dc5c60445bff77fac9c8e","choices":[{"index":0,"delta":{"content":"2","reasoning_content":null},"logprobs":null,"finish_reason":null}],"usage":null}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-flash","system_fingerprint":"a26a7955944dc5c60445bff77fac9c8e","choices":[{"index":0,"delta":{"content":"","reasoning_content":null},"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":17,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":15},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":8}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	recorder, resp := newDeepSeekV4StreamTestContext(t, body)
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	usage, err := OaiStreamHandler(c, deepSeekV4RelayInfo(), resp)

	require.Nil(t, err)
	require.Equal(t, 8, usage.PromptTokens)
	require.Equal(t, 17, usage.CompletionTokens)

	events := dataEvents(recorder)
	require.Len(t, events, 5, "expected 4 data events plus [DONE]")
	assert.Equal(t, "[DONE]", events[len(events)-1])

	final := events[len(events)-2]
	assert.Contains(t, final, `"finish_reason":"stop"`)
	assert.Contains(t, final, `"prompt_cache_miss_tokens":8`)
	assert.NotContains(t, final, `"claude_cache_creation`)
	for _, event := range events[:len(events)-1] {
		assert.Contains(t, event, `"system_fingerprint":"a26a7955944dc5c60445bff77fac9c8e"`)
	}
	for _, event := range events {
		assert.NotContains(t, event, `"choices":[]`, "no usage-only event may be synthesized for V4")
	}
}

// An aggregator upstream that omits system_fingerprint and merges Claude
// extension usage fields must retain the unknown fingerprint state while
// exposing the official seven-key usage shape on the final chunk.
func TestOaiStreamHandlerDeepSeekV4FitsAggregatorShape(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"router-1","object":"chat.completion.chunk","created":1787661827,"model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":null,"logprobs":null,"delta":{"role":"assistant","content":"","reasoning_content":null}}]}`,
		`data: {"id":"router-1","object":"chat.completion.chunk","created":1787661827,"model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":null,"logprobs":null,"delta":{"reasoning_content":"We"}}]}`,
		`data: {"id":"router-1","object":"chat.completion.chunk","created":1787661827,"model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":null,"logprobs":null,"delta":{"content":"2"}}]}`,
		`data: {"id":"router-1","object":"chat.completion.chunk","created":1787661827,"model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":"stop","logprobs":null,"delta":{"reasoning_content":null}}],"usage":{"claude_cache_creation_1_h_tokens":0,"claude_cache_creation_5_m_tokens":0,"completion_tokens":32,"completion_tokens_details":{"text_tokens":0,"audio_tokens":0,"image_tokens":0,"reasoning_tokens":20},"input_tokens":0,"input_tokens_details":null,"output_tokens":0,"prompt_tokens":8,"prompt_tokens_details":{"cached_tokens":0,"text_tokens":0,"audio_tokens":0,"image_tokens":0},"total_tokens":40}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	recorder, resp := newDeepSeekV4StreamTestContext(t, body)
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	usage, err := OaiStreamHandler(c, deepSeekV4RelayInfo(), resp)

	require.Nil(t, err)
	require.Equal(t, 8, usage.PromptTokens)
	require.Equal(t, 32, usage.CompletionTokens)
	require.Equal(t, 20, usage.CompletionTokenDetails.ReasoningTokens)

	events := dataEvents(recorder)
	require.Len(t, events, 5)
	assert.Equal(t, "[DONE]", events[len(events)-1])

	final := events[len(events)-2]
	assert.Contains(t, final, `"finish_reason":"stop"`)
	assert.Contains(t, final, `"prompt_tokens":8`)
	assert.Contains(t, final, `"completion_tokens":32`)
	assert.Contains(t, final, `"total_tokens":40`)
	assert.Contains(t, final, `"prompt_cache_hit_tokens":0`)
	assert.Contains(t, final, `"prompt_cache_miss_tokens":8`)
	assert.Contains(t, final, `"reasoning_tokens":20`)
	assert.NotContains(t, final, `"claude_cache_creation`)
	assert.NotContains(t, final, `"input_tokens"`)
	for _, event := range events[:len(events)-1] {
		assert.NotContains(t, event, `"system_fingerprint"`)
		assert.NotContains(t, event, `"choices":[]`)
	}
}

func TestOaiStreamHandlerDeepSeekV4MergesUsageOnlyTailIntoFinishChunk(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"router-1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":null,"delta":{"content":"ok"}}]}`,
		`data: {"id":"router-1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":"stop","delta":{}}],"usage":null}`,
		`data: {"id":"router-1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":1,"total_tokens":9}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	recorder, resp := newDeepSeekV4StreamTestContext(t, body)
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	usage, err := OaiStreamHandler(c, deepSeekV4RelayInfo(), resp)

	require.Nil(t, err)
	require.Equal(t, 8, usage.PromptTokens)
	events := dataEvents(recorder)
	require.Len(t, events, 3, "expected content, merged finish/usage, and [DONE]")
	final := events[len(events)-2]
	assert.Contains(t, final, `"finish_reason":"stop"`)
	assert.Contains(t, final, `"prompt_tokens":8`)
	assert.NotContains(t, final, `"choices":[]`)
}

func TestOaiStreamHandlerDeepSeekV4PreservesFinishAcrossConsecutiveUsageOnlyTail(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":null,"delta":{"content":"A"}}],"usage":null}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":"stop","delta":{}}],"usage":null}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":1,"total_tokens":9}}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":1,"total_tokens":9}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	recorder, resp := newDeepSeekV4StreamTestContext(t, body)
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	_, err := OaiStreamHandler(c, deepSeekV4RelayInfo(), resp)

	require.Nil(t, err)
	events := dataEvents(recorder)
	require.Len(t, events, 3, "expected content, preserved finish, and [DONE]")
	assert.Contains(t, events[0], `"content":"A"`)
	assert.Contains(t, events[1], `"finish_reason":"stop"`)
	for _, event := range events {
		assert.NotContains(t, event, `"choices":[]`, "consecutive usage-only events must stay internal")
	}
}

func TestOaiStreamHandlerDeepSeekV4PreservesContentBeforeConsecutiveUsageOnlyEvents(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":null,"delta":{"content":"A"}}],"usage":null}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":1,"total_tokens":9}}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":1,"total_tokens":9}}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":"stop","delta":{}}],"usage":null}`,
		`data: [DONE]`,
		``,
	}, "\n")

	recorder, resp := newDeepSeekV4StreamTestContext(t, body)
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	_, err := OaiStreamHandler(c, deepSeekV4RelayInfo(), resp)

	require.Nil(t, err)
	events := dataEvents(recorder)
	require.Len(t, events, 3, "expected preserved content, finish, and [DONE]")
	assert.Contains(t, events[0], `"content":"A"`)
	assert.Contains(t, events[1], `"finish_reason":"stop"`)
	for _, event := range events {
		assert.NotContains(t, event, `"choices":[]`, "usage-only events must not become V4 protocol events")
	}
}

func TestOaiStreamHandlerDeepSeekV4KeepsFinishAcrossRepeatedUsageTail(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"router-1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":null,"delta":{"content":"ok"}}]}`,
		`data: {"id":"router-1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":"stop","delta":{}}],"usage":null}`,
		`data: {"id":"router-1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":1,"total_tokens":9}}`,
		`data: {"id":"router-1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	recorder, resp := newDeepSeekV4StreamTestContext(t, body)
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	usage, err := OaiStreamHandler(c, deepSeekV4RelayInfo(), resp)

	require.Nil(t, err)
	require.Equal(t, 2, usage.CompletionTokens)
	events := dataEvents(recorder)
	require.Len(t, events, 3, "expected content, merged finish/usage, and [DONE]")
	assert.Contains(t, events[0], `"content":"ok"`)
	assert.Contains(t, events[1], `"finish_reason":"stop"`)
	assert.Contains(t, events[1], `"completion_tokens":2`)
	assert.NotContains(t, events[1], `"choices":[]`)
}

func TestOaiStreamHandlerDeepSeekV4KeepsContentBeforeRepeatedUsage(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"router-1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":null,"delta":{"content":"kept"}}]}`,
		`data: {"id":"router-1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":1,"total_tokens":9}}`,
		`data: {"id":"router-1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}`,
		`data: {"id":"router-1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":"stop","delta":{}}],"usage":null}`,
		`data: [DONE]`,
		``,
	}, "\n")

	recorder, resp := newDeepSeekV4StreamTestContext(t, body)
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	_, err := OaiStreamHandler(c, deepSeekV4RelayInfo(), resp)

	require.Nil(t, err)
	events := dataEvents(recorder)
	require.Len(t, events, 3, "expected content, merged finish/usage, and [DONE]")
	assert.Contains(t, events[0], `"content":"kept"`)
	assert.Contains(t, events[1], `"finish_reason":"stop"`)
	assert.NotContains(t, events[1], `"choices":[]`)
}

func TestOaiStreamHandlerDeepSeekV4HonorsIncludeUsageFalse(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":null,"delta":{"content":"ok"}}],"usage":null}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"finish_reason":"stop","delta":{}}],"usage":{"prompt_tokens":8,"completion_tokens":1,"total_tokens":9}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	recorder, resp := newDeepSeekV4StreamTestContext(t, body)
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := deepSeekV4RelayInfo()
	info.ShouldIncludeUsage = false
	_, err := OaiStreamHandler(c, info, resp)

	require.Nil(t, err)
	events := dataEvents(recorder)
	require.Len(t, events, 3)
	final := events[len(events)-2]
	assert.Contains(t, final, `"finish_reason":"stop"`)
	assert.NotContains(t, final, `"prompt_tokens"`)
	assert.NotContains(t, final, `"completion_tokens"`)
}
