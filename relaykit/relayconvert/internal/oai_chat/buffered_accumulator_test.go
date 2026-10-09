package oaichat

import (
	"strings"
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

func chunk(t *testing.T, raw string) *dto.ChatCompletionsStreamResponse {
	t.Helper()
	var c dto.ChatCompletionsStreamResponse
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("test chunk is not valid JSON: %v (%s)", err, raw)
	}
	return &c
}

// The common case: text arrives split across chunks and must be concatenated in
// order, with the identity taken from the first chunk that carries it.
func TestChatBufferedAccumulatorMergesContentDeltas(t *testing.T) {
	a := NewChatBufferedAccumulator()
	for _, raw := range []string{
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"role":"assistant","content":"你"}}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"好"}}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	} {
		a.ProcessChunk(chunk(t, raw))
	}
	if !a.HasContent() {
		t.Fatal("HasContent = false, want true")
	}
	if got := a.FinishReason(); got != "stop" {
		t.Fatalf("FinishReason = %q, want stop", got)
	}
	out := a.BuildResponse("fallback-id", "fallback-model", 1)
	if out.Id != "chatcmpl-1" || out.Model != "kimi-k3" || out.Object != "chat.completion" {
		t.Fatalf("identity wrong: id=%q model=%q object=%q", out.Id, out.Model, out.Object)
	}
	if created, ok := out.Created.(int64); !ok || created != 1700000000 {
		t.Fatalf("created = %#v, want 1700000000", out.Created)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(out.Choices))
	}
	got := out.Choices[0]
	if got.Message.Role != "assistant" {
		t.Fatalf("role = %q, want assistant", got.Message.Role)
	}
	if content, _ := got.Message.Content.(string); content != "你好" {
		t.Fatalf("content = %#v, want 你好", got.Message.Content)
	}
	if got.FinishReason != "stop" {
		t.Fatalf("finish_reason = %q, want stop", got.FinishReason)
	}
}

// Reasoning arrives under either field name depending on the provider; both must
// land in the canonical reasoning_content of the buffered message.
func TestChatBufferedAccumulatorMergesBothReasoningFieldNames(t *testing.T) {
	a := NewChatBufferedAccumulator()
	a.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"reasoning_content":"想一"}}]}`))
	a.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"reasoning":"想二"}}]}`))
	a.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"content":"答"}}]}`))
	out := a.BuildResponse("i", "m", 0)
	msg := out.Choices[0].Message
	if msg.ReasoningContent == nil || *msg.ReasoningContent != "想一想二" {
		t.Fatalf("reasoning_content = %v, want 想一想二", msg.ReasoningContent)
	}
	if content, _ := msg.Content.(string); content != "答" {
		t.Fatalf("content = %#v, want 答", msg.Content)
	}
}

// tool_calls stream by index: identity arrives once, arguments arrive in
// fragments. Concatenating them wrongly produces invalid JSON for the caller.
func TestChatBufferedAccumulatorMergesToolCallFragments(t *testing.T) {
	a := NewChatBufferedAccumulator()
	for _, raw := range []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"ci"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ty\":\"sh\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"now","arguments":"{}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	} {
		a.ProcessChunk(chunk(t, raw))
	}
	// Content stays null when only tool calls were produced (OpenAI's shape).
	out := a.BuildResponse("i", "m", 0)
	msg := out.Choices[0].Message
	if msg.Content != nil {
		t.Fatalf("content = %#v, want nil for a tool-call-only answer", msg.Content)
	}
	var calls []dto.ToolCallResponse
	if err := json.Unmarshal(msg.ToolCalls, &calls); err != nil {
		t.Fatalf("tool_calls is not valid JSON: %v (%s)", err, string(msg.ToolCalls))
	}
	if len(calls) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(calls))
	}
	if calls[0].ID != "call_1" || calls[0].Function.Name != "get_weather" {
		t.Fatalf("call 0 identity wrong: %#v", calls[0])
	}
	if calls[0].Function.Arguments != `{"city":"sh"}` {
		t.Fatalf("call 0 arguments = %q, want {\"city\":\"sh\"}", calls[0].Function.Arguments)
	}
	if calls[1].ID != "call_2" || calls[1].Function.Arguments != "{}" {
		t.Fatalf("call 1 wrong: %#v", calls[1])
	}
	if out.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", out.Choices[0].FinishReason)
	}
}

// Moonshot K3 attaches terminal usage to choices[0].usage instead of the
// top-level object. Dropping it silently loses billing data, so both locations
// are read and the top-level value wins when present.
func TestChatBufferedAccumulatorHonoursChoiceLevelUsage(t *testing.T) {
	choiceOnly := NewChatBufferedAccumulator()
	choiceOnly.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"content":"hi"},"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}]}`))
	usage := choiceOnly.Usage()
	if usage == nil || usage.TotalTokens != 18 || usage.PromptTokens != 11 || usage.CompletionTokens != 7 {
		t.Fatalf("choice-level usage lost: %#v", usage)
	}
	out := choiceOnly.BuildResponse("i", "m", 0)
	if out.Usage.TotalTokens != 18 {
		t.Fatalf("buffered response usage = %#v, want total 18", out.Usage)
	}

	topLevel := NewChatBufferedAccumulator()
	topLevel.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"content":"hi"},"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	if u := topLevel.Usage(); u == nil || u.TotalTokens != 8 {
		t.Fatalf("top-level usage should win: %#v", u)
	}
}

// A usage-only terminal chunk (no choices) must still be recorded: providers that
// send include_usage put it exactly there.
func TestChatBufferedAccumulatorRecordsUsageOnlyChunk(t *testing.T) {
	a := NewChatBufferedAccumulator()
	a.ProcessChunk(chunk(t, `{"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`))
	if u := a.Usage(); u == nil || u.TotalTokens != 13 {
		t.Fatalf("usage-only chunk ignored: %#v", u)
	}
	if a.HasContent() {
		t.Fatal("HasContent = true for a usage-only chunk, want false")
	}
}

// Annotations stream as arrays and cite sources; merging must not drop either side.
func TestChatBufferedAccumulatorMergesAnnotations(t *testing.T) {
	a := NewChatBufferedAccumulator()
	a.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"content":"a","annotations":[{"url":"https://one"}]}}]}`))
	a.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"content":"b","annotations":[{"url":"https://two"}]}}]}`))
	out := a.BuildResponse("i", "m", 0)
	var anns []map[string]string
	if err := json.Unmarshal(out.Choices[0].Message.Annotations, &anns); err != nil {
		t.Fatalf("annotations not valid JSON: %v (%s)", err, string(out.Choices[0].Message.Annotations))
	}
	if len(anns) != 2 || anns[0]["url"] != "https://one" || anns[1]["url"] != "https://two" {
		t.Fatalf("annotations = %#v, want both entries", anns)
	}
}

// Multiple choices must keep their own identities and stay index-ordered.
func TestChatBufferedAccumulatorKeepsChoicesSeparate(t *testing.T) {
	a := NewChatBufferedAccumulator()
	a.ProcessChunk(chunk(t, `{"choices":[{"index":1,"delta":{"content":"B"}}]}`))
	a.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"content":"A"}}]}`))
	out := a.BuildResponse("i", "m", 0)
	if len(out.Choices) != 2 {
		t.Fatalf("choices = %d, want 2", len(out.Choices))
	}
	if out.Choices[0].Index != 0 || out.Choices[1].Index != 1 {
		t.Fatalf("choices not index-sorted: %#v", out.Choices)
	}
	if c0, _ := out.Choices[0].Message.Content.(string); c0 != "A" {
		t.Fatalf("choice 0 content = %#v, want A", out.Choices[0].Message.Content)
	}
	if c1, _ := out.Choices[1].Message.Content.(string); c1 != "B" {
		t.Fatalf("choice 1 content = %#v, want B", out.Choices[1].Message.Content)
	}
}

// A stream that never produced content or a finish_reason is the truncated case
// the handler refuses to answer with; the accumulator must expose that state.
func TestChatBufferedAccumulatorReportsTruncatedStream(t *testing.T) {
	a := NewChatBufferedAccumulator()
	if a.HasContent() || a.FinishReason() != "" {
		t.Fatal("empty accumulator should report no content and no finish reason")
	}
	a.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`))
	if a.HasContent() {
		t.Fatal("a role-only chunk must not count as content")
	}
	if a.FinishReason() != "" {
		t.Fatal("a role-only chunk must not report a finish reason")
	}
	a.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"length"}]}`))
	if !a.HasContent() || a.FinishReason() != "length" {
		t.Fatal("terminal chunk not recorded")
	}
}

// Identity fallbacks matter for a stream that died before the first chunk.
func TestChatBufferedAccumulatorFallsBackForIdentity(t *testing.T) {
	a := NewChatBufferedAccumulator()
	a.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}`))
	out := a.BuildResponse("req-id", "req-model", 42)
	if out.Id != "req-id" || out.Model != "req-model" {
		t.Fatalf("fallbacks not applied: id=%q model=%q", out.Id, out.Model)
	}
	if created, ok := out.Created.(int64); !ok || created != 42 {
		t.Fatalf("created = %#v, want the fallback 42", out.Created)
	}
}

// Review-driven: the streaming fingerprint must survive buffering, otherwise the
// non-streaming answer silently differs from what a streaming client would see.
func TestChatBufferedAccumulatorPreservesSystemFingerprint(t *testing.T) {
	a := NewChatBufferedAccumulator()
	a.ProcessChunk(chunk(t, `{"system_fingerprint":"fp_abc","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}`))
	out := a.BuildResponse("i", "m", 0)
	if out.SystemFingerprint == nil || *out.SystemFingerprint != "fp_abc" {
		t.Fatalf("system_fingerprint = %v, want fp_abc", out.SystemFingerprint)
	}
}

// Review-driven: per-chunk logprobs describe that chunk's tokens, so a buffered
// answer must concatenate them instead of keeping only the last fragment.
func TestChatBufferedAccumulatorConcatenatesLogprobs(t *testing.T) {
	a := NewChatBufferedAccumulator()
	a.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"content":"a"},"logprobs":{"content":[{"token":"a"}]}}]}`))
	a.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"content":"b"},"logprobs":{"content":[{"token":"b"}]},"finish_reason":"stop"}]}`))
	out := a.BuildResponse("i", "m", 0)
	lp := out.Choices[0].Logprobs
	if lp == nil {
		t.Fatalf("logprobs missing: %#v", out.Choices[0].Logprobs)
	}
	obj, ok := (*lp).(map[string]any)
	if !ok {
		t.Fatalf("logprobs shape changed: %#v", *lp)
	}
	arr, _ := obj["content"].([]any)
	if len(arr) != 2 {
		t.Fatalf("logprobs content length = %d, want 2 (concatenated)", len(arr))
	}
}

// Review-driven: a provider that omits tool_call indexes must not have separate
// calls fused into slot 0.
func TestChatBufferedAccumulatorSeparatesIndexlessToolCalls(t *testing.T) {
	a := NewChatBufferedAccumulator()
	a.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_a","type":"function","function":{"name":"first","arguments":"{}"}}]}}]}`))
	a.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_b","type":"function","function":{"name":"second","arguments":"{}"}}]}}]}`))
	out := a.BuildResponse("i", "m", 0)
	var calls []dto.ToolCallResponse
	if err := json.Unmarshal(out.Choices[0].Message.ToolCalls, &calls); err != nil {
		t.Fatalf("tool_calls invalid: %v", err)
	}
	if len(calls) != 2 || calls[0].Function.Name != "first" || calls[1].Function.Name != "second" {
		t.Fatalf("index-less tool calls fused: %#v", calls)
	}
}

// Review-driven: without a [DONE] marker every choice must have terminated;
// one finished choice does not prove the others did.
func TestChatBufferedAccumulatorRequiresAllChoicesToFinish(t *testing.T) {
	a := NewChatBufferedAccumulator()
	// One finished choice means every choice so far finished - the flag answers
	// "did all known choices terminate", so it is true here.
	a.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":"stop"}]}`))
	if !a.AllChoicesFinished() {
		t.Fatal("a single finished choice should count as all-finished")
	}
	// A second choice that never terminates must flip it back: that is the
	// partial multi-choice answer the handler refuses to send.
	a.ProcessChunk(chunk(t, `{"choices":[{"index":1,"delta":{"content":"b"}}]}`))
	if a.AllChoicesFinished() {
		t.Fatal("an unterminated second choice must make AllChoicesFinished false")
	}
	a.ProcessChunk(chunk(t, `{"choices":[{"index":1,"delta":{},"finish_reason":"stop"}]}`))
	if !a.AllChoicesFinished() {
		t.Fatal("both choices finished but AllChoicesFinished = false")
	}
	if a.ChoiceCount() != 2 {
		t.Fatalf("ChoiceCount = %d, want 2", a.ChoiceCount())
	}
}

// Review-driven: the estimation text must include reasoning and tool arguments,
// otherwise a thinking/tool answer is under-billed.
func TestChatBufferedAccumulatorEstimateTextIncludesReasoningAndTools(t *testing.T) {
	a := NewChatBufferedAccumulator()
	a.ProcessChunk(chunk(t, `{"choices":[{"index":0,"delta":{"reasoning_content":"思考","tool_calls":[{"index":0,"function":{"arguments":"{\"k\":1}"}}],"content":"答"},"finish_reason":"tool_calls"}]}`))
	text := a.Text()
	for _, want := range []string{"答", "思考", `{"k":1}`} {
		if !strings.Contains(text, want) {
			t.Fatalf("estimation text %q is missing %q", text, want)
		}
	}
}
