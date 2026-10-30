package claude

import "github.com/QuantumNous/new-api/relaykit/dto"

// ImplicitCacheBlockTokens is the block granularity an implicit prefix cache (Kimi
// prompt caching) reports cache writes in. Measured against ground truth from the
// official channel on 2026-10-30: of 5,961 production rows carrying a reported
// cache write, every write count was a multiple of 256 and prompt-read-write stayed
// below 256 in 99.93% of them (the block remainder is the uncached input).
const ImplicitCacheBlockTokens = 256

// FillEstimatedCacheCreation fills the cache-write count when the upstream reports
// only cache hits. Implicit caching writes the whole prefix it processes, in fixed
// blocks, so the tokens that missed the cache split into whole written blocks and a
// sub-block uncached remainder:
//
//	write    = miss / 256 * 256
//	uncached = miss - write
//
// This preserves the official invariant read + write + uncached = prompt for a
// client that prices the three buckets separately, and it is reporting-side only:
// the OpenAI billing usage keeps its counts (the gateway charges every non-hit token
// at the input price, which equals the 5-minute write price), so the total bill and
// the client's arithmetic on the official price list agree to the token.
func FillEstimatedCacheCreation(usage *dto.ClaudeUsage) {
	if usage == nil {
		return
	}
	if usage.CacheCreationInputTokens > 0 || usage.CacheCreation != nil {
		// The upstream reported a real split; never overwrite it.
		return
	}
	miss := usage.InputTokens + usage.CacheCreationInputTokens
	if miss <= 0 {
		return
	}
	write := miss / ImplicitCacheBlockTokens * ImplicitCacheBlockTokens
	if write <= 0 {
		// A prompt shorter than one block cannot have been written to the cache.
		return
	}
	usage.CacheCreationInputTokens = write
	usage.InputTokens = miss - write
}

func UsageFromOpenAI(usage *dto.Usage) *dto.ClaudeUsage {
	if usage == nil {
		return nil
	}
	// An existing sidecar snapshots the original provider usage; carry it
	// across this bridge unchanged regardless of its dialect. Only synthesize
	// an OpenAI snapshot when no sidecar exists yet.
	existingBillingUsage := dto.CloneBillingUsage(usage.BillingUsage)
	if existingBillingUsage != nil && existingBillingUsage.ClaudeUsage != nil &&
		(existingBillingUsage.Source == dto.BillingUsageSourceClaudeMessages || existingBillingUsage.Semantic == dto.BillingUsageSemanticAnthropic) {
		result := existingBillingUsage.ClaudeUsage
		result.BillingUsage = dto.CloneBillingUsage(usage.BillingUsage)
		return result
	}
	billingUsage := existingBillingUsage
	if billingUsage == nil {
		billingUsage = dto.NewOpenAIChatBillingUsage(usage)
	}
	cacheCreation5m, cacheCreation1h := NormalizeCacheCreationSplit(
		usage.PromptTokensDetails.CachedCreationTokens,
		usage.ClaudeCacheCreation5mTokens,
		usage.ClaudeCacheCreation1hTokens,
	)
	cacheCreationTokens := usage.PromptTokensDetails.CacheCreationTokensTotal()
	inputTokens := usage.PromptTokens
	if usage.UsageSemantic != dto.BillingUsageSemanticAnthropic {
		// OpenAI-style prompt/input totals include cache reads and writes, while
		// Claude reports both separately from input_tokens.
		inputTokens = usage.PromptTokens - usage.PromptTokensDetails.CachedTokens - cacheCreationTokens
		if inputTokens < 0 {
			inputTokens = 0
		}
	}
	result := &dto.ClaudeUsage{
		InputTokens:              inputTokens,
		OutputTokens:             usage.CompletionTokens,
		CacheCreationInputTokens: cacheCreationTokens,
		CacheReadInputTokens:     usage.PromptTokensDetails.CachedTokens,
		BillingUsage:             billingUsage,
	}
	if cacheCreation5m > 0 || cacheCreation1h > 0 {
		result.CacheCreation = &dto.ClaudeCacheCreationUsage{
			Ephemeral5mInputTokens: cacheCreation5m,
			Ephemeral1hInputTokens: cacheCreation1h,
		}
	}
	return result
}
