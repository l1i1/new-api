package openai

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

const prefillBaseJSON = `{"model":"kimi-k3.1-pro","messages":[{"role":"user","content":"写一段介绍"}],"max_tokens":64000,"stream":true}`

func lastMessage(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var decoded struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode continuation body: %v", err)
	}
	if len(decoded.Messages) == 0 {
		t.Fatal("continuation body carries no messages")
	}
	return decoded.Messages[len(decoded.Messages)-1]
}

// TestBuildChatContinuationBodyUsesKimiPartial pins the native prefill shape:
// the assistant prefix is the last message with partial=true (plus
// reasoning_content in thinking mode), no instruction user turn is appended, and
// the model stays the same.
func TestBuildChatContinuationBodyUsesKimiPartial(t *testing.T) {
	out, err := BuildChatContinuationBody([]byte(prefillBaseJSON), "前半段答案", "已思考的内容", 32000, dto.PrefillKimiPartial)
	if err != nil {
		t.Fatalf("build prefill body: %v", err)
	}
	last := lastMessage(t, out)
	if last["role"] != "assistant" {
		t.Fatalf("last message role = %v, want assistant", last["role"])
	}
	if last["content"] != "前半段答案" {
		t.Fatalf("last message content = %v", last["content"])
	}
	if last["partial"] != true {
		t.Fatalf("partial flag missing: %v", last)
	}
	if last["reasoning_content"] != "已思考的内容" {
		t.Fatalf("reasoning_content not carried: %v", last)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if decoded["model"] != "kimi-k3.1-pro" {
		t.Fatalf("model changed: %v", decoded["model"])
	}
	if decoded["max_tokens"] != float64(32000) {
		t.Fatalf("max_tokens = %v, want the remaining budget", decoded["max_tokens"])
	}
	text := string(out)
	if strings.Contains(text, "continue from where you stopped") {
		t.Fatal("prefill body must not carry the instruction-style hint")
	}
}

// TestBuildChatContinuationBodyPrefillDialects pins the two sibling dialects so a
// channel can be configured with the field its provider documents.
func TestBuildChatContinuationBodyPrefillDialects(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		field string
	}{
		{dto.PrefillQwenPartial, "partial"},
		{dto.PrefillDeepseekPrefix, "prefix"},
	} {
		out, err := BuildChatContinuationBody([]byte(prefillBaseJSON), "前缀", "", 4096, tc.mode)
		if err != nil {
			t.Fatalf("%s: %v", tc.mode, err)
		}
		last := lastMessage(t, out)
		if last[tc.field] != true {
			t.Fatalf("%s: %s flag missing: %v", tc.mode, tc.field, last)
		}
		if _, ok := last["reasoning_content"]; ok {
			t.Fatalf("%s: reasoning_content must only accompany kimi partial", tc.mode)
		}
	}
}

// TestBuildChatContinuationBodyPrefillFallsBackForJSON pins the documented
// incompatibility: Kimi says not to mix Partial Mode with response_format
// json_object, so that request keeps the instruction-style continuation.
func TestBuildChatContinuationBodyPrefillFallsBackForJSON(t *testing.T) {
	base := `{"model":"kimi-k3.1-pro","messages":[{"role":"user","content":"返回 JSON"}],"response_format":{"type":"json_object"},"max_tokens":64000,"stream":true}`
	out, err := BuildChatContinuationBody([]byte(base), "前半段", "", 32000, dto.PrefillKimiPartial)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	last := lastMessage(t, out)
	if last["role"] != "user" {
		t.Fatalf("json_object must keep the instruction-style body, last role = %v", last["role"])
	}
	if _, ok := last["partial"]; ok {
		t.Fatal("json_object body must not carry partial")
	}
}

// TestBuildChatContinuationBodyPrefillNeedsContent pins the guard that a cut
// during thinking is not resumed with an empty prefix (the provider would
// restart from scratch).
func TestBuildChatContinuationBodyPrefillNeedsContent(t *testing.T) {
	out, err := BuildChatContinuationBody([]byte(prefillBaseJSON), "  ", "只有思考", 32000, dto.PrefillKimiPartial)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	last := lastMessage(t, out)
	if last["role"] != "user" {
		t.Fatalf("empty prefix must fall back to the instruction body, last role = %v", last["role"])
	}
}

// TestBuildChatAnswerContinuationBodyRejectsPrefill pins that a reasoning-only
// resume cannot be expressed as a native prefill and is refused loudly instead
// of spending a round that restarts the answer.
func TestBuildChatAnswerContinuationBodyRejectsPrefill(t *testing.T) {
	if _, err := BuildChatAnswerContinuationBody([]byte(prefillBaseJSON), "思考", 32000, dto.PrefillKimiPartial); err == nil {
		t.Fatal("reasoning-only resume must be rejected on a prefill channel")
	}
}

// TestChannelSettingsPrefillCapability pins the save-time contract of the new
// channel setting: only the documented dialects are accepted.
func TestChannelSettingsPrefillCapability(t *testing.T) {
	for _, ok := range []string{"", dto.PrefillKimiPartial, dto.PrefillDeepseekPrefix, dto.PrefillQwenPartial} {
		if err := (dto.ChannelSettings{Prefill: ok}).ValidatePrefill(); err != nil {
			t.Fatalf("mode %q rejected: %v", ok, err)
		}
	}
	if err := (dto.ChannelSettings{Prefill: "kimi-prefill"}).ValidatePrefill(); err == nil {
		t.Fatal("an unknown dialect must be rejected at save time")
	}
	if !(dto.ChannelSettings{Prefill: dto.PrefillKimiPartial}).PrefillRequiresVisibleContent() {
		t.Fatal("kimi partial requires visible content")
	}
	if (dto.ChannelSettings{}).PrefillRequiresVisibleContent() {
		t.Fatal("the instruction fallback does not require a visible prefix")
	}
}

// TestOaiStreamHandlerUsesNativePrefillOnContentCut pins the end-to-end shape: a
// content-bearing cut on a prefill channel resumes with the native assistant
// prefix, and a real terminal frame from the resumed round closes the request as
// a success with one terminal frame and the merged usage.
func TestOaiStreamHandlerUsesNativePrefillOnContentCut(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"前半段"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	secondRound := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"后半段"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	var sent []byte
	c, info, recorder := newAbnormalContinuationTestContext(t, func(partial string, remaining int) (*http.Response, error) {
		body, err := BuildChatContinuationBody([]byte(prefillBaseJSON), partial, "", remaining, dto.PrefillKimiPartial)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		sent = body
		return continuationSSEResponse(secondRound), nil
	}, 1, 64000, 1<<20)
	info.ChatContinuation.PrefillMode = dto.PrefillKimiPartial
	info.ChatContinuation.MaxSteps = 1
	info.ChatContinuation.Send = info.ChatContinuation.Abnormal.Send
	info.ChatContinuation.Abnormal = nil
	info.ChatContinuation.Missing = &relaycommon.ChatAbnormalContinuationConfig{MaxSteps: 1, Send: info.ChatContinuation.Send, MaxReasoningBytes: 1 << 20, MinReasoningChars: 1}

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound))
	if err != nil {
		t.Fatalf("prefilled continuation should succeed: %v", err)
	}
	if usage == nil || usage.CompletionTokens != 13 {
		t.Fatalf("usage = %+v, want both rounds billed once", usage)
	}
	last := lastMessage(t, sent)
	if last["partial"] != true || last["content"] != "前半段" {
		t.Fatalf("resumed round did not carry the native prefix: %v", last)
	}
	body := recorder.Body.String()
	if strings.Count(body, `"finish_reason":"stop"`) != 1 {
		t.Fatalf("want one terminal frame, body=%s", body)
	}
	if strings.Count(body, "[DONE]") != 1 {
		t.Fatalf("want one [DONE], body=%s", body)
	}
}

// TestOaiStreamHandlerSkipsReasoningResumeOnPrefillChannel pins red line 7: a
// prefill channel cannot resume a cut that produced no visible prefix, so no
// round is spent and the caller sees the honest failure.
func TestOaiStreamHandlerSkipsReasoningResumeOnPrefillChannel(t *testing.T) {
	firstRound := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"` + strings.Repeat("思考", 300) + `"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	calls := 0
	c, info, _ := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		calls++
		return nil, http.ErrHandlerTimeout
	}, 1, 64000, 1<<20)
	// Wiring mirrors compatible_handler: a prefill channel leaves the reasoning
	// modes unbound.
	info.ChannelSetting.Prefill = dto.PrefillKimiPartial
	info.ChatContinuation.Abnormal = nil
	info.ChatContinuation.Missing = nil

	if _, err := OaiStreamHandler(c, info, continuationSSEResponse(firstRound)); err == nil {
		t.Fatal("a reasoning-only cut on a prefill channel must surface an error")
	}
	if calls != 0 {
		t.Fatalf("no reasoning resume may run on a prefill channel, calls=%d", calls)
	}
}
