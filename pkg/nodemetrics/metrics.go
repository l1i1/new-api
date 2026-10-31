// Package nodemetrics keeps a per-process rolling view of the relay request,
// token and payload-byte counters that are reported in the system-instance
// heartbeat (info.extra.traffic).
//
// The hot path is cheap: one short-lived mutex acquisition per record and no
// allocations. Counters are kept in 60 one-second buckets so a snapshot can sum
// the most recent minute; cumulative counters since process start are kept
// alongside the ring and are never affected by the window.
//
// The package is a leaf: it only imports the standard library.
package nodemetrics

import (
	"sync"
	"time"
)

// WindowSeconds is the rolling window (in seconds) the rate fields are summed
// over. It is also the size of the bucket ring.
const WindowSeconds = 60

// Traffic is the wire shape of info.extra.traffic. Field names and semantics
// are fixed by the observability contract; do not rename them.
type Traffic struct {
	// WindowSeconds is the rolling window the rate fields cover.
	WindowSeconds int `json:"window_seconds"`
	// RPM counts relay requests that finished within the window.
	RPM int64 `json:"rpm"`
	// TPM is PromptTPM + CompletionTPM.
	TPM                   int64 `json:"tpm"`
	PromptTPM             int64 `json:"prompt_tpm"`
	CompletionTPM         int64 `json:"completion_tpm"`
	RequestBytes          int64 `json:"request_bytes"`
	ResponseBytes         int64 `json:"response_bytes"`
	RequestsTotal         int64 `json:"requests_total"`
	PromptTokensTotal     int64 `json:"prompt_tokens_total"`
	CompletionTokensTotal int64 `json:"completion_tokens_total"`
	TokensTotal           int64 `json:"tokens_total"`
	RequestBytesTotal     int64 `json:"request_bytes_total"`
	ResponseBytesTotal    int64 `json:"response_bytes_total"`
}

// bucket is one second of traffic. second identifies the wall-clock second the
// bucket currently holds (0 means empty); a bucket whose stored second differs
// from the second being recorded is reset before adding, which keeps the ring
// correct across clock jumps.
type bucket struct {
	second           int64
	requests         int64
	promptTokens     int64
	completionTokens int64
	requestBytes     int64
	responseBytes    int64
}

// metrics is the counter ring plus the cumulative totals.
type metrics struct {
	mu      sync.Mutex
	buckets [WindowSeconds]bucket

	requestsTotal         int64
	promptTokensTotal     int64
	completionTokensTotal int64
	requestBytesTotal     int64
	responseBytesTotal    int64
}

func newMetrics() *metrics {
	return &metrics{}
}

// defaultMetrics backs the package-level record/snapshot helpers.
var defaultMetrics = newMetrics()

// RecordRequest records one finished relay request and its payload sizes.
// requestBytes is the client request body size (0 when unknown); responseBytes
// is the number of bytes written to the client (0 when unknown). Negative
// sizes are treated as 0, but the request itself is always counted.
func RecordRequest(requestBytes, responseBytes int64) {
	defaultMetrics.recordRequestAt(time.Now(), requestBytes, responseBytes)
}

// RecordTokens records settled prompt/completion tokens (billing view).
// Negative values are treated as 0.
func RecordTokens(promptTokens, completionTokens int) {
	defaultMetrics.recordTokensAt(time.Now(), int64(promptTokens), int64(completionTokens))
}

// Snapshot returns the rolling window and cumulative totals as of now.
func Snapshot(now time.Time) Traffic {
	return defaultMetrics.snapshot(now)
}

func (m *metrics) recordRequestAt(now time.Time, requestBytes, responseBytes int64) {
	if requestBytes < 0 {
		requestBytes = 0
	}
	if responseBytes < 0 {
		responseBytes = 0
	}
	second := now.Unix()
	m.mu.Lock()
	b := m.bucketForSecondLocked(second)
	b.requests++
	b.requestBytes += requestBytes
	b.responseBytes += responseBytes
	m.requestsTotal++
	m.requestBytesTotal += requestBytes
	m.responseBytesTotal += responseBytes
	m.mu.Unlock()
}

func (m *metrics) recordTokensAt(now time.Time, promptTokens, completionTokens int64) {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	second := now.Unix()
	m.mu.Lock()
	b := m.bucketForSecondLocked(second)
	b.promptTokens += promptTokens
	b.completionTokens += completionTokens
	m.promptTokensTotal += promptTokens
	m.completionTokensTotal += completionTokens
	m.mu.Unlock()
}

// bucketForSecondLocked returns the bucket for second, resetting it first when
// it still holds another second's counters (ring rollover or clock jump).
// The caller must hold m.mu.
func (m *metrics) bucketForSecondLocked(second int64) *bucket {
	index := int(((second % WindowSeconds) + WindowSeconds) % WindowSeconds)
	b := &m.buckets[index]
	if b.second != second {
		*b = bucket{second: second}
	}
	return b
}

func (m *metrics) snapshot(now time.Time) Traffic {
	nowSecond := now.Unix()
	fromSecond := nowSecond - WindowSeconds + 1
	traffic := Traffic{WindowSeconds: WindowSeconds}
	m.mu.Lock()
	for i := range m.buckets {
		b := &m.buckets[i]
		if b.second < fromSecond || b.second > nowSecond {
			continue
		}
		traffic.RPM += b.requests
		traffic.PromptTPM += b.promptTokens
		traffic.CompletionTPM += b.completionTokens
		traffic.RequestBytes += b.requestBytes
		traffic.ResponseBytes += b.responseBytes
	}
	traffic.RequestsTotal = m.requestsTotal
	traffic.PromptTokensTotal = m.promptTokensTotal
	traffic.CompletionTokensTotal = m.completionTokensTotal
	traffic.RequestBytesTotal = m.requestBytesTotal
	traffic.ResponseBytesTotal = m.responseBytesTotal
	m.mu.Unlock()
	traffic.TPM = traffic.PromptTPM + traffic.CompletionTPM
	traffic.TokensTotal = traffic.PromptTokensTotal + traffic.CompletionTokensTotal
	return traffic
}
