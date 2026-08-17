package kitutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMaskSensitiveInfoMasksCredentialValues(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		secret   string
		expected string
	}{
		{
			name:     "provider API key error",
			input:    "Incorrect API key provided: sk-live-credential-value",
			secret:   "sk-live-credential-value",
			expected: "Incorrect API key provided: ***",
		},
		{
			name:     "bearer authorization header",
			input:    "Authorization: Bearer bearer-credential-value was rejected",
			secret:   "bearer-credential-value",
			expected: "Authorization: Bearer *** was rejected",
		},
		{
			name:     "JSON API key field",
			input:    `{"api_key":"credential-value"}`,
			secret:   "credential-value",
			expected: `{"api_key":"***"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			masked := MaskSensitiveInfo(tt.input)
			assert.NotContains(t, masked, tt.secret)
			assert.Equal(t, tt.expected, masked)
		})
	}
}
