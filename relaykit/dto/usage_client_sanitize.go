package dto

// SanitizeClientUsage returns the usage object to render to the caller with the two
// cache counters collapsed into one consistent number.
//
// Some upstreams report the prompt and its cache hit independently and disagree:
// a real case is prompt_tokens=7 with prompt_cache_hit_tokens=512, because the
// provider counts its own injected context as "cache read" but only the user's
// tokens as "input". Forwarding both numbers makes every client that renders
// cached_tokens next to prompt_tokens show "cache read > input" (a >100% hit
// ratio), which reads as a billing bug even when the invoice is right.
//
// The sanitized copy therefore:
//   - keeps prompt_tokens_details.cached_tokens (the value billing already uses),
//   - drops the redundant prompt_cache_hit_tokens when it disagrees,
//   - and never reports more cached tokens than prompt tokens.
//
// Billing is untouched: settlement keeps reading the ORIGINAL usage object.
func SanitizeClientUsage(usage Usage) Usage {
	client := usage
	cached := client.PromptTokensDetails.CachedTokens
	if cached == 0 && client.PromptCacheHitTokens > 0 {
		cached = client.PromptCacheHitTokens
		client.PromptTokensDetails.CachedTokens = cached
	}
	if client.PromptCacheHitTokens != cached {
		client.PromptCacheHitTokens = 0
	}
	if cached > client.PromptTokens {
		client.PromptTokensDetails.CachedTokens = client.PromptTokens
		client.PromptCacheHitTokens = 0
	}
	return client
}
