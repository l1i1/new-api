package openai

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func toolCallFragment(index int, name, arguments string) dto.ToolCallResponse {
	idx := index
	return dto.ToolCallResponse{
		Index: &idx,
		Type:  "function",
		Function: dto.FunctionResponse{
			Name:      name,
			Arguments: arguments,
		},
	}
}

func observeChunk(tracker *streamToolCallTracker, calls ...dto.ToolCallResponse) {
	tracker.observe([]dto.ChatCompletionsStreamResponseChoice{{
		Index: 0,
		Delta: dto.ChatCompletionsStreamResponseChoiceDelta{ToolCalls: calls},
	}})
}

// TestStreamToolCallTrackerCompleteCalls pins when a streamed tool call counts
// as delivered: the name must have arrived and the accumulated arguments must
// parse as JSON. Argument-only fragments (OpenAI sends the name once) must still
// accumulate.
func TestStreamToolCallTrackerCompleteCalls(t *testing.T) {
	t.Run("single call in fragments", func(t *testing.T) {
		var tracker streamToolCallTracker
		observeChunk(&tracker, toolCallFragment(0, "bash", ""))
		observeChunk(&tracker, toolCallFragment(0, "", `{"command":`))
		observeChunk(&tracker, toolCallFragment(0, "", `"ls -l"}`))
		count, complete := tracker.completeCalls()
		require.True(t, complete)
		assert.Equal(t, 1, count)
	})

	t.Run("multiple calls by index", func(t *testing.T) {
		var tracker streamToolCallTracker
		observeChunk(&tracker, toolCallFragment(0, "bash", `{"command":"a"}`))
		observeChunk(&tracker, toolCallFragment(1, "bash", `{"command":"b"}`))
		count, complete := tracker.completeCalls()
		require.True(t, complete)
		assert.Equal(t, 2, count)
	})

	t.Run("cumulative snapshot arguments are not duplicated", func(t *testing.T) {
		var tracker streamToolCallTracker
		observeChunk(&tracker, toolCallFragment(0, "bash", `{"command":"a"`))
		observeChunk(&tracker, toolCallFragment(0, "", `{"command":"a","timeout":1}`))
		count, complete := tracker.completeCalls()
		require.True(t, complete)
		assert.Equal(t, 1, count)
	})

	t.Run("cut inside the arguments stays incomplete", func(t *testing.T) {
		var tracker streamToolCallTracker
		observeChunk(&tracker, toolCallFragment(0, "bash", `{"command":"ls`))
		_, complete := tracker.completeCalls()
		assert.False(t, complete)
	})

	t.Run("any incomplete call keeps the whole answer incomplete", func(t *testing.T) {
		var tracker streamToolCallTracker
		observeChunk(&tracker, toolCallFragment(0, "bash", `{"command":"a"}`))
		observeChunk(&tracker, toolCallFragment(1, "bash", `{"command":`))
		_, complete := tracker.completeCalls()
		assert.False(t, complete)
	})

	t.Run("missing name is not a usable call", func(t *testing.T) {
		var tracker streamToolCallTracker
		observeChunk(&tracker, toolCallFragment(0, "", `{"command":"a"}`))
		_, complete := tracker.completeCalls()
		assert.False(t, complete)
	})

	t.Run("empty arguments are not a usable call", func(t *testing.T) {
		var tracker streamToolCallTracker
		observeChunk(&tracker, toolCallFragment(0, "bash", ""))
		_, complete := tracker.completeCalls()
		assert.False(t, complete)
	})
}

// TestOaiStreamHandlerClosesMissingFinishToolCallStreamAsToolCalls pins the
// 2026-10-29 compensation: an upstream that emits the whole tool call and then
// loses only the terminal frame must not hand the caller an error. The caller
// holds a complete, executable answer, so the relay closes it as
// finish_reason=tool_calls and settles it normally instead of refunding and
// surfacing "truncated_stream".
func TestOaiStreamHandlerClosesMissingFinishToolCallStreamAsToolCalls(t *testing.T) {
	c, info, recorder := newMissingFinishTestContext(t)
	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"bash_1","type":"function","function":{"name":"bash","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"{\"command\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls -l /app\"}"}}]},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")

	usage, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	require.Nil(t, err, "a complete tool call cut only at the terminal frame must not be an error")
	require.NotNil(t, usage)
	assert.NotZero(t, usage.TotalTokens, "the delivered answer stays billable")
	assert.Equal(t, "tool_calls", info.StreamFinishReason)
	assert.True(t, info.ClosedAsToolCalls, "the success row must carry the closure marker")
	assert.Empty(t, info.UndeliveredReason, "the answer was delivered, so nothing stays undelivered")
	require.NotNil(t, info.StreamStatus)
	assert.False(t, info.StreamStatus.ResponseFailed())

	delivered := recorder.Body.String()
	assert.Contains(t, delivered, `"finish_reason":"tool_calls"`, "the caller must be told the call is complete")
	assert.NotContains(t, delivered, "truncated_stream", "a usable answer must not carry an error frame")
	assert.Contains(t, delivered, `ls -l /app`, "the held argument fragment must reach the caller before the terminal frame")
}

// TestOaiStreamHandlerKeepsErrorWhenToolCallCutMidArguments is the control: the
// provider was cut inside an argument document, so the call is not executable
// and the request must stay a refunded error.
func TestOaiStreamHandlerKeepsErrorWhenToolCallCutMidArguments(t *testing.T) {
	c, info, recorder := newMissingFinishTestContext(t)
	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"bash_1","type":"function","function":{"name":"bash","arguments":"{\"command\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls"}}]},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")

	usage, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	require.NotNil(t, err, "half an argument document must stay an error")
	assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	require.NotNil(t, usage)
	assert.Zero(t, usage.TotalTokens, "an unusable answer must not be billed")
	assert.Equal(t, "kimi-k3.1-pro", info.UpstreamModelName)
	assert.Empty(t, info.StreamFinishReason)
	assert.Contains(t, recorder.Body.String(), "truncated_stream")
	assert.NotContains(t, recorder.Body.String(), `"finish_reason":"tool_calls"`)
}

// TestOaiStreamHandlerKeepsAbnormalFinishWithCompleteToolCallsAsError pins the
// scope of the compensation: it applies to a stream that ended without any
// terminal frame. A provider that explicitly reported a failed generation
// (finish_reason=error) did not deliver a trustworthy answer even when the
// argument JSON happens to parse, so the request stays an error.
func TestOaiStreamHandlerKeepsAbnormalFinishWithCompleteToolCallsAsError(t *testing.T) {
	c, info, _ := newMissingFinishTestContext(t)
	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"bash_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")

	usage, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	require.NotNil(t, err)
	assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	require.NotNil(t, usage)
	assert.Zero(t, usage.TotalTokens)
}
