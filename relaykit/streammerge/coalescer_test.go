package streammerge

import (
	"encoding/json"
	"testing"
	"time"
)

func chunk(id, model, delta string) string {
	return `{"id":"` + id + `","object":"chat.completion.chunk","created":111,"model":"` + model + `",` +
		`"choices":[{"index":0,"delta":` + delta + `,"finish_reason":null}],"system_fingerprint":"fpv0_x"}`
}

func deltaText(t *testing.T, event, kind string) string {
	t.Helper()
	var parsed struct {
		Choices []struct {
			Delta map[string]string `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(event), &parsed); err != nil {
		t.Fatalf("merged event is not valid JSON: %v (%s)", err, event)
	}
	if len(parsed.Choices) != 1 {
		t.Fatalf("merged event must carry exactly one choice: %s", event)
	}
	return parsed.Choices[0].Delta[kind]
}

// The first event of a stream must not wait for the window: coalescing may not
// add latency to the first visible token.
func TestFirstEventLeavesImmediately(t *testing.T) {
	c := New(Config{MaxChars: 10000, MaxDelay: time.Hour})
	out := c.Add(chunk("c", "m", `{"content":"H"}`))
	if len(out) != 1 {
		t.Fatalf("the first delta must leave at once, got %v", out)
	}
	if got := deltaText(t, out[0], "content"); got != "H" {
		t.Fatalf("first event text = %q", got)
	}
	if out := c.Add(chunk("c", "m", `{"content":"e"}`)); len(out) != 0 {
		t.Fatalf("the second delta must batch, got %v", out)
	}
	out = c.Close()
	if len(out) != 1 {
		t.Fatalf("close must flush the batched tail, got %v", out)
	}
	if got := deltaText(t, out[0], "content"); got != "e" {
		t.Fatalf("batched text = %q", got)
	}
}

func TestBatchesAfterTheFirstEvent(t *testing.T) {
	c := New(Config{MaxChars: 1000, MaxDelay: time.Hour})
	out := c.Add(chunk("chatcmpl-1", "kimi-k3", `{"content":"Hel"}`))
	if len(out) != 1 {
		t.Fatalf("first event: %v", out)
	}
	for _, s := range []string{"lo", " wor", "ld"} {
		out = append(out, c.Add(chunk("chatcmpl-1", "kimi-k3", `{"content":"`+s+`"}`))...)
	}
	if len(out) != 1 {
		t.Fatalf("nothing should flush before the threshold: %v", out)
	}
	out = append(out, c.Close()...)
	if len(out) != 2 {
		t.Fatalf("want the first event plus the merged tail, got %d: %v", len(out), out)
	}
	if got := deltaText(t, out[1], "content"); got != "lo world" {
		t.Fatalf("merged text = %q, want %q", got, "lo world")
	}
	var env struct {
		ID                string `json:"id"`
		Model             string `json:"model"`
		Created           int64  `json:"created"`
		SystemFingerprint string `json:"system_fingerprint"`
	}
	if err := json.Unmarshal([]byte(out[1]), &env); err != nil {
		t.Fatal(err)
	}
	if env.ID != "chatcmpl-1" || env.Model != "kimi-k3" || env.Created != 111 || env.SystemFingerprint != "fpv0_x" {
		t.Fatalf("merged event lost the envelope: %+v", env)
	}
}

func TestFlushesOnCharacterThreshold(t *testing.T) {
	c := New(Config{MaxChars: 5, MaxDelay: time.Hour})
	if out := c.Add(chunk("c", "m", `{"content":"abc"}`)); len(out) != 1 {
		t.Fatalf("first event must leave immediately: %v", out)
	}
	if out := c.Add(chunk("c", "m", `{"content":"de"}`)); len(out) != 0 {
		t.Fatalf("2 chars must not flush a 5-char threshold: %v", out)
	}
	out := c.Add(chunk("c", "m", `{"content":"fgh"}`))
	if len(out) != 1 {
		t.Fatalf("5 chars must flush: %v", out)
	}
	if got := deltaText(t, out[0], "content"); got != "defgh" {
		t.Fatalf("merged text = %q", got)
	}
	if rest := c.Close(); len(rest) != 0 {
		t.Fatalf("buffer should be empty after the flush: %v", rest)
	}
}

func TestSeparatesReasoningFromContent(t *testing.T) {
	c := New(Config{MaxChars: 1000, MaxDelay: time.Hour})
	out := c.Add(chunk("c", "m", `{"reasoning_content":"think"}`))
	if len(out) != 1 {
		t.Fatalf("first reasoning event must leave at once: %v", out)
	}
	if got := deltaText(t, out[0], "reasoning_content"); got != "think" {
		t.Fatalf("reasoning text = %q", got)
	}
	if out := c.Add(chunk("c", "m", `{"content":"answer"}`)); len(out) != 0 {
		t.Fatalf("a kind change with nothing pending must buffer the new kind: %v", out)
	}
	out = c.Close()
	if len(out) != 1 {
		t.Fatalf("content must flush on close: %v", out)
	}
	if got := deltaText(t, out[0], "content"); got != "answer" {
		t.Fatalf("content text = %q", got)
	}
}

// A terminal event must never overtake buffered text: the text is flushed first
// and the terminal event keeps its original bytes.
func TestTerminalEventFlushesPendingTextFirst(t *testing.T) {
	c := New(Config{MaxChars: 1000, MaxDelay: time.Hour})
	c.Add(chunk("c", "m", `{"content":"first"}`))  // leaves immediately
	c.Add(chunk("c", "m", `{"content":"second"}`)) // buffered
	terminal := `{"id":"c","object":"chat.completion.chunk","created":111,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0}]},"finish_reason":"tool_calls"}]}`
	out := c.Add(terminal)
	if len(out) != 2 {
		t.Fatalf("want the flushed text plus the untouched terminal event, got %d: %v", len(out), out)
	}
	if got := deltaText(t, out[0], "content"); got != "second" {
		t.Fatalf("pending text = %q", got)
	}
	if out[1] != terminal {
		t.Fatalf("non-mergeable event must pass through byte-identical:\n got %s\nwant %s", out[1], terminal)
	}
}

func TestNonMergeableShapesPassThrough(t *testing.T) {
	cases := map[string]string{
		"role only":       chunk("c", "m", `{"role":"assistant","content":""}`),
		"multi key delta": chunk("c", "m", `{"content":"x","reasoning_content":"y"}`),
		"empty content":   chunk("c", "m", `{"content":""}`),
		"null content":    chunk("c", "m", `{"content":null}`),
		"usage":           `{"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"x"}}],"usage":{"completion_tokens":3}}`,
		"finish":          `{"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}`,
		"two choices":     `{"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"x"}},{"index":1,"delta":{"content":"y"}}]}`,
		"malformed":       `{"id":`,
		"no choices":      `{"id":"c","model":"m","choices":[]}`,
		"choice usage":    `{"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"x"},"usage":{"completion_tokens":1}}]}`,
	}
	for name, in := range cases {
		c := New(Config{MaxChars: 1000, MaxDelay: time.Hour})
		out := c.Add(in)
		if len(out) != 1 || out[0] != in {
			t.Fatalf("%s: must pass through untouched, got %v", name, out)
		}
		if rest := c.Close(); len(rest) != 0 {
			t.Fatalf("%s: nothing should be pending: %v", name, rest)
		}
	}
}

func TestDelayFlush(t *testing.T) {
	c := New(Config{MaxChars: 10000, MaxDelay: 5 * time.Millisecond})
	if out := c.Add(chunk("c", "m", `{"content":"a"}`)); len(out) != 1 {
		t.Fatalf("first event leaves immediately: %v", out)
	}
	c.Add(chunk("c", "m", `{"content":"b"}`))
	time.Sleep(10 * time.Millisecond)
	out := c.Add(chunk("c", "m", `{"content":"c"}`)) // window elapsed -> flush b+c
	if len(out) != 1 {
		t.Fatalf("the window must flush on the next event: %v", out)
	}
	if got := deltaText(t, out[0], "content"); got != "bc" {
		t.Fatalf("merged text = %q", got)
	}
}

func TestCloseStopsAcceptingInput(t *testing.T) {
	c := New(Config{MaxChars: 10, MaxDelay: time.Hour})
	if out := c.Close(); len(out) != 0 {
		t.Fatalf("empty close must emit nothing: %v", out)
	}
	if got := c.Add(chunk("c", "m", `{"content":"x"}`)); got != nil {
		t.Fatalf("a closed coalescer must ignore input: %v", got)
	}
	if c.Enabled() {
		t.Fatal("a closed coalescer must not report Enabled")
	}
}

func TestNilCoalescerIsInert(t *testing.T) {
	var c *Coalescer
	if c.Enabled() {
		t.Fatal("nil coalescer must be inert")
	}
	if out := c.Add(chunk("c", "m", `{"content":"x"}`)); out != nil {
		t.Fatalf("nil coalescer must emit nothing: %v", out)
	}
	if out := c.Close(); out != nil {
		t.Fatalf("nil coalescer must close to nothing: %v", out)
	}
}
