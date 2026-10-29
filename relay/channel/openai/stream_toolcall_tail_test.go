package openai

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
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

// TestOaiStreamHandlerClosesTerminalStreamWhenProviderHangs pins the 2026-10-29
// neurvibe ch48 shape end to end: the upstream delivered the whole tool call and
// its terminal finish_reason, then held the SSE connection open without [DONE]
// or EOF. The answer is complete, so the handler must close the caller's stream
// after the tail grace, hand over the terminal frame and [DONE], and settle the
// usage — not wait for the idle timeout and return "upstream stream terminated".
func TestOaiStreamHandlerClosesTerminalStreamWhenProviderHangs(t *testing.T) {
	oldTail := constant.StreamTerminalTailTimeout
	constant.StreamTerminalTailTimeout = 1
	t.Cleanup(func() { constant.StreamTerminalTailTimeout = oldTail })

	c, info, recorder := newMissingFinishTestContext(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	go func() {
		_, _ = fmt.Fprint(pw, strings.Join([]string{
			`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"bash_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls -l /app\"}"}}]},"finish_reason":null}]}`,
			`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			``,
		}, "\n"))
		// The provider then holds the connection open: no [DONE], no EOF. The
		// relay must not sit here until the idle timeout.
		time.Sleep(5 * time.Second)
	}()

	started := time.Now()
	usage, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})
	elapsed := time.Since(started)

	require.Nil(t, err, "a completed answer must not be failed because the transport stayed open")
	require.NotNil(t, usage)
	assert.NotZero(t, usage.TotalTokens, "the delivered tool call stays billable")
	assert.Equal(t, "tool_calls", info.StreamFinishReason)
	require.NotNil(t, info.StreamStatus)
	assert.True(t, info.StreamStatus.IsNormalEnd())
	assert.True(t, info.StreamStatus.ClosedAfterTerminal())
	assert.Empty(t, info.UndeliveredReason)
	assert.Less(t, elapsed, 4*time.Second, "the tail grace must end the stream long before the idle timeout")

	delivered := recorder.Body.String()
	assert.Contains(t, delivered, `"finish_reason":"tool_calls"`, "the caller must receive the terminal frame")
	assert.Equal(t, 1, strings.Count(delivered, `"finish_reason":"tool_calls"`), "exactly one terminal frame")
	assert.Contains(t, delivered, `ls -l /app`, "the delivered tool call must reach the caller")
	assert.Equal(t, 1, strings.Count(delivered, "data: [DONE]"), "exactly one [DONE]")
	assert.NotContains(t, delivered, "truncated_stream")
	assert.NotContains(t, delivered, "upstream stream terminated")
}

// TestOaiStreamHandlerDoesNotTailCloseMultiChoice pins the n>1 exclusion: choice
// 0 can finish while another choice is still generating, so a terminal frame on
// one choice must not end the response. The request keeps the old idle-timeout
// path instead of being closed early with a truncated second choice.
func TestOaiStreamHandlerDoesNotTailCloseMultiChoice(t *testing.T) {
	c, info, _ := newMissingFinishTestContext(t)
	oldTail, oldTimeout := constant.StreamTerminalTailTimeout, constant.StreamingTimeout
	constant.StreamTerminalTailTimeout, constant.StreamingTimeout = 1, 2
	t.Cleanup(func() {
		constant.StreamTerminalTailTimeout, constant.StreamingTimeout = oldTail, oldTimeout
	})
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	go func() {
		_, _ = fmt.Fprint(pw, strings.Join([]string{
			`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"},{"index":1,"delta":{"content":"still generating"},"finish_reason":null}]}`,
			``,
		}, "\n"))
		time.Sleep(5 * time.Second)
	}()

	started := time.Now()
	_, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	require.NotNil(t, err, "a multi-choice stream must keep the idle-timeout path")
	assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	assert.False(t, info.StreamStatus.ClosedAfterTerminal())
	assert.GreaterOrEqual(t, time.Since(started), 2*time.Second)
}

// TestOaiStreamHandlerDoesNotTailCloseLength pins the second exclusion: a
// provider can emit finish_reason=length and then keep streaming the answer it
// was cut from (the shape recorded next to shouldDeferTerminalForRepair), so a
// length frame is not final for the caller and must not arm the tail close.
func TestOaiStreamHandlerDoesNotTailCloseLength(t *testing.T) {
	c, info, recorder := newMissingFinishTestContext(t)
	oldTail, oldTimeout := constant.StreamTerminalTailTimeout, constant.StreamingTimeout
	constant.StreamTerminalTailTimeout, constant.StreamingTimeout = 1, 5
	t.Cleanup(func() {
		constant.StreamTerminalTailTimeout, constant.StreamingTimeout = oldTail, oldTimeout
	})
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	go func() {
		_, _ = fmt.Fprint(pw, `data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`+"\n")
		// The provider resumes right after the length frame; the relay must keep
		// reading instead of closing the completed-looking stream.
		for i := 0; i < 12; i++ {
			time.Sleep(250 * time.Millisecond)
			_, _ = fmt.Fprintf(pw, "data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"created\":1710000000,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"part%d \"},\"finish_reason\":null}]}\n", i)
		}
		_, _ = fmt.Fprint(pw, `data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n")
		_, _ = fmt.Fprint(pw, "data: [DONE]\n\n")
	}()

	usage, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.False(t, info.StreamStatus.ClosedAfterTerminal(), "a length frame must not arm the tail close")
	assert.Contains(t, recorder.Body.String(), "part11", "the provider's resumed content must be delivered")
}
