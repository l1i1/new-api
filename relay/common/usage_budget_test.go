package common

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
)

func TestClampUsageToClientBudget(t *testing.T) {
	t.Run("no declared budget leaves usage untouched", func(t *testing.T) {
		info := &RelayInfo{}
		usage := &dto.Usage{PromptTokens: 10, CompletionTokens: 5000, TotalTokens: 5010}
		require.False(t, ClampUsageToClientBudget(info, usage))
		require.Equal(t, 5000, usage.CompletionTokens)
		require.Zero(t, info.UpstreamRawCompletionTokens)
	})

	t.Run("usage within budget is not clamped", func(t *testing.T) {
		info := &RelayInfo{ClientMaxTokens: 100}
		usage := &dto.Usage{PromptTokens: 10, CompletionTokens: 100, TotalTokens: 110}
		require.False(t, ClampUsageToClientBudget(info, usage))
		require.Equal(t, 100, usage.CompletionTokens)
	})

	t.Run("accumulated completion is capped and raw is kept", func(t *testing.T) {
		info := &RelayInfo{ClientMaxTokens: 100}
		usage := &dto.Usage{
			PromptTokens: 86, CompletionTokens: 457, TotalTokens: 543,
			CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 400},
		}
		require.True(t, ClampUsageToClientBudget(info, usage))
		require.Equal(t, 100, usage.CompletionTokens)
		require.Equal(t, 100, usage.CompletionTokenDetails.ReasoningTokens,
			"a client must never see more reasoning tokens than completion tokens")
		require.Equal(t, 186, usage.TotalTokens)
		require.Equal(t, 457, info.UpstreamRawCompletionTokens)

		// A second clamp keeps the largest observed raw value.
		usage.CompletionTokens = 622
		require.True(t, ClampUsageToClientBudget(info, usage))
		require.Equal(t, 622, info.UpstreamRawCompletionTokens)
		require.Equal(t, 100, usage.CompletionTokens)
	})

	t.Run("billing usage copies are capped too", func(t *testing.T) {
		info := &RelayInfo{ClientMaxTokens: 100}
		usage := &dto.Usage{
			PromptTokens: 86, CompletionTokens: 100, TotalTokens: 186,
			BillingUsage: &dto.BillingUsage{
				Source:      dto.BillingUsageSourceOAIChat,
				OpenAIUsage: &dto.Usage{PromptTokens: 86, CompletionTokens: 457, TotalTokens: 543},
				ClaudeUsage: &dto.ClaudeUsage{InputTokens: 86, OutputTokens: 457},
				GeminiUsageMetadata: &dto.GeminiUsageMetadata{
					PromptTokenCount: 86, CandidatesTokenCount: 457, TotalTokenCount: 543,
				},
			},
		}
		require.True(t, ClampUsageToClientBudget(info, usage))
		require.Equal(t, 100, usage.BillingUsage.OpenAIUsage.CompletionTokens)
		require.Equal(t, 186, usage.BillingUsage.OpenAIUsage.TotalTokens)
		require.Equal(t, 100, usage.BillingUsage.ClaudeUsage.OutputTokens)
		require.Equal(t, 100, usage.BillingUsage.GeminiUsageMetadata.CandidatesTokenCount)
		require.Equal(t, 186, usage.BillingUsage.GeminiUsageMetadata.TotalTokenCount)
		require.Equal(t, 457, info.UpstreamRawCompletionTokens)
	})

	t.Run("clamping only the billing copy is still detected", func(t *testing.T) {
		info := &RelayInfo{ClientMaxTokens: 100}
		usage := &dto.Usage{
			PromptTokens: 10, CompletionTokens: 100, TotalTokens: 110,
			BillingUsage: &dto.BillingUsage{OpenAIUsage: &dto.Usage{PromptTokens: 10, CompletionTokens: 250, TotalTokens: 260}},
		}
		require.True(t, ClampUsageToClientBudget(info, usage))
		require.Equal(t, 100, usage.BillingUsage.OpenAIUsage.CompletionTokens)
		require.Equal(t, 250, info.UpstreamRawCompletionTokens)
	})

	t.Run("claude and gemini billing semantics are detected too", func(t *testing.T) {
		for _, testCase := range []struct {
			name   string
			biling *dto.BillingUsage
			get    func(*dto.BillingUsage) int
			raw    int
		}{
			{
				name:   "claude",
				biling: &dto.BillingUsage{ClaudeUsage: &dto.ClaudeUsage{InputTokens: 10, OutputTokens: 500}},
				get:    func(b *dto.BillingUsage) int { return b.ClaudeUsage.OutputTokens },
				raw:    500,
			},
			{
				name: "gemini",
				biling: &dto.BillingUsage{GeminiUsageMetadata: &dto.GeminiUsageMetadata{
					PromptTokenCount: 10, CandidatesTokenCount: 500, TotalTokenCount: 510,
				}},
				get: func(b *dto.BillingUsage) int { return b.GeminiUsageMetadata.CandidatesTokenCount },
				raw: 500,
			},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				info := &RelayInfo{ClientMaxTokens: 100}
				usage := &dto.Usage{PromptTokens: 10, CompletionTokens: 100, TotalTokens: 110, BillingUsage: testCase.biling}
				require.True(t, ClampUsageToClientBudget(info, usage))
				require.Equal(t, 100, testCase.get(usage.BillingUsage))
				require.Equal(t, testCase.raw, info.UpstreamRawCompletionTokens)
			})
		}
	})

	t.Run("nil inputs are safe", func(t *testing.T) {
		require.False(t, ClampUsageToClientBudget(nil, &dto.Usage{}))
		require.False(t, ClampUsageToClientBudget(&RelayInfo{ClientMaxTokens: 1}, nil))
	})
}
