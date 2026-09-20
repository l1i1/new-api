package operation_setting

import "strings"

// AutomaticRetryKeywords holds case-insensitive substrings of an error message
// that force the retry loop to fail over to another channel. Operators use it to
// make a new upstream failure retryable without a code change; the default is
// empty, so existing retry rules are unchanged.
var AutomaticRetryKeywords = []string{}

// NeverRetryKeywords holds case-insensitive substrings of an upstream error
// message that mark the request itself as rejected, so failing over to another
// channel reproduces the same answer while the client waits out the retry
// budget. The default is the set of context-window rejections recorded on the
// live channels -- the upstreams word it differently, but all of them say the
// prompt is longer than the model accepts, and the limit belongs to the model
// rather than to the channel that served it.
//
// Parameter rejections that another channel may well accept (max_tokens outside
// one aggregator's range, an image part a text-only route refuses) are
// deliberately absent: those are what the force-retry rule for 400 is for. An
// operator who meets a new wording adds a line here instead of waiting for a
// release.
var NeverRetryKeywords = []string{
	"context length", // "maximum context length", "the model's context length", "context length exceeded"
	"context_length_exceeded",
	"prompt is too long",
	"input is too long",
	"token exceed the limit",
	"exceeds the maximum allowed input length",
	"exceeds the maximum length",
	"range of input length should be",
}

func AutomaticRetryKeywordsToString() string {
	return strings.Join(AutomaticRetryKeywords, "\n")
}

func AutomaticRetryKeywordsFromString(s string) {
	AutomaticRetryKeywords = parseKeywordLines(s)
}

// MatchesAutomaticRetryKeywords reports whether the error text contains any
// configured retry keyword. Matching is case-insensitive and an empty keyword
// list never matches.
func MatchesAutomaticRetryKeywords(text string) bool {
	return matchesKeyword(AutomaticRetryKeywords, text)
}

func NeverRetryKeywordsToString() string {
	return strings.Join(NeverRetryKeywords, "\n")
}

func NeverRetryKeywordsFromString(s string) {
	NeverRetryKeywords = parseKeywordLines(s)
}

// MatchesNeverRetryKeywords reports whether the error text contains any
// configured never-retry keyword. Matching is case-insensitive and an empty
// keyword list never matches, which is how an operator turns the rule off.
func MatchesNeverRetryKeywords(text string) bool {
	return matchesKeyword(NeverRetryKeywords, text)
}

// parseKeywordLines reads the textarea form: one keyword per line, trimmed and
// lowercased, blanks dropped.
func parseKeywordLines(s string) []string {
	keywords := []string{}
	for _, k := range strings.Split(s, "\n") {
		k = strings.ToLower(strings.TrimSpace(k))
		if k != "" {
			keywords = append(keywords, k)
		}
	}
	return keywords
}

func matchesKeyword(keywords []string, text string) bool {
	if len(keywords) == 0 || text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}
