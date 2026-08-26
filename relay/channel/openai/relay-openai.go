package openai

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel/openrouter"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func sendStreamData(c *gin.Context, info *relaycommon.RelayInfo, data string, forceFormat bool, thinkToContent bool) error {
	if data == "" {
		return nil
	}

	suppressReasoningContent := shouldSuppressReasoningContent(info)
	if !forceFormat && !thinkToContent && !suppressReasoningContent {
		return helper.StringData(c, data)
	}

	var lastStreamResponse dto.ChatCompletionsStreamResponse
	if err := common.UnmarshalJsonStr(data, &lastStreamResponse); err != nil {
		return err
	}
	if suppressReasoningContent {
		for i := range lastStreamResponse.Choices {
			lastStreamResponse.Choices[i].Delta.ReasoningContent = nil
			lastStreamResponse.Choices[i].Delta.Reasoning = nil
			stripReasoningLogprobs(lastStreamResponse.Choices[i].Logprobs)
		}
	}

	if !thinkToContent {
		return helper.ObjectData(c, lastStreamResponse)
	}

	hasThinkingContent := false
	hasContent := false
	var thinkingContent strings.Builder
	for _, choice := range lastStreamResponse.Choices {
		if len(choice.Delta.GetReasoningContent()) > 0 {
			hasThinkingContent = true
			thinkingContent.WriteString(choice.Delta.GetReasoningContent())
		}
		if len(choice.Delta.GetContentString()) > 0 {
			hasContent = true
		}
	}

	// Handle think to content conversion
	if info.ThinkingContentInfo.IsFirstThinkingContent {
		if hasThinkingContent {
			response := lastStreamResponse.Copy()
			for i := range response.Choices {
				// send `think` tag with thinking content
				response.Choices[i].Delta.SetContentString("<think>\n" + thinkingContent.String())
				response.Choices[i].Delta.ReasoningContent = nil
				response.Choices[i].Delta.Reasoning = nil
			}
			info.ThinkingContentInfo.IsFirstThinkingContent = false
			info.ThinkingContentInfo.HasSentThinkingContent = true
			return helper.ObjectData(c, response)
		}
	}

	if lastStreamResponse.Choices == nil || len(lastStreamResponse.Choices) == 0 {
		return helper.ObjectData(c, lastStreamResponse)
	}

	// Process each choice
	for i, choice := range lastStreamResponse.Choices {
		// Handle transition from thinking to content
		// only send `</think>` tag when previous thinking content has been sent
		if hasContent && !info.ThinkingContentInfo.SendLastThinkingContent && info.ThinkingContentInfo.HasSentThinkingContent {
			response := lastStreamResponse.Copy()
			for j := range response.Choices {
				response.Choices[j].Delta.SetContentString("\n</think>\n")
				response.Choices[j].Delta.ReasoningContent = nil
				response.Choices[j].Delta.Reasoning = nil
			}
			info.ThinkingContentInfo.SendLastThinkingContent = true
			helper.ObjectData(c, response)
		}

		// Convert reasoning content to regular content if any
		if len(choice.Delta.GetReasoningContent()) > 0 {
			lastStreamResponse.Choices[i].Delta.SetContentString(choice.Delta.GetReasoningContent())
			lastStreamResponse.Choices[i].Delta.ReasoningContent = nil
			lastStreamResponse.Choices[i].Delta.Reasoning = nil
		} else if !hasThinkingContent && !hasContent {
			// flush thinking content
			lastStreamResponse.Choices[i].Delta.ReasoningContent = nil
			lastStreamResponse.Choices[i].Delta.Reasoning = nil
		}
	}

	return helper.ObjectData(c, lastStreamResponse)
}

func OaiStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, "invalid response or response body")
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	defer service.CloseResponseBodyGracefully(resp)

	model := info.UpstreamModelName
	var responseId string
	var createAt int64 = 0
	var systemFingerprint string
	var containStreamUsage bool
	var responseTextBuilder strings.Builder
	var toolCount int
	var hasContentOutput bool
	var hasToolOutput bool
	var usage = &dto.Usage{}
	var lastStreamData string
	var pendingUsageData string
	var lastStreamHasUsage bool
	var lastStreamHasChoices bool
	var lastStreamHasFinish bool
	var lastStreamWithoutUsage string
	var secondLastStreamData string // 存储倒数第二个stream data，用于音频模型
	var deepSeekV4PendingFinalData string
	var streamErr *types.NewAPIError
	seenStreamToolCalls := make(map[string]struct{})
	var streamFunctionCallNames []string
	includeDeepSeekV4ReasoningUsage := !shouldSuppressReasoningContent(info)

	// 检查是否为音频模型
	isAudioModel := strings.Contains(strings.ToLower(model), "audio")

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		if streamErr != nil {
			sr.Stop(streamErr)
			return
		}
		currentHasUsage := false
		currentHasChoices := false
		currentHasFinish := false
		currentWithoutUsage := ""
		if len(data) > 0 {
			var streamResp struct {
				Choices []dto.ChatCompletionsStreamResponseChoice `json:"choices"`
				Usage   *dto.Usage                                `json:"usage"`
			}
			if err := common.Unmarshal(common.StringToByteSlice(data), &streamResp); err == nil {
				for _, choice := range streamResp.Choices {
					if choice.FinishReason != nil && *choice.FinishReason != "" {
						currentHasFinish = true
						info.StreamFinishReason = *choice.FinishReason
					}
				}
				if streamResp.Usage != nil {
					currentHasUsage = true
					currentHasChoices = len(streamResp.Choices) > 0
					stripped, stripErr := stripStreamUsageData(data)
					if stripErr != nil {
						common.SysLog("error stripping stream usage; suppressing the client event: " + stripErr.Error())
						currentWithoutUsage = ""
					} else {
						currentWithoutUsage = stripped
					}
					if service.ValidUsage(streamResp.Usage) {
						usage = dto.MergeUsage(usage, streamResp.Usage)
						containStreamUsage = true
					}
				}
			}
			// Apply provider-specific cache extraction before the previous
			// usage event is emitted, including caches carried by a later
			// non-usage event (for example Moonshot choices[].usage).
			if containStreamUsage {
				applyUsagePostProcessing(info, usage, common.StringToByteSlice(data))
			}
			if cyberErr := service.NewOpenAICyberPolicyError(c, common.StringToByteSlice(data), resp.StatusCode, true, usage); cyberErr != nil {
				streamErr = cyberErr
				writeCyberPolicyStreamError(c, info, cyberErr)
				sr.Stop(streamErr)
				return
			}
			if upstreamErr := service.NormalizeOpenAIStreamError(common.StringToByteSlice(data), resp.StatusCode); upstreamErr != nil {
				streamErr = upstreamErr
				if !c.Writer.Written() {
					c.Status(streamErr.StatusCode)
				}
				sr.Stop(streamErr)
				return
			}
		}

		if lastStreamData != "" {
			if info.RelayFormat == types.RelayFormatOpenAI && isDeepSeekV4ChatModel(info) {
				// Some compatible providers attach cumulative usage to every
				// chunk. Keep it internal and emit usage exactly once on the
				// terminal finish chunk, matching the official V4 stream.
				if lastStreamHasFinish {
					deepSeekV4PendingFinalData = lastStreamData
				} else if lastStreamHasUsage && !lastStreamHasChoices {
					// Usage-only metadata is folded into the held final chunk.
				} else {
					streamData := lastStreamData
					// Official chunks carry an explicit null usage on every
					// non-terminal event.
					if patched, fitErr := fitDeepSeekV4StreamEvent(streamData, nil, false, false); fitErr == nil {
						streamData = patched
					} else {
						common.SysLog("error fitting DeepSeek V4 stream event: " + fitErr.Error())
					}
					if err := HandleStreamFormat(c, info, streamData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
						common.SysLog("error handling stream format: " + err.Error())
						sr.Error(err)
					}
				}
			} else if info.RelayFormat == types.RelayFormatOpenAI && lastStreamHasUsage {
				// Preserve choices carried beside usage, but keep the usage itself
				// pending so clients receive only one cumulative event.
				pendingUsageData = lastStreamData
				if lastStreamHasChoices {
					if lastStreamWithoutUsage != "" {
						if err := HandleStreamFormat(c, info, lastStreamWithoutUsage, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
							common.SysLog("error handling stream format: " + err.Error())
							sr.Error(err)
						}
					}
					usageOnlyData, err := stripStreamChoicesData(lastStreamData)
					if err != nil {
						common.SysLog("error stripping stream choices; suppressing duplicate pending event: " + err.Error())
						pendingUsageData = ""
					} else {
						pendingUsageData = usageOnlyData
					}
				}
			} else {
				streamData := lastStreamData
				if info.RelayFormat == types.RelayFormatOpenAI {
					patched, err := patchStreamUsageData(streamData, usage)
					if err != nil {
						common.SysLog("error patching stream usage; forwarding original event: " + err.Error())
					} else {
						streamData = patched
					}
				}
				if err := HandleStreamFormat(c, info, streamData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
					common.SysLog("error handling stream format: " + err.Error())
					sr.Error(err)
				}
			}

			// 对音频模型，保存倒数第二个stream data
			if isAudioModel && lastStreamData != "" {
				secondLastStreamData = lastStreamData
			}
		}
		if len(data) > 0 {
			lastStreamData = data
			lastStreamHasUsage = currentHasUsage
			lastStreamHasChoices = currentHasChoices
			lastStreamHasFinish = currentHasFinish
			lastStreamWithoutUsage = currentWithoutUsage
			collectStreamFunctionCallNames(data, seenStreamToolCalls, &streamFunctionCallNames)
			if err := processTokenData(info.RelayMode, data, &responseTextBuilder, &toolCount); err != nil {
				logger.LogError(c, "error processing stream token data: "+err.Error())
				sr.Error(err)
			}
			var output dto.ChatCompletionsStreamResponse
			if err := common.UnmarshalJsonStr(data, &output); err == nil {
				for _, choice := range output.Choices {
					if strings.TrimSpace(choice.Delta.GetContentString()) != "" {
						hasContentOutput = true
					}
					for _, toolCall := range choice.Delta.ToolCalls {
						if isValidStreamFunctionToolCall(toolCall) {
							hasToolOutput = true
							break
						}
					}
				}
			}
		}
	})
	if streamErr != nil {
		return usage, streamErr
	}
	if info.StreamStatus == nil || info.StreamStatus.EndReason != relaycommon.StreamEndReasonDone {
		return usage, incompleteStreamError(info, c.Writer.Written())
	}
	if info.RelayMode == relayconstant.RelayModeChatCompletions && !hasContentOutput && !hasToolOutput {
		return usage, emptyChatCompletionError(c.Writer.Written())
	}
	if isDeepSeekV4ChatModel(info) && strings.TrimSpace(info.StreamFinishReason) == "" {
		return usage, missingStreamFinishReasonError(c.Writer.Written())
	}

	// 对音频模型，从倒数第二个stream data中提取usage信息
	if isAudioModel && secondLastStreamData != "" {
		var streamResp struct {
			Usage *dto.Usage `json:"usage"`
		}
		err := common.Unmarshal([]byte(secondLastStreamData), &streamResp)
		if err == nil && streamResp.Usage != nil && service.ValidUsage(streamResp.Usage) {
			usage = dto.MergeUsage(usage, streamResp.Usage)
			containStreamUsage = true

			if common.DebugEnabled {
				logger.LogDebug(c, "Audio model usage extracted from second last SSE: PromptTokens=%d, CompletionTokens=%d, TotalTokens=%d, InputTokens=%d, OutputTokens=%d",
					usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
					usage.InputTokens, usage.OutputTokens)
			}
		}
	}

	// 处理最后的响应
	shouldSendLastResp := true
	if err := handleLastResponse(lastStreamData, &responseId, &createAt, &systemFingerprint, &model, &usage,
		&containStreamUsage, info, &shouldSendLastResp); err != nil {
		logger.LogError(c, fmt.Sprintf("error handling last response: %s, lastStreamData: [%s]", err.Error(), lastStreamData))
	}

	responseText := responseTextBuilder.String()
	if !containStreamUsage {
		usage = service.ResponseText2Usage(c, responseText, info.UpstreamModelName, info.GetEstimatePromptTokens())
		usage.CompletionTokens += toolCount * 7
	} else {
		patchZeroCompletionUsage(c, info, usage, responseText, toolCount)
	}

	applyUsagePostProcessing(info, usage, common.StringToByteSlice(lastStreamData))

	if info.RelayFormat == types.RelayFormatOpenAI {
		switch {
		case isDeepSeekV4ChatModel(info):
			// Official DeepSeek V4 emits usage inside the final chunk that
			// carries finish_reason and never sends a usage-only event. When
			// the upstream already matched that shape, the raw chunk is
			// forwarded verbatim; otherwise the official usage shape is
			// injected into the final chunk.
			streamData := lastStreamData
			if !lastStreamHasFinish && deepSeekV4PendingFinalData != "" {
				streamData = deepSeekV4PendingFinalData
			}
			if streamData != "" {
				patched, fitErr := fitDeepSeekV4StreamEvent(streamData, usage, info.ShouldIncludeUsage, includeDeepSeekV4ReasoningUsage)
				if fitErr != nil {
					logger.LogError(c, "error fitting final stream usage; forwarding original event: "+fitErr.Error())
				} else {
					streamData = patched
				}
				_ = sendStreamData(c, info, streamData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent)
			}
		case lastStreamHasUsage:
			pendingUsageData = lastStreamData
			if lastStreamHasChoices {
				if lastStreamWithoutUsage != "" {
					_ = sendStreamData(c, info, lastStreamWithoutUsage, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent)
				}
				usageOnlyData, err := stripStreamChoicesData(lastStreamData)
				if err != nil {
					logger.LogError(c, "error stripping final stream choices; suppressing duplicate pending event: "+err.Error())
					pendingUsageData = ""
				} else {
					pendingUsageData = usageOnlyData
				}
			}
		default:
			if shouldSendLastResp {
				streamData := lastStreamData
				patched, err := patchStreamUsageData(streamData, usage)
				if err != nil {
					logger.LogError(c, "error patching final stream usage: "+err.Error())
				} else {
					streamData = patched
				}
				_ = sendStreamData(c, info, streamData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent)
			}
		}
		if info.ShouldIncludeUsage && pendingUsageData != "" && !isDeepSeekV4ChatModel(info) {
			streamData := pendingUsageData
			patched, err := patchStreamUsageData(streamData, usage)
			if err != nil {
				logger.LogError(c, "error patching pending stream usage; forwarding original event: "+err.Error())
			} else {
				streamData = patched
			}
			_ = sendStreamData(c, info, streamData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent)
		}
	}

	for _, name := range streamFunctionCallNames {
		info.CountBillableToolCall(dto.BuildInCallFunctionCall, name)
	}

	HandleFinalResponse(c, info, lastStreamData, responseId, createAt, model, systemFingerprint, usage, containStreamUsage)

	return usage, nil
}

func collectStreamFunctionCallNames(data string, seen map[string]struct{}, names *[]string) {
	var streamResponse dto.ChatCompletionsStreamResponse
	if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
		return
	}
	for _, choice := range streamResponse.Choices {
		for i, tc := range choice.Delta.ToolCalls {
			if !isValidStreamFunctionToolCall(tc) {
				continue
			}
			name := strings.TrimSpace(tc.Function.Name)
			toolIdx := i
			if tc.Index != nil {
				toolIdx = *tc.Index
			}
			key := fmt.Sprintf("%d-%d", choice.Index, toolIdx)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			*names = append(*names, name)
		}
	}
}

func OpenaiHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	var simpleResponse dto.OpenAITextResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	logger.LogDebug(c, "upstream response body: %s", common.LocalLogPreview(common.MaskSensitiveInfo(string(responseBody))))
	// Unmarshal to simpleResponse
	if info.ChannelType == constant.ChannelTypeOpenRouter && info.ChannelOtherSettings.IsOpenRouterEnterprise() {
		// 尝试解析为 openrouter enterprise
		var enterpriseResponse openrouter.OpenRouterEnterpriseResponse
		err = common.Unmarshal(responseBody, &enterpriseResponse)
		if err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		if enterpriseResponse.Success {
			responseBody = enterpriseResponse.Data
		} else {
			logger.LogError(c, fmt.Sprintf("openrouter enterprise response success=false, data: %s", enterpriseResponse.Data))
			return nil, types.NewOpenAIError(fmt.Errorf("openrouter response success=false"), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
	}

	err = common.Unmarshal(responseBody, &simpleResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	if cyberErr := service.NewOpenAICyberPolicyError(c, responseBody, resp.StatusCode, false, &simpleResponse.Usage); cyberErr != nil {
		writeCyberPolicyResponseError(c, info, resp, responseBody, cyberErr)
		return &simpleResponse.Usage, cyberErr
	}
	if oaiError := simpleResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}
	if requiresDeepSeekV4ReasoningLogprobs(info) && !hasBothChatLogprobs(simpleResponse.Choices) {
		return nil, missingReasoningLogprobsError()
	}
	if info.RelayMode == relayconstant.RelayModeChatCompletions && !hasUsableChatCompletionOutput(simpleResponse.Choices) &&
		!(isDeepSeekV4ChatModel(info) && !shouldSuppressReasoningContent(info) && hasDeepSeekV4ReasoningOnlyLengthOutput(simpleResponse.Choices)) {
		return nil, emptyChatCompletionError()
	}

	for _, choice := range simpleResponse.Choices {
		if choice.FinishReason == constant.FinishReasonContentFilter {
			common.SetContextKey(c, constant.ContextKeyAdminRejectReason, "openai_finish_reason=content_filter")
			break
		}
	}

	var responseTextBuilder strings.Builder
	toolCount := 0
	validToolCallNames := make([]string, 0)
	for _, choice := range simpleResponse.Choices {
		content := choice.Message.StringContent()
		responseTextBuilder.WriteString(content)
		responseTextBuilder.WriteString(choice.Message.GetReasoningContent())
		toolCalls := choice.Message.ParseToolCalls()
		for _, tc := range toolCalls {
			if !isValidFunctionToolCall(tc) {
				continue
			}
			toolCount++
			validToolCallNames = append(validToolCallNames, strings.TrimSpace(tc.Function.Name))
		}
	}
	for _, name := range validToolCallNames {
		info.CountBillableToolCall(dto.BuildInCallFunctionCall, name)
	}

	forceFormat := false
	if info.ChannelSetting.ForceFormat {
		forceFormat = true
	}
	suppressReasoningContent := shouldSuppressReasoningContent(info)
	if suppressReasoningContent {
		stripReasoningContentFromTextResponse(&simpleResponse)
	}

	usageModified := false
	if simpleResponse.Usage.PromptTokens == 0 {
		completionTokens := simpleResponse.Usage.CompletionTokens
		if completionTokens == 0 {
			for _, choice := range simpleResponse.Choices {
				ctkm := service.CountTextToken(choice.Message.StringContent()+choice.Message.GetReasoningContent(), info.UpstreamModelName)
				completionTokens += ctkm
			}
		}
		simpleResponse.Usage = dto.Usage{
			PromptTokens:     info.GetEstimatePromptTokens(),
			CompletionTokens: completionTokens,
			TotalTokens:      info.GetEstimatePromptTokens() + completionTokens,
		}
		usageModified = true
	}
	if patchZeroCompletionUsage(c, info, &simpleResponse.Usage, responseTextBuilder.String(), toolCount) {
		usageModified = true
	}

	applyUsagePostProcessing(info, &simpleResponse.Usage, responseBody)

	switch info.RelayFormat {
	case types.RelayFormatOpenAI:
		if suppressReasoningContent && !forceFormat {
			responseBody, err = stripReasoningContentFromResponseBody(responseBody)
			if err != nil {
				return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
			}
		}
		if usageModified {
			var bodyMap map[string]interface{}
			err = common.Unmarshal(responseBody, &bodyMap)
			if err != nil {
				return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
			}
			bodyMap["usage"] = helper.UsageForClient(&simpleResponse.Usage)
			responseBody, _ = common.Marshal(bodyMap)
		}
		if forceFormat {
			responseBody, err = common.Marshal(helper.OpenAITextResponseForClient(&simpleResponse))
			if err != nil {
				return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
			}
		} else if simpleResponse.Usage.BillingUsage != nil {
			// Upstream extensions are preserved in the normal path, but the
			// internal billing extension must never cross the client boundary.
			var bodyMap map[string]interface{}
			if err = common.Unmarshal(responseBody, &bodyMap); err != nil {
				return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
			}
			if rawUsage, ok := bodyMap["usage"]; ok {
				var usageMap map[string]interface{}
				if usageMap, ok = rawUsage.(map[string]interface{}); ok {
					delete(usageMap, "billing_usage")
					bodyMap["usage"] = usageMap
				}
			}
			responseBody, err = common.Marshal(bodyMap)
			if err != nil {
				return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
			}
		} else {
			break
		}
	case types.RelayFormatClaude:
		convertResult, err := relayconvert.ConvertResponse(c, info, types.RelayFormatClaude, &simpleResponse)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		claudeResp, ok := convertResult.Value.(*dto.ClaudeResponse)
		if !ok {
			return nil, types.NewError(fmt.Errorf("expected Claude response, got %T", convertResult.Value), types.ErrorCodeBadResponseBody)
		}
		claudeRespStr, err := common.Marshal(helper.ClaudeResponseForClient(claudeResp))
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		responseBody = claudeRespStr
	case types.RelayFormatGemini:
		convertResult, err := relayconvert.ConvertResponse(c, info, types.RelayFormatGemini, &simpleResponse)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		geminiResp, ok := convertResult.Value.(*dto.GeminiChatResponse)
		if !ok {
			return nil, types.NewError(fmt.Errorf("expected Gemini response, got %T", convertResult.Value), types.ErrorCodeBadResponseBody)
		}
		geminiRespStr, err := common.Marshal(helper.GeminiResponseForClient(geminiResp))
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		responseBody = geminiRespStr
	}

	if info.RelayFormat == types.RelayFormatOpenAI && isDeepSeekV4ChatModel(info) {
		// Apply the V4 client contract after both passthrough and ForceFormat
		// paths so generic usage extensions cannot escape either route.
		fitted, fitErr := fitDeepSeekV4TextResponseBody(responseBody, &simpleResponse.Usage, !suppressReasoningContent)
		if fitErr != nil {
			return nil, types.NewOpenAIError(fitErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		responseBody = fitted
	}

	service.IOCopyBytesGracefully(c, resp, responseBody)

	return &simpleResponse.Usage, nil
}

func hasUsableChatCompletionOutput(choices []dto.OpenAITextResponseChoice) bool {
	for _, choice := range choices {
		if strings.TrimSpace(choice.Message.StringContent()) != "" {
			return true
		}
		for _, call := range choice.Message.ParseToolCalls() {
			if isValidFunctionToolCall(call) {
				return true
			}
		}
	}
	return false
}

func hasDeepSeekV4ReasoningOnlyLengthOutput(choices []dto.OpenAITextResponseChoice) bool {
	for _, choice := range choices {
		if choice.FinishReason == constant.FinishReasonLength && strings.TrimSpace(choice.Message.GetReasoningContent()) != "" {
			return true
		}
	}
	return false
}

func emptyChatCompletionError(committed ...bool) *types.NewAPIError {
	options := make([]types.NewAPIErrorOptions, 0, 1)
	if len(committed) > 0 && committed[0] {
		options = append(options, types.ErrOptionWithSkipRetry())
	}
	return types.NewOpenAIError(
		errors.New("upstream returned empty final content"),
		types.ErrorCode("server_error"),
		http.StatusBadGateway,
		options...,
	)
}

func incompleteStreamError(info *relaycommon.RelayInfo, committed bool) *types.NewAPIError {
	message := "upstream stream did not complete"
	if info != nil && info.StreamStatus != nil {
		switch info.StreamStatus.EndReason {
		case relaycommon.StreamEndReasonEOF:
			message = "upstream stream ended before [DONE]"
		case relaycommon.StreamEndReasonTimeout:
			message = "upstream stream timed out before [DONE]"
		}
	}
	options := make([]types.NewAPIErrorOptions, 0, 1)
	if committed {
		options = append(options, types.ErrOptionWithSkipRetry())
	}
	return types.NewOpenAIError(
		errors.New(message),
		types.ErrorCode("server_error"),
		http.StatusBadGateway,
		options...,
	)
}

func missingStreamFinishReasonError(committed bool) *types.NewAPIError {
	options := make([]types.NewAPIErrorOptions, 0, 1)
	if committed {
		options = append(options, types.ErrOptionWithSkipRetry())
	}
	return types.NewOpenAIError(
		errors.New("upstream stream ended without finish_reason"),
		types.ErrorCode("server_error"),
		http.StatusBadGateway,
		options...,
	)
}

func requiresDeepSeekV4ReasoningLogprobs(info *relaycommon.RelayInfo) bool {
	if info == nil || info.RelayMode != relayconstant.RelayModeChatCompletions {
		return false
	}
	modelName := strings.ToLower(strings.TrimSpace(info.OriginModelName))
	if !strings.HasPrefix(modelName, "deepseek-v4-") || strings.HasSuffix(modelName, "-none") {
		return false
	}
	request, ok := info.Request.(*dto.GeneralOpenAIRequest)
	if !ok || request.LogProbs == nil || !*request.LogProbs || len(request.Tools) > 0 {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(request.ReasoningEffort), "none") || deepSeekThinkingDisabled(request.THINKING) {
		return false
	}
	return true
}

func deepSeekThinkingDisabled(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var thinking struct {
		Type string `json:"type"`
	}
	if err := common.Unmarshal(raw, &thinking); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(thinking.Type), "disabled")
}

func hasBothChatLogprobs(choices []dto.OpenAITextResponseChoice) bool {
	hasContent, hasReasoning := false, false
	for _, choice := range choices {
		if choice.Logprobs == nil {
			continue
		}
		logprobs, ok := (*choice.Logprobs).(map[string]any)
		if !ok {
			continue
		}
		if content, ok := logprobs["content"].([]any); ok && len(content) > 0 {
			hasContent = true
		}
		if reasoning, ok := logprobs["reasoning_content"].([]any); ok && len(reasoning) > 0 {
			hasReasoning = true
		}
	}
	return hasContent && hasReasoning
}

func missingReasoningLogprobsError() *types.NewAPIError {
	return types.NewOpenAIError(
		errors.New("upstream did not return both content and reasoning_content logprobs"),
		types.ErrorCodeChannelUnsupportedFeature,
		http.StatusBadGateway,
	)
}
