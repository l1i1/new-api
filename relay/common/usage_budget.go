package common

import (
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// ClampUsageToClientBudget caps a usage that is about to be rendered into the
// client's response, written to the consume log, or settled, to the output
// budget the request declared (max_tokens / max_completion_tokens /
// max_output_tokens).
//
// Why: some aggregator upstreams retry internally when the reasoning phase
// exhausts the output budget and then report the SUM of every internal round as
// one usage (INC-20261013-02). max_tokens is a hard request-side invariant, so
// an output count above it cannot be legitimate: any excess is necessarily
// accumulated internal work. The clamp is applied before the usage frame / body
// is rendered, so the number the client sees, the number the consume log records
// and the number the settlement bills are identical; the raw upstream count is
// preserved on RelayInfo.UpstreamRawCompletionTokens for the alarm-only L2-A
// deviation marker.
//
// prompt tokens are deliberately NOT clamped: a provider tokenizer may count the
// same request a little differently, and criterion 1 of the deviation guard
// stays alarm-only for that reason.
//
// Returns true when a clamp actually happened.
func ClampUsageToClientBudget(info *RelayInfo, usage *dto.Usage) bool {
	if info == nil || usage == nil || info.ClientMaxTokens <= 0 {
		return false
	}
	budget := info.ClientMaxTokens
	// Settlement prefers BillingUsage when the adaptor attached one
	// (effectiveBillingUsage). That copy must be clamped too or the invoice
	// would still use the accumulated number — including the Claude/Gemini
	// semantics, whose output count lives in another field.
	raw := maxCompletionAcrossCopies(usage)
	if raw <= budget {
		return false
	}
	if raw > info.UpstreamRawCompletionTokens {
		info.UpstreamRawCompletionTokens = raw
	}
	usage.CompletionTokens = budget
	if usage.CompletionTokenDetails.ReasoningTokens > budget {
		// Keep the detail internally consistent: a client must never see more
		// reasoning tokens than completion tokens.
		usage.CompletionTokenDetails.ReasoningTokens = budget
	}
	usage.TotalTokens = usage.PromptTokens + budget
	if bu := usage.BillingUsage; bu != nil {
		clampBillingUsageToBudget(bu, budget)
	}
	return true
}

// maxCompletionAcrossCopies returns the largest output count any settled usage
// copy reports. The top-level usage is what the client sees; BillingUsage is
// what settlement actually bills when present, and its Claude/Gemini semantics
// keep the output count in a different field.
func maxCompletionAcrossCopies(usage *dto.Usage) int {
	if usage == nil {
		return 0
	}
	raw := usage.CompletionTokens
	bu := usage.BillingUsage
	if bu == nil {
		return raw
	}
	if bu.OpenAIUsage != nil && bu.OpenAIUsage.CompletionTokens > raw {
		raw = bu.OpenAIUsage.CompletionTokens
	}
	if bu.ClaudeUsage != nil && bu.ClaudeUsage.OutputTokens > raw {
		raw = bu.ClaudeUsage.OutputTokens
	}
	if gm := bu.GeminiUsageMetadata; gm != nil {
		// Gemini's maxOutputTokens covers candidates and thoughts together, so
		// either copy exceeding the budget is an accumulated count.
		if geminiOutput := gm.CandidatesTokenCount + gm.ThoughtsTokenCount; geminiOutput > raw {
			raw = geminiOutput
		}
	}
	return raw
}

// clampBillingUsageToBudget applies the same output cap to the settlement-side
// usage copy so the billed number matches the displayed one.
func clampBillingUsageToBudget(bu *dto.BillingUsage, budget int) {
	if bu == nil {
		return
	}
	if u := bu.OpenAIUsage; u != nil && u.CompletionTokens > budget {
		u.CompletionTokens = budget
		if u.CompletionTokenDetails.ReasoningTokens > budget {
			u.CompletionTokenDetails.ReasoningTokens = budget
		}
		u.TotalTokens = u.PromptTokens + budget
	}
	if cu := bu.ClaudeUsage; cu != nil && cu.OutputTokens > budget {
		cu.OutputTokens = budget
	}
	if gm := bu.GeminiUsageMetadata; gm != nil {
		// maxOutputTokens is a joint candidate+thoughts budget: clamp the pair
		// together (candidates first, thoughts get the remainder) and keep the
		// total consistent without discarding unrelated components such as
		// tool-use prompt tokens.
		raw := gm.CandidatesTokenCount + gm.ThoughtsTokenCount
		if raw > budget {
			newCandidates := gm.CandidatesTokenCount
			if newCandidates > budget {
				newCandidates = budget
			}
			newThoughts := gm.ThoughtsTokenCount
			if remaining := budget - newCandidates; newThoughts > remaining {
				newThoughts = remaining
			}
			if gm.TotalTokenCount > 0 {
				gm.TotalTokenCount -= raw - (newCandidates + newThoughts)
				if gm.TotalTokenCount < 0 {
					gm.TotalTokenCount = 0
				}
			}
			gm.CandidatesTokenCount = newCandidates
			gm.ThoughtsTokenCount = newThoughts
		}
	}
}
