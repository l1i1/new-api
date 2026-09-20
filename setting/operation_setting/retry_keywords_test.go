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

func TestNeverRetryKeywordsDefaultCoversLiveContextOverflowTexts(t *testing.T) {
	// The shipped default is the list operators start from, so it must keep
	// matching the wordings the live channels return.
	for _, message := range []string{
		"The prompt is too long: 1270711, model maximum context length: 1048571",
		"This model's maximum context length is 1048576 tokens.",
		"Input length 1270721 exceeds the maximum allowed input length of 1048576",
		"Input length 1066563 exceeds the maximum length 1048566",
		"<400> InvalidParameter: Range of input length should be [1, 1048576]",
		"Input token exceed the limit",
	} {
		require.True(t, MatchesNeverRetryKeywords(message), message)
	}
	require.False(t, MatchesNeverRetryKeywords("upstream rejected this channel request"))
	require.False(t, MatchesNeverRetryKeywords(""))
}

func TestNeverRetryKeywordsFromString(t *testing.T) {
	original := NeverRetryKeywords
	t.Cleanup(func() { NeverRetryKeywords = original })

	NeverRetryKeywordsFromString(" Prompt Is Too Long \r\n\n  maximum context length  \n")
	require.Equal(t, []string{"prompt is too long", "maximum context length"}, NeverRetryKeywords)
	require.Equal(t,
		"prompt is too long\nmaximum context length",
		NeverRetryKeywordsToString(),
	)

	// An operator can extend the shipped default with a new upstream wording.
	NeverRetryKeywordsFromString(NeverRetryKeywordsToString() + "\ncontext window overflow")
	require.True(t, MatchesNeverRetryKeywords("error: context window overflow (request 12)"))
	require.True(t, MatchesNeverRetryKeywords("The prompt is too long: 12, model maximum context length: 8"))

	// Clearing the list turns the rule off entirely.
	NeverRetryKeywordsFromString("")
	require.Empty(t, NeverRetryKeywords)
	require.False(t, MatchesNeverRetryKeywords("The prompt is too long: 12, model maximum context length: 8"))
}
