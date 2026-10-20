package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// normalize_response_shape is opt-in, but when a channel enables it the client
// must see the same field set on every response: fields the upstream omitted
// are filled with their neutral value.
func TestNormalizeResponseShapeFillsMissingFields(t *testing.T) {
	body := []byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)

	normalized, err := NormalizeResponseShape(body)
	require.NoError(t, err)

	require.True(t, gjson.GetBytes(normalized, "choices.0.logprobs").Exists())
	require.Equal(t, "null", gjson.GetBytes(normalized, "choices.0.logprobs").Raw)
	require.True(t, gjson.GetBytes(normalized, "choices.0.message.name").Exists())
	require.Equal(t, "null", gjson.GetBytes(normalized, "choices.0.message.name").Raw)
	require.True(t, gjson.GetBytes(normalized, "choices.0.message.tool_calls").Exists())
	require.Equal(t, "null", gjson.GetBytes(normalized, "choices.0.message.tool_calls").Raw)
	require.True(t, gjson.GetBytes(normalized, "usage.prompt_tokens_details").IsObject())
	require.EqualValues(t, 0, gjson.GetBytes(normalized, "usage.prompt_tokens_details.cache_write_tokens").Int())
	// Untouched fields keep their values.
	require.Equal(t, "hi", gjson.GetBytes(normalized, "choices.0.message.content").String())
	require.Equal(t, "x", gjson.GetBytes(normalized, "id").String())
}

func TestNormalizeResponseShapePreservesPresentValues(t *testing.T) {
	body := []byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi","name":"n","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},"logprobs":{"content":[]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":4,"cache_write_tokens":7}}}`)

	normalized, err := NormalizeResponseShape(body)
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(normalized), "a response with all fields present must pass through unchanged")
}

func TestNormalizeResponseShapeIsIdempotent(t *testing.T) {
	body := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":null}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"prompt_tokens_details":{"cached_tokens":0}}}`)

	once, err := NormalizeResponseShape(body)
	require.NoError(t, err)
	twice, err := NormalizeResponseShape(once)
	require.NoError(t, err)
	require.Equal(t, string(once), string(twice))
}

func TestNormalizeResponseShapeReplacesNullPromptTokensDetails(t *testing.T) {
	body := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"prompt_tokens_details":null}}`)

	normalized, err := NormalizeResponseShape(body)
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(normalized, "usage.prompt_tokens_details").IsObject())
	require.EqualValues(t, 0, gjson.GetBytes(normalized, "usage.prompt_tokens_details.cache_write_tokens").Int())
}

func TestNormalizeResponseShapeLeavesUnsupportedBodiesAlone(t *testing.T) {
	t.Run("invalid JSON is an error", func(t *testing.T) {
		_, err := NormalizeResponseShape([]byte(`not json`))
		require.Error(t, err)
	})
	t.Run("a body without choices is unchanged", func(t *testing.T) {
		body := []byte(`{"id":"x","usage":{"prompt_tokens":1}}`)
		normalized, err := NormalizeResponseShape(body)
		require.NoError(t, err)
		require.Equal(t, string(body), string(normalized))
	})
	t.Run("a non-object body is unchanged", func(t *testing.T) {
		body := []byte(`[1,2,3]`)
		normalized, err := NormalizeResponseShape(body)
		require.NoError(t, err)
		require.Equal(t, string(body), string(normalized))
	})
}
