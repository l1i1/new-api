package zhipu_4v

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestOpenAI2ZhipuPreservesOfficialFitFields(t *testing.T) {
	body := `{
		"model": "glm-5.3",
		"messages": [{"role": "user", "content": "hi"}],
		"response_format": {"type": "json_object"},
		"stop": ["end"],
		"reasoning_effort": "low",
		"top_p": 1.0
	}`
	var req dto.GeneralOpenAIRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))

	out := requestOpenAI2Zhipu(req)

	require.NotNil(t, out.ResponseFormat)
	assert.Equal(t, "json_object", out.ResponseFormat.Type)
	assert.Equal(t, []string{"end"}, out.Stop)
	assert.Equal(t, "low", out.ReasoningEffort)
	require.NotNil(t, out.TopP)
	assert.Equal(t, 1.0, *out.TopP)
}

func TestRequestOpenAI2ZhipuCoercesStopShapes(t *testing.T) {
	cases := []struct {
		name string
		stop any
		want []string
	}{
		{"string", "end", []string{"end"}},
		{"array of strings", []string{"a", "b"}, []string{"a", "b"}},
		{"decoded json array", []any{"a", "b"}, []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := dto.GeneralOpenAIRequest{
				Model:    "glm-5.3",
				Messages: []dto.Message{{Role: "user", Content: "hi"}},
				Stop:     tc.stop,
			}
			out := requestOpenAI2Zhipu(req)
			assert.Equal(t, tc.want, out.Stop)
		})
	}
}
