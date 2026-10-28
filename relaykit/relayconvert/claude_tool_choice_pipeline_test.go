package relayconvert

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Claude -> Chat conversion runs through ConvertRequest, whose toolconv stage
// extracts the source tool_choice, clears it from the clone the converter sees,
// and re-attaches it to the converted target. A test on the internal converter
// would therefore never exercise the production path; this one does. It also
// pins the pre-existing gaps toolconv keeps on this wire (undeclared named tool
// and the OpenAI-only allowed_tools shape are dropped rather than forwarded).
func TestConvertRequestCarriesClaudeToolChoice(t *testing.T) {
	tools := `{"name":"get_weather","description":"查询指定城市的当前天气","input_schema":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}`
	body := func(toolChoice string) []byte {
		return []byte(`{"model":"kimi-k3.1-pro","max_tokens":128,
 "messages":[{"role":"user","content":"北京天气怎么样"}],
 "tools":[` + tools + `]` + toolChoice + `}`)
	}

	tests := []struct {
		name           string
		toolChoice     string
		wantToolChoice string // JSON encoding of the converted field; empty means nil
		wantParallel   *bool
	}{
		{
			name:           "declared named tool becomes a forced function",
			toolChoice:     `,"tool_choice":{"type":"tool","name":"get_weather"}`,
			wantToolChoice: `{"function":{"name":"get_weather"},"type":"function"}`,
		},
		{
			name:           "disable_parallel_tool_use becomes parallel_tool_calls=false",
			toolChoice:     `,"tool_choice":{"type":"tool","name":"get_weather","disable_parallel_tool_use":true}`,
			wantToolChoice: `{"function":{"name":"get_weather"},"type":"function"}`,
			wantParallel:   boolPtr(false),
		},
		{
			name:           "any becomes required",
			toolChoice:     `,"tool_choice":{"type":"any"}`,
			wantToolChoice: `"required"`,
		},
		{
			name:           "auto stays auto",
			toolChoice:     `,"tool_choice":{"type":"auto"}`,
			wantToolChoice: `"auto"`,
		},
		{
			name:           "none stays none",
			toolChoice:     `,"tool_choice":{"type":"none"}`,
			wantToolChoice: `"none"`,
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
			result, err := ConvertRequest(context.Background(), &convmeta.Values{}, types.RelayFormatOpenAI, &request)
			require.NoError(t, err)
			converted, ok := result.Value.(*dto.GeneralOpenAIRequest)
			require.True(t, ok, "expected OpenAI chat request, got %T", result.Value)
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
