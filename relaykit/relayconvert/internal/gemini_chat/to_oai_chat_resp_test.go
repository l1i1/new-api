package geminichat

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsageFromGeminiMetadataPreservesImageOnlyPromptDetails(t *testing.T) {
	usage := UsageFromGeminiMetadata(&dto.GeminiUsageMetadata{
		PromptTokenCount: 100,
		TotalTokenCount:  100,
		PromptTokensDetails: []dto.GeminiPromptTokensDetails{
			{Modality: "IMAGE", TokenCount: 100},
		},
	}, 0)

	require.NotNil(t, usage)
	assert.Equal(t, 0, usage.PromptTokensDetails.TextTokens)
	assert.Equal(t, 100, usage.PromptTokensDetails.ImageTokens)
}
