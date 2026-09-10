package operation_setting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAutomaticRetryKeywordsFromString(t *testing.T) {
	original := AutomaticRetryKeywords
	t.Cleanup(func() { AutomaticRetryKeywords = original })

	AutomaticRetryKeywordsFromString(" Endpoint Not Supported \r\n\n  model not found  \n")
	require.Equal(t, []string{"endpoint not supported", "model not found"}, AutomaticRetryKeywords)
	require.Equal(t,
		"endpoint not supported\nmodel not found",
		AutomaticRetryKeywordsToString(),
	)

	AutomaticRetryKeywordsFromString("")
	require.Empty(t, AutomaticRetryKeywords)
	require.Empty(t, AutomaticRetryKeywordsToString())
}

func TestMatchesAutomaticRetryKeywords(t *testing.T) {
	original := AutomaticRetryKeywords
	t.Cleanup(func() { AutomaticRetryKeywords = original })

	AutomaticRetryKeywordsFromString("")
	require.False(t, MatchesAutomaticRetryKeywords("ollama channel: /v1/responses endpoint not supported"))

	AutomaticRetryKeywordsFromString("endpoint not supported")
	require.True(t, MatchesAutomaticRetryKeywords("ollama channel: /v1/responses ENDPOINT NOT SUPPORTED"))
	require.False(t, MatchesAutomaticRetryKeywords("upstream gateway failure"))
	require.False(t, MatchesAutomaticRetryKeywords(""))
}
