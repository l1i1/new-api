package dto

// MergeUsage keeps the latest non-zero usage values while retaining fields
// that a later partial stream event omitted. Streaming providers commonly
// split usage metadata across events, so replacing the whole object can erase
// cache details from an earlier event.
func MergeUsage(previous, next *Usage) *Usage {
	if previous == nil {
		return next
	}
	if next == nil {
		return previous
	}

	merged := *next
	mergeInt(&merged.PromptTokens, previous.PromptTokens)
	mergeInt(&merged.CompletionTokens, previous.CompletionTokens)
	mergeInt(&merged.TotalTokens, previous.TotalTokens)
	mergeInt(&merged.PromptCacheHitTokens, previous.PromptCacheHitTokens)
	mergeInt(&merged.InputTokens, previous.InputTokens)
	mergeInt(&merged.OutputTokens, previous.OutputTokens)
	mergeInt(&merged.ClaudeCacheCreation5mTokens, previous.ClaudeCacheCreation5mTokens)
	mergeInt(&merged.ClaudeCacheCreation1hTokens, previous.ClaudeCacheCreation1hTokens)
	if merged.UsageSemantic == "" {
		merged.UsageSemantic = previous.UsageSemantic
	}
	if merged.UsageSource == "" {
		merged.UsageSource = previous.UsageSource
	}
	if merged.Cost == nil {
		merged.Cost = previous.Cost
	}

	merged.PromptTokensDetails = mergeInputTokenDetails(previous.PromptTokensDetails, merged.PromptTokensDetails)
	merged.CompletionTokenDetails = mergeOutputTokenDetails(previous.CompletionTokenDetails, merged.CompletionTokenDetails)
	if merged.InputTokensDetails == nil {
		if previous.InputTokensDetails != nil {
			inputDetails := *previous.InputTokensDetails
			merged.InputTokensDetails = &inputDetails
		}
	} else if previous.InputTokensDetails != nil {
		inputDetails := mergeInputTokenDetails(*previous.InputTokensDetails, *merged.InputTokensDetails)
		merged.InputTokensDetails = &inputDetails
	}
	merged.BillingUsage = mergeBillingUsage(previous.BillingUsage, next.BillingUsage)
	return &merged
}

func mergeInt(current *int, previous int) {
	if *current == 0 {
		*current = previous
	}
}

func mergeInputTokenDetails(previous, current InputTokenDetails) InputTokenDetails {
	mergeInt(&current.CachedTokens, previous.CachedTokens)
	mergeInt(&current.CachedCreationTokens, previous.CachedCreationTokens)
	mergeInt(&current.CacheWriteTokens, previous.CacheWriteTokens)
	mergeInt(&current.TextTokens, previous.TextTokens)
	mergeInt(&current.AudioTokens, previous.AudioTokens)
	mergeInt(&current.ImageTokens, previous.ImageTokens)
	return current
}

func mergeOutputTokenDetails(previous, current OutputTokenDetails) OutputTokenDetails {
	mergeInt(&current.TextTokens, previous.TextTokens)
	mergeInt(&current.AudioTokens, previous.AudioTokens)
	mergeInt(&current.ImageTokens, previous.ImageTokens)
	mergeInt(&current.ReasoningTokens, previous.ReasoningTokens)
	return current
}

func mergeBillingUsage(previous, next *BillingUsage) *BillingUsage {
	if previous == nil {
		return CloneBillingUsage(next)
	}
	if next == nil {
		return CloneBillingUsage(previous)
	}

	merged := CloneBillingUsage(next)
	if merged.Source == "" {
		merged.Source = previous.Source
	}
	if merged.Semantic == "" {
		merged.Semantic = previous.Semantic
	}
	if merged.OpenAIUsage != nil && previous.OpenAIUsage != nil {
		merged.OpenAIUsage = MergeUsage(previous.OpenAIUsage, merged.OpenAIUsage)
	} else if merged.OpenAIUsage == nil {
		merged.OpenAIUsage = cloneOpenAIUsage(previous.OpenAIUsage)
	}
	if merged.ClaudeUsage == nil {
		merged.ClaudeUsage = cloneClaudeUsage(previous.ClaudeUsage)
	}
	if merged.GeminiUsageMetadata != nil && previous.GeminiUsageMetadata != nil {
		metadata := mergeGeminiUsageMetadata(*previous.GeminiUsageMetadata, *merged.GeminiUsageMetadata)
		merged.GeminiUsageMetadata = &metadata
	} else if merged.GeminiUsageMetadata == nil && previous.GeminiUsageMetadata != nil {
		metadata := cloneGeminiUsageMetadata(*previous.GeminiUsageMetadata)
		merged.GeminiUsageMetadata = &metadata
	}
	return merged
}

func mergeGeminiUsageMetadata(previous, current GeminiUsageMetadata) GeminiUsageMetadata {
	mergeInt(&current.PromptTokenCount, previous.PromptTokenCount)
	mergeInt(&current.ToolUsePromptTokenCount, previous.ToolUsePromptTokenCount)
	mergeInt(&current.CandidatesTokenCount, previous.CandidatesTokenCount)
	mergeInt(&current.TotalTokenCount, previous.TotalTokenCount)
	mergeInt(&current.ThoughtsTokenCount, previous.ThoughtsTokenCount)
	mergeInt(&current.CachedContentTokenCount, previous.CachedContentTokenCount)
	if len(current.PromptTokensDetails) == 0 {
		current.PromptTokensDetails = append([]GeminiPromptTokensDetails{}, previous.PromptTokensDetails...)
	}
	if len(current.ToolUsePromptTokensDetails) == 0 {
		current.ToolUsePromptTokensDetails = append([]GeminiPromptTokensDetails{}, previous.ToolUsePromptTokensDetails...)
	}
	if len(current.CandidatesTokensDetails) == 0 {
		current.CandidatesTokensDetails = append([]GeminiPromptTokensDetails{}, previous.CandidatesTokensDetails...)
	}
	return current
}
