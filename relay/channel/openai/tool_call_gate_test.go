package openai

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	rootcommon "github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// toolCallArguments extracts every tool-call argument document from a rendered
// SSE body, so a test can prove the caller only ever saw complete JSON.
func toolCallArguments(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			for _, call := range choice.Delta.ToolCalls {
				if call.Function.Arguments != "" {
					out = append(out, call.Function.Arguments)
				}
			}
		}
	}
	return out
}

func enableToolBuffer(t *testing.T, channelID int, buffer, repair bool) {
	t.Helper()
	oldBuffer, oldRepair := rootcommon.ChatToolBufferChannelIDs, rootcommon.ChatToolRepairChannelIDs
	t.Cleanup(func() {
		rootcommon.ChatToolBufferChannelIDs, rootcommon.ChatToolRepairChannelIDs = oldBuffer, oldRepair
	})
	rootcommon.ChatToolBufferChannelIDs = map[int]struct{}{}
	rootcommon.ChatToolRepairChannelIDs = map[int]struct{}{}
	if buffer {
		rootcommon.ChatToolBufferChannelIDs[channelID] = struct{}{}
	}
	if repair {
		rootcommon.ChatToolRepairChannelIDs[channelID] = struct{}{}
	}
}

// TestOaiStreamHandlerBuffersToolCallDeltas pins the buffering switch: the caller
// never sees a fragment of the argument document, and the terminal frame is
// preceded by exactly one consolidated call event carrying the complete JSON.
func TestOaiStreamHandlerBuffersToolCallDeltas(t *testing.T) {
	enableToolBuffer(t, 777, true, false)
	stream := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expr\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"1+1\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return nil, http.ErrHandlerTimeout
	}, 1, 64000, 1<<20)
	info.ChannelId = 777
	info.ChatContinuation.Abnormal = nil

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(stream))
	if err != nil {
		t.Fatalf("a complete buffered call set must succeed: %v", err)
	}
	if usage == nil || usage.CompletionTokens != 10 {
		t.Fatalf("usage = %+v", usage)
	}
	body := recorder.Body.String()
	for _, args := range toolCallArguments(body) {
		if !json.Valid([]byte(args)) {
			t.Fatalf("a partial argument document reached the caller: %q", args)
		}
	}
	if !strings.Contains(body, `calculator`) || !strings.Contains(body, `1+1`) {
		t.Fatalf("the consolidated call is missing: %s", body)
	}
	if strings.Count(body, `"tool_calls":[{"function"`) != 1 {
		t.Fatalf("want exactly one consolidated call event, body=%s", body)
	}
	if strings.Count(body, `"finish_reason":"tool_calls"`) != 1 {
		t.Fatalf("want one terminal frame, body=%s", body)
	}
}

// TestOaiStreamHandlerNeverForwardsHalfToolCall pins red line 4 on a cut: the
// buffered fragment is dropped, no terminal frame is rendered and the caller gets
// the truncated-stream error instead of a document it cannot execute.
func TestOaiStreamHandlerNeverForwardsHalfToolCall(t *testing.T) {
	enableToolBuffer(t, 778, true, false)
	stream := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expr\":\"1+"}}]},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return nil, http.ErrHandlerTimeout
	}, 1, 64000, 1<<20)
	info.ChannelId = 778
	info.ChatContinuation.Abnormal = nil

	_, err := OaiStreamHandler(c, info, continuationSSEResponse(stream))
	if err == nil {
		t.Fatal("a cut inside a call must fail, not close")
	}
	if !err.IsTruncatedStream() {
		t.Fatalf("want a truncated-stream error, got %v", err)
	}
	body := recorder.Body.String()
	if strings.Contains(body, `"expr":"1+`) {
		t.Fatalf("half a tool call reached the caller: %s", body)
	}
	if strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("no terminal frame may close an undelivered call: %s", body)
	}
}

// TestOaiStreamHandlerRegeneratesCutToolCall pins the regeneration switch: with no
// visible text delivered, the cut call is re-generated on another channel, its
// complete call set is delivered and the round is billed.
func TestOaiStreamHandlerRegeneratesCutToolCall(t *testing.T) {
	enableToolBuffer(t, 779, true, true)
	stream := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expr\":"}}]},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	regenerated := `{"id":"chat_2","object":"chat.completion","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_2","type":"function","function":{"name":"calculator","arguments":"{\"expr\":\"2+2\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`

	calls := 0
	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return nil, http.ErrHandlerTimeout
	}, 1, 64000, 1<<20)
	info.ChannelId = 779
	info.ChatContinuation.Abnormal = nil
	info.ChatContinuation.Regenerate = func(remaining int) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(regenerated))}, nil
	}

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(stream))
	if err != nil {
		t.Fatalf("a regenerated complete call must succeed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("want exactly one regeneration, got %d", calls)
	}
	if usage == nil || usage.CompletionTokens == 0 {
		t.Fatalf("regenerated round must be billed: %+v", usage)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `2+2`) {
		t.Fatalf("regenerated call missing: %s", body)
	}
	if strings.Count(body, `"finish_reason":"tool_calls"`) != 1 {
		t.Fatalf("want one terminal frame, body=%s", body)
	}
}

// TestOaiStreamHandlerRegeneratesAfterVisiblePreamble pins the 2026-10-30 operator
// decision: a cut tool-call stream usually delivers a short preamble before the
// call, and refusing to regenerate because of it turned a recoverable call into a
// 502. Regeneration now runs; the regenerated answer is only accepted when it is a
// pure call set, so the caller sees "preamble, consolidated call, terminal".
func TestOaiStreamHandlerRegeneratesAfterVisiblePreamble(t *testing.T) {
	enableToolBuffer(t, 780, true, true)
	stream := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"先说一句"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expr\":"}}]},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	regenerated := `{"id":"chat_9","created":9,"model":"kimi-k3.1-pro","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_9","type":"function","function":{"name":"calculator","arguments":"{\"expr\":\"4+4\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`

	calls := 0
	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return nil, http.ErrHandlerTimeout
	}, 1, 64000, 1<<20)
	info.ChannelId = 780
	info.ChatContinuation.Abnormal = nil
	info.ChatContinuation.Regenerate = func(int) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(regenerated))}, nil
	}

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(stream))
	if err != nil {
		t.Fatalf("a recoverable call must not fail: %v", err)
	}
	if usage == nil || usage.CompletionTokens != 3 || usage.PromptTokens != 7 {
		t.Fatalf("the regenerated round is billed once: %+v", usage)
	}
	if calls != 1 {
		t.Fatalf("regeneration must run once after a visible preamble, calls=%d", calls)
	}
	body := recorder.Body.String()
	if strings.Count(body, "先说一句") != 1 {
		t.Fatalf("the preamble must appear exactly once: %s", body)
	}
	if !strings.Contains(body, "4+4") {
		t.Fatalf("the regenerated call must be delivered: %s", body)
	}
	if strings.Contains(body, `"expr\":`) && strings.Contains(body, `{"expr":`) {
		t.Fatalf("half an argument document must not reach the caller: %s", body)
	}
	if strings.Count(body, `"finish_reason":"tool_calls"`) != 1 {
		t.Fatalf("want one tool_calls terminal: %s", body)
	}
}

// TestOaiStreamHandlerRefusesRegeneratedTextAfterPreamble is the guard: the
// regeneration exists to recover the CALL, so a regenerated answer that carries
// its own text is refused (it would duplicate the preamble) and the cut stays an
// honest error.
func TestOaiStreamHandlerRefusesRegeneratedTextAfterPreamble(t *testing.T) {
	enableToolBuffer(t, 783, true, true)
	stream := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"先说一句"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expr\":"}}]},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	regenerated := `{"id":"chat_9","created":9,"model":"kimi-k3.1-pro","choices":[{"index":0,"message":{"role":"assistant","content":"另一段正文","tool_calls":[{"id":"call_9","type":"function","function":{"name":"calculator","arguments":"{\"expr\":\"5+5\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`

	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return nil, http.ErrHandlerTimeout
	}, 1, 64000, 1<<20)
	info.ChannelId = 783
	info.ChatContinuation.Abnormal = nil
	info.ChatContinuation.Regenerate = func(int) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(regenerated))}, nil
	}

	if _, err := OaiStreamHandler(c, info, continuationSSEResponse(stream)); err == nil {
		t.Fatal("a regeneration that returns its own text must be refused")
	}
	body := recorder.Body.String()
	if strings.Contains(body, "另一段正文") || strings.Contains(body, "5+5") {
		t.Fatalf("the refused regeneration must not be delivered: %s", body)
	}
	if strings.Count(body, "先说一句") != 1 {
		t.Fatalf("the delivered preamble stays: %s", body)
	}
}

// TestToolCallGateLeavesTextUntouched is the control: text/reasoning events pass
// through the gate unchanged when buffering is enabled.
func TestToolCallGateLeavesTextUntouched(t *testing.T) {
	enableToolBuffer(t, 781, true, false)
	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return nil, http.ErrHandlerTimeout
	}, 1, 64000, 1<<20)
	info.ChannelId = 781
	info.ToolCallBuffer = relaycommon.NewChatToolCallBuffer(true)
	if _, handled := gateToolCallChunk(c, info, `{"choices":[{"index":0,"delta":{"content":"hi"}}]}`); handled {
		t.Fatal("text deltas must not be gated")
	}
	if recorder.Body.Len() != 0 {
		t.Fatal("the gate must not write anything by itself")
	}
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(`[]`), &decoded); err != nil {
		t.Fatal(err)
	}
}

// TestToolCallGateForwardsTextBesideACallFragment pins the review finding that a
// chunk can carry text (or a terminal frame) together with a call fragment: only
// the fragment may be held, otherwise the caller silently loses answer text.
func TestToolCallGateForwardsTextBesideACallFragment(t *testing.T) {
	enableToolBuffer(t, 782, true, false)
	stream := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"先说明：","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expr\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"1+1\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return nil, http.ErrHandlerTimeout
	}, 1, 64000, 1<<20)
	info.ChannelId = 782
	info.ChatContinuation.Abnormal = nil

	if _, err := OaiStreamHandler(c, info, continuationSSEResponse(stream)); err != nil {
		t.Fatalf("buffered calls with text beside a fragment must succeed: %v", err)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "先说明：") {
		t.Fatalf("text carried beside a call fragment was dropped: %s", body)
	}
	args := toolCallArguments(body)
	if len(args) != 1 || !json.Valid([]byte(args[0])) {
		t.Fatalf("want one complete call, got %v (body=%s)", args, body)
	}
	if strings.Count(body, `"delta":{"tool_calls"`) != 1 {
		t.Fatalf("want one consolidated call event: %s", body)
	}
	if strings.Count(body, `"finish_reason":"tool_calls"`) != 1 {
		t.Fatalf("want one terminal frame: %s", body)
	}
}

// TestToolRegenerationSkipsSameChannelAfterWall pins the wall-aware ladder: a
// request that already spent a whole upstream wall (>=10 minutes) is not retried
// on the channel that just hit that wall, because it would hit the same wall
// again. Only the cross-channel hop runs.
func TestToolRegenerationSkipsSameChannelAfterWall(t *testing.T) {
	enableToolBuffer(t, 784, true, true)
	cut := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expr\":"}}]},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	good := `{"id":"chat_9","created":9,"model":"kimi-k3.1-pro","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_9","type":"function","function":{"name":"calculator","arguments":"{\"expr\":\"6+6\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`

	sameCalls := 0
	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return nil, http.ErrHandlerTimeout
	}, 1, 64000, 1<<20)
	info.ChannelId = 784
	info.StartTime = time.Now().Add(-700 * time.Second)
	info.ChatContinuation.Abnormal = nil
	info.ChatContinuation.RegenerateSame = func(int) (*http.Response, error) {
		sameCalls++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(good))}, nil
	}
	info.ChatContinuation.Regenerate = func(int) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(good))}, nil
	}

	if _, err := OaiStreamHandler(c, info, continuationSSEResponse(cut)); err != nil {
		t.Fatalf("the cross-channel rung should recover the call: %v", err)
	}
	if sameCalls != 0 {
		t.Fatalf("a wall-spent request must not retry the channel that hit the wall, sameCalls=%d", sameCalls)
	}
	if !strings.Contains(recorder.Body.String(), "6+6") {
		t.Fatal("regenerated call missing")
	}
}

// TestToolBufferClosesWallCutCallAsToolCalls pins the 900s-wall shape: content, a
// complete call, then the provider terminal carrying finish_reason=error. The
// terminal must be held so the buffered set closes as a real tool_calls answer
// instead of the caller receiving an "error" terminal and a 502 (2026-10-30
// 05:10-05:26 ch102/ch91, the dominant remaining failure).
func TestToolBufferClosesWallCutCallAsToolCalls(t *testing.T) {
	enableToolBuffer(t, 785, true, false)
	stream := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"我来查一下"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Hangzhou\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":9,"completion_tokens":12,"total_tokens":21}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return nil, http.ErrHandlerTimeout
	}, 1, 64000, 1<<20)
	info.ChannelId = 785
	info.ChatContinuation.Abnormal = nil

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(stream))
	if err != nil {
		t.Fatalf("a complete buffered call set is a usable answer: %v", err)
	}
	if usage == nil || usage.CompletionTokens != 12 {
		t.Fatalf("the closed answer is billed: %+v", usage)
	}
	body := recorder.Body.String()
	if strings.Contains(body, `"finish_reason":"error"`) {
		t.Fatalf("the provider error terminal must not reach the caller: %s", body)
	}
	if strings.Count(body, `"finish_reason":"tool_calls"`) != 1 {
		t.Fatalf("want one tool_calls terminal: %s", body)
	}
	if !strings.Contains(body, `\"city\":\"Hangzhou\"`) {
		t.Fatalf("the complete call must be delivered: %s", body)
	}
	if strings.Count(body, "我来查一下") != 1 {
		t.Fatalf("the delivered preamble must appear once: %s", body)
	}
}

// resetAfterReader returns its payload once and then fails like a dropped
// upstream connection: the transport cut lands after the answer bytes.
type resetAfterReader struct {
	data string
	done bool
}

func (r *resetAfterReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		return copy(p, r.data), nil
	}
	return 0, errors.New("connection reset by peer")
}

// TestToolBufferSalvagesCallAfterTransportCut pins the 2026-10-30 05:38 class: the
// provider drops the connection while a tool call is in progress, so the loop ends
// through the transport-error branch before the normal close path. The buffered
// call set is complete, so the answer must close as a real tool_calls completion
// instead of a 502.
func TestToolBufferSalvagesCallAfterTransportCut(t *testing.T) {
	enableToolBuffer(t, 786, true, false)
	payload := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"稍等"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Hangzhou\"}"}}]},"finish_reason":null}]}`,
		``,
	}, "\n") + "\n"

	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return nil, http.ErrHandlerTimeout
	}, 1, 64000, 1<<20)
	info.ChannelId = 786
	info.ChatContinuation.Abnormal = nil

	usage, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(&resetAfterReader{data: payload}),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})
	if err != nil {
		t.Fatalf("a complete buffered call set survives a transport cut: %v", err)
	}
	if usage == nil {
		t.Fatal("usage missing")
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `\"city\":\"Hangzhou\"`) {
		t.Fatalf("the complete call must be delivered: %s", body)
	}
	if strings.Count(body, `"finish_reason":"tool_calls"`) != 1 {
		t.Fatalf("want one tool_calls terminal: %s", body)
	}
}
