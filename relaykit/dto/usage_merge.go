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
	syncGeminiUsageFromBilling(&merged)
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
	current.PromptTokensDetails = mergeGeminiTokenDetails(previous.PromptTokensDetails, current.PromptTokensDetails)
	current.ToolUsePromptTokensDetails = mergeGeminiTokenDetails(previous.ToolUsePromptTokensDetails, current.ToolUsePromptTokensDetails)
	current.CandidatesTokensDetails = mergeGeminiTokenDetails(previous.CandidatesTokensDetails, current.CandidatesTokensDetails)
	return current
}

// mergeGeminiTokenDetails retains one latest confirmed value per modality. A
// later stream event can omit modalities reported by an earlier event, so
// replacing the whole slice would silently drop billable token classes.
func mergeGeminiTokenDetails(previous, current []GeminiPromptTokensDetails) []GeminiPromptTokensDetails {
	if len(previous) == 0 && len(current) == 0 {
		return nil
	}

	merged := make([]GeminiPromptTokensDetails, 0, len(previous)+len(current))
	byModality := make(map[string]int, len(previous)+len(current))
	merge := func(detail GeminiPromptTokensDetails) {
		if index, ok := byModality[detail.Modality]; ok {
			// Zero is ambiguous on partial streams; retain a confirmed earlier
			// count until a later event provides a non-zero value.
			if detail.TokenCount != 0 {
				merged[index] = detail
			}
			return
		}
		byModality[detail.Modality] = len(merged)
		merged = append(merged, detail)
	}

	for _, detail := range previous {
		merge(detail)
	}
	for _, detail := range current {
		merge(detail)
	}
	return merged
}

// syncGeminiUsageFromBilling keeps the protocol-neutral usage object aligned
// with the merged Gemini metadata used for settlement. This is important when
// separate stream events carry thoughts and candidates token counts.
func syncGeminiUsageFromBilling(usage *Usage) {
	if usage == nil || usage.BillingUsage == nil || usage.BillingUsage.GeminiUsageMetadata == nil {
		return
	}

	metadata := usage.BillingUsage.GeminiUsageMetadata
	promptTokens := metadata.PromptTokenCount + metadata.ToolUsePromptTokenCount
	if promptTokens == 0 && usage.PromptTokens != 0 {
		promptTokens = usage.PromptTokens
		metadata.PromptTokenCount = promptTokens
	}
	completionTokens := metadata.CandidatesTokenCount + metadata.ThoughtsTokenCount
	if completionTokens == 0 && usage.CompletionTokens != 0 {
		completionTokens = usage.CompletionTokens
		metadata.CandidatesTokenCount = completionTokens
	}
	totalTokens := metadata.TotalTokenCount
	if totalTokens == 0 {
		totalTokens = usage.TotalTokens
	}
	derivedTotal := promptTokens + completionTokens
	if derivedTotal > totalTokens {
		totalTokens = derivedTotal
	}
	metadata.TotalTokenCount = totalTokens

	usage.PromptTokens = promptTokens
	usage.CompletionTokens = completionTokens
	usage.TotalTokens = totalTokens
	if metadata.ThoughtsTokenCount != 0 || usage.CompletionTokenDetails.ReasoningTokens == 0 {
		usage.CompletionTokenDetails.ReasoningTokens = metadata.ThoughtsTokenCount
	}
	if metadata.CachedContentTokenCount != 0 || usage.PromptTokensDetails.CachedTokens == 0 {
		usage.PromptTokensDetails.CachedTokens = metadata.CachedContentTokenCount
	}
}
