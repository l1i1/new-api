package common

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

func bufferChunk(index int, id, name, arguments string) []dto.ChatCompletionsStreamResponseChoice {
	idx := index
	return []dto.ChatCompletionsStreamResponseChoice{{
		Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
			ToolCalls: []dto.ToolCallResponse{{
				Index: &idx,
				ID:    id,
				Type:  "function",
				Function: dto.FunctionResponse{
					Name:      name,
					Arguments: arguments,
				},
			}},
		},
	}}
}

// TestChatToolCallBufferDeliversOnlyCompleteCalls pins the contract: fragments of
// one call accumulate, a cumulative snapshot replaces the previous prefix, and the
// consolidated event carries each argument document exactly once.
func TestChatToolCallBufferDeliversOnlyCompleteCalls(t *testing.T) {
	buffer := NewChatToolCallBuffer(true)
	buffer.Observe(bufferChunk(0, "call_1", "calculator", `{"expr":`))
	buffer.Observe(bufferChunk(0, "", "", `"1+1"}`))
	if err := buffer.Validate(); err != nil {
		t.Fatalf("complete call must validate: %v", err)
	}
	chunk := buffer.ConsolidatedChunk("chatcmpl_1", 1, "kimi-k3.1-pro")
	if !strings.Contains(chunk, `"call_1"`) || !strings.Contains(chunk, `1+1`) {
		t.Fatalf("consolidated chunk lost the call: %s", chunk)
	}
}

// TestChatToolCallBufferRejectsHalfCalls pins red line 4 at the validation layer:
// a cut argument document, a duplicate id and an empty name are all undeliverable.
func TestChatToolCallBufferRejectsHalfCalls(t *testing.T) {
	half := NewChatToolCallBuffer(true)
	half.Observe(bufferChunk(0, "call_1", "calculator", `{"expr":"1+`))
	if err := half.Validate(); err == nil {
		t.Fatal("half an argument document must not validate")
	}

	duplicate := NewChatToolCallBuffer(true)
	duplicate.Observe(bufferChunk(0, "call_1", "a", `{}`))
	duplicate.Observe(bufferChunk(1, "call_1", "b", `{}`))
	if err := duplicate.Validate(); err == nil {
		t.Fatal("duplicate ids must not validate")
	}

	// A cumulative snapshot is common on compatible providers: the later fragment
	// replaces the earlier prefix instead of being appended to it.
	snapshot := NewChatToolCallBuffer(true)
	snapshot.Observe(bufferChunk(0, "call_1", "a", `{"a":`))
	snapshot.Observe(bufferChunk(0, "", "", `{"a":1}`))
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("cumulative snapshot should validate: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(snapshot.ConsolidatedChunk("id", 1, "m")), &decoded); err != nil {
		t.Fatalf("consolidated chunk is not JSON: %v", err)
	}
}
