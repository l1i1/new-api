package common

import (
	"bytes"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ClampClientUsageJSON caps output-token counts inside a client-bound JSON
// payload to the request's declared output budget (max_tokens /
// max_completion_tokens / max_output_tokens).
//
// Phase B clamps the usage the gateway itself renders, but native adaptors and
// verbatim pass-through paths forward provider JSON before that happens. This
// helper is the last stop for those bytes: it recognises the usage shapes of
// OpenAI chat/completions, OpenAI Responses (including the nested
// response.usage of stream events), Anthropic messages and Gemini
// usageMetadata, and only rewrites a payload when a count actually exceeds the
// budget. Unknown or under-budget payloads are returned byte-identical.
//
// It is gated on info.ClientMaxTokens > 0 and a cheap "usage" substring probe,
// so ordinary content chunks are not parsed.
func ClampClientUsageJSON(info *RelayInfo, data []byte) []byte {
	if info == nil || info.ClientMaxTokens <= 0 || len(data) == 0 {
		return data
	}
	if !bytes.Contains(data, []byte("usage")) && !bytes.Contains(data, []byte("usageMetadata")) {
		return data
	}
	out := data
	for _, path := range []string{"usage", "response.usage", "message.usage"} {
		out = clampUsageObjectJSON(info, out, path)
	}
	out = clampGeminiUsageMetadataJSON(info, out, "usageMetadata")
	return out
}

// clampUsageObjectJSON rewrites one usage object (OpenAI/Responses/Anthropic
// shape) when its output count exceeds the budget.
func clampUsageObjectJSON(info *RelayInfo, data []byte, path string) []byte {
	if !gjson.GetBytes(data, path).IsObject() {
		return data
	}
	budget := int64(info.ClientMaxTokens)
	raw := int64(0)
	out := data
	changed := false

	// OpenAI chat/completions style.
	completion := gjson.GetBytes(out, path+".completion_tokens")
	if completion.Exists() && completion.Int() > budget {
		raw = completion.Int()
		out, _ = sjson.SetBytes(out, path+".completion_tokens", budget)
		if prompt := gjson.GetBytes(out, path+".prompt_tokens"); prompt.Exists() {
			if total := gjson.GetBytes(out, path+".total_tokens"); total.Exists() && total.Int() > prompt.Int()+budget {
				out, _ = sjson.SetBytes(out, path+".total_tokens", prompt.Int()+budget)
			}
		}
		out = clampReasoningDetail(out, path+".completion_tokens_details.reasoning_tokens", budget)
		changed = true
	}

	// Responses / Anthropic style. The two share the field name; an explicit
	// total_tokens selects the Responses shape (input + output), otherwise only
	// the output count is rewritten (Anthropic usage has no total).
	output := gjson.GetBytes(out, path+".output_tokens")
	if output.Exists() && output.Int() > budget {
		if output.Int() > raw {
			raw = output.Int()
		}
		out, _ = sjson.SetBytes(out, path+".output_tokens", budget)
		if input := gjson.GetBytes(out, path+".input_tokens"); input.Exists() {
			if total := gjson.GetBytes(out, path+".total_tokens"); total.Exists() && total.Int() > input.Int()+budget {
				out, _ = sjson.SetBytes(out, path+".total_tokens", input.Int()+budget)
			}
		}
		out = clampReasoningDetail(out, path+".output_tokens_details.reasoning_tokens", budget)
		changed = true
	}

	if changed && raw > int64(info.UpstreamRawCompletionTokens) {
		info.UpstreamRawCompletionTokens = int(raw)
	}
	return out
}

func clampReasoningDetail(data []byte, path string, budget int64) []byte {
	detail := gjson.GetBytes(data, path)
	if !detail.Exists() || detail.Int() <= budget {
		return data
	}
	out, _ := sjson.SetBytes(data, path, budget)
	return out
}

// clampGeminiUsageMetadataJSON applies the Gemini output cap to the joint
// candidates + thoughts count and keeps totalTokenCount consistent (the total
// may also include tool-use prompt tokens, so only the clamped difference is
// subtracted).
func clampGeminiUsageMetadataJSON(info *RelayInfo, data []byte, path string) []byte {
	if !gjson.GetBytes(data, path).IsObject() {
		return data
	}
	budget := int64(info.ClientMaxTokens)
	candidates := gjson.GetBytes(data, path+".candidatesTokenCount").Int()
	thoughts := gjson.GetBytes(data, path+".thoughtsTokenCount").Int()
	raw := candidates + thoughts
	if raw <= budget {
		return data
	}
	newCandidates := candidates
	if newCandidates > budget {
		newCandidates = budget
	}
	newThoughts := thoughts
	if remaining := budget - newCandidates; newThoughts > remaining {
		newThoughts = remaining
	}
	out := data
	if total := gjson.GetBytes(out, path+".totalTokenCount"); total.Exists() {
		adjusted := total.Int() - (raw - (newCandidates + newThoughts))
		if adjusted < 0 {
			adjusted = 0
		}
		out, _ = sjson.SetBytes(out, path+".totalTokenCount", adjusted)
	}
	if newCandidates != candidates {
		out, _ = sjson.SetBytes(out, path+".candidatesTokenCount", newCandidates)
	}
	if newThoughts != thoughts {
		out, _ = sjson.SetBytes(out, path+".thoughtsTokenCount", newThoughts)
	}
	if raw > int64(info.UpstreamRawCompletionTokens) {
		info.UpstreamRawCompletionTokens = int(raw)
	}
	return out
}
