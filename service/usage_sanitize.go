package service

import (
	"bytes"
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
)

// SanitizeClientUsageJSON is the last line of defense for the token counts a
// client is shown.
//
// Billing refuses to charge an implausible usage (see
// billingexpr.CheckTokenParamsSanity and the zeroing gates in this package),
// and the typed response helpers zero the counts they build themselves (see
// relay/helper/client_usage.go). This function covers the payloads those
// helpers never touch: a body or SSE event forwarded verbatim from the
// upstream, in any relay format. On 2026-10-13 an upstream answered with
// prompt_tokens=1388347719831046; that value reached customers' own accounting,
// where downstream gateways parse these numbers and can overflow or crash on
// them.
//
// The rule is deliberately narrow, so ordinary traffic is byte-identical:
//   - the payload must be JSON whose text mentions "usage at all;
//   - it must carry a usage object (top-level, or nested the way the official
//     K3 shape puts it under choices[]);
//   - that object needs one numeric value that is negative or beyond
//     billingexpr.MaxPlausibleTokensPerField.
//
// Only then is the whole usage object replaced by zeros, keeping its shape: a
// missing usage object turns an accounting bug into a null-pointer one on the
// client's side. A rewritten document is re-encoded, and only in that anomalous
// case — the same tradeoff replaceTopLevelJSONValue documents for the model
// mask. Numbers elsewhere in the document are preserved verbatim (json.Number),
// so no timestamp or id loses precision on the way out.
func SanitizeClientUsageJSON(data []byte) []byte {
	if !usageBearingJSON(data) {
		return data
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return data
	}
	if !sanitizeUsageObjects(document) {
		return data
	}
	encoded, err := common.Marshal(document)
	if err != nil {
		return data
	}
	return encoded
}

// usageBearingJSON is the cheap pre-filter that keeps this out of the way of
// every ordinary body and of binary payloads (audio, images).
func usageBearingJSON(data []byte) bool {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	return bytes.Contains(data, []byte(`"usage`))
}

// sanitizeUsageObjects walks a parsed JSON document and zeroes every usage
// object that carries an implausible count. Only objects reached through a key
// named "usage" or "usageMetadata" are considered, so no unrelated number can
// be touched. The second return value reports whether anything was rewritten.
func sanitizeUsageObjects(value any) bool {
	changed := false
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if key == "usage" || key == "usageMetadata" {
				if zeroImplausibleUsageObject(item) {
					changed = true
				}
				continue
			}
			if sanitizeUsageObjects(item) {
				changed = true
			}
		}
	case []any:
		for _, item := range typed {
			if sanitizeUsageObjects(item) {
				changed = true
			}
		}
	}
	return changed
}

// zeroImplausibleUsageObject zeroes one usage object in place. Every number
// inside a usage object is a token count (or an upstream cost), so the walk
// needs no key list: it cannot miss a field this fork does not model, which is
// exactly the gap a typed check would leave.
func zeroImplausibleUsageObject(value any) bool {
	if _, ok := value.(map[string]any); !ok {
		return false
	}
	if !hasImplausibleCount(value) {
		return false
	}
	zeroCounts(value)
	return true
}

func hasImplausibleCount(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for _, item := range typed {
			if hasImplausibleCount(item) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if hasImplausibleCount(item) {
				return true
			}
		}
	case json.Number:
		number, err := typed.Float64()
		if err != nil {
			return false
		}
		return billingexpr.CheckTokenCount("usage", number) != nil
	case float64:
		return billingexpr.CheckTokenCount("usage", typed) != nil
	}
	return false
}

// zeroCounts zeroes every numeric leaf in place, keeping the object's shape.
func zeroCounts(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			switch item.(type) {
			case json.Number, float64:
				typed[key] = json.Number("0")
			default:
				zeroCounts(item)
			}
		}
	case []any:
		for _, item := range typed {
			zeroCounts(item)
		}
	}
}
