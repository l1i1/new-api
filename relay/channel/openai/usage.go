package openai

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
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
