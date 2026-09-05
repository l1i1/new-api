package dto

import (
	"reflect"
	"strings"
)

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

// mergeGeminiTokenDetails combines partial Gemini usage details by normalized
// modality. A later stream event can omit modalities reported by an earlier
// event, so replacing the whole slice would silently drop billable classes.
func mergeGeminiTokenDetails(previous, current []GeminiPromptTokensDetails) []GeminiPromptTokensDetails {
	if len(previous) == 0 && len(current) == 0 {
		return nil
	}

	merged := make([]GeminiPromptTokensDetails, 0, len(previous)+len(current))
	byModality := make(map[string]int, len(previous)+len(current))
	merge := func(detail GeminiPromptTokensDetails) {
		if detail.TokenCount <= 0 {
			return
		}
		key := normalizeGeminiModality(detail.Modality)
		if index, ok := byModality[key]; ok {
			merged[index].TokenCount += detail.TokenCount
			return
		}
		byModality[key] = len(merged)
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
	if totalTokens > derivedTotal {
		missingCompletionTokens := totalTokens - derivedTotal
		metadata.CandidatesTokenCount += missingCompletionTokens
		completionTokens += missingCompletionTokens
		derivedTotal = totalTokens
	}
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

// MergeUsageNonZero overlays usage snapshots: a later non-zero field
// overwrites the current value, while a later zero value never erases an
// earlier positive count. Compatible BillingUsage snapshots follow the same
// rule within their provider-native payload.
func MergeUsageNonZero(current *Usage, incoming *Usage) *Usage {
	if current == nil {
		current = &Usage{}
	}
	if incoming == nil {
		return current
	}

	if incoming.PromptTokens > 0 {
		current.PromptTokens = incoming.PromptTokens
	}
	if incoming.CompletionTokens > 0 {
		current.CompletionTokens = incoming.CompletionTokens
	}
	if incoming.TotalTokens > 0 {
		current.TotalTokens = incoming.TotalTokens
	}
	if incoming.PromptCacheHitTokens > 0 {
		current.PromptCacheHitTokens = incoming.PromptCacheHitTokens
	}
	if incoming.InputTokens > 0 {
		current.InputTokens = incoming.InputTokens
	}
	if incoming.OutputTokens > 0 {
		current.OutputTokens = incoming.OutputTokens
	}
	if incoming.ClaudeCacheCreation5mTokens > 0 {
		current.ClaudeCacheCreation5mTokens = incoming.ClaudeCacheCreation5mTokens
	}
	if incoming.ClaudeCacheCreation1hTokens > 0 {
		current.ClaudeCacheCreation1hTokens = incoming.ClaudeCacheCreation1hTokens
	}

	mergeInputTokenDetailsNonZero(&current.PromptTokensDetails, incoming.PromptTokensDetails)
	if incoming.InputTokensDetails != nil {
		details := *incoming.InputTokensDetails
		if details.CachedTokens > 0 ||
			details.CachedCreationTokens > 0 ||
			details.CacheWriteTokens > 0 ||
			details.TextTokens > 0 ||
			details.AudioTokens > 0 ||
			details.ImageTokens > 0 {
			if current.InputTokensDetails == nil {
				current.InputTokensDetails = &InputTokenDetails{}
			}
			mergeInputTokenDetailsNonZero(current.InputTokensDetails, details)
		}
	}

	if incoming.CompletionTokenDetails.TextTokens > 0 {
		current.CompletionTokenDetails.TextTokens = incoming.CompletionTokenDetails.TextTokens
	}
	if incoming.CompletionTokenDetails.AudioTokens > 0 {
		current.CompletionTokenDetails.AudioTokens = incoming.CompletionTokenDetails.AudioTokens
	}
	if incoming.CompletionTokenDetails.ImageTokens > 0 {
		current.CompletionTokenDetails.ImageTokens = incoming.CompletionTokenDetails.ImageTokens
	}
	if incoming.CompletionTokenDetails.ReasoningTokens > 0 {
		current.CompletionTokenDetails.ReasoningTokens = incoming.CompletionTokenDetails.ReasoningTokens
	}

	if incoming.UsageSemantic != "" {
		current.UsageSemantic = incoming.UsageSemantic
	}
	if incoming.UsageSource != "" {
		current.UsageSource = incoming.UsageSource
	}
	if incoming.BillingUsage != nil {
		current.BillingUsage = MergeBillingUsageNonZero(current.BillingUsage, incoming.BillingUsage)
	}
	if incoming.Cost != nil && !reflect.ValueOf(incoming.Cost).IsZero() {
		current.Cost = incoming.Cost
	}
	if total := current.PromptTokens + current.CompletionTokens; total > current.TotalTokens {
		current.TotalTokens = total
	}
	if total := current.InputTokens + current.OutputTokens; total > current.TotalTokens {
		current.TotalTokens = total
	}

	return current
}

// MergeBillingUsageNonZero preserves non-zero provider-native fields across
// partial stream snapshots. A snapshot from a different billing dialect
// remains authoritative and replaces the previous payload.
func MergeBillingUsageNonZero(current *BillingUsage, incoming *BillingUsage) *BillingUsage {
	if incoming == nil {
		return CloneBillingUsage(current)
	}
	if current == nil || !sameBillingUsageDialect(current, incoming) {
		replaced := CloneBillingUsage(incoming)
		if current != nil && replaced != nil {
			// Replacement carries the incoming payload; Estimated carries the
			// history of any local synthesis on either side.
			replaced.Estimated = current.Estimated || incoming.Estimated
		}
		return replaced
	}

	merged := CloneBillingUsage(current)
	if incoming.Source != "" {
		merged.Source = incoming.Source
	}
	if incoming.Semantic != "" {
		merged.Semantic = incoming.Semantic
	}
	merged.Estimated = current.Estimated || incoming.Estimated

	switch {
	case current.OpenAIUsage != nil && incoming.OpenAIUsage != nil:
		merged.OpenAIUsage = MergeUsageNonZero(
			cloneOpenAIUsage(current.OpenAIUsage),
			cloneOpenAIUsage(incoming.OpenAIUsage),
		)
	case current.ClaudeUsage != nil && incoming.ClaudeUsage != nil:
		merged.ClaudeUsage = MergeClaudeUsageNonZero(current.ClaudeUsage, incoming.ClaudeUsage)
	case current.GeminiUsageMetadata != nil && incoming.GeminiUsageMetadata != nil:
		merged.GeminiUsageMetadata = MergeGeminiUsageMetadataNonZero(current.GeminiUsageMetadata, incoming.GeminiUsageMetadata)
	}

	return merged
}

func sameBillingUsageDialect(current *BillingUsage, incoming *BillingUsage) bool {
	if current.Source != "" && incoming.Source != "" && !strings.EqualFold(current.Source, incoming.Source) {
		return false
	}
	if current.Semantic != "" && incoming.Semantic != "" && !strings.EqualFold(current.Semantic, incoming.Semantic) {
		return false
	}
	return current.OpenAIUsage != nil && incoming.OpenAIUsage != nil ||
		current.ClaudeUsage != nil && incoming.ClaudeUsage != nil ||
		current.GeminiUsageMetadata != nil && incoming.GeminiUsageMetadata != nil
}

func MergeClaudeUsageNonZero(current *ClaudeUsage, incoming *ClaudeUsage) *ClaudeUsage {
	merged := cloneClaudeUsage(current)
	if merged == nil {
		merged = &ClaudeUsage{}
	}
	if incoming == nil {
		if current != nil {
			merged.BillingUsage = CloneBillingUsage(current.BillingUsage)
		}
		return merged
	}
	if incoming.InputTokens > 0 {
		merged.InputTokens = incoming.InputTokens
	}
	if incoming.CacheCreationInputTokens > 0 {
		merged.CacheCreationInputTokens = incoming.CacheCreationInputTokens
	}
	if incoming.CacheReadInputTokens > 0 {
		merged.CacheReadInputTokens = incoming.CacheReadInputTokens
	}
	if incoming.OutputTokens > 0 {
		merged.OutputTokens = incoming.OutputTokens
	}
	if incoming.ClaudeCacheCreation5mTokens > 0 {
		merged.ClaudeCacheCreation5mTokens = incoming.ClaudeCacheCreation5mTokens
	}
	if incoming.ClaudeCacheCreation1hTokens > 0 {
		merged.ClaudeCacheCreation1hTokens = incoming.ClaudeCacheCreation1hTokens
	}
	if incoming.CacheCreation != nil {
		cacheCreation := *incoming.CacheCreation
		merged.CacheCreation = &cacheCreation
		// Flat legacy fields are the same information as the sub-object.
		// Sync them as a whole overwrite, including explicit zeros, so a
		// later correction cannot leave a stale high-watermark behind.
		merged.ClaudeCacheCreation5mTokens = cacheCreation.Ephemeral5mInputTokens
		merged.ClaudeCacheCreation1hTokens = cacheCreation.Ephemeral1hInputTokens
	}
	if incoming.ServerToolUse != nil {
		if merged.ServerToolUse == nil {
			merged.ServerToolUse = &ClaudeServerToolUse{}
		}
		if incoming.ServerToolUse.WebSearchRequests > 0 {
			merged.ServerToolUse.WebSearchRequests = incoming.ServerToolUse.WebSearchRequests
		}
		if incoming.ServerToolUse.WebFetchRequests > 0 {
			merged.ServerToolUse.WebFetchRequests = incoming.ServerToolUse.WebFetchRequests
		}
		if incoming.ServerToolUse.CodeExecutionRequests > 0 {
			merged.ServerToolUse.CodeExecutionRequests = incoming.ServerToolUse.CodeExecutionRequests
		}
		if incoming.ServerToolUse.ToolSearchRequests > 0 {
			merged.ServerToolUse.ToolSearchRequests = incoming.ServerToolUse.ToolSearchRequests
		}
	}
	// cloneClaudeUsage strips BillingUsage so a nested Claude snapshot cannot
	// recurse. Restore the client-visible sidecar here: incoming wins when
	// present (authoritative/upstream), otherwise keep current's.
	if incoming.BillingUsage != nil {
		merged.BillingUsage = CloneBillingUsage(incoming.BillingUsage)
	} else if current != nil {
		merged.BillingUsage = CloneBillingUsage(current.BillingUsage)
	}
	return merged
}

// MergeGeminiUsageMetadataNonZero overlays Gemini's cumulative usage
// snapshots: a later non-zero field overwrites the current value without
// dropping fields omitted by a later chunk.
func MergeGeminiUsageMetadataNonZero(current *GeminiUsageMetadata, incoming *GeminiUsageMetadata) *GeminiUsageMetadata {
	if current == nil && incoming == nil {
		return nil
	}
	if current == nil {
		metadata := cloneGeminiUsageMetadata(*incoming)
		metadata.BillingUsage = CloneBillingUsage(incoming.BillingUsage)
		return &metadata
	}

	merged := cloneGeminiUsageMetadata(*current)
	merged.BillingUsage = CloneBillingUsage(current.BillingUsage)
	if incoming == nil {
		return &merged
	}
	if incoming.PromptTokenCount > 0 {
		merged.PromptTokenCount = incoming.PromptTokenCount
	}
	if incoming.ToolUsePromptTokenCount > 0 {
		merged.ToolUsePromptTokenCount = incoming.ToolUsePromptTokenCount
	}
	if incoming.CandidatesTokenCount > 0 {
		merged.CandidatesTokenCount = incoming.CandidatesTokenCount
		merged.ThoughtsTokenCount = incoming.ThoughtsTokenCount
	} else if incoming.ThoughtsTokenCount > 0 {
		merged.ThoughtsTokenCount = incoming.ThoughtsTokenCount
	}
	if incoming.TotalTokenCount > 0 {
		merged.TotalTokenCount = incoming.TotalTokenCount
	}
	if incoming.CachedContentTokenCount > 0 {
		merged.CachedContentTokenCount = incoming.CachedContentTokenCount
	}
	merged.PromptTokensDetails = mergeGeminiTokenDetails(merged.PromptTokensDetails, incoming.PromptTokensDetails)
	merged.ToolUsePromptTokensDetails = mergeGeminiTokenDetails(merged.ToolUsePromptTokensDetails, incoming.ToolUsePromptTokensDetails)
	merged.CandidatesTokensDetails = mergeGeminiTokenDetails(merged.CandidatesTokensDetails, incoming.CandidatesTokensDetails)
	if incoming.BillingUsage != nil {
		merged.BillingUsage = MergeBillingUsageNonZero(merged.BillingUsage, incoming.BillingUsage)
	}
	if total := merged.PromptTokenCount + merged.ToolUsePromptTokenCount + merged.CandidatesTokenCount + merged.ThoughtsTokenCount; total > merged.TotalTokenCount {
		merged.TotalTokenCount = total
	}
	return &merged
}

func normalizeGeminiModality(modality string) string {
	return strings.ToUpper(strings.TrimSpace(modality))
}

func mergeInputTokenDetailsNonZero(current *InputTokenDetails, incoming InputTokenDetails) {
	if incoming.CachedTokens > 0 {
		current.CachedTokens = incoming.CachedTokens
	}
	if incoming.CachedCreationTokens > 0 {
		current.CachedCreationTokens = incoming.CachedCreationTokens
	}
	if incoming.CacheWriteTokens > 0 {
		current.CacheWriteTokens = incoming.CacheWriteTokens
	}
	if incoming.TextTokens > 0 {
		current.TextTokens = incoming.TextTokens
	}
	if incoming.AudioTokens > 0 {
		current.AudioTokens = incoming.AudioTokens
	}
	if incoming.ImageTokens > 0 {
		current.ImageTokens = incoming.ImageTokens
	}
}
