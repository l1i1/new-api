package oaichat

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// ChatBufferedAccumulator merges chat.completion.chunk deltas into a single
// non-streaming chat.completion response.
//
// Why this exists: a channel may be configured (internal_stream_for_nonstream)
// to call the upstream with stream=true even when the client asked for one JSON
// body. Streaming makes liveness observable - the upstream's headers and first
// chunks arrive immediately, so a channel that is slow to *start* can be failed
// over before anything is written to the client, and a long generation stops
// being bounded by RELAY_RESPONSE_HEADER_TIMEOUT ("once the headers arrive,
// streaming is unaffected"). Nothing reaches the client until the caller
// marshals BuildResponse, so the response stays uncommitted and the ordinary
// retry loop keeps working.
//
// Field fidelity notes that this type exists to protect:
//   - Moonshot K3 attaches terminal usage to choices[0].usage rather than to the
//     top-level object (see ChatCompletionsStreamResponseChoice.Usage). Ignoring
//     the choice-level copy loses billing data, so both are read.
//   - Reasoning text arrives as either `reasoning_content` or `reasoning`;
//     GetReasoningContent covers both, and the buffered message always writes the
//     canonical `reasoning_content` field.
//   - tool_calls stream by index: id/type/name appear once and `arguments`
//     arrives in fragments that must be concatenated in order.
type ChatBufferedAccumulator struct {
	id          string
	model       string
	object      string
	created     int64
	fingerprint *string
	usage       *dto.Usage
	choices     map[int]*bufferedChatChoice
	order       []int
	chunks      int
}

type bufferedChatChoice struct {
	role         string
	content      strings.Builder
	reasoning    strings.Builder
	toolCalls    map[int]*bufferedToolCall
	toolOrder    []int
	finishReason string
	logprobs     *any
	annotations  json.RawMessage
}

type bufferedToolCall struct {
	id        string
	callType  any
	name      string
	arguments strings.Builder
}

// NewChatBufferedAccumulator returns an empty accumulator.
func NewChatBufferedAccumulator() *ChatBufferedAccumulator {
	return &ChatBufferedAccumulator{choices: map[int]*bufferedChatChoice{}}
}

func (a *ChatBufferedAccumulator) choice(index int) *bufferedChatChoice {
	if c, ok := a.choices[index]; ok {
		return c
	}
	c := &bufferedChatChoice{toolCalls: map[int]*bufferedToolCall{}}
	a.choices[index] = c
	a.order = append(a.order, index)
	return c
}

// ProcessChunk folds one upstream chunk into the buffer. Chunks with no choices
// (the usage-only terminal chunk some providers send) still contribute their
// usage.
func (a *ChatBufferedAccumulator) ProcessChunk(chunk *dto.ChatCompletionsStreamResponse) {
	if chunk == nil {
		return
	}
	a.chunks++
	if chunk.Id != "" {
		a.id = chunk.Id
	}
	if chunk.Model != "" {
		a.model = chunk.Model
	}
	if chunk.Object != "" {
		a.object = chunk.Object
	}
	if chunk.Created != 0 {
		a.created = chunk.Created
	}
	if chunk.SystemFingerprint != nil {
		a.fingerprint = chunk.SystemFingerprint
	}
	if chunk.Usage != nil && chunk.Usage.TotalTokens > 0 {
		a.usage = chunk.Usage
	}

	for _, sc := range chunk.Choices {
		c := a.choice(sc.Index)
		if sc.Delta.Role != "" {
			c.role = sc.Delta.Role
		}
		if text := sc.Delta.GetContentString(); text != "" {
			c.content.WriteString(text)
		}
		if text := sc.Delta.GetReasoningContent(); text != "" {
			c.reasoning.WriteString(text)
		}
		for _, tc := range sc.Delta.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			acc, ok := c.toolCalls[idx]
			if !ok {
				acc = &bufferedToolCall{}
				c.toolCalls[idx] = acc
				c.toolOrder = append(c.toolOrder, idx)
			}
			if tc.ID != "" {
				acc.id = tc.ID
			}
			if tc.Type != nil {
				acc.callType = tc.Type
			}
			if tc.Function.Name != "" {
				acc.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				acc.arguments.WriteString(tc.Function.Arguments)
			}
		}
		if sc.Delta.Annotations != nil {
			c.annotations = mergeRawJSONArrays(c.annotations, sc.Delta.Annotations)
		}
		if sc.Logprobs != nil {
			c.logprobs = sc.Logprobs
		}
		if sc.FinishReason != nil && *sc.FinishReason != "" {
			c.finishReason = *sc.FinishReason
		}
		// K3-style choice-level usage: the terminal chunk carries usage here
		// instead of on the chunk itself.
		if len(sc.Usage) > 0 && (a.usage == nil || a.usage.TotalTokens == 0) {
			var u dto.Usage
			if err := json.Unmarshal(sc.Usage, &u); err == nil && u.TotalTokens > 0 {
				a.usage = &u
			}
		}
	}
}

// Usage returns the best usage observed so far (nil when the upstream never
// reported any; the caller then falls back to the platform's own accounting).
func (a *ChatBufferedAccumulator) Usage() *dto.Usage {
	return a.usage
}

// ChunkCount reports how many chunks were folded in; the caller uses it to tell
// "upstream produced nothing" from "upstream produced a real answer".
func (a *ChatBufferedAccumulator) ChunkCount() int {
	return a.chunks
}

// HasContent reports whether any choice produced text, reasoning or tool calls.
// A stream that only carried a role or a finish_reason is still an empty answer.
func (a *ChatBufferedAccumulator) HasContent() bool {
	for _, c := range a.choices {
		if c.content.Len() > 0 || c.reasoning.Len() > 0 || len(c.toolCalls) > 0 {
			return true
		}
	}
	return false
}

// FinishReason returns the terminal finish_reason seen, if any.
func (a *ChatBufferedAccumulator) FinishReason() string {
	for _, c := range a.choices {
		if c.finishReason != "" {
			return c.finishReason
		}
	}
	return ""
}

// Text returns the concatenated content across choices, used for token
// estimation when the upstream reported no usage.
func (a *ChatBufferedAccumulator) Text() string {
	var sb strings.Builder
	indices := append([]int(nil), a.order...)
	sort.Ints(indices)
	for _, idx := range indices {
		sb.WriteString(a.choices[idx].content.String())
	}
	return sb.String()
}

// BuildResponse assembles the non-streaming response. id/model/created are
// fallbacks used when the upstream stream did not carry them (an upstream may
// send the identity only on the first chunk, and a stream that died early has
// none).
func (a *ChatBufferedAccumulator) BuildResponse(id, model string, created int64) *dto.OpenAITextResponse {
	out := &dto.OpenAITextResponse{
		Id:     firstNonEmpty(a.id, id),
		Model:  firstNonEmpty(a.model, model),
		Object: "chat.completion",
	}
	// Created is `any`, so compare the typed field rather than out.Created:
	// `any(int64(0)) == 0` is false (the constant defaults to int).
	if a.created != 0 {
		out.Created = a.created
	} else {
		out.Created = created
	}
	if a.usage != nil {
		out.Usage = *a.usage
	}

	indices := append([]int(nil), a.order...)
	sort.Ints(indices)
	for _, idx := range indices {
		c := a.choices[idx]
		msg := dto.Message{Role: firstNonEmpty(c.role, "assistant")}
		if c.content.Len() > 0 {
			msg.Content = c.content.String()
		} else {
			// OpenAI returns an explicit null content when only tool calls were
			// produced; an empty string would be a different (and wrong) shape.
			msg.Content = nil
		}
		if c.reasoning.Len() > 0 {
			reasoning := c.reasoning.String()
			msg.ReasoningContent = &reasoning
		}
		if len(c.toolCalls) > 0 {
			msg.ToolCalls = buildToolCallsJSON(c)
		}
		if c.annotations != nil {
			msg.Annotations = c.annotations
		}
		out.Choices = append(out.Choices, dto.OpenAITextResponseChoice{
			Index:        idx,
			Message:      msg,
			Logprobs:     c.logprobs,
			FinishReason: firstNonEmpty(c.finishReason, "stop"),
		})
	}
	return out
}

func buildToolCallsJSON(c *bufferedChatChoice) json.RawMessage {
	indices := append([]int(nil), c.toolOrder...)
	sort.Ints(indices)
	calls := make([]dto.ToolCallResponse, 0, len(indices))
	for _, idx := range indices {
		tc := c.toolCalls[idx]
		callType := tc.callType
		if callType == nil {
			callType = "function"
		}
		calls = append(calls, dto.ToolCallResponse{
			ID:       tc.id,
			Type:     callType,
			Function: dto.FunctionResponse{Name: tc.name, Arguments: tc.arguments.String()},
		})
	}
	encoded, err := json.Marshal(calls)
	if err != nil {
		return nil
	}
	return encoded
}

// mergeRawJSONArrays concatenates two JSON arrays. Anything that is not an array
// on either side falls back to the newer value: the alternative - dropping one -
// would silently lose citations on a provider that changes shape mid-stream.
func mergeRawJSONArrays(existing, incoming json.RawMessage) json.RawMessage {
	if len(existing) == 0 {
		return incoming
	}
	var left, right []json.RawMessage
	if err := json.Unmarshal(existing, &left); err != nil {
		return incoming
	}
	if err := json.Unmarshal(incoming, &right); err != nil {
		return existing
	}
	merged, err := json.Marshal(append(left, right...))
	if err != nil {
		return incoming
	}
	return merged
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
