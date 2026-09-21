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

// AutomaticRetryStatusCodeRanges is the failover list: the upstream answered
// with one of these codes, so the retry loop excludes the channel and tries
// another. It merges what used to be two fields -- the automatic list and the
// "force retry" list -- because both meant "this channel could not serve the
// request, try another", and the split only made the page harder to read: the
// defaults {100-199, 300-407, 409-503, 505-523, 525-599} cover the legacy
// automatic ranges plus 400, which used to arrive through the force-retry
// default.
//
// The list is deliberately not gated on the error type. A local (platform)
// error carrying one of these codes is often a channel capability gap -- the
// live adaptors report "unsupported ollama response format type", "ollama
// channel: image endpoint not supported" and DFlash's "does not support
// return_logprob yet" as local 400s, and those requests are served by failing
// over to a channel that does support them. Request-shaped local errors are
// kept out by their skip-retry marker, which is the explicit signal, rather
// than by a status-code rule that cannot tell the two apart.
var AutomaticRetryStatusCodeRanges = []StatusCodeRange{
	{Start: 100, End: 199},
	{Start: 300, End: 407},
	{Start: 409, End: 503},
	{Start: 505, End: 523},
	{Start: 525, End: 599},
}

// ForceRetryStatusCodeRanges is a deprecated alias kept for installs whose
// options row still holds a value: those codes are read, honored and shown as
// part of the failover list (see FailoverStatusCodesToString), but new saves
// write the failover list alone. The default is empty.
var ForceRetryStatusCodeRanges = []StatusCodeRange{}

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

// ForceRetryStatusCodesToString reports the deprecated force-retry list. The
// field was merged into the failover list, so a fresh install reports empty;
// an install whose persisted value has not been migrated yet reports it, and
// ShouldRetryByStatusCode honors it either way.
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

// MergeRetryStatusCodes returns the union of two status-code lists in the same
// normalized form the option fields use. Either list may be empty or blank.
func MergeRetryStatusCodes(lists ...string) (string, error) {
	nonEmpty := make([]string, 0, len(lists))
	for _, list := range lists {
		if strings.TrimSpace(list) != "" {
			nonEmpty = append(nonEmpty, list)
		}
	}
	if len(nonEmpty) == 0 {
		return "", nil
	}
	ranges, err := ParseHTTPStatusCodeRanges(strings.Join(nonEmpty, ","))
	if err != nil {
		return "", err
	}
	return statusCodeRangesToString(ranges), nil
}

// FoldLegacyForceRetryStatusCodes merges the deprecated force-retry list into
// the failover list in memory. The persisted row is removed by
// model.MigrateRetiredFrontendOptions, which only runs on the master node; this
// covers non-master nodes and the window before that migration runs.
// ShouldRetryByStatusCode already unions both lists, so the merge is
// behavior-preserving on its own.
func FoldLegacyForceRetryStatusCodes() {
	if len(ForceRetryStatusCodeRanges) == 0 {
		return
	}
	merged, err := MergeRetryStatusCodes(AutomaticRetryStatusCodesToString(), ForceRetryStatusCodesToString())
	if err != nil {
		return
	}
	if err := AutomaticRetryStatusCodesFromString(merged); err != nil {
		return
	}
	ForceRetryStatusCodeRanges = nil
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

func IsAlwaysSkipRetryCode(errorCode types.ErrorCode) bool {
	_, exists := alwaysSkipRetryCodes[errorCode]
	return exists
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

// IsForceRetryStatusCode reports whether the code is on the failover list.
// Deprecated name: the force-retry field was merged into the failover list, so
// this is now the same decision as ShouldRetryByStatusCode.
func IsForceRetryStatusCode(code int) bool {
	return ShouldRetryByStatusCode(code)
}

// ShouldRetryByStatusCode applies the failover list: the upstream answered with
// one of these codes, so the retry loop excludes the channel and tries another.
// Never-retry wins, so an overlap resolves to no retry.
func ShouldRetryByStatusCode(code int) bool {
	if IsNeverRetryStatusCode(code) {
		return false
	}
	if shouldMatchStatusCodeRanges(AutomaticRetryStatusCodeRanges, code) {
		return true
	}
	// A legacy force-retry list that has not been folded in yet (options not
	// loaded, or a direct write) still counts.
	return shouldMatchStatusCodeRanges(ForceRetryStatusCodeRanges, code)
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
