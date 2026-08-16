package dto

import (
	"testing"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewGeminiChatBillingUsageRequiresTokenContent(t *testing.T) {
	require.Nil(t, NewGeminiChatBillingUsage(nil))
	require.Nil(t, NewGeminiChatBillingUsage(&GeminiUsageMetadata{}))

	billingUsage := NewGeminiChatBillingUsage(&GeminiUsageMetadata{PromptTokenCount: 1})
	require.NotNil(t, billingUsage)
	require.NotNil(t, billingUsage.GeminiUsageMetadata)
	assert.Equal(t, BillingUsageSourceGeminiChat, billingUsage.Source)
	assert.Equal(t, BillingUsageSemanticGemini, billingUsage.Semantic)
	assert.False(t, billingUsage.Estimated)
}

func TestNewClaudeMessagesBillingUsageRequiresTokenContent(t *testing.T) {
	require.Nil(t, NewClaudeMessagesBillingUsage(nil))
	require.Nil(t, NewClaudeMessagesBillingUsage(&ClaudeUsage{}))
	require.Nil(t, NewClaudeMessagesBillingUsage(&ClaudeUsage{CacheCreation: &ClaudeCacheCreationUsage{}}))

	billingUsage := NewClaudeMessagesBillingUsage(&ClaudeUsage{InputTokens: 1})
	require.NotNil(t, billingUsage)
	require.NotNil(t, billingUsage.ClaudeUsage)
	assert.Equal(t, BillingUsageSourceClaudeMessages, billingUsage.Source)
	assert.Equal(t, BillingUsageSemanticAnthropic, billingUsage.Semantic)

	cacheOnly := NewClaudeMessagesBillingUsage(&ClaudeUsage{
		CacheCreation: &ClaudeCacheCreationUsage{Ephemeral5mInputTokens: 4},
	})
	require.NotNil(t, cacheOnly)
}

func TestNewOpenAIChatBillingUsageRequiresTokenContent(t *testing.T) {
	require.Nil(t, NewOpenAIChatBillingUsage(nil))
	require.Nil(t, NewOpenAIChatBillingUsage(&Usage{}))

	billingUsage := NewOpenAIChatBillingUsage(&Usage{PromptTokens: 1})
	require.NotNil(t, billingUsage)
	require.NotNil(t, billingUsage.OpenAIUsage)
	assert.Equal(t, BillingUsageSourceOAIChat, billingUsage.Source)
	assert.Equal(t, BillingUsageSemanticOpenAI, billingUsage.Semantic)
	assert.Equal(t, 1, billingUsage.OpenAIUsage.PromptTokens)
}

func TestNewEstimatedGeminiChatBillingUsage(t *testing.T) {
	billingUsage := NewEstimatedGeminiChatBillingUsage(&Usage{
		PromptTokens:     11,
		CompletionTokens: 7,
		PromptTokensDetails: InputTokenDetails{
			CachedTokens: 5,
		},
	})

	require.NotNil(t, billingUsage)
	require.NotNil(t, billingUsage.GeminiUsageMetadata)
	assert.True(t, billingUsage.Estimated)
	assert.Equal(t, 11, billingUsage.GeminiUsageMetadata.PromptTokenCount)
	assert.Equal(t, 7, billingUsage.GeminiUsageMetadata.CandidatesTokenCount)
	assert.Equal(t, 18, billingUsage.GeminiUsageMetadata.TotalTokenCount)
	assert.Equal(t, 5, billingUsage.GeminiUsageMetadata.CachedContentTokenCount)
}

func TestMergeUsageRetainsGeminiCacheFromEarlierEvent(t *testing.T) {
	previous := &Usage{
		PromptTokens:        100,
		TotalTokens:         105,
		BillingUsage:        NewGeminiChatBillingUsage(&GeminiUsageMetadata{PromptTokenCount: 100, TotalTokenCount: 105, CachedContentTokenCount: 80}),
		PromptTokensDetails: InputTokenDetails{CachedTokens: 80},
	}
	next := &Usage{
		PromptTokens:     100,
		CompletionTokens: 5,
		TotalTokens:      105,
		BillingUsage:     NewGeminiChatBillingUsage(&GeminiUsageMetadata{PromptTokenCount: 100, CandidatesTokenCount: 5, TotalTokenCount: 105}),
	}

	merged := MergeUsage(previous, next)
	require.NotNil(t, merged)
	assert.Equal(t, 80, merged.PromptTokensDetails.CachedTokens)
	require.NotNil(t, merged.BillingUsage)
	require.NotNil(t, merged.BillingUsage.GeminiUsageMetadata)
	assert.Equal(t, 80, merged.BillingUsage.GeminiUsageMetadata.CachedContentTokenCount)
}

func TestMergeUsageKeepsGeminiThoughtsAndCandidatesConsistent(t *testing.T) {
	previousMetadata := &GeminiUsageMetadata{
		PromptTokenCount:   100,
		ThoughtsTokenCount: 2,
		TotalTokenCount:    102,
	}
	nextMetadata := &GeminiUsageMetadata{
		PromptTokenCount:     100,
		CandidatesTokenCount: 5,
		TotalTokenCount:      105,
	}

	previous := &Usage{
		BillingUsage: NewGeminiChatBillingUsage(previousMetadata),
	}
	next := &Usage{
		BillingUsage: NewGeminiChatBillingUsage(nextMetadata),
	}

	merged := MergeUsage(previous, next)
	require.NotNil(t, merged)
	require.NotNil(t, merged.BillingUsage)
	require.NotNil(t, merged.BillingUsage.GeminiUsageMetadata)

	metadata := merged.BillingUsage.GeminiUsageMetadata
	assert.Equal(t, 2, metadata.ThoughtsTokenCount)
	assert.Equal(t, 5, metadata.CandidatesTokenCount)
	assert.Equal(t, 100, metadata.PromptTokenCount)
	assert.Equal(t, 107, metadata.TotalTokenCount)
	assert.Equal(t, metadata.PromptTokenCount+metadata.ToolUsePromptTokenCount, merged.PromptTokens)
	assert.Equal(t, metadata.CandidatesTokenCount+metadata.ThoughtsTokenCount, merged.CompletionTokens)
	assert.Equal(t, metadata.TotalTokenCount, merged.TotalTokens)
}

func TestMergeUsageMergesGeminiModalitiesAcrossPartialEvents(t *testing.T) {
	previous := &Usage{
		BillingUsage: NewGeminiChatBillingUsage(&GeminiUsageMetadata{
			PromptTokensDetails: []GeminiPromptTokensDetails{
				{Modality: "TEXT", TokenCount: 100},
			},
			ToolUsePromptTokensDetails: []GeminiPromptTokensDetails{
				{Modality: "AUDIO", TokenCount: 20},
			},
			CandidatesTokensDetails: []GeminiPromptTokensDetails{
				{Modality: "IMAGE", TokenCount: 30},
			},
		}),
	}
	next := &Usage{
		BillingUsage: NewGeminiChatBillingUsage(&GeminiUsageMetadata{
			PromptTokensDetails: []GeminiPromptTokensDetails{
				{Modality: "IMAGE", TokenCount: 7},
			},
			ToolUsePromptTokensDetails: []GeminiPromptTokensDetails{
				{Modality: "TEXT", TokenCount: 8},
			},
			CandidatesTokensDetails: []GeminiPromptTokensDetails{
				{Modality: "AUDIO", TokenCount: 9},
			},
		}),
	}

	merged := MergeUsage(previous, next)
	require.NotNil(t, merged)
	require.NotNil(t, merged.BillingUsage)
	require.NotNil(t, merged.BillingUsage.GeminiUsageMetadata)

	metadata := merged.BillingUsage.GeminiUsageMetadata
	assert.Equal(t, []GeminiPromptTokensDetails{
		{Modality: "TEXT", TokenCount: 100},
		{Modality: "IMAGE", TokenCount: 7},
	}, metadata.PromptTokensDetails)
	assert.Equal(t, []GeminiPromptTokensDetails{
		{Modality: "AUDIO", TokenCount: 20},
		{Modality: "TEXT", TokenCount: 8},
	}, metadata.ToolUsePromptTokensDetails)
	assert.Equal(t, []GeminiPromptTokensDetails{
		{Modality: "IMAGE", TokenCount: 30},
		{Modality: "AUDIO", TokenCount: 9},
	}, metadata.CandidatesTokensDetails)
}

func TestMergeUsageKeepsGeminiFallbackTotalsForCacheOnlyMetadata(t *testing.T) {
	previous := &Usage{
		PromptTokens:     100,
		CompletionTokens: 5,
		TotalTokens:      105,
		BillingUsage: NewGeminiChatBillingUsage(&GeminiUsageMetadata{
			CachedContentTokenCount: 80,
		}),
	}
	next := &Usage{
		BillingUsage: NewGeminiChatBillingUsage(&GeminiUsageMetadata{
			CachedContentTokenCount: 80,
		}),
	}

	merged := MergeUsage(previous, next)
	require.NotNil(t, merged)
	require.NotNil(t, merged.BillingUsage)
	require.NotNil(t, merged.BillingUsage.GeminiUsageMetadata)
	assert.Equal(t, 100, merged.PromptTokens)
	assert.Equal(t, 5, merged.CompletionTokens)
	assert.Equal(t, 105, merged.TotalTokens)
	assert.Equal(t, 100, merged.BillingUsage.GeminiUsageMetadata.PromptTokenCount)
	assert.Equal(t, 5, merged.BillingUsage.GeminiUsageMetadata.CandidatesTokenCount)
	assert.Equal(t, 105, merged.BillingUsage.GeminiUsageMetadata.TotalTokenCount)
}

func TestBillingUsageJSONUsesProtocolNamedFields(t *testing.T) {
	billingUsage := &BillingUsage{
		OpenAIUsage:         &Usage{PromptTokens: 1, BillingUsage: NewClaudeMessagesBillingUsage(&ClaudeUsage{InputTokens: 9})},
		ClaudeUsage:         &ClaudeUsage{InputTokens: 2, BillingUsage: NewOpenAIChatBillingUsage(&Usage{PromptTokens: 8})},
		GeminiUsageMetadata: &GeminiUsageMetadata{PromptTokenCount: 3, BillingUsage: NewOpenAIChatBillingUsage(&Usage{PromptTokens: 7})},
	}

	data, err := kitutil.Marshal(billingUsage)
	require.NoError(t, err)

	assert.Contains(t, string(data), `"openai_usage"`)
	assert.Contains(t, string(data), `"claude_usage"`)
	assert.Contains(t, string(data), `"gemini_usage_metadata"`)
	assert.NotContains(t, string(data), `"usage":`)
	assert.NotContains(t, string(data), `"usage_metadata"`)

	clone := CloneBillingUsage(billingUsage)
	require.NotNil(t, clone.OpenAIUsage)
	require.NotNil(t, clone.ClaudeUsage)
	require.NotNil(t, clone.GeminiUsageMetadata)
	assert.Nil(t, clone.OpenAIUsage.BillingUsage)
	assert.Nil(t, clone.ClaudeUsage.BillingUsage)
	assert.Nil(t, clone.GeminiUsageMetadata.BillingUsage)
}
