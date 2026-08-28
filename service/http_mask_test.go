package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplaceTopLevelJSONValueBasic(t *testing.T) {
	data := []byte(`{"model":"@cf/gpt-4o-2024-11-20","choices":[{"message":{"model":"nested"}}],"usage":{"prompt_tokens":1}}`)
	out, ok := replaceTopLevelJSONValue(data, "model", []byte(`"gpt-4o"`))
	require.True(t, ok)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(out, &payload))
	assert.Equal(t, "gpt-4o", payload["model"])
	// Nested object and usage keys survive untouched.
	assert.Equal(t, []interface{}{map[string]interface{}{
		"message": map[string]interface{}{"model": "nested"},
	}}, payload["choices"])
	assert.Equal(t, map[string]interface{}{"prompt_tokens": float64(1)}, payload["usage"])
}

func TestReplaceTopLevelJSONValueKeepsNestedKey(t *testing.T) {
	// A nested "model" key must NOT be touched.
	data := []byte(`{"obj":{"model":"inner"},"model":"outer","x":[{"model":"list"}]}`)
	out, ok := replaceTopLevelJSONValue(data, "model", []byte(`"top"`))
	require.True(t, ok)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(out, &payload))
	assert.Equal(t, "top", payload["model"])
	assert.Equal(t, map[string]interface{}{"model": "inner"}, payload["obj"])
	assert.Equal(t, []interface{}{map[string]interface{}{"model": "list"}}, payload["x"])
}

func TestReplaceTopLevelJSONValueMissingKey(t *testing.T) {
	data := []byte(`{"id":"abc","choices":[]}`)
	out, ok := replaceTopLevelJSONValue(data, "model", []byte(`"m"`))
	assert.False(t, ok)
	assert.Equal(t, string(data), string(out))
}

func TestReplaceTopLevelJSONValueNotObject(t *testing.T) {
	data := []byte(`[1,2,3]`)
	_, ok := replaceTopLevelJSONValue(data, "model", []byte(`"m"`))
	assert.False(t, ok)
}

func TestReplaceTopLevelJSONValueEscapedQuotes(t *testing.T) {
	data := []byte(`{"model":"up\"stream","obj":{"model":"x"},"n":1}`)
	out, ok := replaceTopLevelJSONValue(data, "model", []byte(`"origin"`))
	require.True(t, ok)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(out, &payload))
	assert.Equal(t, "origin", payload["model"])
	// Nested model key and other fields survive.
	assert.Equal(t, map[string]interface{}{"model": "x"}, payload["obj"])
	assert.Equal(t, float64(1), payload["n"])
}
