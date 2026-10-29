package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// A provider can end a tool-call stream without the terminal finish_reason frame
// even though the model already finished writing every call. On 2026-10-29 the
// neurvibe :8111 upstream did this for 88-98% of the kimi-k3.1-pro tool-call
// streams, and 13 of the 14 sampled cuts carried complete argument JSON: only
// the last frame was missing.
//
// The relay cannot resume half a call, but a call whose arguments already parse
// is an answer the caller can execute. streamToolCallTracker accumulates the
// calls of one upstream attempt from the chunks the handler already parses so
// the close path can tell "the provider only lost the terminal frame" from "the
// provider was cut inside an argument" — the first is closed as a tool_calls
// answer, the second keeps the error frame.
type streamedToolCall struct {
	id   string
	name string
	args strings.Builder
}

func (c *streamedToolCall) addArguments(fragment string) {
	if fragment == "" {
		return
	}
	current := c.args.String()
	if current == "" {
		c.args.WriteString(fragment)
		return
	}
	// Some compatible providers resend the arguments as a cumulative snapshot
	// instead of sending a delta; appending those would duplicate the JSON.
	if strings.HasPrefix(fragment, current) {
		c.args.Reset()
		c.args.WriteString(fragment)
		return
	}
	c.args.WriteString(fragment)
}

type streamToolCallTracker struct {
	calls map[string]*streamedToolCall
	order []string
}

func (t *streamToolCallTracker) observe(choices []dto.ChatCompletionsStreamResponseChoice) {
	for _, choice := range choices {
		for position, call := range choice.Delta.ToolCalls {
			if !keepStreamedToolCallFragment(call) {
				continue
			}
			key := streamedToolCallKey(choice.Index, position, call)
			state := t.calls[key]
			if state == nil {
				if t.calls == nil {
					t.calls = make(map[string]*streamedToolCall)
				}
				state = &streamedToolCall{}
				t.calls[key] = state
				t.order = append(t.order, key)
			}
			if state.id == "" {
				state.id = strings.TrimSpace(call.ID)
			}
			if state.name == "" {
				state.name = strings.TrimSpace(call.Function.Name)
			}
			state.addArguments(call.Function.Arguments)
		}
	}
}

// completeCalls reports how many calls the stream delivered in full, and false
// when any observed call is still missing a name or an argument document. That
// is the signature of a stream cut inside a call, which must stay an error.
func (t *streamToolCallTracker) completeCalls() (int, bool) {
	if t == nil || len(t.order) == 0 {
		return 0, false
	}
	complete := 0
	for _, key := range t.order {
		state := t.calls[key]
		if state == nil || strings.TrimSpace(state.name) == "" {
			return 0, false
		}
		args := strings.TrimSpace(state.args.String())
		if args == "" || !json.Valid([]byte(args)) {
			return 0, false
		}
		complete++
	}
	return complete, true
}

// keepStreamedToolCallFragment accepts every chunk that belongs to a function
// call, including argument-only fragments: unlike isValidStreamFunctionToolCall
// it must not require a name, because OpenAI streams send the name once and the
// arguments over the following chunks.
func keepStreamedToolCallFragment(call dto.ToolCallResponse) bool {
	switch typ := call.Type.(type) {
	case nil:
	case string:
		if trimmed := strings.TrimSpace(typ); trimmed != "" && !strings.EqualFold(trimmed, "function") {
			return false
		}
	default:
		return false
	}
	if call.Index != nil {
		return true
	}
	return strings.TrimSpace(call.ID) != "" || strings.TrimSpace(call.Function.Name) != ""
}

func streamedToolCallKey(choiceIndex, position int, call dto.ToolCallResponse) string {
	if call.Index != nil {
		return fmt.Sprintf("choice-%d-index-%d", choiceIndex, *call.Index)
	}
	if id := strings.TrimSpace(call.ID); id != "" {
		return fmt.Sprintf("choice-%d-id-%s", choiceIndex, id)
	}
	return fmt.Sprintf("choice-%d-position-%d", choiceIndex, position)
}
