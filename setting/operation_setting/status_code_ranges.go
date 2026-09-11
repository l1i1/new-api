package operation_setting

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/types"
)

type StatusCodeRange struct {
	Start int
	End   int
}

var AutomaticDisableStatusCodeRanges = []StatusCodeRange{{Start: 401, End: 401}}

// Default behavior matches legacy hardcoded retry rules in controller/relay.go shouldRetry:
// retry for 1xx, 3xx, 4xx(except 400/408), 5xx(except 504/524), and no retry for 2xx.
var AutomaticRetryStatusCodeRanges = []StatusCodeRange{
	{Start: 100, End: 199},
	{Start: 300, End: 399},
	{Start: 401, End: 407},
	{Start: 409, End: 499},
	{Start: 500, End: 503},
	{Start: 505, End: 523},
	{Start: 525, End: 599},
}

// ForceRetryStatusCodeRanges holds status codes that are always retried, even
// when they are absent from AutomaticRetryStatusCodeRanges. The default {400}
// preserves the legacy rule that an upstream 400 means "this channel cannot
// serve the request", so the retry loop excludes the channel and tries another.
// Callers decide whether the rule applies to local (platform) errors too.
var ForceRetryStatusCodeRanges = []StatusCodeRange{{Start: 400, End: 400}}

// NeverRetryStatusCodeRanges holds status codes that are never retried, even
// when they match AutomaticRetryStatusCodeRanges or ForceRetryStatusCodeRanges.
// The default {504,524} preserves the legacy "timeout is not retryable" rule.
var NeverRetryStatusCodeRanges = []StatusCodeRange{
	{Start: 504, End: 504},
	{Start: 524, End: 524},
}

var alwaysSkipRetryCodes = map[types.ErrorCode]struct{}{
	types.ErrorCodeBadResponseBody: {},
}

func AutomaticDisableStatusCodesToString() string {
	return statusCodeRangesToString(AutomaticDisableStatusCodeRanges)
}

func AutomaticDisableStatusCodesFromString(s string) error {
	ranges, err := ParseHTTPStatusCodeRanges(s)
	if err != nil {
		return err
	}
	AutomaticDisableStatusCodeRanges = ranges
	return nil
}

func ShouldDisableByStatusCode(code int) bool {
	return shouldMatchStatusCodeRanges(AutomaticDisableStatusCodeRanges, code)
}

func AutomaticRetryStatusCodesToString() string {
	return statusCodeRangesToString(AutomaticRetryStatusCodeRanges)
}

func AutomaticRetryStatusCodesFromString(s string) error {
	ranges, err := ParseHTTPStatusCodeRanges(s)
	if err != nil {
		return err
	}
	AutomaticRetryStatusCodeRanges = ranges
	return nil
}

func ForceRetryStatusCodesToString() string {
	return statusCodeRangesToString(ForceRetryStatusCodeRanges)
}

func ForceRetryStatusCodesFromString(s string) error {
	ranges, err := ParseHTTPStatusCodeRanges(s)
	if err != nil {
		return err
	}
	ForceRetryStatusCodeRanges = ranges
	return nil
}

func NeverRetryStatusCodesToString() string {
	return statusCodeRangesToString(NeverRetryStatusCodeRanges)
}

func NeverRetryStatusCodesFromString(s string) error {
	ranges, err := ParseHTTPStatusCodeRanges(s)
	if err != nil {
		return err
	}
	NeverRetryStatusCodeRanges = ranges
	return nil
}

func IsNeverRetryStatusCode(code int) bool {
	return shouldMatchStatusCodeRanges(NeverRetryStatusCodeRanges, code)
}

// MultiKeyCredentialRetryStatusCodeRanges holds the upstream status codes that
// identify a single credential as unusable rather than the whole channel, so a
// multi-key channel retries itself with another key before the channel is
// excluded. Which codes qualify depends on how the channel maps keys to
// upstream accounts: a channel with one key per account (an official relay)
// needs rotation on an out-of-balance 402, while a pool whose keys share one
// account gains nothing but one extra attempt. Operators configure this in
// Routing Reliability.
//
// 402 is included by default because that is the credential-scoped failure that
// silently broke official-pinned traffic: the channel stayed "healthy", the
// affinity hash kept selecting the drained key, and the request died with
// get_channel_failed once the pin left no other candidate. 401 is deliberately
// absent: it usually points at the channel's auth or base-url configuration,
// which another key cannot fix (an operator who disagrees can add it).
var MultiKeyCredentialRetryStatusCodeRanges = []StatusCodeRange{
	{Start: 402, End: 402},
	{Start: 403, End: 403},
	{Start: 429, End: 429},
}

func MultiKeyCredentialRetryStatusCodesToString() string {
	return statusCodeRangesToString(MultiKeyCredentialRetryStatusCodeRanges)
}

func MultiKeyCredentialRetryStatusCodesFromString(s string) error {
	ranges, err := ParseHTTPStatusCodeRanges(s)
	if err != nil {
		return err
	}
	MultiKeyCredentialRetryStatusCodeRanges = ranges
	return nil
}

// ShouldRotateMultiKeyCredential reports whether the status code means "try
// another key of this channel". Callers own the surrounding gates: never-retry,
// local validation errors and non-multi-key channels are out of scope here.
func ShouldRotateMultiKeyCredential(code int) bool {
	return shouldMatchStatusCodeRanges(MultiKeyCredentialRetryStatusCodeRanges, code)
}

// IsForceRetryStatusCode reports whether the code is configured to always
// retry. Never-retry wins, so an overlap resolves to no retry. Callers decide
// whether the rule applies to local (platform) errors; local validation errors
// must stay non-retryable even for a force-retry code such as 400.
func IsForceRetryStatusCode(code int) bool {
	if IsNeverRetryStatusCode(code) {
		return false
	}
	return shouldMatchStatusCodeRanges(ForceRetryStatusCodeRanges, code)
}

func IsAlwaysSkipRetryCode(errorCode types.ErrorCode) bool {
	_, exists := alwaysSkipRetryCodes[errorCode]
	return exists
}

// ShouldRetryByStatusCode applies the automatic retry ranges. Force-retry and
// never-retry codes are separate rules; callers that want the full decision use
// IsForceRetryStatusCode in addition to this function.
func ShouldRetryByStatusCode(code int) bool {
	if IsNeverRetryStatusCode(code) {
		return false
	}
	return shouldMatchStatusCodeRanges(AutomaticRetryStatusCodeRanges, code)
}

func statusCodeRangesToString(ranges []StatusCodeRange) string {
	if len(ranges) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		if r.Start == r.End {
			parts = append(parts, strconv.Itoa(r.Start))
			continue
		}
		parts = append(parts, fmt.Sprintf("%d-%d", r.Start, r.End))
	}
	return strings.Join(parts, ",")
}

func shouldMatchStatusCodeRanges(ranges []StatusCodeRange, code int) bool {
	if code < 100 || code > 599 {
		return false
	}
	for _, r := range ranges {
		if code < r.Start {
			return false
		}
		if code <= r.End {
			return true
		}
	}
	return false
}

func ParseHTTPStatusCodeRanges(input string) ([]StatusCodeRange, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}

	input = strings.NewReplacer("，", ",").Replace(input)
	segments := strings.Split(input, ",")

	var ranges []StatusCodeRange
	var invalid []string

	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		r, err := parseHTTPStatusCodeToken(seg)
		if err != nil {
			invalid = append(invalid, seg)
			continue
		}
		ranges = append(ranges, r)
	}

	if len(invalid) > 0 {
		return nil, fmt.Errorf("invalid http status code rules: %s", strings.Join(invalid, ", "))
	}
	if len(ranges) == 0 {
		return nil, nil
	}

	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].Start == ranges[j].Start {
			return ranges[i].End < ranges[j].End
		}
		return ranges[i].Start < ranges[j].Start
	})

	merged := []StatusCodeRange{ranges[0]}
	for _, r := range ranges[1:] {
		last := &merged[len(merged)-1]
		if r.Start <= last.End+1 {
			if r.End > last.End {
				last.End = r.End
			}
			continue
		}
		merged = append(merged, r)
	}

	return merged, nil
}

func parseHTTPStatusCodeToken(token string) (StatusCodeRange, error) {
	token = strings.TrimSpace(token)
	token = strings.ReplaceAll(token, " ", "")
	if token == "" {
		return StatusCodeRange{}, fmt.Errorf("empty token")
	}

	if strings.Contains(token, "-") {
		parts := strings.Split(token, "-")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return StatusCodeRange{}, fmt.Errorf("invalid range token: %s", token)
		}
		start, err := strconv.Atoi(parts[0])
		if err != nil {
			return StatusCodeRange{}, fmt.Errorf("invalid range start: %s", token)
		}
		end, err := strconv.Atoi(parts[1])
		if err != nil {
			return StatusCodeRange{}, fmt.Errorf("invalid range end: %s", token)
		}
		if start > end {
			return StatusCodeRange{}, fmt.Errorf("range start > end: %s", token)
		}
		if start < 100 || end > 599 {
			return StatusCodeRange{}, fmt.Errorf("range out of bounds: %s", token)
		}
		return StatusCodeRange{Start: start, End: end}, nil
	}

	code, err := strconv.Atoi(token)
	if err != nil {
		return StatusCodeRange{}, fmt.Errorf("invalid status code: %s", token)
	}
	if code < 100 || code > 599 {
		return StatusCodeRange{}, fmt.Errorf("status code out of bounds: %s", token)
	}
	return StatusCodeRange{Start: code, End: code}, nil
}
