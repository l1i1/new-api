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

// MultiKeyCredentialRetryKeywords holds case-insensitive substrings of an
// upstream error message that mark one credential as unusable while the
// channel itself may still work, so a multi-key channel retries itself with
// another key instead of being excluded. This is the text form of
// MultiKeyCredentialRetryStatusCodeRanges and matches the live out-of-balance
// wordings: an upstream 400 "insufficient credits" is key-scoped (another key
// of the same channel has its own balance), and the Ark wording puts the same
// verdict behind "balance insufficient". A channel-shaped text ("endpoint not
// supported") still belongs to the channel-failover keywords.
//
// The request-level rejections are deliberately absent: an upstream that words
// a request rejection with these words (none observed on the live channels)
// would make every key repeat it, but the rotation is budget-free and stops at
// the first untried-key exhaustion, so the cost is one extra attempt.
var MultiKeyCredentialRetryKeywords = []string{
	"insufficient credits",
	"insufficient balance",
	"balance insufficient",
}

func MultiKeyCredentialRetryKeywordsToString() string {
	return strings.Join(MultiKeyCredentialRetryKeywords, "\n")
}

func MultiKeyCredentialRetryKeywordsFromString(s string) {
	MultiKeyCredentialRetryKeywords = parseKeywordLines(s)
}

// MatchesMultiKeyCredentialRetryKeywords reports whether the error text marks
// the credential rather than the channel. Case-insensitive; an empty keyword
// list never matches, which disables the text rule and leaves the status-code
// list in charge.
func MatchesMultiKeyCredentialRetryKeywords(text string) bool {
	return matchesKeyword(MultiKeyCredentialRetryKeywords, text)
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
