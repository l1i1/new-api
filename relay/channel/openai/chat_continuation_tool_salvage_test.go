package openai

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// TestOaiStreamHandlerSalvagesCompleteToolCallsFromResumeRound pins the 2026-10-30
// finding: the resume round of a visible-content cut can switch to a tool call and
// then lose its terminal frame. When every buffered argument document is complete
// the call set is delivered once and closes as a tool_calls answer instead of
// surfacing a 502 for work that is usable.
func TestOaiStreamHandlerSalvagesCompleteToolCallsFromResumeRound(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"前半段答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	resumeRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expr\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"1+1\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	sent := 0
	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		sent++
		return continuationSSEResponse(resumeRound), nil
	}, 1, 64000, 1<<20)
	info.ChatContinuation.MaxSteps = 1
	info.ChatContinuation.Send = info.ChatContinuation.Abnormal.Send
	info.ChatContinuation.Missing = &relaycommon.ChatAbnormalContinuationConfig{MaxSteps: 1, Send: info.ChatContinuation.Send, MaxReasoningBytes: 1 << 20, MinReasoningChars: 1}
	info.ChatContinuation.Abnormal = nil

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	if err != nil {
		t.Fatalf("a complete tool-call set from the resume round is a usable answer: %v", err)
	}
	if sent != 1 {
		t.Fatalf("want one resume round, got %d", sent)
	}
	if usage == nil || usage.CompletionTokens < 10 {
		t.Fatalf("the salvaged round must be billed: %+v", usage)
	}
	body := recorder.Body.String()
	if strings.Count(body, `"delta":{"tool_calls"`) != 1 {
		t.Fatalf("want exactly one consolidated tool-call event, body=%s", body)
	}
	if strings.Count(body, `"finish_reason":"tool_calls"`) != 1 {
		t.Fatalf("want one tool_calls terminal, body=%s", body)
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:"))
		if line == "" || line == "[DONE]" || !strings.Contains(line, `"tool_calls"`) {
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
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			t.Fatalf("tool-call event is not JSON: %v (%s)", err, line)
		}
		for _, choice := range chunk.Choices {
			for _, call := range choice.Delta.ToolCalls {
				if !json.Valid([]byte(call.Function.Arguments)) {
					t.Fatalf("a partial argument document reached the caller: %q", call.Function.Arguments)
				}
			}
		}
	}
	if !strings.Contains(body, "前半段答案") {
		t.Fatal("the first round's delivered prefix must stay")
	}
}

// TestOaiStreamHandlerDropsIncompleteToolCallsFromResumeRound pins the other half
// of red line 4 in the repair path: a resumed call cut mid-argument is dropped
// entirely, never delivered, and the request fails honestly with no terminal frame.
func TestOaiStreamHandlerDropsIncompleteToolCallsFromResumeRound(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"前半段答案"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	resumeRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expr\":\"1+"}}]},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return continuationSSEResponse(resumeRound), nil
	}, 1, 64000, 1<<20)
	info.ChatContinuation.MaxSteps = 1
	info.ChatContinuation.Send = info.ChatContinuation.Abnormal.Send
	info.ChatContinuation.Missing = &relaycommon.ChatAbnormalContinuationConfig{MaxSteps: 1, Send: info.ChatContinuation.Send, MaxReasoningBytes: 1 << 20, MinReasoningChars: 1}
	info.ChatContinuation.Abnormal = nil

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	if err == nil {
		t.Fatal("half a tool call from the resume round must fail the request")
	}
	if !err.IsTruncatedStream() {
		t.Fatalf("want a truncated-stream error, got %v", err)
	}
	if usage == nil || usage.TotalTokens != 0 {
		t.Fatalf("the unusable round stays unbilled: %+v", usage)
	}
	body := recorder.Body.String()
	if strings.Contains(body, `"expr\"`) || strings.Contains(body, "calculator") {
		t.Fatalf("half a tool call reached the caller: %s", body)
	}
	if strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("no terminal frame may close an undelivered call: %s", body)
	}
}
