package common

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// Response-body override: the response-side sibling of ParamOverride.
//
// It exists so a channel whose upstream cannot produce the official shape can
// still serve the request: the gateway rewrites the response body — dropping
// fields, moving values, pruning structures — instead of routing the request
// away to another channel. Like ParamOverride the document is per channel and
// edited through the admin surface, so a change takes effect on the normal
// channel-cache path (seconds) and never needs a build.
//
// Shape only, deliberately: token accounting is NOT rewritable. The save path
// rejects any operation whose path or from/to targets a usage path, and the
// runtime skips such an operation should one reach it, because hiding tokens
// the upstream really generated (or the reverse) would misreport what the
// caller was billed for — the exact mismatch this feature must not create.

const (
	// responseOverrideUsagePrefix is the path prefix the save path refuses.
	responseOverrideUsagePrefix = "usage"
)

// responseOverrideHeaderModes are request-header operations: meaningless (and
// harmful) on a response whose headers are already on the wire.
var responseOverrideHeaderModes = map[string]struct{}{
	"set_header":    {},
	"delete_header": {},
	"copy_header":   {},
	"move_header":   {},
	"pass_headers":  {},
}

// IsResponseOverrideForbiddenPath reports whether a rewritten path would touch
// token accounting. Matching is boundary-aware so a field named "usage_notes"
// is not mistaken for "usage".
func IsResponseOverrideForbiddenPath(path string) bool {
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(path), "."))
	if trimmed == "" {
		return false
	}
	if trimmed == responseOverrideUsagePrefix {
		return true
	}
	return strings.HasPrefix(trimmed, responseOverrideUsagePrefix+".") ||
		strings.HasPrefix(trimmed, responseOverrideUsagePrefix+"[")
}

// ValidateResponseOverride checks a candidate response-override document for
// the structural rules the runtime relies on: it must be a JSON object, every
// operation must name a mode this gateway implements, and no operation may
// touch token accounting or response headers. It returns the first problem so
// the caller can surface it verbatim at save time.
func ValidateResponseOverride(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		return fmt.Errorf("response_override must be a JSON object: %w", err)
	}
	rawCorrection, hasUsageCorrection := document[responseOverrideUsageCorrectionKey]
	if hasUsageCorrection {
		if err := validateResponseUsageCorrection(rawCorrection); err != nil {
			return err
		}
	}
	operations, ok := tryParseOperations(document)
	if !ok {
		if _, hasOperations := document["operations"]; hasOperations {
			return fmt.Errorf("response_override has an \"operations\" entry that does not parse; every operation needs a mode and a path (the same shape as param_override)")
		}
		if hasUsageCorrection {
			// A usage correction needs no shaping operations: a channel can
			// carry the token fix alone.
			return nil
		}
		return fmt.Errorf("response_override needs an \"operations\" array (the same shape as param_override)")
	}
	if len(operations) == 0 {
		if hasUsageCorrection {
			return nil
		}
		return fmt.Errorf("response_override has no operations")
	}
	for i, operation := range operations {
		mode := strings.TrimSpace(operation.Mode)
		if mode == "" {
			return fmt.Errorf("response_override operation %d has no mode", i)
		}
		if _, isHeader := responseOverrideHeaderModes[mode]; isHeader {
			return fmt.Errorf("response_override operation %d uses %q; response headers are already sent and cannot be rewritten", i, mode)
		}
		for _, candidate := range []string{operation.Path, operation.From, operation.To} {
			if IsResponseOverrideForbiddenPath(candidate) {
				return fmt.Errorf("response_override operation %d targets %q; token accounting is not rewritable (shape only)", i, candidate)
			}
		}
	}
	return nil
}

// sanitizeResponseOverride drops operations the runtime must not apply. The
// save path rejects them outright; this is the second line of defence for a
// document that reached the database around the API.
func sanitizeResponseOverride(document map[string]any, channelID int) map[string]any {
	operations, ok := tryParseOperations(document)
	if !ok || len(operations) == 0 {
		return document
	}
	kept := make([]ParamOperation, 0, len(operations))
	for _, operation := range operations {
		if _, isHeader := responseOverrideHeaderModes[strings.TrimSpace(operation.Mode)]; isHeader {
			common.SysLog(fmt.Sprintf("response override: channel_id=%d skipping header operation %q", channelID, operation.Mode))
			continue
		}
		forbidden := false
		for _, candidate := range []string{operation.Path, operation.From, operation.To} {
			if IsResponseOverrideForbiddenPath(candidate) {
				common.SysLog(fmt.Sprintf("response override: channel_id=%d skipping usage-targeting operation on %q", channelID, candidate))
				forbidden = true
				break
			}
		}
		if forbidden {
			continue
		}
		kept = append(kept, operation)
	}
	if len(kept) == 0 {
		return nil
	}
	// Re-encode as []any of maps: that is the shape tryParseOperations accepts,
	// and handing it []ParamOperation would fall through to the legacy
	// path→value interpretation — which would treat the word "operations" as a
	// JSON path.
	encoded, err := json.Marshal(kept)
	if err != nil {
		common.SysLog("response override: cannot re-encode sanitized operations: " + err.Error())
		return nil
	}
	var sanitized []any
	if err := json.Unmarshal(encoded, &sanitized); err != nil {
		return nil
	}
	return map[string]any{"operations": sanitized}
}

// ApplyResponseOverride rewrites one response payload with the channel's
// response-override document. A channel without one, or a payload that is not
// a JSON object, passes through untouched: the override must never break a
// response it was not written for.
func ApplyResponseOverride(data []byte, info *RelayInfo) ([]byte, error) {
	if info == nil || info.ChannelMeta == nil {
		return data, nil
	}
	document := info.ChannelMeta.ResponseOverride
	if len(document) == 0 || len(data) == 0 {
		return data, nil
	}
	document = sanitizeResponseOverride(document, info.ChannelId)
	if len(document) == 0 {
		return data, nil
	}
	overrideCtx := BuildParamOverrideContext(info)
	recorder := &paramOverrideAuditRecorder{all: true}
	overrideCtx[paramOverrideContextAuditRecorder] = recorder
	result, err := ApplyParamOverride(data, document, overrideCtx)
	if err != nil {
		return data, err
	}
	if len(recorder.lines) > 0 {
		info.ResponseOverrideAudit = append(info.ResponseOverrideAudit, recorder.lines...)
	}
	return result, nil
}

// Response usage correction.
//
// Token accounting stays protected: the operations above still cannot name a
// usage path. This block is the one deliberate exception, and it is bounded to
// subtraction. It exists for a case the gateway cannot prevent — an upstream
// that prepends a message of its own before generating (2026-10-30: a Kimi
// provider rejects a user-first conversation, so it injects a space as a system
// message and moves the caller's message to second) and then counts that
// injected framing in prompt_tokens. The caller sees 1-2 more prompt tokens
// than the official endpoint reports for the same request.
//
// Because the injection is the upstream's, not the caller's, billing and the
// client view subtract it together: apply_to_billing defaults to true so the
// caller, the consume log and the settlement keep telling the same story. The
// correction only runs for requests that match the configured characteristic,
// so a channel can serve both shapes without the rule leaking onto traffic the
// upstream did not touch.
const responseOverrideUsageCorrectionKey = "usage_correction"

// ResponseUsageCorrection configures the subtraction above. It lives inside the
// channel's response_override document:
//
//	{"usage_correction": {
//	   "prompt_tokens_delta": -1,
//	   "conditions": [{"path": "messages.0.role", "mode": "full", "value": "user"}],
//	   "logic": "AND",
//	   "apply_to_billing": true
//	}}
//
// Every field is optional except the delta, which must be negative. Empty
// conditions mean "every request that gets a response through this channel".
type ResponseUsageCorrection struct {
	PromptTokensDelta int                  `json:"prompt_tokens_delta"`
	ApplyToBilling    *bool                `json:"apply_to_billing,omitempty"`
	Conditions        []ConditionOperation `json:"conditions,omitempty"`
	Logic             string               `json:"logic,omitempty"`
}

// ParseResponseUsageCorrection extracts the usage_correction block from a
// response_override document. ok is false when the document carries none, or
// carries one that would not subtract (the runtime never applies such a block:
// the save path rejects it, and a document that reached the database around
// the API must not inflate accounting either).
func ParseResponseUsageCorrection(document map[string]any) (ResponseUsageCorrection, bool) {
	raw, exists := document[responseOverrideUsageCorrectionKey]
	if !exists || raw == nil {
		return ResponseUsageCorrection{}, false
	}
	block, ok := raw.(map[string]any)
	if !ok {
		return ResponseUsageCorrection{}, false
	}
	correction := ResponseUsageCorrection{Logic: "AND"}
	if delta, ok := responseUsageCorrectionInt(block["prompt_tokens_delta"]); ok {
		correction.PromptTokensDelta = delta
	}
	if correction.PromptTokensDelta >= 0 {
		return ResponseUsageCorrection{}, false
	}
	if apply, ok := block["apply_to_billing"].(bool); ok {
		correction.ApplyToBilling = &apply
	}
	if logic, ok := block["logic"].(string); ok && strings.TrimSpace(logic) != "" {
		correction.Logic = strings.ToUpper(strings.TrimSpace(logic))
	}
	if rawConditions, exists := block["conditions"]; exists && rawConditions != nil {
		conditions, err := parseConditionOperations(rawConditions)
		if err != nil {
			return ResponseUsageCorrection{}, false
		}
		correction.Conditions = conditions
	}
	return correction, true
}

func responseUsageCorrectionInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case int32:
		return int(typed), true
	default:
		return 0, false
	}
}

// validateResponseUsageCorrection checks the block at save time so an operator
// sees the problem in the admin form instead of silently losing the rule.
func validateResponseUsageCorrection(raw any) error {
	block, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("usage_correction must be a JSON object")
	}
	delta, ok := responseUsageCorrectionInt(block["prompt_tokens_delta"])
	if !ok {
		return fmt.Errorf("usage_correction needs an integer prompt_tokens_delta")
	}
	if delta >= 0 {
		return fmt.Errorf("usage_correction prompt_tokens_delta must be negative: it may only subtract tokens an upstream injected on its own side")
	}
	if logic, exists := block["logic"]; exists {
		text, ok := logic.(string)
		if !ok || (strings.ToUpper(strings.TrimSpace(text)) != "AND" && strings.ToUpper(strings.TrimSpace(text)) != "OR") {
			return fmt.Errorf(`usage_correction logic must be "and" or "or"`)
		}
	}
	if apply, exists := block["apply_to_billing"]; exists {
		if _, ok := apply.(bool); !ok {
			return fmt.Errorf("usage_correction apply_to_billing must be true or false")
		}
	}
	if rawConditions, exists := block["conditions"]; exists && rawConditions != nil {
		if _, err := parseConditionOperations(rawConditions); err != nil {
			return fmt.Errorf("usage_correction conditions do not parse: %w", err)
		}
	}
	return nil
}

// ApplyResponseUsageCorrection subtracts the configured prompt-token delta from
// usage when the request matches the channel's usage_correction block. It
// returns true when it adjusted the usage; the delta is clamped so a count never
// goes below zero. requestBody is the caller's stored request body (the stored
// shape, not the upstream-bound one), so the conditions describe what the caller
// asked for.
func ApplyResponseUsageCorrection(requestBody []byte, info *RelayInfo, usage *dto.Usage) bool {
	if info == nil || info.ChannelMeta == nil || usage == nil {
		return false
	}
	if info.UsageCorrectionApplied {
		return false
	}
	correction, ok := ParseResponseUsageCorrection(info.ChannelMeta.ResponseOverride)
	if !ok {
		return false
	}
	if len(correction.Conditions) > 0 {
		if len(requestBody) == 0 && info.Request != nil {
			// The stored body is always available on the HTTP paths; the parsed
			// request keeps the rule working on the paths that assemble usage
			// without one (tests, internal re-sends).
			if encoded, err := common.Marshal(info.Request); err == nil {
				requestBody = encoded
			}
		}
		matched, err := checkConditions(requestBody, "", correction.Conditions, correction.Logic)
		if err != nil || !matched {
			return false
		}
	}
	delta := correction.PromptTokensDelta
	if usage.PromptTokens+delta < 0 {
		delta = -usage.PromptTokens
	}
	if delta == 0 {
		return false
	}
	usage.PromptTokens += delta
	usage.TotalTokens += delta
	applyToBilling := correction.ApplyToBilling == nil || *correction.ApplyToBilling
	if applyToBilling {
		adjustBillingUsagePromptTokens(usage, delta)
	}
	info.UsageCorrectionApplied = true
	info.ResponseOverrideAudit = append(info.ResponseOverrideAudit,
		fmt.Sprintf("usage_correction: prompt_tokens %+d (billing=%t)", delta, applyToBilling))
	return true
}

// adjustBillingUsagePromptTokens keeps the billing sidecar consistent with the
// corrected top-level count: settlement reads the sidecar for the semantic
// usage, so a correction that only touched the client view would make the
// invoice disagree with what the caller was shown.
func adjustBillingUsagePromptTokens(usage *dto.Usage, delta int) {
	if usage == nil || usage.BillingUsage == nil {
		return
	}
	if openaiUsage := usage.BillingUsage.OpenAIUsage; openaiUsage != nil {
		step := delta
		if openaiUsage.PromptTokens+step < 0 {
			step = -openaiUsage.PromptTokens
		}
		openaiUsage.PromptTokens += step
		openaiUsage.TotalTokens += step
	}
	if claudeUsage := usage.BillingUsage.ClaudeUsage; claudeUsage != nil {
		step := delta
		if claudeUsage.InputTokens+step < 0 {
			step = -claudeUsage.InputTokens
		}
		claudeUsage.InputTokens += step
	}
}
