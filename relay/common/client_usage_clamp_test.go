package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newClientUsageClampInfo(budget int) *RelayInfo {
	return &RelayInfo{ClientMaxTokens: budget}
}

func TestClampClientUsageJSONOpenAIChunk(t *testing.T) {
	info := newClientUsageClampInfo(100)
	in := []byte(`{"id":"c1","choices":[],"usage":{"prompt_tokens":50,"completion_tokens":457,"total_tokens":507,"completion_tokens_details":{"reasoning_tokens":300}}}`)
	out := ClampClientUsageJSON(info, in)

	assert.Equal(t, int64(100), gjsonInt(out, "usage.completion_tokens"))
	assert.Equal(t, int64(150), gjsonInt(out, "usage.total_tokens"))
	assert.Equal(t, int64(100), gjsonInt(out, "usage.completion_tokens_details.reasoning_tokens"))
	assert.Equal(t, 457, info.UpstreamRawCompletionTokens)
}

func TestClampClientUsageJSONResponsesEvent(t *testing.T) {
	info := newClientUsageClampInfo(100)
	in := []byte(`{"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":50,"output_tokens":457,"total_tokens":507,"output_tokens_details":{"reasoning_tokens":300}}}}`)
	out := ClampClientUsageJSON(info, in)

	assert.Equal(t, int64(100), gjsonInt(out, "response.usage.output_tokens"))
	assert.Equal(t, int64(150), gjsonInt(out, "response.usage.total_tokens"))
	assert.Equal(t, int64(100), gjsonInt(out, "response.usage.output_tokens_details.reasoning_tokens"))
}

func TestClampClientUsageJSONAnthropicMessageDelta(t *testing.T) {
	info := newClientUsageClampInfo(100)
	in := []byte(`{"type":"message_delta","usage":{"input_tokens":50,"output_tokens":457}}`)
	out := ClampClientUsageJSON(info, in)

	assert.Equal(t, int64(100), gjsonInt(out, "usage.output_tokens"))
}

func TestClampClientUsageJSONGeminiJointBudget(t *testing.T) {
	info := newClientUsageClampInfo(100)
	in := []byte(`{"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":80,"thoughtsTokenCount":300,"totalTokenCount":430}}`)
	out := ClampClientUsageJSON(info, in)

	assert.Equal(t, int64(80), gjsonInt(out, "usageMetadata.candidatesTokenCount"))
	assert.Equal(t, int64(20), gjsonInt(out, "usageMetadata.thoughtsTokenCount"))
	assert.Equal(t, int64(150), gjsonInt(out, "usageMetadata.totalTokenCount"))
	assert.Equal(t, 380, info.UpstreamRawCompletionTokens)
}

func TestClampClientUsageJSONLeavesInBudgetAndNonUsageBytesAlone(t *testing.T) {
	info := newClientUsageClampInfo(100)
	inBudget := []byte(`{"usage":{"prompt_tokens":50,"completion_tokens":100,"total_tokens":150}}`)
	require.Equal(t, inBudget, ClampClientUsageJSON(info, inBudget))

	content := []byte(`{"choices":[{"delta":{"content":"hello"}}]}`)
	require.Equal(t, content, ClampClientUsageJSON(info, content))

	noBudget := &RelayInfo{}
	over := []byte(`{"usage":{"prompt_tokens":50,"completion_tokens":457,"total_tokens":507}}`)
	require.Equal(t, over, ClampClientUsageJSON(noBudget, over))
}

func gjsonInt(data []byte, path string) int64 {
	return gjson.GetBytes(data, path).Int()
}
