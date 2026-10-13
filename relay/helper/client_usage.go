package helper

import (
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// Client-view usage sanitation.
//
// Billing already refuses to charge an implausible usage (see
// billingexpr.CheckTokenParamsSanity and the zeroing gates in service/), but a
// garbage count that reaches the *client* is an incident of its own: on
// 2026-10-13 an upstream answered with prompt_tokens=1388347719831046 and every
// relayed usage object carried that value into the customer's own accounting.
// Downstream gateways parse these numbers; absurd values overflow or crash
// them, and a customer that believed them would mis-bill its own users.
//
// So the client view is sanitized with the same yardstick as the billing view
// (billingexpr.CheckTokenCount, the shared bound): when any token dimension of
// the upstream usage is negative or beyond the plausible bound, every count in
// the client-visible usage is reported as zero.
//
// The field is kept and zeroed rather than deleted: OpenAI-, Claude- and
// Gemini-shaped clients dereference usage fields unconditionally, so dropping
// the object turns an accounting problem into a null-pointer one on the
// customer's side. Internal billing keeps the raw counts — the charge decision,
// the admin audit marker and the dashboards are unaffected by this view.

// tokenCount is one wire-visible token dimension: the JSON name used in audit
// logs plus its upstream-reported value.
type tokenCount struct {
	field string
	value int
}

// firstImplausibleCount returns the first dimension that fails the shared
// plausibility bound, or nil when every count is plausible.
func firstImplausibleCount(counts []tokenCount) *billingexpr.UsageAnomaly {
	for _, count := range counts {
		if anomaly := billingexpr.CheckTokenCount(count.field, float64(count.value)); anomaly != nil {
			return anomaly
		}
	}
	return nil
}

// inputDetailCounts lists every count carried by an OpenAI-style input-details
// object. prefix is the JSON path of the object (prompt_tokens_details or
// input_tokens_details), so the same shape is reported distinctly.
func inputDetailCounts(prefix string, details *dto.InputTokenDetails) []tokenCount {
	if details == nil {
		return nil
	}
	counts := []tokenCount{
		{prefix + ".cached_tokens", details.CachedTokens},
		{prefix + ".cached_creation_tokens", details.CachedCreationTokens},
		{prefix + ".cache_write_tokens", details.CacheWriteTokens},
		{prefix + ".text_tokens", details.TextTokens},
		{prefix + ".image_tokens", details.ImageTokens},
		{prefix + ".audio_tokens", details.AudioTokens},
	}
	if cached := details.CachedTokensDetails; cached != nil {
		for _, entry := range []struct {
			name  string
			value *int
		}{
			{"text_tokens", cached.TextTokens},
			{"image_tokens", cached.ImageTokens},
			{"audio_tokens", cached.AudioTokens},
		} {
			if entry.value != nil {
				counts = append(counts, tokenCount{prefix + ".cached_tokens_details." + entry.name, *entry.value})
			}
		}
	}
	return counts
}

// outputDetailCounts lists every count carried by an OpenAI-style
// output-details object.
func outputDetailCounts(prefix string, details *dto.OutputTokenDetails) []tokenCount {
	if details == nil {
		return nil
	}
	return []tokenCount{
		{prefix + ".text_tokens", details.TextTokens},
		{prefix + ".audio_tokens", details.AudioTokens},
		{prefix + ".image_tokens", details.ImageTokens},
		{prefix + ".reasoning_tokens", details.ReasoningTokens},
	}
}

// usageCounts lists every token dimension of an OpenAI-shaped usage object.
// Comprehensiveness matters here: a count that is checked by the billing gate
// but not by this list, or the other way round, would let the two views
// disagree about the same response.
func usageCounts(usage *dto.Usage) []tokenCount {
	if usage == nil {
		return nil
	}
	counts := []tokenCount{
		{"prompt_tokens", usage.PromptTokens},
		{"completion_tokens", usage.CompletionTokens},
		{"total_tokens", usage.TotalTokens},
		{"prompt_cache_hit_tokens", usage.PromptCacheHitTokens},
		{"input_tokens", usage.InputTokens},
		{"output_tokens", usage.OutputTokens},
		{"claude_cache_creation_5_m_tokens", usage.ClaudeCacheCreation5mTokens},
		{"claude_cache_creation_1_h_tokens", usage.ClaudeCacheCreation1hTokens},
	}
	counts = append(counts, inputDetailCounts("prompt_tokens_details", &usage.PromptTokensDetails)...)
	counts = append(counts, outputDetailCounts("completion_tokens_details", &usage.CompletionTokenDetails)...)
	counts = append(counts, inputDetailCounts("input_tokens_details", usage.InputTokensDetails)...)
	counts = append(counts, outputDetailCounts("output_tokens_details", usage.OutputTokensDetails)...)
	return counts
}

// UsageCountAnomaly reports the first implausible token dimension of an
// OpenAI-shaped usage object, or nil when the counts are plausible. Callers
// that forward a body which the struct does not own (a verbatim upstream body)
// use it to decide whether that body has to be rewritten too.
func UsageCountAnomaly(usage *dto.Usage) *billingexpr.UsageAnomaly {
	return firstImplausibleCount(usageCounts(usage))
}

// claudeUsageCountAnomaly is the Claude-shaped counterpart of
// UsageCountAnomaly.
func claudeUsageCountAnomaly(usage *dto.ClaudeUsage) *billingexpr.UsageAnomaly {
	if usage == nil {
		return nil
	}
	counts := []tokenCount{
		{"input_tokens", usage.InputTokens},
		{"output_tokens", usage.OutputTokens},
		{"cache_creation_input_tokens", usage.CacheCreationInputTokens},
		{"cache_read_input_tokens", usage.CacheReadInputTokens},
		{"claude_cache_creation_5_m_tokens", usage.ClaudeCacheCreation5mTokens},
		{"claude_cache_creation_1_h_tokens", usage.ClaudeCacheCreation1hTokens},
	}
	if creation := usage.CacheCreation; creation != nil {
		counts = append(counts,
			tokenCount{"cache_creation.ephemeral_5m_input_tokens", creation.Ephemeral5mInputTokens},
			tokenCount{"cache_creation.ephemeral_1h_input_tokens", creation.Ephemeral1hInputTokens},
		)
	}
	return firstImplausibleCount(counts)
}

// geminiUsageCountAnomaly is the Gemini-shaped counterpart of
// UsageCountAnomaly.
func geminiUsageCountAnomaly(usage *dto.GeminiUsageMetadata) *billingexpr.UsageAnomaly {
	if usage == nil {
		return nil
	}
	counts := []tokenCount{
		{"promptTokenCount", usage.PromptTokenCount},
		{"toolUsePromptTokenCount", usage.ToolUsePromptTokenCount},
		{"candidatesTokenCount", usage.CandidatesTokenCount},
		{"totalTokenCount", usage.TotalTokenCount},
		{"thoughtsTokenCount", usage.ThoughtsTokenCount},
		{"cachedContentTokenCount", usage.CachedContentTokenCount},
	}
	for _, group := range []struct {
		field  string
		values []dto.GeminiPromptTokensDetails
	}{
		{"promptTokensDetails", usage.PromptTokensDetails},
		{"toolUsePromptTokensDetails", usage.ToolUsePromptTokensDetails},
		{"candidatesTokensDetails", usage.CandidatesTokensDetails},
	} {
		for _, entry := range group.values {
			counts = append(counts, tokenCount{group.field + ".tokenCount", entry.TokenCount})
		}
	}
	return firstImplausibleCount(counts)
}

// zeroUsageCounts clears every token count of a usage object the caller owns.
// Pointer-bearing sub-objects are cloned first, so a sanitized client view can
// never write through into the billing copy it was derived from.
func zeroUsageCounts(usage *dto.Usage) {
	usage.PromptTokens = 0
	usage.CompletionTokens = 0
	usage.TotalTokens = 0
	usage.PromptCacheHitTokens = 0
	usage.InputTokens = 0
	usage.OutputTokens = 0
	usage.ClaudeCacheCreation5mTokens = 0
	usage.ClaudeCacheCreation1hTokens = 0
	usage.PromptTokensDetails = zeroInputTokenDetails(usage.PromptTokensDetails)
	usage.CompletionTokenDetails = dto.OutputTokenDetails{}
	if usage.InputTokensDetails != nil {
		details := zeroInputTokenDetails(*usage.InputTokensDetails)
		usage.InputTokensDetails = &details
	}
	if usage.OutputTokensDetails != nil {
		// Presence is preserved (the client may branch on the object being
		// there); every count inside it is zero.
		usage.OutputTokensDetails = &dto.OutputTokenDetails{}
	}
	// Cost (OpenRouter-shaped) is a billing figure we cannot vouch for when the
	// token counts themselves were garbage.
	usage.Cost = nil
}

// zeroInputTokenDetails clears every count of one input-details object,
// preserving which modalities the upstream reported.
func zeroInputTokenDetails(details dto.InputTokenDetails) dto.InputTokenDetails {
	cloned := details.Clone()
	cloned.CachedTokens = 0
	cloned.CachedCreationTokens = 0
	cloned.CacheWriteTokens = 0
	cloned.TextTokens = 0
	cloned.ImageTokens = 0
	cloned.AudioTokens = 0
	if cached := cloned.CachedTokensDetails; cached != nil {
		zero := 0
		if cached.TextTokens != nil {
			cached.TextTokens = &zero
		}
		if cached.ImageTokens != nil {
			cached.ImageTokens = &zero
		}
		if cached.AudioTokens != nil {
			cached.AudioTokens = &zero
		}
	}
	return cloned
}

func zeroClaudeUsageCounts(usage *dto.ClaudeUsage) {
	usage.InputTokens = 0
	usage.OutputTokens = 0
	usage.CacheCreationInputTokens = 0
	usage.CacheReadInputTokens = 0
	usage.ClaudeCacheCreation5mTokens = 0
	usage.ClaudeCacheCreation1hTokens = 0
	if creation := usage.CacheCreation; creation != nil {
		cloned := *creation
		cloned.Ephemeral5mInputTokens = 0
		cloned.Ephemeral1hInputTokens = 0
		usage.CacheCreation = &cloned
	}
	if tools := usage.ServerToolUse; tools != nil {
		cloned := *tools
		cloned.WebSearchRequests = 0
		cloned.WebFetchRequests = 0
		cloned.CodeExecutionRequests = 0
		cloned.ToolSearchRequests = 0
		usage.ServerToolUse = &cloned
	}
}

func zeroGeminiUsageCounts(usage *dto.GeminiUsageMetadata) {
	usage.PromptTokenCount = 0
	usage.ToolUsePromptTokenCount = 0
	usage.CandidatesTokenCount = 0
	usage.TotalTokenCount = 0
	usage.ThoughtsTokenCount = 0
	usage.CachedContentTokenCount = 0
	for _, group := range []*[]dto.GeminiPromptTokensDetails{
		&usage.PromptTokensDetails,
		&usage.ToolUsePromptTokensDetails,
		&usage.CandidatesTokensDetails,
	} {
		if len(*group) == 0 {
			continue
		}
		cloned := make([]dto.GeminiPromptTokensDetails, len(*group))
		copy(cloned, *group)
		for i := range cloned {
			cloned[i].TokenCount = 0
		}
		*group = cloned
	}
}

// sanitizedUsageForClient returns a copy of usage with every count zeroed. The
// caller's object is left untouched, including the sub-objects it shares.
func sanitizedUsageForClient(usage *dto.Usage) *dto.Usage {
	if usage == nil {
		return nil
	}
	cloned := *usage
	cloned.BillingUsage = nil
	zeroUsageCounts(&cloned)
	return &cloned
}

// sanitizedClaudeUsageForClient returns a copy of a Claude usage object with
// every count zeroed.
func sanitizedClaudeUsageForClient(usage *dto.ClaudeUsage) *dto.ClaudeUsage {
	if usage == nil {
		return nil
	}
	cloned := *usage
	cloned.BillingUsage = nil
	zeroClaudeUsageCounts(&cloned)
	return &cloned
}

// sanitizedGeminiUsageForClient returns a copy of a Gemini usage object with
// every count zeroed.
func sanitizedGeminiUsageForClient(usage dto.GeminiUsageMetadata) dto.GeminiUsageMetadata {
	cloned := usage
	cloned.BillingUsage = nil
	zeroGeminiUsageCounts(&cloned)
	return cloned
}
