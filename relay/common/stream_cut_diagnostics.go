package common

// StreamCutDiagnostics explains, on the gateway side, why a stream ended
// without a usable terminal result and what the repair path had to work with.
//
// It is attached to the failed request's type=5 row (admin_info.stream_cut) so
// an unrepaired cut can be explained from /api/log/ alone. The same numbers used
// to live only in a container log line ("abnormal finish observed: ..."), which
// is ephemeral: on 2026-10-29 a 986s cut on an upstream with a ~900s stream
// limit could not be explained after the fact because request capture had
// quietly exhausted its budget and the node's log line was long gone.
//
// The keys are the operator-facing contract; do not rename them.
type StreamCutDiagnostics struct {
	// Shape of the cut.
	FinishReason        string `json:"finish_reason,omitempty"`
	MissingFinish       bool   `json:"missing_finish,omitempty"`
	ReasoningTruncated  bool   `json:"reasoning_truncated,omitempty"`
	VisibleContentChars int    `json:"visible_content_chars"`
	ReasoningChars      int    `json:"reasoning_chars"`
	ToolCalls           int    `json:"tool_calls,omitempty"`
	ChoiceCount         int    `json:"choice_count"`

	// The caller's output budget versus what the provider already produced. A
	// non-positive BudgetLeft by itself disables every continuation mode, which
	// is the difference between "repair was impossible by design" and "repair
	// was possible and still failed".
	ClientMaxTokens  int `json:"client_max_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens"`
	BudgetLeft       int `json:"budget_left"`

	// Why a repair could or could not be configured for this request.
	ContinuationConfigured bool `json:"continuation_configured"`
	AbnormalConfigured     bool `json:"abnormal_configured,omitempty"`
	MissingConfigured      bool `json:"missing_configured,omitempty"`
	ThinkingToContent      bool `json:"thinking_to_content,omitempty"`
	ReasoningSuppressed    bool `json:"reasoning_suppressed,omitempty"`

	// Outcome of the repair attempt. The zero values mean no repair ran.
	RepairAttempted bool   `json:"repair_attempted,omitempty"`
	RepairMode      string `json:"repair_mode,omitempty"`
	RepairRounds    int    `json:"repair_rounds,omitempty"`
	// RepairFailedReason says why a repair that did run did not deliver a usable
	// answer, e.g. "round_incomplete" or "round_abnormal_finish".
	RepairFailedReason string `json:"repair_failed_reason,omitempty"`
}

// MarkRepairAttempted records that a repair mode started, so a row that ends
// without a recovered finish shows the attempt rather than an idle gateway.
func (d *StreamCutDiagnostics) MarkRepairAttempted(mode string) {
	if d == nil {
		return
	}
	d.RepairAttempted = true
	d.RepairMode = mode
}

// MarkRepairOutcome records how a started repair ended. reason is ignored when
// the repair delivered an answer, because that request never writes an error row.
func (d *StreamCutDiagnostics) MarkRepairOutcome(rounds int, reason string) {
	if d == nil {
		return
	}
	d.RepairRounds = rounds
	d.RepairFailedReason = reason
}
