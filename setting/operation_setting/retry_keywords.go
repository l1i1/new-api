package operation_setting

import "strings"

// AutomaticRetryKeywords holds case-insensitive substrings of an error message
// that force the retry loop to fail over to another channel. Operators use it to
// make a new upstream failure retryable without a code change; the default is
// empty, so existing retry rules are unchanged.
var AutomaticRetryKeywords = []string{}

func AutomaticRetryKeywordsToString() string {
	return strings.Join(AutomaticRetryKeywords, "\n")
}

func AutomaticRetryKeywordsFromString(s string) {
	AutomaticRetryKeywords = []string{}
	for _, k := range strings.Split(s, "\n") {
		k = strings.ToLower(strings.TrimSpace(k))
		if k != "" {
			AutomaticRetryKeywords = append(AutomaticRetryKeywords, k)
		}
	}
}

// MatchesAutomaticRetryKeywords reports whether the error text contains any
// configured retry keyword. Matching is case-insensitive and an empty keyword
// list never matches.
func MatchesAutomaticRetryKeywords(text string) bool {
	if len(AutomaticRetryKeywords) == 0 || text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, keyword := range AutomaticRetryKeywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}
