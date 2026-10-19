package dto

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

// TestGetTokenCountMetaCountsReplayedReasoningAndToolCalls pins the estimator
// fix for INC-20261019-01: thinking-mode clients echo reasoning_content and
// tool calls back in the conversation, the upstream tokenizes them, and the
// local estimate used to ignore both — which made the L2-A usage-deviation
// guard fire on ordinary traffic (upstream prompt 1.2–5.9x the estimate).
func TestGetTokenCountMetaCountsReplayedReasoningAndToolCalls(t *testing.T) {
	raw := `{
		"model": "kimi-k3",
		"messages": [
			{"role": "user", "content": "count the tokens"},
			{"role": "assistant", "content": "calling the tool", "reasoning_content": "I should look up the order first, then report its status.", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "lookup_order", "arguments": "{\"order_id\":\"A-100\"}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "status: shipped"}
		]
	}`

	withReasoning := &GeneralOpenAIRequest{}
	require.NoError(t, kitutil.Unmarshal([]byte(raw), withReasoning))
	meta := withReasoning.GetTokenCountMeta()
	assert.Contains(t, meta.CombineText, "I should look up the order first")
	assert.Contains(t, meta.CombineText, "lookup_order")
	assert.Contains(t, meta.CombineText, "A-100")

	// A message without reasoning/tool calls must not gain any bytes.
	plain := &GeneralOpenAIRequest{}
	require.NoError(t, kitutil.Unmarshal([]byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}]}`), plain))
	assert.Equal(t, "user\nhi", plain.GetTokenCountMeta().CombineText)
}
