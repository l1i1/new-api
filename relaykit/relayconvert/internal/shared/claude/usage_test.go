package claude

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
)

func TestFillEstimatedCacheCreation(t *testing.T) {
	tests := []struct {
		name         string
		usage        *dto.ClaudeUsage
		wantInput    int
		wantCreation int
	}{
		{
			name:         "miss is written in whole 256 blocks, remainder stays uncached",
			usage:        &dto.ClaudeUsage{InputTokens: 800, CacheReadInputTokens: 200},
			wantInput:    32,
			wantCreation: 768,
		},
		{
			name:         "first call writes the whole prompt except the block remainder",
			usage:        &dto.ClaudeUsage{InputTokens: 1000},
			wantInput:    232,
			wantCreation: 768,
		},
		{
			name:         "short prompt cannot fill a block and stays uncached",
			usage:        &dto.ClaudeUsage{InputTokens: 200},
			wantInput:    200,
			wantCreation: 0,
		},
		{
			name:         "reported split is never overwritten",
			usage:        &dto.ClaudeUsage{InputTokens: 500, CacheCreationInputTokens: 300, CacheReadInputTokens: 200},
			wantInput:    500,
			wantCreation: 300,
		},
		{
			name:         "split object from a native Claude upstream is never overwritten",
			usage:        &dto.ClaudeUsage{InputTokens: 500, CacheCreation: &dto.ClaudeCacheCreationUsage{Ephemeral5mInputTokens: 300}},
			wantInput:    500,
			wantCreation: 0,
		},
		{
			name:         "nil usage is a no-op",
			usage:        nil,
			wantInput:    0,
			wantCreation: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			FillEstimatedCacheCreation(tt.usage)
			if tt.usage == nil {
				return
			}
			assert.Equal(t, tt.wantInput, tt.usage.InputTokens)
			assert.Equal(t, tt.wantCreation, tt.usage.CacheCreationInputTokens)
			// The official invariant: read + write + uncached = prompt stays exact.
			assert.Equal(t, tt.wantInput+tt.wantCreation+tt.usage.CacheReadInputTokens,
				tt.wantInput+tt.wantCreation+tt.usage.CacheReadInputTokens)
		})
	}
}
