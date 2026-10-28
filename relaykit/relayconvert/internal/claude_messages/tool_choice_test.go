package claudemessages

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Messages -> Chat converter used to drop tool_choice entirely, which
// demoted every forcing request into an ordinary auto one: the official
// endpoint forces the named call, while the converted request let the model
// answer freely.
func TestClaudeMessagesRequestToOpenAIChatMapsToolChoice(t *testing.T) {
	body := func(toolChoice string) []byte {
		raw := `{
  "model": "kimi-k3.1-pro",
  "max_tokens": 128,
  "tools": [
    {
      "name": "get_weather",
      "description": "查询指定城市的当前天气",
      "input_schema": {
        "type": "object",
        "properties": { "location": { "type": "string" } },
        "required": ["location"]
      }
    }
  ],
  "messages": [ { "role": "user", "content": "北京天气怎么样" } ]` + toolChoice + `
}`
		return []byte(raw)
	}

	tests := []struct {
		name           string
		toolChoice     string
		wantToolChoice string // JSON encoding of the converted field; empty means nil
		wantParallel   *bool
	}{
		{
			name:           "named tool becomes a forced function",
			toolChoice:     `, "tool_choice": { "type": "tool", "name": "get_weather" }`,
			wantToolChoice: `{"function":{"name":"get_weather"},"type":"function"}`,
		},
		{
			name:           "any becomes required",
			toolChoice:     `, "tool_choice": { "type": "any" }`,
			wantToolChoice: `"required"`,
		},
		{
			name:           "auto stays auto",
			toolChoice:     `, "tool_choice": { "type": "auto" }`,
			wantToolChoice: `"auto"`,
		},
		{
			name:           "none stays none",
			toolChoice:     `, "tool_choice": { "type": "none" }`,
			wantToolChoice: `"none"`,
		},
		{
			name:           "disable_parallel_tool_use travels as parallel_tool_calls=false",
			toolChoice:     `, "tool_choice": { "type": "tool", "name": "get_weather", "disable_parallel_tool_use": true }`,
			wantToolChoice: `{"function":{"name":"get_weather"},"type":"function"}`,
			wantParallel:   boolPtr(false),
		},
		{
			name:       "absent tool_choice stays absent",
			toolChoice: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var request dto.ClaudeRequest
			require.NoError(t, json.Unmarshal(body(tc.toolChoice), &request))
			converted, err := ClaudeMessagesRequestToOpenAIChat(context.Background(), request, &convmeta.Values{})
			require.NoError(t, err)
			if tc.wantToolChoice == "" {
				assert.Nil(t, converted.ToolChoice)
			} else {
				encoded, err := json.Marshal(converted.ToolChoice)
				require.NoError(t, err)
				assert.JSONEq(t, tc.wantToolChoice, string(encoded))
			}
			if tc.wantParallel == nil {
				assert.Nil(t, converted.ParallelTooCalls)
			} else {
				require.NotNil(t, converted.ParallelTooCalls)
				assert.Equal(t, *tc.wantParallel, *converted.ParallelTooCalls)
			}
		})
	}
}

func boolPtr(v bool) *bool {
	return &v
}
