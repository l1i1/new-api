package openai

import (
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
	}
}

func stripReasoningContentFromResponseBody(body []byte) ([]byte, error) {
	var payload map[string]interface{}
	if err := common.Unmarshal(body, &payload); err != nil {
		return body, err
	}
	rawChoices, ok := payload["choices"]
	if !ok {
		return body, nil
	}
	encodedChoices, err := common.Marshal(rawChoices)
	if err != nil {
		return body, err
	}
	var choices []map[string]interface{}
	if err := common.Unmarshal(encodedChoices, &choices); err != nil {
		return body, err
	}
	for _, choice := range choices {
		message, ok := choice["message"].(map[string]interface{})
		if !ok {
			continue
		}
		delete(message, "reasoning_content")
		delete(message, "reasoning")
	}
	payload["choices"] = choices
	stripped, err := common.Marshal(payload)
	if err != nil {
		return body, err
	}
	return stripped, nil
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
