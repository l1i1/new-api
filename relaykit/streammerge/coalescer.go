// Package streammerge merges consecutive plain-text deltas of an OpenAI-style
// SSE stream into fewer events before they reach the client.
//
// Why: the Kimi family (Moonshot official and the NeurVibe aggregator alike)
// emits roughly two characters per SSE event, and each event carries ~200 bytes
// of JSON envelope. Measured on 2026-10-02: 2,380 events / 494 KB for 4.5 KB of
// text, i.e. ~100x framing overhead, and ~99% of the gateway's client-facing
// egress for that model. Merging consecutive same-kind text deltas is
// transparent to clients (any chunk granularity is valid SSE) and cuts the
// egress to roughly the payload size.
//
// Only the unambiguously mergeable shape is touched: a single choice whose
// delta carries exactly one of content/reasoning_content as a non-empty string,
// no finish_reason and no usage. Every other event (role-only, tool_calls,
// usage, terminal finish) flushes what is pending and passes through untouched.
package streammerge

import (
	"encoding/json"
	"strings"
	"time"
)

// Config tunes the merge window. Zero values fall back to the defaults.
type Config struct {
	// MaxChars flushes a pending buffer once it reaches this many characters.
	MaxChars int
	// MaxDelay flushes a pending buffer once this much time passed since its
	// first character. The upstream is chatty, so the delay is evaluated on
	// each Add call rather than with a timer goroutine.
	MaxDelay time.Duration
}

const (
	defaultMaxChars = 512
	defaultMaxDelay = 150 * time.Millisecond
)

type pending struct {
	choice int
	kind   string // "content" or "reasoning_content"
	text   strings.Builder
	start  time.Time
}

// Coalescer is a per-request state machine. It is not safe for concurrent use;
// one request owns one instance.
type Coalescer struct {
	cfg     Config
	current *pending
	// envelope carries the most recent id/created/model/system_fingerprint so a
	// merged event looks like the events it replaces.
	id                string
	created           int64
	model             string
	systemFingerprint *string
	closed            bool
	// emitted records whether any event already left this coalescer.
	emitted bool
}

// New creates a coalescer with defaults applied.
func New(cfg Config) *Coalescer {
	if cfg.MaxChars <= 0 {
		cfg.MaxChars = defaultMaxChars
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = defaultMaxDelay
	}
	return &Coalescer{cfg: cfg}
}

// Enabled reports whether a coalescer was configured.
func (c *Coalescer) Enabled() bool { return c != nil && !c.closed }

type rawChunk struct {
	ID                string          `json:"id"`
	Object            string          `json:"object"`
	Created           int64           `json:"created"`
	Model             string          `json:"model"`
	SystemFingerprint *string         `json:"system_fingerprint,omitempty"`
	Choices           []rawChoice     `json:"choices"`
	Usage             json.RawMessage `json:"usage"`
}

type rawChoice struct {
	Index        int             `json:"index"`
	Delta        json.RawMessage `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
	Usage        json.RawMessage `json:"usage"`
}

// Add feeds one upstream SSE data object (the JSON text, without the "data: "
// prefix) and returns zero or more events that must be written to the client.
func (c *Coalescer) Add(data string) []string {
	if c == nil || c.closed || data == "" {
		return nil
	}
	text, kind, choice, ok := c.classify(data)
	if !ok {
		out := c.flush()
		c.emitted = true
		return append(out, data)
	}
	c.remember(data)
	now := time.Now()
	var out []string
	// A different kind (reasoning vs content) or choice must not be merged into
	// one event: flush what is pending and start a fresh buffer.
	if c.current != nil && (c.current.kind != kind || c.current.choice != choice) {
		out = c.flush()
	}
	if c.current == nil {
		c.current = &pending{choice: choice, kind: kind, start: now}
	}
	c.current.text.WriteString(text)
	// The first event of a stream leaves immediately: coalescing must never add
	// latency to the first visible token. Later events batch until the character
	// threshold or the time window is reached.
	if !c.emitted || c.current.text.Len() >= c.cfg.MaxChars || now.Sub(c.current.start) >= c.cfg.MaxDelay {
		out = append(out, c.flush()...)
		c.emitted = true
	}
	return out
}

// Close flushes everything still pending and stops accepting input.
func (c *Coalescer) Close() []string {
	if c == nil || c.closed {
		return nil
	}
	out := c.flush()
	c.closed = true
	return out
}

// classify reports whether data is a mergeable single-choice text delta.
func (c *Coalescer) classify(data string) (text, kind string, choice int, ok bool) {
	var chunk rawChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return "", "", 0, false
	}
	if len(chunk.Usage) > 0 || len(chunk.Choices) != 1 {
		return "", "", 0, false
	}
	ch := chunk.Choices[0]
	if len(ch.Usage) > 0 || (ch.FinishReason != nil && *ch.FinishReason != "") {
		return "", "", 0, false
	}
	if len(ch.Delta) == 0 {
		return "", "", 0, false
	}
	var delta map[string]json.RawMessage
	if err := json.Unmarshal(ch.Delta, &delta); err != nil {
		return "", "", 0, false
	}
	if len(delta) != 1 {
		return "", "", 0, false
	}
	for _, k := range []string{"content", "reasoning_content"} {
		raw, present := delta[k]
		if !present {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil || s == "" {
			return "", "", 0, false
		}
		return s, k, ch.Index, true
	}
	return "", "", 0, false
}

// remember keeps the envelope of the latest coalescable event so the merged
// event can mirror it.
func (c *Coalescer) remember(data string) {
	var chunk rawChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return
	}
	if chunk.ID != "" {
		c.id = chunk.ID
	}
	if chunk.Created != 0 {
		c.created = chunk.Created
	}
	if chunk.Model != "" {
		c.model = chunk.Model
	}
	if chunk.SystemFingerprint != nil {
		c.systemFingerprint = chunk.SystemFingerprint
	}
}

// flush emits the pending buffer as one event.
func (c *Coalescer) flush() []string {
	if c.current == nil || c.current.text.Len() == 0 {
		c.current = nil
		return nil
	}
	cur := c.current
	c.current = nil
	delta := map[string]string{cur.kind: cur.text.String()}
	deltaRaw, err := json.Marshal(delta)
	if err != nil {
		return nil
	}
	event := map[string]any{
		"id":      c.id,
		"object":  "chat.completion.chunk",
		"created": c.created,
		"model":   c.model,
		"choices": []map[string]any{{
			"index":         cur.choice,
			"delta":         json.RawMessage(deltaRaw),
			"finish_reason": nil,
		}},
	}
	if c.systemFingerprint != nil {
		event["system_fingerprint"] = *c.systemFingerprint
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return nil
	}
	return []string{string(encoded)}
}
