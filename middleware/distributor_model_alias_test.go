package middleware

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenModelLimitAllowsCompactAlias(t *testing.T) {
	assert.True(t, tokenModelLimitAllowsCandidate(
		map[string]bool{"gpt-5.5": true},
		"gpt-5.5-openai-compact",
	))
	assert.True(t, tokenModelLimitAllowsCandidate(
		map[string]bool{"gpt-5.6-sol-openai-compact": true},
		"gpt-5.6-sol-openai-compact",
	))
	assert.False(t, tokenModelLimitAllowsCandidate(
		map[string]bool{"gpt-5.4": true},
		"gpt-5.5-openai-compact",
	))
	assert.False(t, tokenModelLimitAllowsCandidate(
		map[string]bool{"gpt-5.5": true},
		"gpt-5.6-sol",
	))

	baseOnlyLimit := map[string]bool{"gpt-5.5": true}
	assert.True(t, tokenModelLimitAllowsResolved(
		baseOnlyLimit,
		"gpt-5.5-openai-compact",
		"gpt-5.5",
	))
	assert.False(t, tokenModelLimitAllowsResolved(
		baseOnlyLimit,
		"gpt-5.5-openai-compact",
		"",
	))
}
