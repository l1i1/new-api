package helper

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/officialfit"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The rejection matrix is calibrated against the live api.moonshot.cn API
// (re-probed 2026-09-21): the fixed temperature follows the thinking state,
// logprobs=true is accepted, and the tool-call chain, dynamic tools,
// response_format and tool_choice each carry their own texts.
func TestKimiK3OfficialFieldsReject(t *testing.T) {
	thinkingDisabled := json.RawMessage(`{"type":"disabled"}`)
	message := func(role, content string) dto.Message {
		return dto.Message{Role: role, Content: content}
	}
	toolMessage := func(id string) dto.Message {
		return dto.Message{Role: "tool", ToolCallId: id, Content: "{}"}
	}
	assistantCalls := func(ids ...string) dto.Message {
		calls := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			calls = append(calls, map[string]any{
				"id": id, "type": "function",
				"function": map[string]any{"name": "get_weather", "arguments": "{}"},
			})
		}
		raw, err := json.Marshal(calls)
		require.NoError(t, err)
		return dto.Message{Role: "assistant", Content: "", ToolCalls: raw}
	}
	dynamicTools := func(role string, content string, tools any) dto.Message {
		raw, err := json.Marshal(tools)
		require.NoError(t, err)
		return dto.Message{Role: role, Content: content, Tools: raw}
	}
	functionTool := func(name string) map[string]any {
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": name, "description": "d",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
			},
		}
	}

	rejected := []struct {
		name    string
		request *dto.GeneralOpenAIRequest
		message string
	}{
		{
			"temperature 0 with thinking enabled (default)",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Temperature: floatPtr(0)},
			kimiK3TemperatureThinkingMessage,
		},
		{
			"temperature 0.6 while thinking enabled",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Temperature: floatPtr(0.6)},
			kimiK3TemperatureThinkingMessage,
		},
		{
			"temperature 1.0 while thinking disabled",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", THINKING: thinkingDisabled, Temperature: floatPtr(1.0)},
			kimiK3TemperatureDisabledMessage,
		},
		{
			"temperature 0 while thinking disabled",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", THINKING: thinkingDisabled, Temperature: floatPtr(0)},
			kimiK3TemperatureDisabledMessage,
		},
		{
			"top_p non-fixed",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", TopP: floatPtr(1.5)},
			kimiK3TopPMessage,
		},
		{
			"n above one",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", N: intPtr(2)},
			kimiK3NMessage,
		},
		{
			"presence_penalty non-fixed",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", PresencePenalty: floatPtr(1.5)},
			kimiK3PresencePenaltyMessage,
		},
		{
			"frequency_penalty non-fixed",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", FrequencyPenalty: floatPtr(1.5)},
			kimiK3FrequencyPenaltyMessage,
		},
		{
			"top_logprobs without logprobs rejects the pair",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", TopLogProbs: intPtr(5)},
			kimiK3TopLogprobsPairMessage,
		},
		{
			"top_logprobs with logprobs false rejects the pair",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", LogProbs: boolPtr(false), TopLogProbs: intPtr(5)},
			kimiK3TopLogprobsPairMessage,
		},
		{
			"tool_choice outside the three strings while thinking is enabled",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", ToolChoice: "bogus"},
			kimiK3ToolChoiceSpecifiedMessage,
		},
		{
			"tool_choice specified function object while thinking is enabled",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3",
				ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}},
			kimiK3ToolChoiceSpecifiedMessage,
		},
		{
			// Q1: the object form is rejected by the thinking state alone, so a
			// request that declares no tools is rejected the same way.
			"tool_choice specified function object without tools while thinking is enabled",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", THINKING: json.RawMessage(`{"type":"enabled"}`),
				ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}},
			kimiK3ToolChoiceSpecifiedMessage,
		},
		{
			// S2/G4: with thinking off the same unknown string answers the
			// unknown-strategy text instead, naming the value.
			"tool_choice outside the three strings while thinking is off",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", THINKING: json.RawMessage(`{"type":"disabled"}`),
				ToolChoice: "bogus"},
			kimiK3UnknownToolChoicePrefix + "bogus" + kimiK3UnknownToolChoiceSuffix,
		},
		{
			// A/C: `reasoning_effort: "none"` is the second control that turns
			// thinking off, so it pins the same 0.6 temperature.
			"temperature above the thinking-off pin with effort none",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", ReasoningEffort: "none", Temperature: floatPtr(1.0)},
			kimiK3TemperatureDisabledMessage,
		},
		{
			// K1: an explicit thinking type outranks the effort field.
			"temperature at the thinking-off pin while thinking is explicitly enabled",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", THINKING: json.RawMessage(`{"type":"enabled"}`),
				ReasoningEffort: "none", Temperature: floatPtr(0.6)},
			kimiK3TemperatureThinkingMessage,
		},
		{
			"tool_choice required without tools",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", ToolChoice: "required"},
			kimiK3ToolChoiceRequiredMessage,
		},
		{
			// Q2: the required-without-tools check runs before the
			// thinking-state one, so it answers the same text with thinking off.
			"tool_choice required without tools while thinking is off",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", THINKING: json.RawMessage(`{"type":"disabled"}`),
				ToolChoice: "required"},
			kimiK3ToolChoiceRequiredMessage,
		},
		{
			"illegal tool name with space",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3",
				Tools: []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "get weather"}}}},
			kimiK3ToolNameMessage,
		},
		{
			"tool name starting with a digit",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3",
				Tools: []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "1weather"}}}},
			kimiK3ToolNameMessage,
		},
		{
			"tool name with a dot",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3",
				Tools: []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "get.weather"}}}},
			kimiK3ToolNameMessage,
		},
		{
			"empty messages",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3"},
			kimiK3MessagesEmptyMessage,
		},
		{
			"tool call answered by a non-tool message",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Messages: []dto.Message{
				message("user", "天气？"), assistantCalls("c1"), message("assistant", "好的"),
			}},
			kimiK3UnansweredToolCallsPrefix + "get_weather:0",
		},
		{
			"tool call left unanswered at the end",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Messages: []dto.Message{
				message("user", "天气？"), assistantCalls("c1"),
			}},
			kimiK3UnansweredToolCallsPrefix + "get_weather:0",
		},
		{
			"two unanswered calls list both in declaration order",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Messages: []dto.Message{
				message("user", "天气？"), assistantCalls("c1", "c2"),
			}},
			kimiK3UnansweredToolCallsPrefix + "get_weather:0, get_weather:1",
		},
		{
			"unknown tool_call_id",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Messages: []dto.Message{
				message("user", "天气？"), assistantCalls("c1"), toolMessage("nope"),
			}},
			kimiK3ToolCallIDNotFoundMessage,
		},
		{
			"tool message without any declaration",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Messages: []dto.Message{
				message("user", "天气？"), toolMessage("c1"),
			}},
			kimiK3ToolCallIDNotFoundMessage,
		},
		{
			"duplicate tool_call_id in one assistant",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Messages: []dto.Message{
				message("user", "天气？"), assistantCalls("dup", "dup"),
			}},
			kimiK3DuplicateToolCallIDPrefix + "dup" + kimiK3DuplicateToolCallIDSuffix,
		},
		{
			"dynamic tools on a user message",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Messages: []dto.Message{
				dynamicTools("user", "hi", []any{functionTool("x")}),
			}},
			kimiK3MessageToolsPositionPrefix + "0" + kimiK3MessageToolsRoleSuffix,
		},
		{
			"dynamic tools with non-empty system content",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Messages: []dto.Message{
				dynamicTools("system", "not empty", []any{functionTool("x")}),
			}},
			kimiK3MessageToolsPositionPrefix + "0" + kimiK3MessageToolsContentSuffix,
		},
		{
			"invalid dynamic tool name",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Messages: []dto.Message{
				dynamicTools("system", "", []any{functionTool("1bad")}),
			}},
			kimiK3MessageToolsPositionPrefix + "0" + kimiK3MessageToolsInvalidSuffix + kimiK3FunctionNameInvalidText,
		},
		{
			"unsupported dynamic tool type",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Messages: []dto.Message{
				dynamicTools("system", "", []any{map[string]any{"type": "bogus", "function": map[string]any{"name": "x"}}}),
			}},
			kimiK3MessageToolsPositionPrefix + "0" + kimiK3MessageToolsInvalidSuffix +
				kimiK3UnknownToolTypePrefix + "bogus" + kimiK3UnknownToolTypeSuffix,
		},
		{
			"duplicate names inside one dynamic declaration",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Messages: []dto.Message{
				dynamicTools("system", "", []any{functionTool("dup"), functionTool("dup")}),
			}},
			kimiK3MessageToolsPositionPrefix + "0" + kimiK3MessageToolsInvalidSuffix + "function name dup is duplicated",
		},
		{
			"duplicate names across dynamic declarations",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3", Messages: []dto.Message{
				dynamicTools("system", "", []any{functionTool("dup")}),
				dynamicTools("system", "", []any{functionTool("dup")}),
			}},
			kimiK3DuplicateToolNamePrefix + "dup" + kimiK3DuplicateBetweenMessages + "0 and 1",
		},
		{
			"duplicate name between request-level and dynamic tools",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3",
				Tools: []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "dup"}}},
				Messages: []dto.Message{
					dynamicTools("system", "", []any{functionTool("dup")}),
				}},
			kimiK3DuplicateToolNamePrefix + "dup" + kimiK3DuplicateWithGlobalTools + "0",
		},
		{
			"response_format type outside the enum",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3",
				ResponseFormat: &dto.ResponseFormat{Type: "bogus"}},
			kimiK3ResponseFormatTypeMessage,
		},
		{
			"json_schema without a name",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3",
				ResponseFormat: &dto.ResponseFormat{Type: "json_schema", JsonSchema: json.RawMessage(`{"schema":{"type":"object"}}`)}},
			kimiK3ResponseFormatMissingSchemaName,
		},
		{
			"json_schema with an empty schema",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3",
				ResponseFormat: &dto.ResponseFormat{Type: "json_schema", JsonSchema: json.RawMessage(`{"name":"w"}`)}},
			kimiK3ResponseFormatEmptySchema,
		},
		{
			"json_schema field missing entirely",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3",
				ResponseFormat: &dto.ResponseFormat{Type: "json_schema"}},
			kimiK3ResponseFormatMissingSchema,
		},
		{
			"json_schema strict non-boolean",
			&dto.GeneralOpenAIRequest{Model: "kimi-k3",
				ResponseFormat: &dto.ResponseFormat{Type: "json_schema",
					JsonSchema: json.RawMessage(`{"name":"w","strict":"yes","schema":{"type":"object"}}`)}},
			kimiK3ResponseFormatStrictTypePrefix + `"yes" is not acceptable`,
		},
	}
	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			err := validateKimiK3OfficialFields(tt.request)
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			assert.Equal(t, tt.message, apiErr.ToOpenAIError().Message)
		})
	}
}

// Everything the live endpoint tolerates must pass through unvalidated.
func TestKimiK3OfficialFieldsAccept(t *testing.T) {
	base := dto.GeneralOpenAIRequest{Model: "kimi-k3", Messages: []dto.Message{{Role: "user", Content: "1+1=?"}}}
	dynamicTool := func(name string) dto.Message {
		raw, err := json.Marshal([]any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": name, "description": "d",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
			},
		}})
		require.NoError(t, err)
		return dto.Message{Role: "system", Content: "", Tools: raw}
	}
	accepted := []*dto.GeneralOpenAIRequest{
		&base,
		{Model: "kimi-k3", Messages: base.Messages, THINKING: json.RawMessage(`{"type":"disabled"}`), Temperature: floatPtr(0.6)},
		{Model: "kimi-k3", Messages: base.Messages, ReasoningEffort: "ultra"},
		{Model: "kimi-k3", Messages: base.Messages, ReasoningEffort: "low"},
		{Model: "kimi-k3", Messages: base.Messages, ReasoningEffort: "max"},
		{Model: "kimi-k3", Messages: base.Messages, Temperature: floatPtr(1.0)},
		{Model: "kimi-k3", Messages: base.Messages, THINKING: json.RawMessage(`{"type":"enabled","effort":"high"}`), Temperature: floatPtr(1.0)},
		{Model: "kimi-k3", Messages: base.Messages, TopP: floatPtr(0.95)},
		{Model: "kimi-k3", Messages: base.Messages, N: intPtr(1)},
		{Model: "kimi-k3", Messages: base.Messages, PresencePenalty: floatPtr(0)},
		{Model: "kimi-k3", Messages: base.Messages, FrequencyPenalty: floatPtr(0)},
		{Model: "kimi-k3", Messages: base.Messages, LogProbs: boolPtr(false)},
		// logprobs=true is accepted and returns data; only a positive
		// top_logprobs without it is rejected.
		{Model: "kimi-k3", Messages: base.Messages, LogProbs: boolPtr(true)},
		{Model: "kimi-k3", Messages: base.Messages, LogProbs: boolPtr(true), TopLogProbs: intPtr(5)},
		{Model: "kimi-k3", Messages: base.Messages, TopLogProbs: intPtr(0)},
		{Model: "kimi-k3", Messages: base.Messages, ToolChoice: "auto"},
		{Model: "kimi-k3", Messages: base.Messages, ToolChoice: "none"},
		{Model: "kimi-k3", Messages: base.Messages, ToolChoice: ""},
		{Model: "kimi-k3", Messages: base.Messages, ToolChoice: "required",
			Tools: []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "get_weather"}}}},
		{Model: "kimi-k3", Messages: []dto.Message{dynamicTool("get_weather"), {Role: "user", Content: "天气？"}}, ToolChoice: "required"},
		// R2: `required` with a tool is accepted while thinking is on too
		// (unlike DeepSeek V4, which rejects it there).
		{Model: "kimi-k3", Messages: base.Messages, ToolChoice: "required",
			Tools:       []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "get_weather"}}},
			Temperature: floatPtr(1.0)},
		// T1/T2/O1: the named-function object is legal once thinking is off,
		// which is the shape the Channel Mate probe sends; the two controls
		// that turn it off are each covered, plus the effort-axis fallbacks
		// (M3/M4) that leave the state to the effort field.
		{Model: "kimi-k3", Messages: base.Messages, THINKING: json.RawMessage(`{"type":"disabled"}`),
			ToolChoice:  map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}},
			Tools:       []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "get_weather"}}},
			Temperature: floatPtr(0.6)},
		{Model: "kimi-k3", Messages: base.Messages, ReasoningEffort: "none",
			ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}},
			Tools:      []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "get_weather"}}}},
		{Model: "kimi-k3", Messages: base.Messages, THINKING: json.RawMessage(`null`), ReasoningEffort: "NONE",
			ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}},
		{Model: "kimi-k3", Messages: base.Messages, THINKING: json.RawMessage(`{}`), ReasoningEffort: "none",
			ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}},
		// K2: thinking "disabled" outranks a high effort, so the 0.6 pin holds.
		{Model: "kimi-k3", Messages: base.Messages, THINKING: json.RawMessage(`{"type":"disabled"}`),
			ReasoningEffort: "high", Temperature: floatPtr(0.6)},
		// strict=false and a missing parameters field are both tolerated.
		{Model: "kimi-k3", Messages: []dto.Message{dynamicTool("x")}},
		// The chain may be answered across several tool messages in any order.
		{Model: "kimi-k3", Messages: []dto.Message{
			{Role: "user", Content: "天气？"},
			{Role: "assistant", Content: "", ToolCalls: json.RawMessage(`[{"id":"c1","type":"function","function":{"name":"get_weather","arguments":"{}"}},{"id":"c2","type":"function","function":{"name":"get_time","arguments":"{}"}}]`)},
			{Role: "tool", ToolCallId: "c2", Content: "{}"},
			{Role: "tool", ToolCallId: "c1", Content: "{}"},
		}},
		// Duplicate ids across two different assistant messages are allowed.
		{Model: "kimi-k3", Messages: []dto.Message{
			{Role: "user", Content: "天气？"},
			{Role: "assistant", Content: "", ToolCalls: json.RawMessage(`[{"id":"dup","type":"function","function":{"name":"get_weather","arguments":"{}"}}]`)},
			{Role: "tool", ToolCallId: "dup", Content: "{}"},
			{Role: "assistant", Content: "", ToolCalls: json.RawMessage(`[{"id":"dup","type":"function","function":{"name":"get_weather","arguments":"{}"}}]`)},
			{Role: "tool", ToolCallId: "dup", Content: "{}"},
		}},
		{Model: "kimi-k3", Messages: base.Messages, ResponseFormat: &dto.ResponseFormat{Type: "text"}},
		{Model: "kimi-k3", Messages: base.Messages, ResponseFormat: &dto.ResponseFormat{Type: "json_object"}},
		{Model: "kimi-k3", Messages: base.Messages, ResponseFormat: &dto.ResponseFormat{Type: "json_schema",
			JsonSchema: json.RawMessage(`{"name":"w","strict":false,"schema":{"type":"object"}}`)}},
		{Model: "kimi-k3", Messages: base.Messages, ResponseFormat: &dto.ResponseFormat{Type: "json_schema",
			JsonSchema: json.RawMessage(`{"name":"w","schema":{"type":"object","properties":{}}}`)}},
		// FIM-style prefix/suffix requests may omit messages (same exemption
		// as the generic path).
		{Model: "kimi-k3", Prefix: "<fill>", Suffix: "</fill>"},
		// non-kimi models are never inspected
		{Model: "deepseek-v4-flash", ReasoningEffort: "extreme", Temperature: floatPtr(0)},
		{Model: "kimi-k2.6", THINKING: json.RawMessage(`{"type":"disabled"}`)},
	}
	for i, req := range accepted {
		assert.NoError(t, validateKimiK3OfficialFields(req), "case %d", i)
	}
}

func TestKimiK3ValidationMessageRecognized(t *testing.T) {
	for _, msg := range []string{
		kimiK3TemperatureThinkingMessage,
		kimiK3TemperatureDisabledMessage,
		kimiK3TopPMessage,
		kimiK3NMessage,
		kimiK3PresencePenaltyMessage,
		kimiK3FrequencyPenaltyMessage,
		kimiK3TopLogprobsPairMessage,
		kimiK3ToolChoiceSpecifiedMessage,
		kimiK3ToolChoiceRequiredMessage,
		kimiK3MessagesEmptyMessage,
		kimiK3ToolCallIDNotFoundMessage,
		kimiK3UnansweredToolCallsPrefix + "get_weather:0",
		kimiK3DuplicateToolCallIDPrefix + "dup" + kimiK3DuplicateToolCallIDSuffix,
		kimiK3MessageToolsPositionPrefix + "0" + kimiK3MessageToolsRoleSuffix,
		kimiK3MessageToolsTypePrefix + "object is not acceptable",
		kimiK3DuplicateToolNamePrefix + "dup" + kimiK3DuplicateBetweenMessages + "0 and 1",
		kimiK3ResponseFormatTypeMessage,
		kimiK3ResponseFormatMissingSchemaName,
		kimiK3ResponseFormatEmptySchema,
		kimiK3ResponseFormatMissingSchema,
		kimiK3ResponseFormatStrictTypePrefix + `"yes" is not acceptable`,
	} {
		assert.True(t, IsStrictFitValidationMessage(msg), "%q", msg)
	}
	// DeepSeek texts are still recognized by the combined predicate.
	assert.True(t, IsStrictFitValidationMessage("Invalid top_p value, the valid range of top_p is (0, 1.0]"))
	assert.False(t, IsStrictFitValidationMessage("some internal platform error"))
}

// The K3 wire shape must render as application/json (the DeepSeek default of
// application/octet-stream is a per-family property, not a global one).
func TestStrictFitContentTypeByFamily(t *testing.T) {
	assert.Equal(t, "application/json", StrictFitContentType(officialfit.WireShapeMoonshot, kimiK3TopPMessage))
	assert.Equal(t, "application/json", StrictFitContentType(officialfit.WireShapeMoonshot, "Failed to parse the request body as JSON: boom"))
	assert.Equal(t, "application/octet-stream", StrictFitContentType(officialfit.WireShapeOpenAI, "Invalid top_p value, the valid range of top_p is (0, 1.0]"))
	assert.Equal(t, "application/json", StrictFitContentType(officialfit.WireShapeOpenAI, "Failed to deserialize the JSON body into the target type: x"))
}

func boolPtr(value bool) *bool { return &value }

func intPtr(v int) *int { return &v }
