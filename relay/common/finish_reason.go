package common

import "strings"

// IsAbnormalFinishReason reports whether a provider finish_reason means the
// request did NOT complete normally. Anything outside the documented terminal
// set is treated as a failure: aggregators report internal failures as a
// finish reason (e.g. "error"), and the official Moonshot endpoint reports
// capacity failures the same way ("engine_overloaded",
// "insufficient_system_resource"). None of them delivered a finished answer, so
// the request must not be recorded or billed as a success.
func IsAbnormalFinishReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "",
		"stop",
		"length",
		"tool_calls",
		"function_call",
		"content_filter":
		return false
	default:
		return true
	}
}
