package common

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// ChatToolCallBuffer holds the tool-call deltas of one streaming answer until the
// whole call set is known to be complete.
//
// A cut inside a tool call used to reach the caller as a truncated JSON argument
// document: the client then executes a broken call or retries with a half-parsed
// tool result (2026-10-30 review: 26 sampled ch102 cuts carried tool calls, 25 of
// them with partial argument JSON). Holding the deltas means the caller either
// receives every call with complete arguments or receives none of them, and the
// relay can regenerate instead of surfacing half a document.
type ChatToolCallBuffer struct {
	enabled bool
	invalid bool
	order   []int
	calls   map[int]*ChatBufferedToolCall
}

// ChatBufferedToolCall is one accumulated call of a buffered stream.
type ChatBufferedToolCall struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

// NewChatToolCallBuffer returns a buffer. A disabled buffer lets the handler skip
// every gate, so the option can be cleared without changing hot-path behaviour.
func NewChatToolCallBuffer(enabled bool) *ChatToolCallBuffer {
	if !enabled {
		return nil
	}
	return &ChatToolCallBuffer{enabled: true, calls: make(map[int]*ChatBufferedToolCall)}
}

// Enabled reports whether the gate is active for this request.
func (b *ChatToolCallBuffer) Enabled() bool { return b != nil && b.enabled }

// Invalid reports whether a complete call set could not be reconstructed.
func (b *ChatToolCallBuffer) Invalid() bool { return b != nil && b.invalid }

// CallCount reports how many calls the buffer holds (for diagnostics).
func (b *ChatToolCallBuffer) CallCount() int {
	if b == nil {
		return 0
	}
	return len(b.order)
}

// Empty reports whether nothing has been buffered yet.
func (b *ChatToolCallBuffer) Empty() bool { return b == nil || len(b.order) == 0 }

// Observe accumulates one chunk's tool-call deltas. Duplicate ids are not merged:
// the same index carries the fragments of one call, so an index seen twice is an
// accumulation, not a conflict; ids are validated at delivery time.
func (b *ChatToolCallBuffer) Observe(choices []dto.ChatCompletionsStreamResponseChoice) {
	if b == nil || !b.enabled {
		return
	}
	for _, choice := range choices {
		for _, call := range choice.Delta.ToolCalls {
			index := 0
			if call.Index != nil {
				index = *call.Index
			}
			state := b.calls[index]
			if state == nil {
				state = &ChatBufferedToolCall{}
				b.calls[index] = state
				b.order = append(b.order, index)
			}
			if state.ID == "" && call.ID != "" {
				state.ID = strings.TrimSpace(call.ID)
			}
			if state.Name == "" && call.Function.Name != "" {
				state.Name = strings.TrimSpace(call.Function.Name)
			}
			if call.Function.Arguments != "" {
				current := state.Arguments.String()
				fragment := call.Function.Arguments
				// Some providers resend the arguments as a cumulative snapshot.
				if current != "" && strings.HasPrefix(fragment, current) {
					state.Arguments.Reset()
					state.Arguments.WriteString(fragment)
				} else {
					state.Arguments.WriteString(fragment)
				}
			}
		}
	}
}

// Validate reports whether every buffered call is complete and deliverable: an
// id, a function name and a valid JSON argument document, with no id repeated.
func (b *ChatToolCallBuffer) Validate() error {
	if b == nil || !b.enabled {
		return nil
	}
	if len(b.order) == 0 {
		return fmt.Errorf("tool call buffer is empty")
	}
	seen := make(map[string]struct{}, len(b.order))
	for _, index := range b.order {
		state := b.calls[index]
		if state == nil {
			return fmt.Errorf("tool call %d missing", index)
		}
		if state.ID == "" {
			return fmt.Errorf("tool call %d has no id", index)
		}
		if _, ok := seen[state.ID]; ok {
			return fmt.Errorf("tool call id %q is duplicated", state.ID)
		}
		seen[state.ID] = struct{}{}
		if state.Name == "" {
			return fmt.Errorf("tool call %d has no function name", index)
		}
		raw := strings.TrimSpace(state.Arguments.String())
		if raw == "" {
			return fmt.Errorf("tool call %d has empty arguments", index)
		}
		if !json.Valid([]byte(raw)) {
			return fmt.Errorf("tool call %d arguments are not valid JSON", index)
		}
	}
	return nil
}

// MarkInvalid records that the buffered call set cannot be delivered, so the
// caller must be told the answer failed instead of receiving half a call.
func (b *ChatToolCallBuffer) MarkInvalid() {
	if b != nil {
		b.invalid = true
	}
}

// ConsolidatedChunk renders every buffered call as one complete delta event, so
// the caller receives each call's full argument document exactly once.
func (b *ChatToolCallBuffer) ConsolidatedChunk(id string, created int64, model string) string {
	if b == nil || len(b.order) == 0 {
		return ""
	}
	calls := make([]map[string]any, 0, len(b.order))
	for position, index := range b.order {
		state := b.calls[index]
		if state == nil {
			continue
		}
		calls = append(calls, map[string]any{
			"index": position,
			"id":    state.ID,
			"type":  "function",
			"function": map[string]any{
				"name":      state.Name,
				"arguments": state.Arguments.String(),
			},
		})
	}
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{"tool_calls": calls},
			"finish_reason": nil,
		}},
	}
	encoded, err := json.Marshal(chunk)
	if err != nil {
		return ""
	}
	// The SSE framing is added by the caller's writer (writeStreamData renders the
	// event with its own data:/blank-line encoding), so the chunk is raw JSON.
	return string(encoded)
}

// SetInvalidForTesting marks the buffer invalid (test seam).
func (b *ChatToolCallBuffer) SetInvalidForTesting() { b.MarkInvalid() }
