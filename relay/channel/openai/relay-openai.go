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
	"github.com/QuantumNous/new-api/officialfit"
	"github.com/QuantumNous/new-api/relay/channel/openrouter"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func sendStreamData(c *gin.Context, info *relaycommon.RelayInfo, data string, forceFormat bool, thinkToContent bool) error {
	if data == "" {
		return nil
	}

	// Global mask: when a channel maps the request model to a different
	// upstream id, hide it from every SSE chunk (the model field is echoed
	// per chunk). Applies before any other transformation so even the raw
	// passthrough branch cannot leak the upstream id.
	if maskUpstreamModelNameEnabled() && info != nil && info.OriginModelName != "" &&
		info.GetUpstreamModelName() != "" && info.OriginModelName != info.GetUpstreamModelName() {
		data = maskModelNameInStreamData(data, info)
	}

	suppressReasoningContent := shouldSuppressReasoningContent(info)
	if !forceFormat && !thinkToContent && !suppressReasoningContent {
		var streamResponse dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err == nil &&
			streamResponse.Usage != nil && streamResponse.Usage.BillingUsage != nil {
			return helper.ObjectData(c, &streamResponse)
		}
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
	var usage = &dto.Usage{}
	var lastStreamData string
	var pendingUsageData string
	var lastStreamHasUsage bool
	var lastStreamHasChoices bool
	var lastStreamHasFinish bool
	var lastStreamWithoutUsage string
	var secondLastStreamData string // 存储倒数第二个stream data，用于音频模型
	var deepSeekV4PendingFinalData string
	var kimiK3PendingFinalData string
	var streamErr *types.NewAPIError
	seenStreamToolCalls := make(map[string]struct{})
	var streamFunctionCallNames []string
	includeDeepSeekV4ReasoningUsage := !shouldSuppressReasoningContent(info)
	isV4OpenAIStream := info.RelayFormat == types.RelayFormatOpenAI && deepSeekV4FitEnabled(info)
	isK3OpenAIStream := info.RelayFormat == types.RelayFormatOpenAI && kimiK3FitEnabled(info)
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
					if !isV4OpenAIStream {
						// V4 streams consume the raw event through the fit
						// layer and never use the stripped copy.
						stripped, stripErr := stripStreamUsageData(data)
						if stripErr != nil {
							common.SysLog("error stripping stream usage; suppressing the client event: " + stripErr.Error())
							currentWithoutUsage = ""
						} else {
							currentWithoutUsage = stripped
						}
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
			if info.RelayFormat == types.RelayFormatOpenAI && deepSeekV4FitEnabled(info) {
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
					if patched, fitErr := fitDeepSeekV4StreamEvent(streamData, nil, false, false, includeDeepSeekV4ReasoningUsage); fitErr == nil {
						streamData = patched
					} else {
						common.SysLog("error fitting DeepSeek V4 stream event: " + fitErr.Error())
					}
					if err := HandleStreamFormat(c, info, streamData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
						common.SysLog("error handling stream format: " + err.Error())
						sr.Error(err)
					}
				}
			} else if info.RelayFormat == types.RelayFormatOpenAI && kimiK3FitEnabled(info) {
				// Official K3 attaches usage to choices[0] of the terminal
				// chunk and never emits a usage-only event. Hold the
				// terminal chunk so the official usage shape lands exactly
				// where the official endpoint puts it; forward every other
				// chunk with the aggregator's top-level usage and blanket
				// choice.logprobs stripped.
				if lastStreamHasFinish {
					kimiK3PendingFinalData = lastStreamData
				} else if lastStreamHasUsage && !lastStreamHasChoices {
					// Usage-only metadata: official has no such event, so
					// it is folded away rather than forwarded.
				} else {
					streamData := FitKimiK3StreamEventForAdapters(c, info, lastStreamData, nil, false)
					if streamData != "" {
						if err := HandleStreamFormat(c, info, streamData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
							common.SysLog("error handling stream format: " + err.Error())
							sr.Error(err)
						}
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

			if len(data) > 0 {
				if lastStreamData != "" {
					secondLastStreamData = lastStreamData
				}
			}
			// Audio models also use the previous frame as a usage fallback.
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
			observeStreamChoices(info, data, seenStreamToolCalls, &streamFunctionCallNames)
			if err := processTokenData(info, data, &responseTextBuilder, &toolCount); err != nil {
				logger.LogError(c, "error processing stream token data: "+err.Error())
				sr.Error(err)
			}
		}
	})
	if streamErr != nil {
		return usage, streamErr
	}
	for _, name := range streamFunctionCallNames {
		info.CountBillableToolCall(dto.BuildInCallFunctionCall, name)
	}
	if info.StreamStatus != nil && !info.StreamStatus.IsNormalEnd() {
		responseText := responseTextBuilder.String()
		if !containStreamUsage {
			usage = service.ResponseText2Usage(c, responseText, info.UpstreamModelName, info.GetEstimatePromptTokens())
			usage.CompletionTokens += toolCount * 7
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		} else {
			patchZeroCompletionUsage(c, info, usage, responseText, toolCount)
		}
		applyUsagePostProcessing(info, usage, common.StringToByteSlice(lastStreamData))

		// A caller that walked away mid-stream is not an upstream failure, and
		// there is nobody left to receive an error: settle the observed usage and
		// finish without synthesizing one. The scanner reports the same event as
		// scanner_error whenever the cancelled request context closes the upstream
		// body first, so the request context is the authoritative signal.
		if info.StreamStatus.EndReason == relaycommon.StreamEndReasonClientGone ||
			(c.Request != nil && c.Request.Context().Err() != nil) {
			return usage, nil
		}

		terminationErr := info.StreamStatus.EndError
		if terminationErr == nil {
			terminationErr = fmt.Errorf("upstream stream terminated: %s", info.StreamStatus.EndReason)
		} else {
			terminationErr = fmt.Errorf("upstream stream terminated (%s): %w", info.StreamStatus.EndReason, terminationErr)
		}
		options := make([]types.NewAPIErrorOptions, 0, 1)
		if c.Writer.Written() || info.ReceivedResponseCount > 0 {
			options = append(options, types.ErrOptionWithSkipRetry())
		}
		return usage, types.NewOpenAIError(
			terminationErr,
			types.ErrorCode("server_error"),
			http.StatusBadGateway,
			options...,
		)
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

	info.StreamStatus.RequireTerminal()

	// 处理最后的响应
	shouldSendLastResp := true
	if err := handleLastResponse(lastStreamData, &responseId, &createAt, &systemFingerprint, &model, &usage,
		&containStreamUsage, info, &shouldSendLastResp); err != nil {
		logger.LogError(c, fmt.Sprintf("error handling last response: %s, lastStreamData: [%s]", err.Error(), lastStreamData))
	}

	responseText := responseTextBuilder.String()
	// 部分兼容网关把完整的累计usage附在倒数第二个事件上，随后发送一个空的最后事件。
	// 仅当最后一个事件没有有效usage时，回退到倒数第二个事件的完整快照。
	usageFrame := lastStreamData
	if !containStreamUsage && secondLastStreamData != "" {
		var streamResp struct {
			Usage *dto.Usage `json:"usage"`
		}
		err := common.Unmarshal([]byte(secondLastStreamData), &streamResp)
		if err == nil && streamResp.Usage != nil &&
			streamResp.Usage.PromptTokens > 0 &&
			(streamResp.Usage.CompletionTokens > 0 || streamResp.Usage.TotalTokens > 0) {
			usage = dto.MergeUsageNonZero(usage, streamResp.Usage)
			containStreamUsage = true
			usageFrame = secondLastStreamData

			if common.DebugEnabled {
				logger.LogDebug(c, "usage extracted from second last SSE: PromptTokens=%d, CompletionTokens=%d, TotalTokens=%d, InputTokens=%d, OutputTokens=%d",
					usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
					usage.InputTokens, usage.OutputTokens)
			}
		}
	}

	if !containStreamUsage {
		usage = service.ResponseText2Usage(c, responseText, info.UpstreamModelName, info.GetEstimatePromptTokens())
		usage.CompletionTokens += toolCount * 7
	} else {
		patchZeroCompletionUsage(c, info, usage, responseText, toolCount)
	}

	applyUsagePostProcessing(info, usage, common.StringToByteSlice(usageFrame))

	if info.RelayFormat == types.RelayFormatOpenAI {
		switch {
		case isV4OpenAIStream:
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
				patched, fitErr := fitDeepSeekV4StreamEvent(streamData, usage, info.ShouldIncludeUsage, includeDeepSeekV4ReasoningUsage, includeDeepSeekV4ReasoningUsage)
				if fitErr != nil {
					logger.LogError(c, "error fitting final stream usage; forwarding original event: "+fitErr.Error())
				} else {
					streamData = patched
				}
				_ = sendStreamData(c, info, streamData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent)
			}
		case isK3OpenAIStream:
			// Official K3 renders usage inside choices[0] of the terminal
			// chunk. The held terminal chunk is emitted here with the official
			// usage shape injected; a usage-only event has already been folded
			// away by the per-chunk branch.
			streamData := lastStreamData
			if !lastStreamHasFinish && kimiK3PendingFinalData != "" {
				streamData = kimiK3PendingFinalData
			}
			if streamData != "" {
				streamData = FitKimiK3StreamEventForAdapters(c, info, streamData, usage, true)
				if streamData != "" {
					_ = sendStreamData(c, info, streamData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent)
				}
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
		if info.ShouldIncludeUsage && pendingUsageData != "" && !isV4OpenAIStream {
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

	HandleFinalResponse(c, info, lastStreamData, responseId, createAt, model, systemFingerprint, usage, containStreamUsage)

	return usage, nil
}

// observeStreamChoices collects billable function call names and records the
// finish reason facts used by health sampling from one parsed chunk.
func observeStreamChoices(info *relaycommon.RelayInfo, data string, seen map[string]struct{}, names *[]string) {
	var streamResponse dto.ChatCompletionsStreamResponse
	if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
		return
	}
	for _, choice := range streamResponse.Choices {
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			if *choice.FinishReason == constant.FinishReasonContentFilter {
				info.PerformanceBusinessRejection = true
			}
			info.StreamStatus.MarkCompleted()
		}
		for i, tc := range choice.Delta.ToolCalls {
			if !isValidStreamFunctionToolCall(tc) {
				continue
			}
			name := strings.TrimSpace(tc.Function.Name)
			toolIdx := i
			if tc.Index != nil {
				toolIdx = *tc.Index
			}
			fallbackKey := fmt.Sprintf("index\x00%d\x00%d\x00%s", choice.Index, toolIdx, name)
			activeKey := fmt.Sprintf("active\x00%d\x00%d\x00%s", choice.Index, toolIdx, name)
			callID := strings.TrimSpace(tc.ID)
			if callID != "" {
				idKey := fmt.Sprintf("id\x00%d\x00%s", choice.Index, callID)
				if _, ok := seen[idKey]; ok {
					continue
				}
				seen[idKey] = struct{}{}
				seen[activeKey] = struct{}{}
				if _, delayedID := seen[fallbackKey]; delayedID {
					delete(seen, fallbackKey)
					continue
				}
			} else {
				if _, ok := seen[fallbackKey]; ok {
					continue
				}
				if _, ok := seen[activeKey]; ok {
					continue
				}
				seen[fallbackKey] = struct{}{}
				seen[activeKey] = struct{}{}
			}
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
		// A non-JSON body (an HTML/text error page, or SSE frames the upstream
		// sent despite the non-stream request) parses as a generic syntax error
		// like "invalid character 'd' looking for beginning of value". Log a
		// masked preview so the offending channel/body is diagnosable: the
		// error is retried on another channel, so its caused-by evidence is
		// otherwise lost.
		logger.LogError(c, fmt.Sprintf("upstream response body is not JSON (channel #%d): %v, body: %s",
			info.ChannelId, err, common.LocalLogPreview(common.MaskSensitiveInfo(string(responseBody)))))
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	if cyberErr := service.NewOpenAICyberPolicyError(c, responseBody, resp.StatusCode, false, &simpleResponse.Usage); cyberErr != nil {
		writeCyberPolicyResponseError(c, info, resp, responseBody, cyberErr)
		return &simpleResponse.Usage, cyberErr
	}
	if oaiError := simpleResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}
	info.ObserveResponseModel(simpleResponse.Model)
	// The dual-path logprobs gate exists to catch aggregators that drop the
	// reasoning_content path. The official upstream is exempt: it decides
	// per-response whether a reasoning path exists (e.g. max_tokens exhausted
	// during thinking yields content-path-only), and dropping the official
	// response here would 502 the one channel that defines the contract.
	if !isOfficialDeepSeekV4Upstream(info) &&
		requiresDeepSeekV4ReasoningLogprobs(info) && !hasBothChatLogprobs(simpleResponse.Choices) {
		return nil, missingReasoningLogprobsError()
	}
	for _, choice := range simpleResponse.Choices {
		if choice.FinishReason == constant.FinishReasonContentFilter {
			info.PerformanceBusinessRejection = true
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
		fallbackUsage := &dto.Usage{
			PromptTokens:     info.GetEstimatePromptTokens(),
			CompletionTokens: completionTokens,
			TotalTokens:      info.GetEstimatePromptTokens() + completionTokens,
		}
		simpleResponse.Usage = *fallbackUsage
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
			encodedUsage, err := common.Marshal(helper.UsageForClient(&simpleResponse.Usage))
			if err != nil {
				return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
			}
			// Splice the usage value so upstream key order survives; the map
			// rewrite remains only as a fallback.
			if patched, ok := replaceTopLevelJSONValue(responseBody, "usage", encodedUsage); ok {
				responseBody = patched
			} else {
				var bodyMap map[string]any
				if err = common.Unmarshal(responseBody, &bodyMap); err != nil {
					return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
				}
				bodyMap["usage"] = helper.UsageForClient(&simpleResponse.Usage)
				responseBody, _ = common.Marshal(bodyMap)
			}
		}
		if forceFormat {
			responseBody, err = common.Marshal(helper.OpenAITextResponseForClient(&simpleResponse))
			if err != nil {
				return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
			}
		} else if simpleResponse.Usage.BillingUsage != nil {
			// Upstream extensions are preserved in the normal path, but the
			// internal billing extension must never cross the client boundary.
			var bodyMap map[string]any
			if err = common.Unmarshal(responseBody, &bodyMap); err != nil {
				return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
			}
			if rawUsage, ok := bodyMap["usage"]; ok {
				var usageMap map[string]any
				if usageMap, ok = rawUsage.(map[string]any); ok {
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
		convertResult, err := service.ConvertResponse(c, info, types.RelayFormatClaude, &simpleResponse)
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
		convertResult, err := service.ConvertResponse(c, info, types.RelayFormatGemini, &simpleResponse)
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

	if info.RelayFormat == types.RelayFormatOpenAI && deepSeekV4FitEnabled(info) {
		// Apply the V4 client contract after both passthrough and ForceFormat
		// paths so generic usage extensions cannot escape either route.
		fitted, fitErr := fitDeepSeekV4TextResponseBody(responseBody, &simpleResponse.Usage, !suppressReasoningContent, deepSeekV4RequestAllowsToolCalls(info))
		if fitErr != nil {
			return nil, types.NewOpenAIError(fitErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		responseBody = fitted
	}

	if info.RelayFormat == types.RelayFormatOpenAI && kimiK3FitEnabled(info) {
		// The K3 fit runs after conversion as well, so aggregator usage
		// extensions cannot escape either the passthrough or the ForceFormat
		// route.
		responseBody = FitKimiK3TextResponseBodyForAdapters(c, info, responseBody, &simpleResponse.Usage)
	}

	if info.RelayFormat == types.RelayFormatOpenAI {
		// Global mask: hide the upstream-mapped model id from the client.
		// Byte-level rewrite preserves the upstream key order; the fit
		// branch above already ran, so this only affects the final body.
		if patched, ok := maskModelNameInResponse(responseBody, info); ok {
			responseBody = patched
		}
	}

	service.IOCopyBytesGracefully(c, resp, responseBody)

	return &simpleResponse.Usage, nil
}

func requiresDeepSeekV4ReasoningLogprobs(info *relaycommon.RelayInfo) bool {
	if info == nil || info.RelayMode != relayconstant.RelayModeChatCompletions {
		return false
	}
	modelName := strings.ToLower(strings.TrimSpace(info.OriginModelName))
	// The "-none" suffix alias disables thinking and therefore needs no logprobs pair.
	if officialfit.FamilyOf(modelName) != officialfit.FamilyDeepSeekV4 || strings.HasSuffix(modelName, "-none") {
		return false
	}
	profile, ok := info.UserSetting.OfficialFitProfileFor(info.OriginModelName)
	if !ok || !profile.Validate {
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

// isOfficialDeepSeekV4Upstream reports whether the response came from the
// official DeepSeek channel (type 43). Official passthrough defines the fit
// contract, so response-level fit gates must not reject it.
func isOfficialDeepSeekV4Upstream(info *relaycommon.RelayInfo) bool {
	return info != nil && info.ChannelMeta != nil && info.ChannelType == constant.ChannelTypeDeepSeek
}

func missingReasoningLogprobsError() *types.NewAPIError {
	return types.NewOpenAIError(
		errors.New("upstream did not return both content and reasoning_content logprobs"),
		types.ErrorCodeChannelUnsupportedFeature,
		http.StatusBadGateway,
	)
}
