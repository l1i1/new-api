package helper

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The expected columns below are the live official values recorded in
// tools/cdp-bench/data/baselines/official-ds-v4-deepseek-flash-2026-09-15T06-03-02-009.json.
// They are byte offsets into the exact request body the bench sent, so they pin
// the scanner against real upstream behaviour rather than against itself.
func TestSerdeLocationReproducesOfficialColumns(t *testing.T) {
	tests := []struct {
		name string
		body string
		path []any
		want int
	}{
		{
			name: "DS-013 unknown reasoning_effort variant",
			body: `{"model":"deepseek-flash","messages":[{"role":"user","content":"1+1=?"}],"max_completion_tokens":2048,"reasoning_effort":"extreme"}`,
			path: []any{"reasoning_effort"},
			want: 130,
		},
		{
			name: "DS-015 another unknown effort variant",
			body: `{"model":"deepseek-flash","messages":[{"role":"user","content":"1+1=?"}],"max_completion_tokens":2048,"reasoning_effort":"ultra"}`,
			path: []any{"reasoning_effort"},
			want: 128,
		},
		{
			name: "DS-045 negative top_logprobs",
			body: `{"model":"deepseek-flash","messages":[{"role":"user","content":"1+1=?"}],"max_completion_tokens":2048,"logprobs":true,"top_logprobs":-1}`,
			path: []any{"top_logprobs"},
			want: 135,
		},
		{
			name: "DS-129 developer role",
			body: `{"max_tokens":64,"messages":[{"role":"developer","content":"x"},{"role":"user","content":"1+1等于几？简答。"}],"model":"deepseek-flash"}`,
			path: []any{"messages", 0, "role"},
			want: 48,
		},
		{
			name: "DS-132 unknown thinking.type variant",
			body: `{"max_tokens":64,"thinking":{"type":"didn't"},"messages":[{"role":"user","content":"用一个词描述春天。"}],"model":"deepseek-flash"}`,
			path: []any{"thinking", "type"},
			want: 44,
		},
		{
			name: "DS-034 thinking missing type",
			body: `{"max_tokens":64,"thinking":{},"messages":[{"role":"user","content":"用一个词描述春天。"}],"model":"deepseek-flash"}`,
			path: []any{"thinking"},
			want: 30,
		},
		{
			name: "DS-037 scalar string thinking",
			body: `{"max_tokens":64,"thinking":"disabled","messages":[{"role":"user","content":"用一个词描述春天。"}],"model":"deepseek-flash"}`,
			path: []any{"thinking"},
			want: 38,
		},
		{
			name: "DS-038 scalar integer thinking",
			body: `{"max_tokens":64,"thinking":1,"messages":[{"role":"user","content":"用一个词描述春天。"}],"model":"deepseek-flash"}`,
			path: []any{"thinking"},
			want: 29,
		},
		{
			name: "DS-071 invalid tool_choice string",
			body: `{"messages":[{"role":"user","content":"北京天气？"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}],"tool_choice":"sometimes","max_tokens":64,"model":"deepseek-flash"}`,
			path: []any{"tool_choice"},
			want: 178,
		},
		{
			name: "R6-6 buyer body with thinking.type variant",
			body: `{"stream":false,"messages":[{"role":"user","content":"用一个词描述春天。"}],"top_p":0.1,"max_tokens":256,"thinking":{"type":"didn't"},"model":"deepseek-flash"}`,
			path: []any{"thinking", "type"},
			want: 141,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			location := newSerdeLocation([]byte(tt.body))
			require.NotNil(t, location)
			valueEnd, ok := locateJSONValue([]byte(tt.body), tt.path)
			require.True(t, ok, "path %v must resolve", tt.path)
			assert.Equal(t, tt.want, valueEnd)
		})
	}
}

// A missing member is reported against the innermost *named* container in the
// path, so `thinking` points at the end of the thinking object while
// `messages[2]` points at the end of the messages array.
// Live values: DS-034 -> 30 (end of the thinking object), DS-115 -> 263 (end of
// the messages array, not the element's own closing brace at 262).
func TestSerdeLocationMissingFieldUsesTheNamedContainer(t *testing.T) {
	thinkingBody := `{"max_tokens":64,"thinking":{},"messages":[{"role":"user","content":"用一个词描述春天。"}],"model":"deepseek-flash"}`
	thinking := newSerdeLocation([]byte(thinkingBody))
	require.NotNil(t, thinking)
	assert.Equal(t,
		"Failed to deserialize the JSON body into the target type: thinking: missing field `type` at line 1 column 30",
		thinking.withContainerLocation("Failed to deserialize the JSON body into the target type: thinking: missing field `type`", "thinking"))

	body := `{"max_tokens":64,"messages":[{"role":"user","content":"北京天气？"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_real","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}}]},{"role":"tool","content":"晴"}],"model":"deepseek-flash"}`
	location := newSerdeLocation([]byte(body))
	require.NotNil(t, location)
	assert.Equal(t,
		"Failed to deserialize the JSON body into the target type: messages[2]: missing field `tool_call_id` at line 1 column 263",
		location.withContainerLocation("Failed to deserialize the JSON body into the target type: messages[2]: missing field `tool_call_id`", "messages", 2))

	// The element's own end is where the closing brace sits; the reported
	// missing-field offset is the enclosing array instead.
	elementEnd, ok := locateJSONValue([]byte(body), []any{"messages", 2})
	require.True(t, ok)
	arrayEnd, ok := locateJSONValue([]byte(body), []any{"messages"})
	require.True(t, ok)
	assert.Less(t, elementEnd, arrayEnd)
}

// The suffix must depend on the bytes actually received, not on a fixed table:
// the same logical request with different key order or spacing yields the
// column of that body.
func TestSerdeLocationFollowsTheClientBytes(t *testing.T) {
	location := newSerdeLocation([]byte(`{"thinking":{"type":"didn't"}}`))
	require.NotNil(t, location)
	assert.Equal(t,
		"thinking.type at line 1 column 28",
		location.withLocation("thinking.type", "thinking", "type"))

	reordered := newSerdeLocation([]byte(`{"model":"deepseek-flash","thinking":{"type":"didn't"}}`))
	require.NotNil(t, reordered)
	assert.Equal(t,
		"thinking.type at line 1 column 53",
		reordered.withLocation("thinking.type", "thinking", "type"))
}

func TestSerdeLocationDegradesWithoutAResolvablePath(t *testing.T) {
	message := "thinking.type"

	var absent *serdeLocation
	assert.Equal(t, message, absent.withLocation(message, "thinking", "type"))
	assert.Equal(t, message, absent.withContainerLocation(message, "messages", 0))

	location := newSerdeLocation([]byte(`{"thinking":{"type":"didn't"}}`))
	require.NotNil(t, location)
	// Unknown keys and out-of-range indices drop the suffix instead of
	// reporting a wrong column.
	assert.Equal(t, message, location.withLocation(message, "missing"))
	assert.Equal(t, message, location.withLocation(message, "thinking", "missing"))
	assert.Equal(t, message, location.withLocation(message, "thinking", 7, "role"))

	require.Nil(t, newSerdeLocation(nil))
	require.Nil(t, newSerdeLocation([]byte{}))
}

func TestSerdeLocationIgnoresNonJSONBody(t *testing.T) {
	location := newSerdeLocation([]byte("not json at all"))
	require.NotNil(t, location)
	assert.Equal(t, "x", location.withLocation("x", "thinking"))
}

// Nested braces and brackets inside strings must not confuse the scanner.
func TestSerdeLocationHandlesNestingInsideStrings(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"a}]}b"}],"thinking":{"type":"nope"}}`
	location := newSerdeLocation([]byte(body))
	require.NotNil(t, location)
	valueEnd, ok := locateJSONValue([]byte(body), []any{"thinking", "type"})
	require.True(t, ok)
	// The column lands on the closing quote of the value, which sits two bytes
	// before the end of the body (`"nope"}}`).
	assert.Equal(t, len(body)-2, valueEnd)
}

func TestSerdeColumnSuffix(t *testing.T) {
	assert.Equal(t, "", serdeColumnSuffix(0))
	assert.Equal(t, "", serdeColumnSuffix(-3))
	assert.Equal(t, " at line 1 column 7", serdeColumnSuffix(7))
}
