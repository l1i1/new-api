package openai

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/officialfit"
	"github.com/QuantumNous/new-api/relay/channel/openrouter"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/streammerge"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// sendStreamData writes one SSE event to the client. When the channel enables
// stream coalescing (StreamCoalesceMs) consecutive same-kind text deltas are
// merged first: Kimi-family upstreams emit ~2 characters per event with a
// ~200-byte JSON envelope, so an uncoalesced stream costs roughly 100x the
// payload in framing (measured 2026-10-02: 494 KB for 4.5 KB of text).
func sendStreamData(c *gin.Context, info *relaycommon.RelayInfo, data string, forceFormat bool, thinkToContent bool) error {
	if data == "" {
		return nil
	}
	if info != nil && info.StreamCoalescer.Enabled() {
		for _, merged := range info.StreamCoalescer.Add(data) {
			if err := writeStreamData(c, info, merged, forceFormat, thinkToContent); err != nil {
				return err
			}
		}
		return nil
	}
	return writeStreamData(c, info, data, forceFormat, thinkToContent)
}

// writeStreamData is the uncoalesced event writer.
func writeStreamData(c *gin.Context, info *relaycommon.RelayInfo, data string, forceFormat bool, thinkToContent bool) error {
	if data == "" {
		return nil
	}
	// Channel-configured response rewrite, applied to every outgoing event
	// before any other transformation so the parsed copy and the forwarded
	// copy agree. A misbehaving document leaves the event untouched.
	if rewritten, err := relaycommon.ApplyResponseOverride(common.StringToByteSlice(data), info); err != nil {
		logger.LogDebug(c, "response override skipped for channel #%d: %v", info.ChannelId, err)
	} else if len(rewritten) > 0 {
		data = string(rewritten)
	}
	if data == "" {
		return nil
	}
	// Observability: whether the response path ever carried reasoning content.
	// Recorded before any transformation so the request log can distinguish
	// "upstream produced no reasoning" from "the gateway stripped it".
	if info != nil && !info.ReasoningContentSeen && strings.Contains(data, `"reasoning_content"`) {
		info.ReasoningContentSeen = true
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

func resetContinuationCoalescer(info *relaycommon.RelayInfo) {
	if info == nil {
		return
	}
	if ms := info.ChannelSetting.StreamCoalesceMs; ms > 0 {
		info.StreamCoalescer = streammerge.New(streammerge.Config{MaxDelay: time.Duration(ms) * time.Millisecond})
	} else {
		info.StreamCoalescer = nil
	}
}

func continuationReasoningLimit(info *relaycommon.RelayInfo) int {
	if info == nil || info.ChatContinuation == nil {
		return 0
	}
	limit := 0
	if cfg := info.ChatContinuation.Abnormal; cfg != nil && cfg.MaxReasoningBytes > limit {
		limit = cfg.MaxReasoningBytes
	}
	if cfg := info.ChatContinuation.Missing; cfg != nil && cfg.MaxReasoningBytes > limit {
		limit = cfg.MaxReasoningBytes
	}
	return limit
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
	// Captured reasoning for the abnormal-finish continuation mode. The cap is
	// enforced here so a 900s thinking round cannot grow process memory without
	// bound; if the trace is truncated the continuation is skipped.
	var reasoningTextBuilder strings.Builder
	reasoningBytes := 0
	reasoningTruncated := false
	visibleContentChars := 0
	streamChoiceCount := 0
	var toolCount int
	var usage = &dto.Usage{}
	var lastStreamData string
	var pendingUsageData string
	var lastStreamHasUsage bool
	var lastStreamHasChoices bool
	var lastStreamHasFinish bool
	var lastStreamFinishReason string
	// deferredTerminalData holds a provider terminal chunk that was NOT forwarded
	// because the handler may still repair the answer. Sending a finish_reason to
	// the caller is final: content that arrives afterwards corrupts the response
	// (2026-10-29 review, reproduced: length finish -> resumed content -> stop).
	// Every repair path renders its own single terminal frame; the normal path
	// forwards this chunk just before finishing.
	deferredTerminalData := ""
	// finishFrameForwarded records that the provider's own terminal frame already
	// reached the client, so the truncated close below must not synthesize a
	// second one with a contradictory finish_reason.
	finishFrameForwarded := false
	var lastStreamWithoutUsage string
	var secondLastStreamData string // 存储倒数第二个stream data，用于音频模型
	var deepSeekV4PendingFinalData string
	var kimiK3PendingFinalData string
	var streamErr *types.NewAPIError
	// streamAbnormalFinish keeps the first non-terminal finish reason a provider
	// used to signal a failed generation (e.g. "error" from aggregators,
	// "engine_overloaded" from the official Moonshot endpoint). Such a stream
	// ends with [DONE] and a normal transport end, but it did not deliver a
	// finished answer, so the request must not settle or log as a success.
	var streamAbnormalFinish string
	// streamFinishSeen records whether the provider ever closed the generation with
	// a terminal finish_reason. A transport that is cut off can still send a usage
	// event and [DONE] while the answer is mid-sentence (2026-10-27: YJ aggregator
	// channels ended at 60s with reasoning truncated and no finish marker), so
	// [DONE] alone is not proof of a finished answer; such a stream used to settle
	// and bill as a success.
	var streamFinishSeen bool
	seenStreamToolCalls := make(map[string]struct{})
	var streamFunctionCallNames []string
	// streamedToolCalls accumulates the tool calls of this attempt so a stream
	// that dies without a terminal frame can still be closed as a tool_calls
	// answer when every call it delivered is complete (see the salvage branch
	// next to the truncated-close guard below).
	var streamedToolCalls streamToolCallTracker
	includeDeepSeekV4ReasoningUsage := !shouldSuppressReasoningContent(info)
	isV4OpenAIStream := info.RelayFormat == types.RelayFormatOpenAI && deepSeekV4FitEnabled(info)
	isK3OpenAIStream := info.RelayFormat == types.RelayFormatOpenAI && kimiK3FitEnabled(info)
	isAudioModel := strings.Contains(strings.ToLower(model), "audio")
	// One coalescer per attempt: a retry must not inherit a previous attempt's
	// buffered text.
	info.StreamCoalescer = nil
	if ms := info.ChannelSetting.StreamCoalesceMs; ms > 0 {
		info.StreamCoalescer = streammerge.New(streammerge.Config{MaxDelay: time.Duration(ms) * time.Millisecond})
	}

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
				ID                string                                    `json:"id"`
				Created           int64                                     `json:"created"`
				Model             string                                    `json:"model"`
				SystemFingerprint string                                    `json:"system_fingerprint"`
				Choices           []dto.ChatCompletionsStreamResponseChoice `json:"choices"`
				Usage             *dto.Usage                                `json:"usage"`
			}
			if err := common.Unmarshal(common.StringToByteSlice(data), &streamResp); err == nil {
				for _, choice := range streamResp.Choices {
					if choice.Index+1 > streamChoiceCount {
						streamChoiceCount = choice.Index + 1
					}
				}
				// A missing-finish stream may have no terminal chunk to carry
				// the envelope, so capture it from the first chunk that has it.
				if responseId == "" && streamResp.ID != "" {
					responseId = streamResp.ID
				}
				if createAt == 0 && streamResp.Created > 0 {
					createAt = streamResp.Created
				}
				if model == "" && streamResp.Model != "" {
					model = streamResp.Model
				}
				if systemFingerprint == "" && streamResp.SystemFingerprint != "" {
					systemFingerprint = streamResp.SystemFingerprint
				}
				for _, choice := range streamResp.Choices {
					if choice.FinishReason != nil && *choice.FinishReason != "" {
						currentHasFinish = true
						streamFinishSeen = true
						info.StreamFinishReason = *choice.FinishReason
						if relaycommon.IsAbnormalFinishReason(*choice.FinishReason) {
							if streamAbnormalFinish == "" {
								streamAbnormalFinish = *choice.FinishReason
							}
							info.StreamStatus.MarkFailed("upstream_finish_reason", "upstream_error", http.StatusBadGateway)
							info.StreamStatus.RecordError("upstream finish_reason=" + *choice.FinishReason)
						} else if streamChoiceCount <= 1 {
							// Normal terminal frame: the answer is complete even if the
							// provider never closes the stream. The idle guard uses this
							// to close the request after a short tail grace instead of
							// failing it as a timeout (2026-10-29: neurvibe ch48 sent
							// terminal tool_calls then held the connection).
							//
							// A multi-choice (n>1) stream is excluded: choice 0 can
							// finish while another choice is still generating, and the
							// tail close would silently truncate it.
							info.StreamStatus.MarkTerminalFinishSeen()
						}
					}
				}
				// Track every function-call fragment as the provider sent it: the
				// truncated-close guard needs to know whether a cut landed after the
				// last complete argument document or inside one.
				streamedToolCalls.observe(streamResp.Choices)
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
			// Count delivered answer text for every channel, not only the ones with
			// continuation configured: the truncated-close guard below must see the
			// answer a non-whitelisted channel already delivered (2026-10-28: ch14
			// and ch77 cut after content but kept the error frame because this counter
			// stayed zero).
			for _, choice := range streamResp.Choices {
				visibleContentChars += len(choice.Delta.GetContentString())
			}
			if info.ChatContinuation != nil && (info.ChatContinuation.Abnormal != nil || info.ChatContinuation.Missing != nil) {
				limit := continuationReasoningLimit(info)
				if limit <= 0 {
					limit = 512 * 1024
				}
				for _, choice := range streamResp.Choices {
					reasoning := choice.Delta.GetReasoningContent()
					if reasoning == "" {
						continue
					}
					if reasoningBytes+len(reasoning) > limit {
						reasoningTruncated = true
						continue
					}
					reasoningTextBuilder.WriteString(reasoning)
					reasoningBytes += len(reasoning)
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
				// 只把错误交回重试循环，**不要**在这里写客户 writer 的状态。
				// 这一跳仍可能被判为可重试并换道（controller/relay.go 的换道闸门
				// 此时看到的是 helper.ResponseNotCommitted），而 gin 的 WriteHeader
				// 只把状态记在 writer 上、直到第一次写正文才真正落盘，于是换道成功
				// 后的首帧会把这一跳的 5xx 当作响应状态发给客户：客户拿到
				// 「502 + 完整正文」（自己解析不出内容），我方却记成功并计费。
				// 2026-10-12 20:29 cdp 投诉即此形态。request_id
				// 202610121227574862194198268d9d6lEWDlnxW 在平台库有三行：
				// 20:28:44 type=5 ch48（流内 502 @47.5s）→ 20:29:24 type=5 ch37
				// （503 @87.4s）→ 20:29:55 type=2 ch19 成功并计费（118s，
				// 145201/610 tokens）；同期入口哨兵只留聚合计数（「客户可见
				// 5xx=3」@20:29:42），逐条入口 access log 未归档，故不引用具体日志行。
				// 真正终局失败时，状态由 controller/relay.go 的错误 defer 统一写出，
				// 错误本身仍带 StatusCode，可见性不受影响。
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
					deferThis := false
					if lastStreamHasFinish && shouldDeferTerminalForRepair(info, lastStreamFinishReason, toolCount, streamChoiceCount, isAudioModel, streamFunctionCallNames) {
						deferredTerminalData = lastStreamData
						pendingUsageData = ""
						deferThis = true
					}
					if !deferThis {
						if lastStreamWithoutUsage != "" {
							if lastStreamHasFinish {
								finishFrameForwarded = true
							}
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
				}
			} else {
				streamData := lastStreamData
				deferThis := false
				if lastStreamHasFinish {
					if shouldDeferTerminalForRepair(info, lastStreamFinishReason, toolCount, streamChoiceCount, isAudioModel, streamFunctionCallNames) {
						deferredTerminalData = lastStreamData
						deferThis = true
					} else {
						finishFrameForwarded = true
					}
				}
				if !deferThis {
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
			if currentHasFinish {
				lastStreamFinishReason = info.StreamFinishReason
			} else {
				lastStreamFinishReason = ""
			}
			lastStreamWithoutUsage = currentWithoutUsage
			observeStreamChoices(info, data, seenStreamToolCalls, &streamFunctionCallNames)
			if err := processTokenData(info, data, &responseTextBuilder, &toolCount); err != nil {
				logger.LogError(c, "error processing stream token data: "+err.Error())
				sr.Error(err)
			}
		}
	})
	if streamErr != nil {
		// An upstream that dies mid-stream after the answer already reached the
		// caller is the same repair opportunity as an abnormal finish: the prefix
		// is delivered, so replay it and continue instead of handing the caller a
		// raw transport error over content it already read (2026-10-28 22:55:
		// ch103 streamed 1263 chunks and then returned "Upstream response stream
		// ended before completion"; every repair path was skipped because this
		// branch returns before them).
		deliveredChars := max(visibleContentChars, responseTextBuilder.Len())
		if types.IsSkipRetryError(streamErr) {
			// An in-band decision such as a content-policy rejection already wrote its
			// error event and [DONE]: resuming it from another round would append data
			// after the terminator and turn a refusal into a billed success
			// (2026-10-29 review).
			return usage, streamErr
		}
		// SendResponseCount proves bytes actually reached the caller: a repair that
		// replays a prefix the caller never received would deliver an answer with a
		// missing beginning, so only repair after at least one frame was written.
		if deliveredChars > 0 && info.SendResponseCount > 0 && toolCount == 0 && len(streamFunctionCallNames) == 0 &&
			info.ChatContinuation != nil && info.ChatContinuation.Send != nil &&
			info.RelayFormat == types.RelayFormatOpenAI && !isAudioModel && !deepSeekV4FitEnabled(info) &&
			streamChoiceCount <= 1 && (c.Request == nil || c.Request.Context().Err() == nil) &&
			chatContinuationBudgetLeft(info, usage) > 0 {
			// An upstream that breaks mid-answer after content is unreliable in a way
			// a retry cannot fix; two such breaks inside two minutes cool the channel
			// for a few minutes so it stops absorbing customer traffic (2026-10-28:
			// ch77 kept returning "Upstream completed without answer text").
			common.RecordChannelFailure(info.ChannelId)
			common.SysLog(fmt.Sprintf("stream error after visible content, attempting continuation: ch=%d content=%d err=%s", info.ChannelId, deliveredChars, streamErr.Error()))
			// The loop still holds the last chunk it read (it is forwarded only when
			// the next chunk arrives). The continuation treats everything
			// accumulated so far as the caller's prefix, so deliver that tail first
			// or the merged answer would silently miss its last fragment.
			flushHeldAnswerChunk(c, info, lastStreamData)
			resetContinuationCoalescer(info)
			result := runChatContinuation(c, info, usage, responseTextBuilder.String(), chatContinuationMeta{ID: responseId, Created: createAt, Model: model, SystemFingerprint: systemFingerprint})
			if c.Request != nil && c.Request.Context().Err() != nil {
				return result.usage, nil
			}
			if result.rounds > 0 && result.contentChars > 0 && result.terminal && result.toolCalls == 0 &&
				result.finish != "" && !relaycommon.IsAbnormalFinishReason(result.finish) {
				finalUsage := result.usage
				relaycommon.ClampUsageToClientBudget(info, finalUsage)
				info.StreamFinishReason = result.finish
				if info.StreamStatus != nil {
					info.StreamStatus.MarkRecovered()
				}
				markContinuationAudit(info, "stream-error", result.rounds, result.contentChars)
				logger.LogInfo(c, fmt.Sprintf("chat continuation recovered: mode=stream-error rounds=%d finish=%s prompt=%d completion=%d", result.rounds, result.finish, finalUsage.PromptTokens, finalUsage.CompletionTokens))
				flushContinuationCoalescer(c, info)
				helper.ExtendWriteDeadline(c)
				sendChatContinuationTerminal(c, info, responseId, createAt, model, result.finish, finalUsage)
				helper.ExtendWriteDeadline(c)
				HandleFinalResponse(c, info, "", responseId, createAt, model, systemFingerprint, finalUsage, false)
				return finalUsage, nil
			}
			// The repair did not produce a usable answer either: close honestly as
			// a truncated answer rather than throwing the delivered prefix away.
			return closeTruncatedDeliveredStream(c, info, usage, "stream_error_after_visible_content", responseId, createAt, model, systemFingerprint), nil
		}
		return usage, streamErr
	}
	// Flush merged text before anything terminal (finish_reason / usage) is
	// written: buffered deltas are always older than the held final chunk, and
	// text must never trail the finish marker.
	if info.StreamCoalescer.Enabled() {
		for _, merged := range info.StreamCoalescer.Close() {
			if err := writeStreamData(c, info, merged, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
				logger.LogError(c, "error writing coalesced stream event: "+err.Error())
			}
		}
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
		// Cap the output count at the budget the request declared before this
		// usage is settled and recorded: aggregator continuation retries can
		// accumulate several internal rounds into one usage, and max_tokens is
		// a hard request-side invariant.
		relaycommon.ClampUsageToClientBudget(info, usage)

		// The transport did not end normally. Two cases are deliberately
		// separated:
		//   - a caller that walked away (client_gone) chose to leave after the
		//     provider had already generated tokens for it, so the observed usage
		//     stays billable and no error is synthesized;
		//   - any upstream-side termination (scanner error, timeout, panic, ping
		//     failure) never delivered a finished answer, so it must not be
		//     billed and the counts returned to the caller/settlement are zeroed.
		clientGone := info.StreamStatus.EndReason == relaycommon.StreamEndReasonClientGone ||
			(c.Request != nil && c.Request.Context().Err() != nil)
		if !clientGone {
			undeliveredReason := relaycommon.UndeliveredStreamPrefix + string(info.StreamStatus.EndReason)
			// The transport died after the caller had already read answer text. The
			// prefix is deliverable, so repair it like an in-band error instead of
			// ending the stream with a bare EOF (2026-10-28: a reset left callers with
			// no finish_reason at all, which most clients read as success).
			flushHeldAnswerChunk(c, info, lastStreamData)
			deliveredChars := max(visibleContentChars, responseTextBuilder.Len())
			if deliveredChars > 0 && toolCount == 0 && len(streamFunctionCallNames) == 0 && streamChoiceCount <= 1 &&
				info.ChatContinuation != nil && info.ChatContinuation.Send != nil &&
				info.RelayFormat == types.RelayFormatOpenAI && !isAudioModel &&
				!deepSeekV4FitEnabled(info) && !kimiK3FitEnabled(info) &&
				(c.Request == nil || c.Request.Context().Err() == nil) &&
				chatContinuationBudgetLeft(info, usage) > 0 {
				common.RecordChannelFailure(info.ChannelId)
				common.SysLog(fmt.Sprintf("stream transport failure after visible content, attempting continuation: ch=%d reason=%s content=%d", info.ChannelId, info.StreamStatus.EndReason, deliveredChars))
				resetContinuationCoalescer(info)
				result := runChatContinuation(c, info, usage, responseTextBuilder.String(), chatContinuationMeta{ID: responseId, Created: createAt, Model: model, SystemFingerprint: systemFingerprint})
				if c.Request != nil && c.Request.Context().Err() != nil {
					return result.usage, nil
				}
				if result.rounds > 0 && result.contentChars > 0 && result.terminal && result.toolCalls == 0 &&
					result.finish != "" && !relaycommon.IsAbnormalFinishReason(result.finish) {
					finalUsage := result.usage
					relaycommon.ClampUsageToClientBudget(info, finalUsage)
					info.StreamFinishReason = result.finish
					if info.StreamStatus != nil {
						info.StreamStatus.MarkRecovered()
					}
					markContinuationAudit(info, "transport-error", result.rounds, result.contentChars)
					logger.LogInfo(c, fmt.Sprintf("chat continuation recovered: mode=transport-error rounds=%d finish=%s prompt=%d completion=%d", result.rounds, result.finish, finalUsage.PromptTokens, finalUsage.CompletionTokens))
					flushContinuationCoalescer(c, info)
					helper.ExtendWriteDeadline(c)
					sendChatContinuationTerminal(c, info, responseId, createAt, model, result.finish, finalUsage)
					helper.ExtendWriteDeadline(c)
					HandleFinalResponse(c, info, "", responseId, createAt, model, systemFingerprint, finalUsage, false)
					return finalUsage, nil
				}
				return closeTruncatedDeliveredStream(c, info, usage, "transport_error_after_visible_content", responseId, createAt, model, systemFingerprint), nil
			}
			if deliveredChars > 0 && toolCount == 0 && len(streamFunctionCallNames) == 0 && streamChoiceCount <= 1 &&
				!deepSeekV4FitEnabled(info) && info.RelayFormat == types.RelayFormatOpenAI && !isAudioModel {
				// No continuation is configured for this channel: close the delivered
				// answer honestly instead of leaving the stream without a terminal frame.
				return closeTruncatedDeliveredStream(c, info, usage, undeliveredReason, responseId, createAt, model, systemFingerprint), nil
			}
			usage = markUndeliveredStreamUsage(info, undeliveredReason, usage)
			service.MarkCaptureAbnormalStatus(c, http.StatusBadGateway)
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
		// The caller left mid-stream: bill the observed partial usage, but do not
		// synthesize an error nobody can receive. The scanner reports the same
		// event as scanner_error whenever the cancelled request context closes the
		// upstream body first, so the request context is the authoritative signal.
		return usage, nil
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
	// Cap the output count before the terminal/usage frames are rendered, so
	// the client, the consume log and the settlement all carry the same number
	// (see ClampUsageToClientBudget for why upstream accumulation happens).
	relaycommon.ClampUsageToClientBudget(info, usage)

	// Tool-call deltas forwarded by a resumed round: once the caller holds half a
	// call, the answer must not be closed as a clean truncation.
	continuationToolCalls := 0

	// Transparent continuation for a provider-side output cap: the upstream ended
	// the stream with finish_reason=length while the client still had budget left,
	// so continue the same answer inside this same client stream instead of
	// truncating it. The branch is inert unless the relay enabled continuation for
	// this request (info.ChatContinuation != nil).
	if !isAudioModel && chatContinuationCandidate(info, usage, responseTextBuilder.Len(), toolCount, streamChoiceCount) && len(streamFunctionCallNames) == 0 {
		flushHeldAnswerChunk(c, info, lastStreamData)
		resetContinuationCoalescer(info)
		result := runChatContinuation(c, info, usage, responseTextBuilder.String(), chatContinuationMeta{ID: responseId, Created: createAt, Model: model, SystemFingerprint: systemFingerprint})
		continuationToolCalls += result.toolCalls
		// A caller-side disconnect during the continuation rounds stays billable
		// (same rule as client_gone on a normal stream): the provider already
		// generated the merged usage.
		if result.rounds > 0 && result.contentChars > 0 && result.terminal && result.toolCalls == 0 &&
			result.finish != "" && !relaycommon.IsAbnormalFinishReason(result.finish) {
			finalUsage := result.usage
			relaycommon.ClampUsageToClientBudget(info, finalUsage)
			info.StreamFinishReason = result.finish
			if info.StreamStatus != nil {
				info.StreamStatus.MarkCompleted()
			}
			markContinuationAudit(info, "length", result.rounds, result.contentChars)
			logger.LogInfo(c, fmt.Sprintf("chat continuation recovered: mode=length rounds=%d finish=%s prompt=%d completion=%d", result.rounds, result.finish, finalUsage.PromptTokens, finalUsage.CompletionTokens))
			flushContinuationCoalescer(c, info)
			helper.ExtendWriteDeadline(c)
			sendChatContinuationTerminal(c, info, responseId, createAt, model, result.finish, finalUsage)
			helper.ExtendWriteDeadline(c)
			HandleFinalResponse(c, info, "", responseId, createAt, model, systemFingerprint, finalUsage, false)
			return finalUsage, nil
		}
	}

	// Abnormal cut after the answer already started (900s wall hit mid-answer):
	// the caller is reading a partial answer, so resuming the thinking round is
	// pointless and discarding the prefix (the old fallback) throws away tens of
	// kilobytes of finished work. Continue the visible answer prefix instead, the
	// same way an upstream length cap is repaired.
	if streamAbnormalFinish != "" && !reasoningTruncated && visibleContentChars > 0 &&
		info.ChatContinuation != nil && info.ChatContinuation.Send != nil &&
		info.IsStream && info.RelayFormat == types.RelayFormatOpenAI &&
		toolCount == 0 && streamChoiceCount <= 1 && len(streamFunctionCallNames) == 0 &&
		!deepSeekV4FitEnabled(info) &&
		!info.ChannelSetting.ThinkingToContent && !shouldSuppressReasoningContent(info) &&
		chatContinuationBudgetLeft(info, usage) > 0 {
		common.SysLog(fmt.Sprintf("abnormal continuation from visible content: ch=%d content=%d clientMax=%d completion=%d", info.ChannelId, visibleContentChars, info.ClientMaxTokens, usage.CompletionTokens))
		info.StreamCutDiagnostics.MarkRepairAttempted("abnormal-content")
		flushHeldAnswerChunk(c, info, lastStreamData)
		resetContinuationCoalescer(info)
		result := runChatContinuation(c, info, usage, responseTextBuilder.String(), chatContinuationMeta{ID: responseId, Created: createAt, Model: model, SystemFingerprint: systemFingerprint})
		info.StreamCutDiagnostics.MarkRepairOutcome(result.rounds, "round_incomplete")
		continuationToolCalls += result.toolCalls
		if c.Request != nil && c.Request.Context().Err() != nil {
			finalUsage := result.usage
			relaycommon.ClampUsageToClientBudget(info, finalUsage)
			info.StreamFinishReason = result.finish
			return finalUsage, nil
		}
		if result.rounds > 0 && result.contentChars > 0 && result.terminal && result.finish != "" && !relaycommon.IsAbnormalFinishReason(result.finish) {
			finalUsage := result.usage
			relaycommon.ClampUsageToClientBudget(info, finalUsage)
			info.StreamFinishReason = result.finish
			if info.StreamStatus != nil {
				info.StreamStatus.MarkRecovered()
			}
			markContinuationAudit(info, "abnormal-content", result.rounds, result.contentChars)
			logger.LogInfo(c, fmt.Sprintf("chat continuation recovered: mode=abnormal-content rounds=%d finish=%s prompt=%d completion=%d", result.rounds, result.finish, finalUsage.PromptTokens, finalUsage.CompletionTokens))
			flushContinuationCoalescer(c, info)
			helper.ExtendWriteDeadline(c)
			sendChatContinuationTerminal(c, info, responseId, createAt, model, result.finish, finalUsage)
			helper.ExtendWriteDeadline(c)
			HandleFinalResponse(c, info, "", responseId, createAt, model, systemFingerprint, finalUsage, false)
			return finalUsage, nil
		}
	}

	// Abnormal-finish continuation (900s cut-off shape): the provider ended
	// with finish_reason=error before any visible content, after emitting a long
	// reasoning trace. Resume the thinking round inside the same client stream
	// instead of surfacing the failure. Must run before the terminal/usage frame
	// switch below, otherwise the client would receive the provider's error
	// terminal frame and no continuation could repair it.
	if streamAbnormalFinish != "" && !reasoningTruncated &&
		chatAbnormalContinuationCandidate(info, usage, visibleContentChars, reasoningTextBuilder.Len(), toolCount, streamChoiceCount) &&
		len(streamFunctionCallNames) == 0 {
		info.StreamCutDiagnostics.MarkRepairAttempted("abnormal-reasoning")
		flushHeldAnswerChunk(c, info, lastStreamData)
		resetContinuationCoalescer(info)
		result := runAbnormalChatContinuation(c, info, usage, reasoningTextBuilder.String(), chatContinuationMeta{ID: responseId, Created: createAt, Model: model, SystemFingerprint: systemFingerprint})
		info.StreamCutDiagnostics.MarkRepairOutcome(result.rounds, "round_incomplete")
		continuationToolCalls += result.toolCalls
		if c.Request != nil && c.Request.Context().Err() != nil {
			// The caller walked away mid-continuation: the provider already
			// generated the observed usage (same rule as client_gone on a
			// normal stream), so keep it billable and do not synthesize an
			// error nobody can receive.
			finalUsage := result.usage
			relaycommon.ClampUsageToClientBudget(info, finalUsage)
			info.StreamFinishReason = result.finish
			return finalUsage, nil
		}
		if result.rounds > 0 && result.contentChars > 0 && result.terminal && result.finish != "" && !relaycommon.IsAbnormalFinishReason(result.finish) {
			finalUsage := result.usage
			relaycommon.ClampUsageToClientBudget(info, finalUsage)
			info.StreamFinishReason = result.finish
			if info.StreamStatus != nil {
				info.StreamStatus.MarkRecovered()
			}
			markContinuationAudit(info, "abnormal", result.rounds, result.contentChars)
			logger.LogInfo(c, fmt.Sprintf("thinking continuation recovered: mode=abnormal rounds=%d finish=%s prompt=%d completion=%d", result.rounds, result.finish, finalUsage.PromptTokens, finalUsage.CompletionTokens))
			flushContinuationCoalescer(c, info)
			helper.ExtendWriteDeadline(c)
			sendChatContinuationTerminal(c, info, responseId, createAt, model, result.finish, finalUsage)
			helper.ExtendWriteDeadline(c)
			HandleFinalResponse(c, info, "", responseId, createAt, model, systemFingerprint, finalUsage, false)
			return finalUsage, nil
		}
	}

	if streamAbnormalFinish != "" {
		common.SysLog(fmt.Sprintf("abnormal finish observed: ch=%d reason=%s content=%d reasoning=%d truncated=%v continuation=%v abnormalCfg=%v failed=%v clientMax=%d completion=%d",
			info.ChannelId, streamAbnormalFinish, visibleContentChars, reasoningTextBuilder.Len(), reasoningTruncated,
			info.ChatContinuation != nil, info.ChatContinuation != nil && info.ChatContinuation.Abnormal != nil,
			info.StreamStatus != nil && info.StreamStatus.ResponseFailed(), info.ClientMaxTokens, usage.CompletionTokens))
	}

	// Snapshot the cut diagnostics before any repair attempt mutates the state.
	// The failed type=5 row carries them (admin_info.stream_cut), so an
	// unrepaired cut is explainable from /api/log/ instead of from a container
	// log line that is usually gone by the time the customer reports the id.
	if streamAbnormalFinish != "" || !streamFinishSeen {
		info.StreamCutDiagnostics = buildStreamCutDiagnostics(info, usage,
			visibleContentChars, reasoningTextBuilder.Len(), reasoningTruncated,
			toolCount, streamChoiceCount, streamAbnormalFinish, streamAbnormalFinish == "" && !streamFinishSeen)
	}

	// Length-cap continuation during the thinking phase (32k cap, no visible
	// answer yet): the prefix continuation has nothing to replay, so resume the
	// thinking round from the captured reasoning instead of truncating.
	if streamAbnormalFinish == "" && !reasoningTruncated &&
		chatLengthThinkingContinuationCandidate(info, usage, visibleContentChars, reasoningTextBuilder.Len(), toolCount, streamChoiceCount) &&
		len(streamFunctionCallNames) == 0 {
		common.SysLog(fmt.Sprintf("length thinking continuation: ch=%d reasoning=%d clientMax=%d completion=%d", info.ChannelId, reasoningTextBuilder.Len(), info.ClientMaxTokens, usage.CompletionTokens))
		flushHeldAnswerChunk(c, info, lastStreamData)
		resetContinuationCoalescer(info)
		result := runLengthThinkingChatContinuation(c, info, usage, reasoningTextBuilder.String(), chatContinuationMeta{ID: responseId, Created: createAt, Model: model, SystemFingerprint: systemFingerprint})
		continuationToolCalls += result.toolCalls
		if c.Request != nil && c.Request.Context().Err() != nil {
			finalUsage := result.usage
			relaycommon.ClampUsageToClientBudget(info, finalUsage)
			info.StreamFinishReason = result.finish
			return finalUsage, nil
		}
		// Once a continuation round forwarded visible content the client is
		// already reading that answer, so the response must end on a terminal
		// frame consistent with what was delivered: an empty or abnormal finish
		// is reported as "length" (an output-cap truncation) instead of falling
		// through to the first round's terminal, which would contradict the
		// streamed content and drop the continuation usage from the bill.
		if result.rounds > 0 && result.contentChars > 0 && result.terminal {
			finish := result.finish
			if finish == "" || relaycommon.IsAbnormalFinishReason(finish) {
				finish = "length"
			}
			finalUsage := result.usage
			relaycommon.ClampUsageToClientBudget(info, finalUsage)
			info.StreamFinishReason = finish
			if info.StreamStatus != nil {
				info.StreamStatus.MarkRecovered()
			}
			markContinuationAudit(info, "length-thinking", result.rounds, result.contentChars)
			common.SysLog(fmt.Sprintf("length thinking continuation delivered: rounds=%d finish=%s prompt=%d completion=%d", result.rounds, finish, finalUsage.PromptTokens, finalUsage.CompletionTokens))
			flushContinuationCoalescer(c, info)
			helper.ExtendWriteDeadline(c)
			sendChatContinuationTerminal(c, info, responseId, createAt, model, finish, finalUsage)
			helper.ExtendWriteDeadline(c)
			HandleFinalResponse(c, info, "", responseId, createAt, model, systemFingerprint, finalUsage, false)
			return finalUsage, nil
		}
	}

	// Missing-finish continuation after the answer already started: the upstream
	// reached EOF/[DONE] with no terminal finish_reason at all, but the client is
	// already reading a partial answer. The reasoning resume below refuses any
	// visible content, and discarding the prefix would surface a 502 for work
	// that can still be completed, so continue the visible prefix through the
	// same transparent continuation used for a length cap and for an abnormal
	// cut after the answer started.
	if streamAbnormalFinish == "" && !streamFinishSeen && !reasoningTruncated &&
		streamMissingFinishMarksTruncation(info, isAudioModel) &&
		chatMissingVisibleContinuationCandidate(info, usage, visibleContentChars, toolCount, streamChoiceCount, streamFinishSeen) &&
		len(streamFunctionCallNames) == 0 {
		common.SysLog(fmt.Sprintf("missing continuation from visible content: ch=%d content=%d clientMax=%d completion=%d", info.ChannelId, visibleContentChars, info.ClientMaxTokens, usage.CompletionTokens))
		info.StreamCutDiagnostics.MarkRepairAttempted("missing-content")
		flushHeldAnswerChunk(c, info, lastStreamData)
		resetContinuationCoalescer(info)
		result := runChatContinuation(c, info, usage, responseTextBuilder.String(), chatContinuationMeta{ID: responseId, Created: createAt, Model: model, SystemFingerprint: systemFingerprint})
		info.StreamCutDiagnostics.MarkRepairOutcome(result.rounds, "round_incomplete")
		continuationToolCalls += result.toolCalls
		if c.Request != nil && c.Request.Context().Err() != nil {
			finalUsage := result.usage
			relaycommon.ClampUsageToClientBudget(info, finalUsage)
			info.StreamFinishReason = result.finish
			return finalUsage, nil
		}
		if result.rounds > 0 && result.contentChars > 0 && result.terminal && result.finish != "" && !relaycommon.IsAbnormalFinishReason(result.finish) {
			finalUsage := result.usage
			relaycommon.ClampUsageToClientBudget(info, finalUsage)
			info.StreamFinishReason = result.finish
			if info.StreamStatus != nil {
				info.StreamStatus.MarkRecovered()
			}
			markContinuationAudit(info, "missing-content", result.rounds, result.contentChars)
			logger.LogInfo(c, fmt.Sprintf("chat continuation recovered: mode=missing-content rounds=%d finish=%s prompt=%d completion=%d", result.rounds, result.finish, finalUsage.PromptTokens, finalUsage.CompletionTokens))
			flushContinuationCoalescer(c, info)
			helper.ExtendWriteDeadline(c)
			sendChatContinuationTerminal(c, info, responseId, createAt, model, result.finish, finalUsage)
			helper.ExtendWriteDeadline(c)
			HandleFinalResponse(c, info, "", responseId, createAt, model, systemFingerprint, finalUsage, false)
			return finalUsage, nil
		}
	}

	// Missing-finish continuation (60s silent cut shape): the provider reached
	// EOF/[DONE] without any terminal finish_reason, emitted only reasoning, and
	// therefore delivered no visible answer. Resume the thinking round inside
	// the same client stream instead of surfacing a truncation.
	if streamAbnormalFinish == "" && !streamFinishSeen && !reasoningTruncated {
		common.SysLog(fmt.Sprintf("missing finish observed: ch=%d content=%d reasoning=%d marks=%v candidate=%v steps=%d onMissing=%v cfg=%v", info.ChannelId, visibleContentChars, reasoningTextBuilder.Len(), streamMissingFinishMarksTruncation(info, isAudioModel), chatMissingContinuationCandidate(info, usage, visibleContentChars, reasoningTextBuilder.Len(), toolCount, streamChoiceCount, streamFinishSeen), relaycommon.ChatMissingContinuationStepsForRequest(info), common.ChatContinuationOnMissing, info.ChatContinuation != nil && info.ChatContinuation.Missing != nil))
	}
	if streamAbnormalFinish == "" && !streamFinishSeen && !reasoningTruncated &&
		streamMissingFinishMarksTruncation(info, isAudioModel) &&
		chatMissingContinuationCandidate(info, usage, visibleContentChars, reasoningTextBuilder.Len(), toolCount, streamChoiceCount, streamFinishSeen) &&
		len(streamFunctionCallNames) == 0 {
		flushHeldAnswerChunk(c, info, lastStreamData)
		resetContinuationCoalescer(info)
		info.StreamCutDiagnostics.MarkRepairAttempted("missing-reasoning")
		result := runMissingChatContinuation(c, info, usage, reasoningTextBuilder.String(), chatContinuationMeta{ID: responseId, Created: createAt, Model: model, SystemFingerprint: systemFingerprint})
		info.StreamCutDiagnostics.MarkRepairOutcome(result.rounds, "round_incomplete")
		continuationToolCalls += result.toolCalls
		if c.Request != nil && c.Request.Context().Err() != nil {
			finalUsage := result.usage
			relaycommon.ClampUsageToClientBudget(info, finalUsage)
			info.StreamFinishReason = result.finish
			return finalUsage, nil
		}
		if result.rounds > 0 && result.contentChars > 0 && result.terminal && result.finish != "" && !relaycommon.IsAbnormalFinishReason(result.finish) {
			finalUsage := result.usage
			relaycommon.ClampUsageToClientBudget(info, finalUsage)
			info.StreamFinishReason = result.finish
			if info.StreamStatus != nil {
				info.StreamStatus.MarkRecovered()
			}
			markContinuationAudit(info, "missing", result.rounds, result.contentChars)
			logger.LogInfo(c, fmt.Sprintf("thinking continuation recovered: mode=missing rounds=%d finish=%s prompt=%d completion=%d", result.rounds, result.finish, finalUsage.PromptTokens, finalUsage.CompletionTokens))
			flushContinuationCoalescer(c, info)
			helper.ExtendWriteDeadline(c)
			sendChatContinuationTerminal(c, info, responseId, createAt, model, result.finish, finalUsage)
			helper.ExtendWriteDeadline(c)
			HandleFinalResponse(c, info, "", responseId, createAt, model, systemFingerprint, finalUsage, false)
			return finalUsage, nil
		}
	}

	// The transport ended normally, but the generation never reached a terminal
	// state: either the provider marked the failure in-band with an abnormal
	// finish_reason, or it closed the stream ([DONE]/EOF) without ever sending a
	// finish_reason. Both are truncated answers. Classify before any terminal or
	// usage frame is rendered so the caller-visible usage is zero too.
	undeliveredReason := ""
	if streamAbnormalFinish != "" {
		undeliveredReason = relaycommon.UndeliveredAbnormalFinish
	} else if !streamFinishSeen && streamMissingFinishMarksTruncation(info, isAudioModel) {
		undeliveredReason = relaycommon.UndeliveredMissingFinish
	}
	if undeliveredReason != "" {
		// A stream cut after the answer is already written is closed below. The
		// tool-call shape needs its own close: the provider can lose only the
		// terminal frame after the model finished every call, and the caller then
		// holds a complete, executable answer (2026-10-29: neurvibe :8111 cut
		// 88-98% of the kimi-k3.1-pro tool-call streams; 13 of 14 sampled cuts had
		// complete argument JSON). Closing it as finish_reason=tool_calls keeps the
		// delivered answer usable and billable instead of charging nothing and
		// handing the caller an error for work that is done. A cut inside a call
		// keeps the error frame: half an argument document is not executable.
		if undeliveredReason == relaycommon.UndeliveredMissingFinish &&
			!finishFrameForwarded && continuationToolCalls == 0 && streamChoiceCount <= 1 &&
			!isAudioModel && info.RelayFormat == types.RelayFormatOpenAI {
			if completeToolCalls, complete := streamedToolCalls.completeCalls(); complete {
				// Deliver the held tail first: it can be the provider's last
				// argument fragment, and the caller must hold it before the
				// terminal frame closes the call.
				flushHeldAnswerChunk(c, info, lastStreamData)
				info.StreamFinishReason = constant.FinishReasonToolCalls
				info.ClosedAsToolCalls = true
				if info.StreamStatus != nil {
					info.StreamStatus.MarkRecovered()
				}
				common.SysLog(fmt.Sprintf("truncated stream closed as tool_calls: ch=%d tools=%d content=%d reasoning=%d", info.ChannelId, completeToolCalls, visibleContentChars, reasoningTextBuilder.Len()))
				if c.Request != nil && c.Request.Context().Err() != nil {
					// The caller is gone; the answer was generated and delivered up
					// to the terminal frame, so it stays billable like any other
					// client-gone stream.
					return usage, nil
				}
				flushContinuationCoalescer(c, info)
				helper.ExtendWriteDeadline(c)
				sendChatContinuationTerminal(c, info, responseId, createAt, model, constant.FinishReasonToolCalls, usage)
				helper.ExtendWriteDeadline(c)
				// The normal terminal path forwards the provider's usage event.
				// Here the provider never sent one, so render it when the caller
				// asked for usage; HandleFinalResponse emits it only when the
				// upstream usage was missing altogether.
				if info.ShouldIncludeUsage && containStreamUsage && !deepSeekV4FitEnabled(info) && !kimiK3FitEnabled(info) {
					response := helper.GenerateFinalUsageResponse(responseId, createAt, model, *usage)
					response.SetSystemFingerprint(systemFingerprint)
					helper.ObjectData(c, response)
				}
				HandleFinalResponse(c, info, "", responseId, createAt, model, systemFingerprint, usage, containStreamUsage)
				return usage, nil
			}
		}
		// A channel that only ever breaks this way never reaches the cooldown:
		// every other promotion point sits behind a repair attempt that a
		// truncation does not have, so ch102 kept absorbing customer traffic for
		// ten minutes while 88-98% of its answers were cut (2026-10-29). Record
		// the failure here, the one place every unrepaired truncation passes
		// through, so two cuts inside two minutes take the channel out of
		// selection for a few minutes without touching its DB status.
		common.RecordChannelFailure(info.ChannelId)
		// Log the refusal reason before the usage is zeroed: a repaired truncation
		// returns earlier, so reaching here means every continuation mode declined
		// and the caller sees the cut. One line names the caller, the shape and the
		// guard, which is what the 2026-10-28 gap investigation lacked.
		common.SysLog(fmt.Sprintf("truncated stream unrepaired: ch=%d shape=%s content=%d reasoning=%d tools=%d choices=%d clientMax=%d completion=%d reason=%s",
			info.ChannelId, undeliveredReason, visibleContentChars, reasoningTextBuilder.Len(), toolCount, streamChoiceCount,
			info.ClientMaxTokens, usage.CompletionTokens,
			truncationRepairSkipReason(info, usage, visibleContentChars, reasoningTextBuilder.Len(), toolCount, streamChoiceCount)))
		// The caller already holds the answer prefix. Turning a cut that could
		// not be finished into a 502 throws that work away and forces a full
		// fifteen-minute retry (2026-10-28 21:21-21:33: 25 customer requests),
		// so close the stream honestly as a truncated answer instead: one
		// terminal frame with finish_reason=length, the prefix stays delivered,
		// and the usage stays unbilled. A cut inside a tool call keeps the error
		// frame - half a tool call is not a usable answer and the caller must
		// retry - and so does a reasoning-only cut, which delivered no answer.
		if visibleContentChars > 0 && toolCount == 0 && len(streamFunctionCallNames) == 0 &&
			continuationToolCalls == 0 && streamChoiceCount <= 1 && !finishFrameForwarded &&
			!deepSeekV4FitEnabled(info) && info.RelayFormat == types.RelayFormatOpenAI && !isAudioModel {
			flushHeldAnswerChunk(c, info, lastStreamData)
			common.SysLog(fmt.Sprintf("partial answer closed as truncated: ch=%d shape=%s content=%d reasoning=%d", info.ChannelId, undeliveredReason, visibleContentChars, reasoningTextBuilder.Len()))
			usage = markUndeliveredStreamUsage(info, undeliveredReason, usage)
			service.MarkCaptureAbnormalStatus(c, http.StatusBadGateway)
			// Health and rate-limit sampling must not read this as a clean success:
			// the caller received a truncated answer. The abnormal shape already
			// recorded its failure when the upstream terminal arrived; the silent
			// (no finish_reason at all) shape reaches here still unknown.
			if info.StreamStatus != nil && undeliveredReason == relaycommon.UndeliveredMissingFinish {
				info.StreamStatus.MarkFailed("upstream_missing_finish_reason", "upstream_error", http.StatusBadGateway)
				info.StreamStatus.RecordError("upstream stream ended without a terminal finish_reason")
			}
			if c.Request != nil && c.Request.Context().Err() != nil {
				common.SysLog(fmt.Sprintf("partial answer terminal skipped, caller gone: ch=%d", info.ChannelId))
				return usage, nil
			}
			info.StreamFinishReason = "length"
			flushContinuationCoalescer(c, info)
			helper.ExtendWriteDeadline(c)
			sendChatContinuationTerminal(c, info, responseId, createAt, model, "length", usage)
			helper.ExtendWriteDeadline(c)
			HandleFinalResponse(c, info, "", responseId, createAt, model, systemFingerprint, usage, false)
			return usage, nil
		}
		if deferredTerminalData != "" && !finishFrameForwarded {
			// No repair is possible for this shape, so the provider's terminal frame
			// is the honest end of the answer: release it now rather than leaving the
			// caller without a finish_reason.
			payload := deferredTerminalData
			if patched, err := patchStreamUsageData(payload, usage); err == nil {
				payload = patched
			}
			helper.ExtendWriteDeadline(c)
			if err := writeStreamData(c, info, payload, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err == nil {
				finishFrameForwarded = true
			}
		}
		usage = markUndeliveredStreamUsage(info, undeliveredReason, usage)
		service.MarkCaptureAbnormalStatus(c, http.StatusBadGateway)
	}
	// A missing finish_reason means the provider never sent a terminal frame at
	// all: everything below would render the last upstream event plus [DONE],
	// which every client reads as a successful, empty answer. That is exactly the
	// silent truncation the caller cannot see (2026-10-28: 600s kimi cut). Speak
	// up instead of finishing cleanly: the answer was not delivered, the usage is
	// zeroed above, and the client must be able to tell and retry.
	if undeliveredReason == relaycommon.UndeliveredMissingFinish && info.RelayFormat == types.RelayFormatOpenAI {
		common.SysLog(fmt.Sprintf("missing finish surfaced as an error frame: ch=%d content=%d reasoning=%d", info.ChannelId, visibleContentChars, reasoningTextBuilder.Len()))
		_ = helper.ObjectData(c, gin.H{"error": gin.H{
			"message": "upstream stream ended without a terminal finish_reason",
			"type":    "upstream_error",
			"code":    "truncated_stream",
		}})
	}

	if deferredTerminalData != "" && !finishFrameForwarded && info.RelayFormat == types.RelayFormatOpenAI {
		// The provider's terminal chunk was held for a repair that never ran; the
		// answer ends here, so forward it as the single terminal frame.
		payload := deferredTerminalData
		if patched, err := patchStreamUsageData(payload, usage); err == nil {
			payload = patched
		}
		helper.ExtendWriteDeadline(c)
		if err := sendStreamData(c, info, payload, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
			logger.LogError(c, "error writing deferred terminal frame: "+err.Error())
		}
		finishFrameForwarded = true
	}

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
			// chunk, then — only when the client asked for stream usage —
			// follows it with a choices:[] event carrying the same usage at the
			// top level. A usage-only event from the upstream has already been
			// folded away by the per-chunk branch, and the official one is
			// rebuilt here from the fitted terminal chunk.
			streamData := lastStreamData
			if !lastStreamHasFinish && kimiK3PendingFinalData != "" {
				streamData = kimiK3PendingFinalData
			}
			if streamData != "" {
				streamData = FitKimiK3StreamEventForAdapters(c, info, streamData, usage, true)
				if streamData != "" {
					_ = sendStreamData(c, info, streamData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent)
				}
				if usageOnly := FitKimiK3StreamUsageOnlyChunk(info, streamData); usageOnly != "" {
					_ = sendStreamData(c, info, usageOnly, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent)
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

	if usage != nil && info.UndeliveredReason == "" {
		info.UpstreamReasoningTokens = usage.CompletionTokenDetails.ReasoningTokens
	}

	HandleFinalResponse(c, info, lastStreamData, responseId, createAt, model, systemFingerprint, usage, containStreamUsage)

	if streamAbnormalFinish != "" {
		// The client has received the provider's own terminal frame, but the
		// generation failed: surface it as a failed request so the controller
		// refunds the reservation instead of settling it as a success, and drop
		// the channel affinity binding so a retry is not pinned to the same
		// broken upstream again.
		return usage, types.NewOpenAIError(
			fmt.Errorf("upstream terminated the stream with finish_reason %q", streamAbnormalFinish),
			types.ErrorCode("upstream_error"), http.StatusBadGateway,
			types.ErrOptionWithSkipRetry(), types.ErrOptionWithTruncatedStream())
	}

	if !streamFinishSeen && streamMissingFinishMarksTruncation(info, isAudioModel) {
		info.StreamStatus.MarkFailed("upstream_missing_finish_reason", "upstream_error", http.StatusBadGateway)
		info.StreamStatus.RecordError("upstream stream ended without a terminal finish_reason")
		// Same contract as an abnormal finish reason: the client already holds the
		// provider's bytes, but the generation never reached a terminal state, so
		// the request must not settle as a success and the affinity binding must be
		// dropped (otherwise the retry lands on the same truncating channel).
		return usage, types.NewOpenAIError(
			fmt.Errorf("upstream stream ended without a terminal finish_reason"),
			types.ErrorCode("upstream_error"), http.StatusBadGateway,
			types.ErrOptionWithSkipRetry(), types.ErrOptionWithTruncatedStream())
	}

	return usage, nil
}

// markUndeliveredStreamUsage records why this stream result is unbillable and
// returns a zeroed usage for both the caller-facing terminal frames and the
// settlement/log. The upstream-reported counts are preserved on the relay info
// for the consume log's audit marker only.
// shouldDeferTerminalForRepair reports whether a provider terminal chunk must be
// held back instead of forwarded: the handler may still repair this answer, and a
// finish_reason that already reached the caller is final - appending resumed
// content afterwards corrupts the stream (2026-10-29 review, reproduced: length
// finish, then resumed content, then stop). Only shapes a repair can use are
// deferred; fit envelopes, audio, tool calls and multi-choice answers are final as
// soon as the provider says so.
func shouldDeferTerminalForRepair(info *relaycommon.RelayInfo, finishReason string, toolCount int, choiceCount int, isAudioModel bool, functionCallNames []string) bool {
	if info == nil || info.ChatContinuation == nil || info.RelayFormat != types.RelayFormatOpenAI || isAudioModel {
		return false
	}
	if toolCount > 0 || len(functionCallNames) > 0 || choiceCount > 1 {
		return false
	}
	if kimiK3FitEnabled(info) || deepSeekV4FitEnabled(info) || finishReason == "" {
		return false
	}
	if finishReason == constant.FinishReasonLength {
		return info.ChatContinuation.Send != nil && info.ChatContinuation.MaxSteps > 0
	}
	return relaycommon.IsAbnormalFinishReason(finishReason) &&
		info.ChatContinuation.Abnormal != nil && info.ChatContinuation.Abnormal.MaxSteps > 0
}

// markContinuationAudit records a transparent continuation that repaired this
// response on the success log row (admin_info.continuation). The mode names
// match the SysLog lines the same branch writes, so a log row and a container
// log line can be joined; a caller reaches this only after the round delivered
// visible content, which is the same condition the recovery branches require.
func markContinuationAudit(info *relaycommon.RelayInfo, mode string, rounds, contentChars int) {
	if info == nil || rounds <= 0 {
		return
	}
	info.ContinuationAudit = &relaycommon.ContinuationAudit{Mode: mode, Rounds: rounds, ContentChars: contentChars}
}

// flushHeldAnswerChunk writes the answer text the stream loop was still holding
// when the handler decided to finish early: every early return (repair, truncated
// close, transport fault) happens before the normal terminal rendering, so without
// this the caller loses the last fragment - and because the continuation prompt
// includes that fragment, the merged answer would be missing exactly the piece the
// model continued from (2026-10-29 review, reproduced: a held "尾块" never reached
// the caller while the resumed round picked up after it).
//
// Only content/reasoning deltas are replayed: usage and finish_reason belong to the
// terminal frame the handler renders itself. Coalesced deltas are flushed first so
// text order is preserved.
func flushHeldAnswerChunk(c *gin.Context, info *relaycommon.RelayInfo, pending string) {
	if info == nil {
		return
	}
	if info.StreamCoalescer.Enabled() {
		for _, merged := range info.StreamCoalescer.Close() {
			helper.ExtendWriteDeadline(c)
			if err := writeStreamData(c, info, merged, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
				logger.LogError(c, "error writing coalesced stream event: "+err.Error())
			}
		}
	}
	if pending == "" {
		return
	}
	payload := pending
	if stripped, err := stripStreamUsageData(pending); err == nil {
		payload = stripped
	}
	if stripped, err := stripStreamFinishData(payload); err == nil {
		payload = stripped
	}
	var probe struct {
		Choices []dto.ChatCompletionsStreamResponseChoice `json:"choices"`
	}
	if err := common.Unmarshal(common.StringToByteSlice(payload), &probe); err != nil {
		return
	}
	// The held tail is not always text: a provider can lose the terminal frame
	// right after the last argument fragment of a tool call, and the caller must
	// hold that fragment before a synthesized terminal frame closes the call
	// (2026-10-29 tool-call cuts). Forwarding it here is safe for the existing
	// callers: every repair path that flushes a held tail refuses requests with
	// tool calls, so only the new tool-call close reaches this branch with one.
	hasAnswerPayload := false
	for _, choice := range probe.Choices {
		if choice.Delta.GetContentString() != "" || choice.Delta.GetReasoningContent() != "" || len(choice.Delta.ToolCalls) > 0 {
			hasAnswerPayload = true
			break
		}
	}
	if !hasAnswerPayload {
		return
	}
	helper.ExtendWriteDeadline(c)
	if err := writeStreamData(c, info, payload, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
		logger.LogError(c, "error writing held answer tail: "+err.Error())
	}
}

// closeTruncatedDeliveredStream ends a response whose answer prefix already
// reached the caller but cannot be completed: one terminal frame with
// finish_reason=length, zero usage, explicit capture status. The caller keeps
// the delivered answer and can continue or resend instead of receiving an error
// for work that was already read.
func closeTruncatedDeliveredStream(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, reason, responseId string, createAt int64, model, systemFingerprint string) *dto.Usage {
	common.SysLog(fmt.Sprintf("partial answer closed as truncated: ch=%d reason=%s", info.ChannelId, reason))
	finalUsage := markUndeliveredStreamUsage(info, reason, usage)
	service.MarkCaptureAbnormalStatus(c, http.StatusBadGateway)
	if info.StreamStatus != nil {
		info.StreamStatus.MarkFailed("upstream_stream_error", "upstream_error", http.StatusBadGateway)
	}
	if c.Request != nil && c.Request.Context().Err() != nil {
		return finalUsage
	}
	info.StreamFinishReason = "length"
	flushContinuationCoalescer(c, info)
	helper.ExtendWriteDeadline(c)
	sendChatContinuationTerminal(c, info, responseId, createAt, model, "length", finalUsage)
	helper.ExtendWriteDeadline(c)
	HandleFinalResponse(c, info, "", responseId, createAt, model, systemFingerprint, finalUsage, false)
	return finalUsage
}

func markUndeliveredStreamUsage(info *relaycommon.RelayInfo, reason string, usage *dto.Usage) *dto.Usage {
	if info != nil && usage != nil {
		info.UpstreamReasoningTokens = usage.CompletionTokenDetails.ReasoningTokens
	}
	info.MarkUndelivered(reason, usage)
	return relaycommon.ZeroUsage()
}

// streamMissingFinishMarksTruncation reports whether a normally-ended stream that
// never carried a finish_reason must be treated as truncated. The DeepSeek V4
// family legitimately ends this way in some official shapes, and audio streams use
// their own terminal event, so both are excluded. A stream that ended because the
// caller left, timed out or failed at the transport layer is already classified by
// the branch above this helper.
func streamMissingFinishMarksTruncation(info *relaycommon.RelayInfo, audioModel bool) bool {
	if info == nil || info.StreamStatus == nil || audioModel {
		return false
	}
	if isDeepSeekV4Family(info) {
		return false
	}
	if info.StreamStatus.ResponseFailed() {
		return false
	}
	switch info.StreamStatus.EndReason {
	case relaycommon.StreamEndReasonDone, relaycommon.StreamEndReasonEOF:
		return true
	default:
		return false
	}
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
	// A non-terminal finish reason means the provider failed the generation
	// (aggregator "error", official "engine_overloaded"/capacity values). The
	// body must not be forwarded or settled as a success.
	for _, choice := range simpleResponse.Choices {
		if relaycommon.IsAbnormalFinishReason(choice.FinishReason) {
			// A non-streaming upstream that reports an abnormal finish produced no
			// usable answer at all: count it so a channel that keeps doing this cools
			// down instead of being retried forever.
			common.RecordChannelFailure(info.ChannelId)
			// Nothing has been written to a non-streaming caller yet, so another
			// channel can still serve this request: retry instead of surfacing the
			// first upstream's abnormal finish. Retrying used to be suppressed here,
			// so a single bad upstream answer ended the request after tens of
			// seconds with zero content (2026-10-28 22:54, kimi-k3.1-pro, ch91,
			// 85s, 0 tokens, customer-visible 502).
			return &simpleResponse.Usage, types.NewOpenAIError(
				fmt.Errorf("upstream returned finish_reason %q", choice.FinishReason),
				types.ErrorCode("upstream_error"), http.StatusBadGateway,
				types.ErrOptionWithTruncatedStream())
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
	if simpleResponse.Usage.CompletionTokenDetails.ReasoningTokens > 0 {
		info.UpstreamReasoningTokens = simpleResponse.Usage.CompletionTokenDetails.ReasoningTokens
	}
	for i := range simpleResponse.Choices {
		if simpleResponse.Choices[i].Message.GetReasoningContent() != "" {
			info.ReasoningContentSeen = true
			break
		}
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
	// Cap the output count before the body is rendered for the client and
	// before the caller settles it: the displayed usage, the consume log and
	// the invoice must agree, and no legitimate answer can exceed max_tokens.
	if relaycommon.ClampUsageToClientBudget(info, &simpleResponse.Usage) {
		usageModified = true
	}

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

	// Channel-configured response rewrite, applied to the bytes the client
	// actually receives: several branches above rebuild the body from the
	// typed response, which would drop a field the document added or
	// reintroduce one it removed. The upstream's own body has already been
	// parsed for usage and contract checks, so accounting stays the
	// upstream's while the client's copy is the configured shape.
	if rewritten, rewriteErr := relaycommon.ApplyResponseOverride(responseBody, info); rewriteErr != nil {
		logger.LogDebug(c, "response override skipped for channel #%d: %v", info.ChannelId, rewriteErr)
	} else if len(rewritten) > 0 {
		responseBody = rewritten
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

// buildStreamCutDiagnostics snapshots what an undelivered stream cut left
// behind: the shape of the cut, the caller's remaining budget, and which repair
// modes were configured for the request. This is what turns "the customer got a
// 502 on request X" into an answer without container logs — see
// relaycommon.StreamCutDiagnostics.
func buildStreamCutDiagnostics(info *relaycommon.RelayInfo, usage *dto.Usage, visibleContentChars, reasoningChars int, reasoningTruncated bool, toolCount, choiceCount int, abnormalFinish string, missingFinish bool) *relaycommon.StreamCutDiagnostics {
	if info == nil {
		return nil
	}
	diag := &relaycommon.StreamCutDiagnostics{
		FinishReason:        abnormalFinish,
		MissingFinish:       missingFinish,
		ReasoningTruncated:  reasoningTruncated,
		VisibleContentChars: visibleContentChars,
		ReasoningChars:      reasoningChars,
		ToolCalls:           toolCount,
		ChoiceCount:         choiceCount,
		ClientMaxTokens:     info.ClientMaxTokens,
	}
	if usage != nil {
		diag.CompletionTokens = usage.CompletionTokens
	}
	diag.BudgetLeft = chatContinuationBudgetLeft(info, usage)
	if cfg := info.ChatContinuation; cfg != nil {
		diag.ContinuationConfigured = cfg.Send != nil && cfg.MaxSteps > 0
		diag.AbnormalConfigured = cfg.Abnormal != nil && cfg.Abnormal.Send != nil && cfg.Abnormal.MaxSteps > 0
		diag.MissingConfigured = cfg.Missing != nil && cfg.Missing.Send != nil && cfg.Missing.MaxSteps > 0
	}
	diag.ThinkingToContent = info.ChannelSetting.ThinkingToContent
	diag.ReasoningSuppressed = shouldSuppressReasoningContent(info)
	return diag
}
