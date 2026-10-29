package dto

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSanitizeClientUsageCollapsesDisagreeingCacheCounters pins the 2026-10-29
// anomaly: an upstream reported prompt_tokens=7 together with
// prompt_cache_hit_tokens=512, so the caller rendered "cache read > input".
func TestSanitizeClientUsageCollapsesDisagreeingCacheCounters(t *testing.T) {
	usage := Usage{PromptTokens: 7, CompletionTokens: 16, TotalTokens: 23}
	usage.PromptCacheHitTokens = 512
	usage.PromptTokensDetails.CachedTokens = 6

	client := SanitizeClientUsage(usage)

	assert.Equal(t, 6, client.PromptTokensDetails.CachedTokens, "the billed cache count wins")
	assert.Zero(t, client.PromptCacheHitTokens, "the disagreeing duplicate must not be rendered")
	assert.Equal(t, 7, client.PromptTokens)
	assert.Equal(t, 16, client.CompletionTokens)
	// Billing still sees the original numbers.
	assert.Equal(t, 512, usage.PromptCacheHitTokens)
}

func TestSanitizeClientUsageClampsCacheToPrompt(t *testing.T) {
	usage := Usage{PromptTokens: 100, CompletionTokens: 5, TotalTokens: 105}
	usage.PromptTokensDetails.CachedTokens = 300

	client := SanitizeClientUsage(usage)

	assert.Equal(t, 100, client.PromptTokensDetails.CachedTokens)
	assert.LessOrEqual(t, client.PromptTokensDetails.CachedTokens, client.PromptTokens)
}

func TestSanitizeClientUsageFillsCacheFromRawField(t *testing.T) {
	usage := Usage{PromptTokens: 900, CompletionTokens: 5, TotalTokens: 905}
	usage.PromptCacheHitTokens = 400

	client := SanitizeClientUsage(usage)

	assert.Equal(t, 400, client.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 400, client.PromptCacheHitTokens)
}
