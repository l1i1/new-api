package common

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The response-body override is shape-only by construction: the save path
// refuses documents that touch token accounting or response headers, and the
// runtime drops such operations again in case one reached the database around
// the API. These tests pin both halves, plus the rewrite itself.

func responseInfo(id int, documentJSON string) *RelayInfo {
	var document map[string]any
	if documentJSON != "" {
		_ = json.Unmarshal([]byte(documentJSON), &document)
	}
	return &RelayInfo{
		ChannelMeta: &ChannelMeta{ChannelId: id, ResponseOverride: document},
	}
}

func TestValidateResponseOverride(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "empty document is allowed", raw: ""},
		{name: "whitespace only is allowed", raw: "   "},
		{
			name: "a shape operation is allowed",
			raw:  `{"operations":[{"path":"choices.0.message.reasoning_content","mode":"delete"}]}`,
		},
		{
			name: "a set operation is allowed",
			raw:  `{"operations":[{"path":"choices.0.message.content","mode":"set","value":"x"}]}`,
		},
		{
			name:    "usage is not rewritable",
			raw:     `{"operations":[{"path":"usage.completion_tokens","mode":"set","value":0}]}`,
			wantErr: "token accounting is not rewritable",
		},
		{
			name:    "nested usage paths are not rewritable",
			raw:     `{"operations":[{"path":"usage.completion_tokens_details.reasoning_tokens","mode":"delete"}]}`,
			wantErr: "token accounting is not rewritable",
		},
		{
			name:    "usage as a move target is refused",
			raw:     `{"operations":[{"path":"choices.0.text","mode":"move","to":"usage.fake"}]}`,
			wantErr: "token accounting is not rewritable",
		},
		{
			name:    "header operations are refused",
			raw:     `{"operations":[{"path":"choices","mode":"set_header","value":"x"}]}`,
			wantErr: "response headers are already sent",
		},
		{
			name:    "a non-object document is refused",
			raw:     `["operations"]`,
			wantErr: "must be a JSON object",
		},
		{
			name:    "a document without operations is refused",
			raw:     `{"foo":"bar"}`,
			wantErr: "operations",
		},
		{
			name:    "an operation without a mode is refused",
			raw:     `{"operations":[{"path":"choices.0.message"}]}`,
			wantErr: "needs a mode",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidateResponseOverride(testCase.raw)
			if testCase.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.wantErr)
		})
	}
}

// TestResponseOverridePathBoundaries pins the boundary rule: a field that
// merely starts with the same letters must not be treated as accounting.
func TestResponseOverridePathBoundaries(t *testing.T) {
	assert.True(t, IsResponseOverrideForbiddenPath("usage"))
	assert.True(t, IsResponseOverrideForbiddenPath("usage.completion_tokens"))
	assert.True(t, IsResponseOverrideForbiddenPath(".usage.total_tokens"))
	assert.True(t, IsResponseOverrideForbiddenPath("usage[0]"))
	assert.False(t, IsResponseOverrideForbiddenPath("usage_notes"))
	assert.False(t, IsResponseOverrideForbiddenPath("choices.0.usage_like_field"))
	assert.False(t, IsResponseOverrideForbiddenPath(""))
	require.NoError(t, ValidateResponseOverride(`{"operations":[{"path":"usage_notes","mode":"delete"}]}`))
}

func TestApplyResponseOverride(t *testing.T) {
	body := []byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi","reasoning_content":"secret"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4,"completion_tokens_details":{"reasoning_tokens":2}}}`)

	t.Run("no document passes the body through", func(t *testing.T) {
		got, err := ApplyResponseOverride(body, responseInfo(7, ""))
		require.NoError(t, err)
		assert.Equal(t, string(body), string(got))
	})

	t.Run("a nil relay info passes the body through", func(t *testing.T) {
		got, err := ApplyResponseOverride(body, nil)
		require.NoError(t, err)
		assert.Equal(t, string(body), string(got))
	})

	t.Run("a delete operation removes the field and leaves usage alone", func(t *testing.T) {
		info := responseInfo(7, `{"operations":[{"path":"choices.0.message.reasoning_content","mode":"delete"}]}`)
		got, err := ApplyResponseOverride(body, info)
		require.NoError(t, err)
		assert.NotContains(t, string(got), "reasoning_content")
		assert.Contains(t, string(got), `"content":"hi"`)
		// Shape only: the accounting the upstream reported survives verbatim.
		assert.Contains(t, string(got), `"reasoning_tokens":2`)
		assert.NotEmpty(t, info.ResponseOverrideAudit, "the applied rewrite is recorded for the request log")
	})

	t.Run("a conditional operation only fires when its condition matches", func(t *testing.T) {
		document := `{"operations":[{"path":"choices.0.finish_reason","mode":"set","value":"length","conditions":[{"path":"choices.0.message.reasoning_content","mode":"full","value":"other"}]}]}`
		got, err := ApplyResponseOverride(body, responseInfo(7, document))
		require.NoError(t, err)
		assert.Contains(t, string(got), `"finish_reason":"stop"`, "the condition did not match, so nothing changed")

		document = `{"operations":[{"path":"choices.0.finish_reason","mode":"set","value":"length","conditions":[{"path":"choices.0.message.reasoning_content","mode":"full","value":"secret"}]}]}`
		got, err = ApplyResponseOverride(body, responseInfo(7, document))
		require.NoError(t, err)
		assert.Contains(t, string(got), `"finish_reason":"length"`)
	})

	t.Run("a forbidden operation that bypassed the save path is skipped at runtime", func(t *testing.T) {
		info := responseInfo(7, `{"operations":[{"path":"usage.completion_tokens","mode":"set","value":0},{"path":"choices.0.message.reasoning_content","mode":"delete"}]}`)
		got, err := ApplyResponseOverride(body, info)
		require.NoError(t, err)
		assert.Contains(t, string(got), `"completion_tokens":4`, "accounting stays the upstream's own")
		assert.NotContains(t, string(got), "reasoning_content")
	})

	t.Run("a body that is not JSON is returned untouched", func(t *testing.T) {
		info := responseInfo(7, `{"operations":[{"path":"choices.0.message.content","mode":"delete"}]}`)
		raw := []byte("<html>upstream error page</html>")
		got, _ := ApplyResponseOverride(raw, info)
		assert.Equal(t, string(raw), string(got), "a response the document was not written for passes through")
	})
}
