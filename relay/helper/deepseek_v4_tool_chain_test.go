package helper

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolChainBody renders a chat-completions request body with the given
// messages JSON (already-encoded message objects).
func toolChainBody(messagesJSON string) string {
	return toolChainBodyModel("deepseek-v4-pro", messagesJSON)
}

func toolChainBodyModel(model string, messagesJSON string) string {
	return `{"model":"` + model + `","messages":` + messagesJSON + `,"max_tokens":32}`
}

func assistantToolCallsJSON(id string) string {
	return `{"role":"assistant","content":null,"tool_calls":[{"id":"` + id +
		`","type":"function","function":{"name":"get_weather","arguments":"{}"}}]}`
}

func TestDeepSeekV4ToolCallIDsExtraction(t *testing.T) {
	message := dto.Message{ToolCalls: []byte(`[
		{"id":"call_a","type":"function","function":{"name":"f","arguments":"{}"}},
		{"id":"call_b","type":"function","function":{"name":"f","arguments":"{}"}}]`)}
	ids := deepSeekV4ToolCallIDs(message.ToolCalls)
	assert.True(t, ids["call_a"])
	assert.True(t, ids["call_b"])
	assert.False(t, ids["call_c"])

	assert.Empty(t, deepSeekV4ToolCallIDs(nil))
	assert.Empty(t, deepSeekV4ToolCallIDs([]byte(`null`)))
	assert.Empty(t, deepSeekV4ToolCallIDs([]byte(`[{`))) // unparsable: no pending calls
}

func reasoningPtr(v string) *string { return &v }

func TestDeepSeekV4ReasoningPassbackRule(t *testing.T) {
	passback := "The `reasoning_content` in the thinking mode must be passed back to the API."

	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "tool conversation continued from a model turn without reasoning is rejected",
			body:    toolChainBody(`[{"role":"user","content":"北京天气？"},` + assistantToolCallsJSON("call_real") + `,{"role":"tool","content":"晴","tool_call_id":"call_real"}]`),
			wantErr: passback,
		},
		{
			name: "tool conversation with reasoning passed back is accepted",
			body: toolChainBody(`[{"role":"user","content":"北京天气？"},{"role":"assistant","content":null,"reasoning_content":"调用工具。","tool_calls":[{"id":"call_real","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},{"role":"tool","content":"晴","tool_call_id":"call_real"}]`),
		},
		{
			// Official accepts empty and blank reasoning_content: the check is
			// presence-based (probed 2026-09-01, E2/E3).
			name: "empty string reasoning counts as passed back",
			body: toolChainBody(`[{"role":"user","content":"北京天气？"},{"role":"assistant","content":null,"reasoning_content":"","tool_calls":[{"id":"call_real","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},{"role":"tool","content":"晴","tool_call_id":"call_real"}]`),
		},
		{
			name: "blank string reasoning counts as passed back",
			body: toolChainBody(`[{"role":"user","content":"北京天气？"},{"role":"assistant","content":null,"reasoning_content":"   ","tool_calls":[{"id":"call_real","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},{"role":"tool","content":"晴","tool_call_id":"call_real"}]`),
		},
		{
			// Official rejects an explicit null the same as an absent field
			// (probed 2026-09-01, F3).
			name:    "null reasoning is not passed back",
			body:    toolChainBody(`[{"role":"user","content":"北京天气？"},{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"id":"call_real","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},{"role":"tool","content":"晴","tool_call_id":"call_real"}]`),
			wantErr: passback,
		},
		{
			// The vision variant never enforces the passback (probed
			// 2026-09-01: every passback shape returned 200 live) — the
			// incident shape that reached a ZCode user.
			name: "vision variant is exempt from the passback rule",
			body: toolChainBodyModel("deepseek-v4-flash-vision-exp", `[{"role":"user","content":"北京天气？"},`+assistantToolCallsJSON("call_real")+`,{"role":"tool","content":"晴","tool_call_id":"call_real"}]`),
		},
		{
			name: "vision variant stays subject to the tool state machine",
			body: toolChainBodyModel("deepseek-v4-flash-vision-exp", `[{"role":"user","content":"北京天气？"},{"role":"tool","content":"晴","tool_call_id":"call_fake"}]`),
			wantErr: "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'",
		},
		{
			name: "tool conversation ending on a user turn is exempt",
			body: toolChainBody(`[{"role":"user","content":"北京天气？"},` + assistantToolCallsJSON("call_real") + `,{"role":"tool","content":"晴","tool_call_id":"call_real"},{"role":"assistant","content":"晴 25 度。"},{"role":"user","content":"谢谢"}]`),
		},
		{
			// E6 shape: the raw agent loop re-sent with a fresh user turn,
			// without the intermediate answer turn.
			name: "tool loop followed directly by a user turn is exempt",
			body: toolChainBody(`[{"role":"user","content":"北京天气？"},` + assistantToolCallsJSON("call_real") + `,{"role":"tool","content":"晴","tool_call_id":"call_real"},{"role":"user","content":"上海呢？"}]`),
		},
		{
			// E4 shape: only the tool-call issuer's reasoning is checked; a
			// trailing bare assistant without reasoning passes.
			name: "conversation ending on a bare assistant is exempt",
			body: toolChainBody(`[{"role":"user","content":"北京天气？"},{"role":"assistant","content":null,"reasoning_content":"调用工具。","tool_calls":[{"id":"call_real","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},{"role":"tool","content":"晴","tool_call_id":"call_real"},{"role":"assistant","content":"晴 25 度。"}]`),
		},
		{
			// F4 shape: the check targets the nearest assistant before the
			// trailing tool response, not an earlier one.
			name:    "nearest assistant before the trailing tool response is the checked one",
			body:    toolChainBody(`[{"role":"user","content":"北京天气？"},{"role":"assistant","content":"旧答案。"},` + assistantToolCallsJSON("call_real") + `,{"role":"tool","content":"晴","tool_call_id":"call_real"}]`),
			wantErr: passback,
		},
		{
			name: "tool-free conversation with a trailing assistant is exempt",
			body: toolChainBody(`[{"role":"user","content":"你好"},{"role":"assistant","content":"你好！"}]`),
		},
		{
			name: "thinking disabled exempts the passback",
			body: `{"model":"deepseek-v4-pro","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"北京天气？"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_real","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},{"role":"tool","content":"晴","tool_call_id":"call_real"}],"max_tokens":32}`,
		},
		{
			name: "effort none exempts the passback",
			body: `{"model":"deepseek-v4-pro","reasoning_effort":"none","messages":[{"role":"user","content":"北京天气？"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_real","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},{"role":"tool","content":"晴","tool_call_id":"call_real"}],"max_tokens":32}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GetAndValidateTextRequest(newDeepSeekV4FitContext(tt.body), constant.RelayModeChatCompletions)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			assert.Equal(t, tt.wantErr, apiErr.ToOpenAIError().Message)
		})
	}
}

func TestDeepSeekV4ToolCallChainValidation(t *testing.T) {
	orphan := "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'"
	unansweredID := "An assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id', The following tool_call_ids did not have response messages: "
	insufficient := "An assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id'. (insufficient tool messages following tool_calls message)"
	missingToolCallIDErr := "Failed to deserialize the JSON body into the target type: messages[2]: missing field `tool_call_id`"

	tests := []struct {
		name    string
		body    string
		wantErr *string
		wantID  string
	}{
		{
			name:    "orphan tool after user is rejected (the live incident shape)",
			body:    toolChainBody(`[{"role":"user","content":"北京天气？"},{"role":"tool","content":"晴 25 度","tool_call_id":"call_fake"}]`),
			wantErr: &orphan,
		},
		{
			name:    "standalone tool message is rejected",
			body:    toolChainBody(`[{"role":"tool","content":"晴","tool_call_id":"call_fake"}]`),
			wantErr: &orphan,
		},
		{
			name:    "tool after an assistant without tool_calls is rejected",
			body:    toolChainBody(`[{"role":"user","content":"你好"},{"role":"assistant","content":"你好！"},{"role":"tool","content":"晴","tool_call_id":"call_fake"}]`),
			wantErr: &orphan,
		},
		{
			name:    "wrong tool_call_id lists the unknown id",
			body:    toolChainBody(`[{"role":"user","content":"北京天气？"},` + assistantToolCallsJSON("call_real") + `,{"role":"tool","content":"晴","tool_call_id":"call_other"}]`),
			wantErr: &unansweredID,
			wantID:  "call_other",
		},
		{
			// Official accepts this shape (probed 2026-09-01, E4): the
			// passback rule only fires when the conversation ends on a tool
			// response, so a trailing bare assistant needs no reasoning.
			name: "conversation ending on a bare assistant turn is exempt from the passback",
			body: toolChainBody(`[{"role":"user","content":"北京天气？"},` + assistantToolCallsJSON("call_real") + `,{"role":"tool","content":"晴","tool_call_id":"call_real"},{"role":"assistant","content":"晴 25 度"}]`),
		},
		{
			name:    "pending unanswered then user is rejected",
			body:    toolChainBody(`[{"role":"user","content":"北京天气？"},` + assistantToolCallsJSON("call_real") + `,{"role":"user","content":"算了"}]`),
			wantErr: &insufficient,
		},
		{
			name:    "pending unanswered then assistant is rejected",
			body:    toolChainBody(`[{"role":"user","content":"北京天气？"},` + assistantToolCallsJSON("call_real") + `,{"role":"assistant","content":"晴 25 度"}]`),
			wantErr: &insufficient,
		},
		{
			name:    "trailing pending at end of conversation is rejected",
			body:    toolChainBody(`[{"role":"user","content":"北京天气？"},` + assistantToolCallsJSON("call_real") + `]`),
			wantErr: &insufficient,
		},
		{
			name:    "duplicated answer degrades into the orphan rule",
			body:    toolChainBody(`[{"role":"user","content":"北京天气？"},` + assistantToolCallsJSON("call_real") + `,{"role":"tool","content":"晴","tool_call_id":"call_real"},{"role":"tool","content":"雨","tool_call_id":"call_real"}]`),
			wantErr: &orphan,
		},
		{
			name:    "tool message without tool_call_id is a deserialization error",
			body:    toolChainBody(`[{"role":"user","content":"北京天气？"},` + assistantToolCallsJSON("call_real") + `,{"role":"tool","content":"晴"}]`),
			wantErr: &missingToolCallIDErr,
		},
		{
			name: "non-V4 model chain is untouched",
			body: `{"model":"gpt-4o","messages":[{"role":"tool","content":"晴","tool_call_id":"call_fake"}],"max_tokens":32}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GetAndValidateTextRequest(newDeepSeekV4FitContext(tt.body), constant.RelayModeChatCompletions)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			oaiErr := apiErr.ToOpenAIError()
			want := *tt.wantErr
			if tt.wantID != "" {
				want += tt.wantID
			}
			assert.Equal(t, want, oaiErr.Message)
			assert.Equal(t, "invalid_request_error", oaiErr.Type)
			assert.Equal(t, "invalid_request_error", oaiErr.Code)
			assert.Nil(t, oaiErr.Param)
		})
	}

	t.Run("chain validation is gated by the official-fit profile", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			bytes.NewBufferString(toolChainBody(`[{"role":"tool","content":"晴","tool_call_id":"call_fake"}]`)))
		c.Request.Header.Set("Content-Type", "application/json")
		_, err := GetAndValidateTextRequest(c, constant.RelayModeChatCompletions)
		require.NoError(t, err)
	})

	t.Run("valid two-call chain passes", func(t *testing.T) {
		body := toolChainBody(`[{"role":"user","content":"北京和上海天气？"},
			{"role":"assistant","content":null,"tool_calls":[
			{"id":"call_a","type":"function","function":{"name":"get_weather","arguments":"{}"}},
			{"id":"call_b","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},
			{"role":"tool","content":"北京 晴","tool_call_id":"call_a"},
			{"role":"tool","content":"上海 雨","tool_call_id":"call_b"},
			{"role":"assistant","content":"北京晴，上海雨。"},{"role":"user","content":"谢谢"}]`)
		_, err := GetAndValidateTextRequest(newDeepSeekV4FitContext(body), constant.RelayModeChatCompletions)
		require.NoError(t, err)
	})
}

func TestIsStrictFitValidationMessageCoversRound23Texts(t *testing.T) {
	recognized := []string{
		"Invalid max_tokens value, the valid range of max_tokens is [1, 393216]",
		"Stop string array too long: 17",
		"Messages with role 'tool' must be a response to a preceding message with 'tool_calls'",
		"An assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id', The following tool_call_ids did not have response messages: call_other",
		"An assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id'. (insufficient tool messages following tool_calls message)",
		"Failed to deserialize the JSON body into the target type: messages[2]: missing field `tool_call_id`",
		"The `reasoning_content` in the thinking mode must be passed back to the API.",
	}
	for _, msg := range recognized {
		assert.True(t, IsStrictFitValidationMessage(msg), "%q", msg)
	}
	assert.False(t, IsStrictFitValidationMessage("some internal platform error"))
}
