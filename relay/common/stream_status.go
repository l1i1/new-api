package common

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type StreamEndReason string

const (
	StreamEndReasonNone        StreamEndReason = ""
	StreamEndReasonDone        StreamEndReason = "done"
	StreamEndReasonTimeout     StreamEndReason = "timeout"
	StreamEndReasonClientGone  StreamEndReason = "client_gone"
	StreamEndReasonScannerErr  StreamEndReason = "scanner_error"
	StreamEndReasonHandlerStop StreamEndReason = "handler_stop"
	StreamEndReasonEOF         StreamEndReason = "eof"
	StreamEndReasonPanic       StreamEndReason = "panic"
	StreamEndReasonPingFail    StreamEndReason = "ping_fail"
)

// ResponseOutcome is the protocol-level result of one response, independent of
// how the transport ended. Adaptors mark it from the events they already parse.
type ResponseOutcome string

const (
	ResponseOutcomeUnknown    ResponseOutcome = ""
	ResponseOutcomeCompleted  ResponseOutcome = "completed"
	ResponseOutcomeFailed     ResponseOutcome = "failed"
	ResponseOutcomeIncomplete ResponseOutcome = "incomplete"
	ResponseOutcomeCancelled  ResponseOutcome = "cancelled"
)

const maxStreamErrorEntries = 20

type StreamErrorEntry struct {
	Message   string
	Timestamp time.Time
}

type StreamStatus struct {
	EndReason StreamEndReason
	EndError  error
	endOnce   sync.Once

	mu         sync.Mutex
	Errors     []StreamErrorEntry
	ErrorCount int

	response         ResponseOutcome
	errorCode        string
	errorType        string
	errorStatus      int
	incompleteReason string
	expectsTerminal  bool

	// terminalFinishSeen records that the provider sent a terminal finish_reason
	// that means the generation itself completed, so the answer the caller is
	// holding is executable. It says nothing about the transport: a provider can
	// emit the terminal chunk and then hold the SSE connection open without
	// [DONE] (neurvibe ch48, 2026-10-29), which used to end as an idle timeout
	// 502 after the answer had already been generated.
	terminalFinishSeen bool
	terminalFinishAt   time.Time
	// closedAfterTerminal records that the relay ended such a stream itself
	// after the tail grace instead of waiting for the idle timeout, so the
	// production logs can count how often the tail path fires.
	closedAfterTerminal bool
}

// StreamOutcome holds classification facts only; upstream messages never
// enter it because they may contain credentials or request content.
type StreamOutcome struct {
	EndReason        StreamEndReason
	HasErrors        bool
	ExpectsTerminal  bool
	Response         ResponseOutcome
	ErrorCode        string
	ErrorType        string
	ErrorStatus      int
	IncompleteReason string
}

func NewStreamStatus() *StreamStatus {
	return &StreamStatus{}
}

func (s *StreamStatus) SetEndReason(reason StreamEndReason, err error) {
	if s == nil {
		return
	}
	s.endOnce.Do(func() {
		s.EndReason = reason
		s.EndError = err
	})
}

// PromoteEOFToDone converts a provisional EOF into a normal end after the
// stream workers have drained a terminal response chunk. Callers must invoke
// this only after all concurrent stream processing has stopped.
func (s *StreamStatus) PromoteEOFToDone() {
	if s == nil || s.EndReason != StreamEndReasonEOF {
		return
	}
	s.EndReason = StreamEndReasonDone
	s.EndError = nil
}

func (s *StreamStatus) RecordError(msg string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ErrorCount++
	if len(s.Errors) < maxStreamErrorEntries {
		s.Errors = append(s.Errors, StreamErrorEntry{
			Message:   msg,
			Timestamp: time.Now(),
		})
	}
}

// RequireTerminal declares that the protocol always ends with an explicit
// terminal event, so a stream that ends without one was cut short.
func (s *StreamStatus) RequireTerminal() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expectsTerminal = true
}

// MarkCompleted, MarkIncomplete and MarkCancelled keep the first terminal seen;
// MarkFailed always wins because an error after completion is still a failure.
func (s *StreamStatus) MarkCompleted() {
	s.markTerminal(ResponseOutcomeCompleted, "")
}

func (s *StreamStatus) MarkIncomplete(reason string) {
	s.markTerminal(ResponseOutcomeIncomplete, reason)
}

// MarkRecovered turns a failed stream into a delivered one after a successful
// continuation round. The first attempt's abnormal finish_reason is expected to
// have marked the response failed; settlement and health sampling must see the
// merged result as success, while the original error text stays out of the
// outcome snapshot.
func (s *StreamStatus) MarkRecovered() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.response = ResponseOutcomeCompleted
	s.errorCode = ""
	s.errorType = ""
	s.errorStatus = 0
	s.incompleteReason = ""
	s.Errors = nil
	s.ErrorCount = 0
	s.EndError = nil
}

func (s *StreamStatus) MarkCancelled() {
	s.markTerminal(ResponseOutcomeCancelled, "")
}

func (s *StreamStatus) markTerminal(outcome ResponseOutcome, incompleteReason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.response != ResponseOutcomeUnknown {
		return
	}
	s.response = outcome
	s.incompleteReason = incompleteReason
}

// MarkFailed records a protocol failure. Empty details never erase details
// recorded earlier, so a bare error envelope keeps the structured error.
func (s *StreamStatus) MarkFailed(code, errorType string, status int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.response = ResponseOutcomeFailed
	if code != "" {
		s.errorCode = code
	}
	if errorType != "" {
		s.errorType = errorType
	}
	if status != 0 {
		s.errorStatus = status
	}
}

func (s *StreamStatus) ResponseOutcome() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.response)
}

func (s *StreamStatus) ResponseFailed() bool {
	return s.ResponseOutcome() == string(ResponseOutcomeFailed)
}

func (s *StreamStatus) OutcomeSnapshot() StreamOutcome {
	if s == nil {
		return StreamOutcome{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return StreamOutcome{
		EndReason:        s.EndReason,
		HasErrors:        s.ErrorCount > 0,
		ExpectsTerminal:  s.expectsTerminal,
		Response:         s.response,
		ErrorCode:        s.errorCode,
		ErrorType:        s.errorType,
		ErrorStatus:      s.errorStatus,
		IncompleteReason: s.incompleteReason,
	}
}

func (s *StreamStatus) HasErrors() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ErrorCount > 0
}

func (s *StreamStatus) TotalErrorCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ErrorCount
}

// MarkTerminalFinishSeen records that the provider closed the generation with a
// finish_reason that means the answer is complete. The idle guard uses it to
// stop waiting for a transport that will not produce anything else.
//
// The timestamp is what the tail grace is measured against, not the last line
// read: a provider that keeps sending keep-alive comments after its terminal
// frame is exactly the shape this path exists for, and a line-triggered clock
// would let it sit until the full idle timeout.
func (s *StreamStatus) MarkTerminalFinishSeen() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminalFinishSeen {
		return
	}
	s.terminalFinishSeen = true
	s.terminalFinishAt = time.Now()
}

func (s *StreamStatus) TerminalFinishSeen() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminalFinishSeen
}

// TimeSinceTerminalFinish reports how long ago the terminal finish_reason
// arrived, or 0 when none arrived (or the clock is somehow in the future).
func (s *StreamStatus) TimeSinceTerminalFinish() time.Duration {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.terminalFinishSeen || s.terminalFinishAt.IsZero() {
		return 0
	}
	elapsed := time.Since(s.terminalFinishAt)
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

// MarkClosedAfterTerminal records that the relay closed a stream whose terminal
// finish_reason had already arrived, instead of failing it as an idle timeout.
func (s *StreamStatus) MarkClosedAfterTerminal() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closedAfterTerminal = true
}

func (s *StreamStatus) ClosedAfterTerminal() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closedAfterTerminal
}

func (s *StreamStatus) IsNormalEnd() bool {
	if s == nil {
		return true
	}
	return s.EndReason == StreamEndReasonDone ||
		s.EndReason == StreamEndReasonEOF ||
		s.EndReason == StreamEndReasonHandlerStop
}

func (s *StreamStatus) Summary() string {
	if s == nil {
		return "StreamStatus<nil>"
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, "reason=%s", s.EndReason)
	if s.EndError != nil {
		fmt.Fprintf(b, " end_error=%q", s.EndError.Error())
	}
	s.mu.Lock()
	if s.ErrorCount > 0 {
		fmt.Fprintf(b, " soft_errors=%d", s.ErrorCount)
	}
	if s.closedAfterTerminal {
		b.WriteString(" closed_after_terminal=true")
	}
	s.mu.Unlock()
	return b.String()
}
