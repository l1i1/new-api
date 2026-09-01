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
	return `{"model":"deepseek-v4-pro","messages":` + messagesJSON + `,"max_tokens":32}`
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
			name: "tool conversation ending on a user turn is exempt",
			body: toolChainBody(`[{"role":"user","content":"北京天气？"},` + assistantToolCallsJSON("call_real") + `,{"role":"tool","content":"晴","tool_call_id":"call_real"},{"role":"assistant","content":"晴 25 度。"},{"role":"user","content":"谢谢"}]`),
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
	passbackErr := "The `reasoning_content` in the thinking mode must be passed back to the API."

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
			name:    "tool conversation continued from an assistant turn without reasoning is the DS-116 passback shape",
			body:    toolChainBody(`[{"role":"user","content":"北京天气？"},` + assistantToolCallsJSON("call_real") + `,{"role":"tool","content":"晴","tool_call_id":"call_real"},{"role":"assistant","content":"晴 25 度"}]`),
			wantErr: &passbackErr,
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
