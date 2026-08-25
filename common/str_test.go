package common

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDebugLogPreviewMasksCredentialsAndBoundsPayloads(t *testing.T) {
	originalDebug := DebugEnabled
	DebugEnabled = true
	t.Cleanup(func() { DebugEnabled = originalDebug })

	secret := "sk-debug-payload-secret-123456"
	input := `{"messages":[{"role":"user","content":"` + secret + `"}]}` + strings.Repeat("x", LocalLogContentLimit)

	preview := DebugLogPreview(input)

	require.NotContains(t, preview, secret)
	require.Contains(t, preview, "sk-***")
	require.Contains(t, preview, "[truncated")
	require.LessOrEqual(t, len(preview), LocalLogContentLimit+80)
}
