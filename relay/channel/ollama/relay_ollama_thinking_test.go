package ollama

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveOllamaThinkHonorsThinkingToggle(t *testing.T) {
	tests := []struct {
		name      string
		request   *dto.GeneralOpenAIRequest
		wantThink string
		wantNil   bool
		wantErr   bool
	}{
		{
			name:      "thinking disabled maps to think=false",
			request:   &dto.GeneralOpenAIRequest{THINKING: []byte(`{"type":"disabled"}`)},
			wantThink: "false",
		},
		{
			name:      "thinking disabled wins over a reasoning effort",
			request:   &dto.GeneralOpenAIRequest{THINKING: []byte(`{"type":"disabled"}`), ReasoningEffort: "high"},
			wantThink: "false",
		},
		{
			name:    "thinking enabled without effort keeps the ollama default",
			request: &dto.GeneralOpenAIRequest{THINKING: []byte(`{"type":"enabled"}`)},
			wantNil: true,
		},
		{
			name:    "boolean thinking keeps legacy ignore behavior",
			request: &dto.GeneralOpenAIRequest{THINKING: []byte(`true`)},
			wantNil: true,
		},
		{
			name:    "string thinking keeps legacy ignore behavior",
			request: &dto.GeneralOpenAIRequest{THINKING: []byte(`"disabled"`)},
			wantNil: true,
		},
		{
			name:      "thinking enabled with effort maps through the effort enum",
			request:   &dto.GeneralOpenAIRequest{THINKING: []byte(`{"type":"enabled"}`), ReasoningEffort: "high"},
			wantThink: `"high"`,
		},
		{
			name:    "malformed thinking object is rejected",
			request: &dto.GeneralOpenAIRequest{THINKING: []byte(`{`)},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			think, err := resolveOllamaThink(tt.request)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantNil {
				assert.Nil(t, think)
				return
			}
			require.NotNil(t, think)
			assert.Equal(t, tt.wantThink, string(think))
		})
	}

	t.Run("converted chat request carries think=false for a disabled toggle", func(t *testing.T) {
		chatReq, err := openAIChatToOllamaChat(nil, &dto.GeneralOpenAIRequest{
			Model:    "deepseek-v4-pro",
			Messages: []dto.Message{{Role: "user", Content: "hi"}},
			THINKING: []byte(`{"type":"disabled"}`),
		})
		require.NoError(t, err)
		require.NotNil(t, chatReq)
		assert.Equal(t, "false", string(chatReq.Think))
	})
}
