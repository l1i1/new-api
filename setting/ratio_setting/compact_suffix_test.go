package ratio_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCompactBaseModelName(t *testing.T) {
	tests := []struct {
		name      string
		modelName string
		wantBase  string
		wantOK    bool
	}{
		{name: "versioned model", modelName: "gpt-5.5-openai-compact", wantBase: "gpt-5.5", wantOK: true},
		{name: "hyphenated model", modelName: "gpt-5.6-sol-openai-compact", wantBase: "gpt-5.6-sol", wantOK: true},
		{name: "suffix only", modelName: CompactModelSuffix, wantBase: "", wantOK: false},
		{name: "different suffix", modelName: "gpt-5.5-compact", wantBase: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseModel, ok := CompactBaseModelName(tt.modelName)
			assert.Equal(t, tt.wantBase, baseModel)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}
