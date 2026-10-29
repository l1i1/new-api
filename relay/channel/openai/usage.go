package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/officialfit"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// isValidFunctionToolCall keeps empty or non-function tool objects from being
// treated as a successful assistant response. A function name is the stable
// part of a completed OpenAI tool call; arguments may legitimately be empty
// for providers that encode a no-argument call that way.
func isValidFunctionToolCall(call dto.ToolCallRequest) bool {
	if call.Type != "" && !strings.EqualFold(strings.TrimSpace(call.Type), "function") {
		return false
	}
	return strings.TrimSpace(call.Function.Name) != ""
}

func isValidStreamFunctionToolCall(call dto.ToolCallResponse) bool {
	switch typ := call.Type.(type) {
	case nil:
	case string:
		if strings.TrimSpace(typ) != "" &&
			!strings.EqualFold(strings.TrimSpace(typ), "function") {
			return false
		}
	default:
		return false
	}
	return strings.TrimSpace(call.Function.Name) != ""
}

func shouldSuppressReasoningContent(info *relaycommon.RelayInfo) bool {
	if info == nil {
		return false
	}
	// Kimi K3 is exempt from response-side stripping. The disable-thinking
	// intent is already made real on the REQUEST side (see
	// applyKimiK3DisabledThinkingDialect), and a streaming gateway cannot know
	// whether the upstream honoured it by the time the deltas arrive: hiding
	// reasoning that may already have been billed would leave the caller paying
	// for tokens it cannot see, which is exactly the failure this work removed.
	// When the upstream really stops thinking the official shape follows on its
	// own (no reasoning_content, no reasoning tokens).
	// Kimi K3 is exempt from response-side stripping because the disable-thinking
	// intent is made real on the request side and the upstream honours it. A
	// channel that is known NOT to honour it (reseller, thinking_off_emulation)
	// opts back into stripping: the reasoning deltas it emits are not something
	// the caller asked for, and applyThinkingOffEmulation keeps the billed usage
	// in step with the hidden tokens.
	if kimiK3ReasoningExempt(info) && !thinkingOffEmulationActive(info) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(info.GetReasoningEffort()), "none") {
		return true
	}
	request, ok := info.Request.(*dto.GeneralOpenAIRequest)
	if !ok || len(request.THINKING) == 0 {
		return false
	}
	var thinking struct {
		Type string `json:"type"`
	}
	if err := common.Unmarshal(request.THINKING, &thinking); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(thinking.Type), "disabled")
}

// thinkingOffEmulationActive reports whether this request is answered by a
// channel that emulates the official thinking-off contract. The upstream keeps
// thinking regardless of the disable flag, so the gateway removes the preamble
// from the prompt count and the reasoning tokens from the completion count, and
// hides reasoning_content from the caller (see ChannelSettings.
// ThinkingOffEmulation).
func thinkingOffEmulationActive(info *relaycommon.RelayInfo) bool {
	if info == nil || !info.ChannelSetting.ThinkingOffEmulation {
		return false
	}
	request, ok := info.Request.(*dto.GeneralOpenAIRequest)
	if !ok || request == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(info.GetReasoningEffort()), "none") {
		return true
	}
	if len(request.THINKING) == 0 {
		return false
	}
	var thinking struct {
		Type string `json:"type"`
	}
	if err := common.Unmarshal(request.THINKING, &thinking); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(thinking.Type), "disabled")
}

// thinkingOffPromptOffset computes how many prompt tokens the upstream added for
// its thinking preamble on this request. Measured 2026-10-29: a fixed 68
// (ch91/102/37/105) or 69 (ch106) regardless of input length, +3 per message
// beyond the first, and +37 on ch106 when tools are present.
func thinkingOffPromptOffset(info *relaycommon.RelayInfo) int {
	if info == nil {
		return 0
	}
	offset := info.ChannelSetting.ThinkingOffPromptOffset
	request, ok := info.Request.(*dto.GeneralOpenAIRequest)
	if !ok || request == nil {
		return offset
	}
	if extra := info.ChannelSetting.ThinkingOffPromptOffsetPerMessage; extra > 0 && len(request.Messages) > 1 {
		offset += extra * (len(request.Messages) - 1)
	}
	if extra := info.ChannelSetting.ThinkingOffPromptOffsetTools; extra > 0 && len(request.Tools) > 0 {
		offset += extra
	}
	return offset
}

// applyThinkingOffEmulation rewrites one usage snapshot to the official
// thinking-off shape: prompt minus the preamble, completion minus the reasoning
// tokens, and no completion token details (the official usage carries none when
// the model was not asked to think). Snapshots are cumulative, so subtracting the
// same constant from every snapshot is idempotent under the fill-only merge the
// stream handler uses.
func applyThinkingOffEmulation(info *relaycommon.RelayInfo, usage *dto.Usage) bool {
	if usage == nil || !thinkingOffEmulationActive(info) {
		return false
	}
	changed := false
	if offset := thinkingOffPromptOffset(info); offset > 0 && usage.PromptTokens > 0 {
		if usage.PromptTokens > offset {
			usage.PromptTokens -= offset
		} else {
			usage.PromptTokens = 1
		}
		changed = true
	}
	if rt := usage.CompletionTokenDetails.ReasoningTokens; rt > 0 {
		if usage.CompletionTokens > rt {
			usage.CompletionTokens -= rt
		} else {
			usage.CompletionTokens = 0
		}
		changed = true
	}
	if changed {
		usage.CompletionTokenDetails = dto.OutputTokenDetails{}
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return changed
}

// omitZeroCompletionTokenDetails drops a completion_tokens_details object whose
// every field is zero. The official endpoint sends no such object when the model
// was not asked to think, so an all-zero one is a pure shape difference: it
// carries no data and would make an emulated answer visibly unlike the official
// one.
func omitZeroCompletionTokenDetails(payload map[string]json.RawMessage) {
	raw, ok := payload["completion_tokens_details"]
	if !ok {
		return
	}
	var details map[string]int
	if err := common.Unmarshal(raw, &details); err != nil {
		return
	}
	for _, value := range details {
		if value != 0 {
			return
		}
	}
	delete(payload, "completion_tokens_details")
}

// omitZeroCompletionTokenDetailsFromBody does the same for a marshalled response
// body's usage object, leaving every other byte untouched.
func omitZeroCompletionTokenDetailsFromBody(body []byte) []byte {
	var payload map[string]json.RawMessage
	if err := common.Unmarshal(body, &payload); err != nil {
		return body
	}
	rawUsage, ok := payload["usage"]
	if !ok {
		return body
	}
	var usagePayload map[string]json.RawMessage
	if err := common.Unmarshal(rawUsage, &usagePayload); err != nil {
		return body
	}
	before := len(usagePayload)
	omitZeroCompletionTokenDetails(usagePayload)
	if len(usagePayload) == before {
		return body
	}
	encodedUsage, err := common.Marshal(usagePayload)
	if err != nil {
		return body
	}
	patched, ok := replaceTopLevelJSONValue(body, "usage", encodedUsage)
	if !ok {
		return body
	}
	return patched
}

// mergeUsageForChannel merges one upstream usage snapshot into the accumulated
// usage, applying the thinking-off emulation to the snapshot first. Subtracting
// the preamble from every cumulative snapshot is idempotent under MergeUsage
// (the latest snapshot wins and zero fields fall back to the previous value).
func mergeUsageForChannel(info *relaycommon.RelayInfo, previous, next *dto.Usage) *dto.Usage {
	if next == nil {
		return dto.MergeUsage(previous, next)
	}
	if applyThinkingOffEmulation(info, next) {
		// The snapshot was rewritten in place; the caller owns the pointer only
		// for the duration of the merge, but streams reuse the chunk struct, so
		// keep a copy to avoid rewriting a chunk that is also rendered.
		adjusted := *next
		return dto.MergeUsage(previous, &adjusted)
	}
	return dto.MergeUsage(previous, next)
}

func stripReasoningContentFromTextResponse(response *dto.OpenAITextResponse) {
	if response == nil {
		return
	}
	for i := range response.Choices {
		response.Choices[i].Message.ReasoningContent = nil
		response.Choices[i].Message.Reasoning = nil
		stripReasoningLogprobs(response.Choices[i].Logprobs)
	}
}

func stripReasoningLogprobs(logprobs *any) {
	if logprobs == nil {
		return
	}
	values, ok := (*logprobs).(map[string]any)
	if !ok {
		return
	}
	delete(values, "reasoning_content")
}

// stripReasoningContentFromResponseBody removes reasoning fields from a
// non-stream response body. Editing is surgical via the byte-level splice
// layer so upstream key order and formatting survive; the map round-trip is
// kept only as a fallback for structurally unexpected inputs.
func stripReasoningContentFromResponseBody(body []byte) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := common.Unmarshal(body, &payload); err != nil {
		return body, err
	}
	rawChoices, ok := payload["choices"]
	if !ok {
		return body, nil
	}
	if stripped, ok := stripReasoningFromChoicesInPlace(rawChoices); ok {
		if patched, ok := replaceTopLevelJSONValue(body, "choices", stripped); ok {
			return patched, nil
		}
		return body, nil
	}

	encodedChoices, err := common.Marshal(rawChoices)
	if err != nil {
		return body, err
	}
	var choices []map[string]interface{}
	if err = common.Unmarshal(encodedChoices, &choices); err != nil {
		return body, err
	}
	for _, choice := range choices {
		message, ok := choice["message"].(map[string]interface{})
		if ok {
			delete(message, "reasoning_content")
			delete(message, "reasoning")
		}
		if logprobs, ok := choice["logprobs"].(map[string]interface{}); ok {
			delete(logprobs, "reasoning_content")
		}
	}
	payload["choices"], _ = json.Marshal(choices)
	stripped, err := common.Marshal(payload)
	if err != nil {
		return body, err
	}
	return stripped, nil
}

// stripReasoningFromChoicesInPlace removes reasoning keys from every choice's
// message and logprobs objects while preserving every other byte. ok=false
// lets the caller fall back to the map rewrite.
func stripReasoningFromChoicesInPlace(rawChoices json.RawMessage) (json.RawMessage, bool) {
	spans, ok := jsonArrayElementSpans(rawChoices)
	if !ok {
		return nil, false
	}
	if len(spans) == 0 {
		return rawChoices, true
	}
	type edit struct {
		start, end int
		bytes      []byte
	}
	edits := make([]edit, 0, len(spans))
	for _, span := range spans {
		element := rawChoices[span[0]:span[1]]
		fitted, ok := stripReasoningFromChoiceInPlace(element)
		if !ok {
			return nil, false
		}
		if !bytes.Equal(fitted, element) {
			edits = append(edits, edit{start: span[0], end: span[1], bytes: fitted})
		}
	}
	if len(edits) == 0 {
		return rawChoices, true
	}
	out := make([]byte, 0, len(rawChoices))
	prev := 0
	for _, e := range edits {
		out = append(out, rawChoices[prev:e.start]...)
		out = append(out, e.bytes...)
		prev = e.end
	}
	out = append(out, rawChoices[prev:]...)
	return out, true
}

// stripReasoningFromChoiceInPlace deletes reasoning keys from one choice
// object's message and logprobs without disturbing any other byte.
func stripReasoningFromChoiceInPlace(choice []byte) ([]byte, bool) {
	result := choice
	changed := false
	choicePairs, _, err := parseTopLevelPairs(choice)
	if err != nil {
		return nil, false
	}
	for _, key := range []string{"message", "logprobs"} {
		pair, found, err := findJSONPair(choicePairs, key)
		if err != nil || !found {
			if err != nil {
				return nil, false
			}
			continue
		}
		value := result[pair.valueStart:pair.valueEnd]
		trimmed := bytes.TrimLeft(value, " \t\r\n")
		if len(trimmed) == 0 || trimmed[0] != '{' {
			// null / non-object values carry no reasoning keys.
			continue
		}
		removals := []string{"reasoning_content", "reasoning"}
		if key == "logprobs" {
			removals = []string{"reasoning_content"}
		}
		stripped := value
		valueChanged := false
		for _, remove := range removals {
			patched, ok := deleteTopLevelJSONKey(stripped, remove)
			if !ok {
				return nil, false
			}
			if !bytes.Equal(patched, stripped) {
				valueChanged = true
			}
			stripped = patched
		}
		if valueChanged {
			out := make([]byte, 0, len(choice))
			out = append(out, result[:pair.valueStart]...)
			out = append(out, stripped...)
			out = append(out, result[pair.valueEnd:]...)
			result = out
			changed = true
			// Offsets shifted; re-parse for the next key.
			choicePairs, _, err = parseTopLevelPairs(result)
			if err != nil {
				return nil, false
			}
		}
	}
	if !changed {
		return choice, true
	}
	return result, true
}

// patchZeroCompletionUsage fills a missing output count from content that was
// actually received. Some OpenAI-compatible providers emit prompt-only usage
// even though the response body contains assistant output.
func patchZeroCompletionUsage(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, responseText string, toolCount int) bool {
	if info == nil || usage == nil || usage.CompletionTokens != 0 {
		return false
	}

	// Prefer exact output counts that were present in an OpenAI billing
	// extension or derivable from a consistent top-level total. Only estimate
	// from text when the upstream response contains no exact output count.
	exactCompletionTokens := 0
	if usage.BillingUsage != nil && usage.BillingUsage.OpenAIUsage != nil {
		exactCompletionTokens = usage.BillingUsage.OpenAIUsage.CompletionTokens
		if exactCompletionTokens == 0 {
			exactCompletionTokens = usage.BillingUsage.OpenAIUsage.OutputTokens
		}
	}
	if exactCompletionTokens == 0 && usage.OutputTokens > 0 {
		exactCompletionTokens = usage.OutputTokens
	}
	if exactCompletionTokens == 0 && usage.TotalTokens > usage.PromptTokens {
		exactCompletionTokens = usage.TotalTokens - usage.PromptTokens
	}
	if exactCompletionTokens > 0 {
		usage.CompletionTokens = exactCompletionTokens
		if usage.TotalTokens < usage.PromptTokens+usage.CompletionTokens {
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}
		if usage.BillingUsage != nil && usage.BillingUsage.OpenAIUsage != nil {
			billingUsage := usage.BillingUsage.OpenAIUsage
			if billingUsage.PromptTokens == 0 {
				billingUsage.PromptTokens = usage.PromptTokens
			}
			billingUsage.CompletionTokens = usage.CompletionTokens
			billingUsage.TotalTokens = billingUsage.PromptTokens + billingUsage.CompletionTokens
			if billingUsage.OutputTokens == 0 {
				billingUsage.OutputTokens = usage.CompletionTokens
			}
		}
		return true
	}
	if responseText == "" && toolCount == 0 {
		return false
	}

	estimated := service.ResponseText2Usage(c, responseText, info.UpstreamModelName, usage.PromptTokens)
	estimatedCompletionTokens := estimated.CompletionTokens + toolCount*7
	if estimatedCompletionTokens <= 0 {
		return false
	}

	usage.CompletionTokens = estimatedCompletionTokens
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	if usage.BillingUsage == nil {
		usage.BillingUsage = dto.NewOpenAIChatBillingUsage(usage)
	}
	if usage.BillingUsage != nil {
		usage.BillingUsage.Estimated = true
	}
	if usage.BillingUsage != nil && usage.BillingUsage.OpenAIUsage != nil {
		billingUsage := usage.BillingUsage.OpenAIUsage
		if billingUsage.PromptTokens == 0 {
			billingUsage.PromptTokens = usage.PromptTokens
		}
		billingUsage.CompletionTokens = usage.CompletionTokens
		billingUsage.TotalTokens = billingUsage.PromptTokens + billingUsage.CompletionTokens
		if billingUsage.OutputTokens == 0 {
			billingUsage.OutputTokens = usage.CompletionTokens
		}
	}
	return true
}

func applyUsagePostProcessing(info *relaycommon.RelayInfo, usage *dto.Usage, responseBody []byte) {
	if info == nil || usage == nil {
		return
	}
	defer logInconsistentUpstreamCacheUsage(info, usage)

	switch info.ChannelType {
	case constant.ChannelTypeDeepSeek:
		if usage.PromptTokensDetails.CachedTokens == 0 && usage.PromptCacheHitTokens != 0 {
			usage.PromptTokensDetails.CachedTokens = usage.PromptCacheHitTokens
		}
	case constant.ChannelTypeZhipu_v4:
		// 智普的cached_tokens在标准位置: usage.prompt_tokens_details.cached_tokens
		if usage.PromptTokensDetails.CachedTokens == 0 {
			if usage.InputTokensDetails != nil && usage.InputTokensDetails.CachedTokens > 0 {
				usage.PromptTokensDetails.CachedTokens = usage.InputTokensDetails.CachedTokens
			} else if cachedTokens, ok := extractCachedTokensFromBody(responseBody); ok {
				usage.PromptTokensDetails.CachedTokens = cachedTokens
			} else if usage.PromptCacheHitTokens > 0 {
				usage.PromptTokensDetails.CachedTokens = usage.PromptCacheHitTokens
			}
		}
	case constant.ChannelTypeMoonshot:
		// Moonshot的cached_tokens在非标准位置: choices[].usage.cached_tokens
		if usage.PromptTokensDetails.CachedTokens == 0 {
			if usage.InputTokensDetails != nil && usage.InputTokensDetails.CachedTokens > 0 {
				usage.PromptTokensDetails.CachedTokens = usage.InputTokensDetails.CachedTokens
			} else if cachedTokens, ok := extractMoonshotCachedTokensFromBody(responseBody); ok {
				usage.PromptTokensDetails.CachedTokens = cachedTokens
			} else if cachedTokens, ok := extractCachedTokensFromBody(responseBody); ok {
				usage.PromptTokensDetails.CachedTokens = cachedTokens
			} else if usage.PromptCacheHitTokens > 0 {
				usage.PromptTokensDetails.CachedTokens = usage.PromptCacheHitTokens
			}
		}
	case constant.ChannelTypeOpenAI:
		if usage.PromptTokensDetails.CachedTokens == 0 {
			if cachedTokens, ok := extractLlamaCachedTokensFromBody(responseBody); ok {
				usage.PromptTokensDetails.CachedTokens = cachedTokens
			}
		}
	}
}

func extractCachedTokensFromBody(body []byte) (int, bool) {
	if len(body) == 0 {
		return 0, false
	}

	var payload struct {
		Usage struct {
			PromptTokensDetails struct {
				CachedTokens *int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CachedTokens         *int `json:"cached_tokens"`
			PromptCacheHitTokens *int `json:"prompt_cache_hit_tokens"`
		} `json:"usage"`
	}

	if err := common.Unmarshal(body, &payload); err != nil {
		return 0, false
	}

	if payload.Usage.PromptTokensDetails.CachedTokens != nil {
		return *payload.Usage.PromptTokensDetails.CachedTokens, true
	}
	if payload.Usage.CachedTokens != nil {
		return *payload.Usage.CachedTokens, true
	}
	if payload.Usage.PromptCacheHitTokens != nil {
		return *payload.Usage.PromptCacheHitTokens, true
	}
	return 0, false
}

// extractMoonshotCachedTokensFromBody 从Moonshot的非标准位置提取cached_tokens
// Moonshot的流式响应格式: {"choices":[{"usage":{"cached_tokens":111}}]}
func extractMoonshotCachedTokensFromBody(body []byte) (int, bool) {
	if len(body) == 0 {
		return 0, false
	}

	var payload struct {
		Choices []struct {
			Usage struct {
				CachedTokens *int `json:"cached_tokens"`
			} `json:"usage"`
		} `json:"choices"`
	}

	if err := common.Unmarshal(body, &payload); err != nil {
		return 0, false
	}

	// 遍历choices查找cached_tokens
	for _, choice := range payload.Choices {
		if choice.Usage.CachedTokens != nil && *choice.Usage.CachedTokens > 0 {
			return *choice.Usage.CachedTokens, true
		}
	}

	return 0, false
}

// extractLlamaCachedTokensFromBody 从llama.cpp的非标准位置提取cache_n
func extractLlamaCachedTokensFromBody(body []byte) (int, bool) {
	if len(body) == 0 {
		return 0, false
	}

	var payload struct {
		Timings struct {
			CachedTokens *int `json:"cache_n"`
		} `json:"timings"`
	}

	if err := common.Unmarshal(body, &payload); err != nil {
		return 0, false
	}

	if payload.Timings.CachedTokens == nil {
		return 0, false
	}
	return *payload.Timings.CachedTokens, true
}

// kimiK3ReasoningExempt reports whether the request targets the Kimi K3 family,
// whose reasoning content is always delivered rather than stripped.
func kimiK3ReasoningExempt(info *relaycommon.RelayInfo) bool {
	if info == nil {
		return false
	}
	model := info.OriginModelName
	if model == "" {
		model = info.GetUpstreamModelName()
	}
	return officialfit.FamilyOf(model) == officialfit.FamilyKimiK3
}

// logInconsistentUpstreamCacheUsage records the upstreams that report a prompt and
// its cache hit as independent counters with incompatible values. The caller-facing
// usage is normalized in dto.SanitizeClientUsage, but silently normalizing would hide
// which provider is lying about its own accounting (2026-10-29: prompt 7, cache 512).
func logInconsistentUpstreamCacheUsage(info *relaycommon.RelayInfo, usage *dto.Usage) {
	if info == nil || usage == nil {
		return
	}
	cached := usage.PromptTokensDetails.CachedTokens
	if raw := usage.PromptCacheHitTokens; raw > cached {
		cached = raw
	}
	if cached <= usage.PromptTokens {
		return
	}
	common.SysLog(fmt.Sprintf("upstream usage inconsistent: ch=%d model=%s prompt=%d completion=%d cached=%d cacheHitRaw=%d (client view clamped)",
		info.ChannelId, info.UpstreamModelName, usage.PromptTokens, usage.CompletionTokens,
		usage.PromptTokensDetails.CachedTokens, usage.PromptCacheHitTokens))
}
