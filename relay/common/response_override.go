package common

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
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
	operations, ok := tryParseOperations(document)
	if !ok {
		if _, hasOperations := document["operations"]; hasOperations {
			return fmt.Errorf("response_override has an \"operations\" entry that does not parse; every operation needs a mode and a path (the same shape as param_override)")
		}
		return fmt.Errorf("response_override needs an \"operations\" array (the same shape as param_override)")
	}
	if len(operations) == 0 {
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
