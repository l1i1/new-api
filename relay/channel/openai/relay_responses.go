package openai

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func OaiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	usage := usageFromResponsesResponse(&responsesResponse)
	if cyberErr := service.NewOpenAICyberPolicyError(c, responseBody, resp.StatusCode, false, usage); cyberErr != nil {
		service.IOCopyBytesGracefully(c, resp, responseBody)
		service.MarkOpsCyberPolicyForwarded(c)
		return usage, cyberErr
	}
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && (oaiError.Type != "" || oaiError.Message != "" || oaiError.Code != nil) {
		service.NormalizeServerOverloadError(oaiError)
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	// 写入新的 response body
	service.IOCopyBytesGracefully(c, resp, responseBody)

	// compute usage
	// Count actual tool invocations from Output (not tool declarations).
	for _, output := range responsesResponse.Output {
		switch output.Type {
		case dto.BuildInCallWebSearchCall:
			info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
		case dto.BuildInCallFileSearchCall:
			info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
		case dto.BuildInCallFunctionCall:
			info.CountBillableToolCall(dto.BuildInCallFunctionCall, output.Name)
		}
	}

	imageCounter := &relaycommon.ImageGenerationCallCounter{}
	if !relaycommon.IsNonBillableResponsesStatus(responsesResponse.Status) {
		for i := range responsesResponse.Output {
			idx := i
			imageCounter.Observe(&responsesResponse.Output[i], &idx)
		}
	}
	imageCounter.Commit(info)

	return usage, nil
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, "invalid response or response body")
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	var usage = &dto.Usage{}
	var responseTextBuilder strings.Builder
	imageCounter := &relaycommon.ImageGenerationCallCounter{}
	imageCommitted := false
	responseDataSent := false
	sawTerminalEvent := false
	hasToolCall := false
	var streamErr *types.NewAPIError

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		if streamErr != nil {
			sr.Stop(streamErr)
			return
		}
		// 检查当前数据是否包含 completed 状态和 usage 信息
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			logger.LogError(c, "failed to unmarshal stream response: "+err.Error())
			sr.Error(err)
			return
		}
		// Reseller channels emit a content-free `keepalive` data event. Forwarding
		// it would commit the response before any real output, which blocks the
		// relay from retrying another channel if the upstream then fails without
		// ever producing content.
		if streamResponse.Type == "keepalive" {
			return
		}
		if streamResponse.Response != nil && streamResponse.Response.Usage != nil {
			usage = dto.MergeUsage(usage, usageFromResponsesResponse(streamResponse.Response))
		}
		if cyberErr := service.NewOpenAICyberPolicyError(c, common.StringToByteSlice(data), resp.StatusCode, true, usage); cyberErr != nil {
			streamErr = cyberErr
			sendResponsesStreamData(c, streamResponse, data)
			helper.Done(c)
			service.MarkOpsCyberPolicyForwarded(c)
			sr.Stop(streamErr)
			return
		}
		rewrittenData, rewritten, err := rewriteResponsesServerOverload(data)
		if err != nil {
			logger.LogError(c, "failed to rewrite Responses overload event: "+err.Error())
			sr.Error(err)
			return
		}
		if rewritten {
			logger.LogWarn(c, "rewrote Responses overload event code to server_error for client retry")
			data = rewrittenData
			if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
				logger.LogError(c, "failed to unmarshal rewritten stream response: "+err.Error())
				sr.Error(err)
				return
			}
		}
		// Reseller channels also report a failure as a bare SSE error event
		// (type "error") rather than the protocol's response.failed/response.error.
		if streamResponse.Type == "error" || streamResponse.Type == "response.failed" || streamResponse.Type == "response.error" {
			sawTerminalEvent = true
			var oaiError *types.OpenAIError
			if streamResponse.Response != nil {
				oaiError = streamResponse.Response.GetOpenAIError()
			}
			if oaiError == nil {
				oaiError = dto.GetOpenAIError(streamResponse.Error)
			}
			if oaiError == nil {
				oaiError = &types.OpenAIError{Type: "server_error", Message: "responses stream failed"}
			}
			service.NormalizeServerOverloadError(oaiError)
			// The upstream reported the failure in-band, so the pinned channel
			// must be dropped from the affinity binding even when the stream
			// was already committed and cannot be retried within this request.
			streamErr = types.WithOpenAIError(*oaiError, http.StatusServiceUnavailable, types.ErrOptionWithUpstreamFailure())
		}
		// A failure received before any event was forwarded has not committed
		// the response. Returning the error without writing lets the relay retry
		// another channel; forwarding the failure event would commit a 200 and
		// block that retry.
		if streamErr != nil && !responseDataSent {
			sr.Stop(streamErr)
			return
		}
		sendResponsesStreamData(c, streamResponse, data)
		if streamErr != nil {
			sr.Stop(streamErr)
			return
		}
		responseDataSent = true
		switch streamResponse.Type {
		case "response.completed", "response.done":
			sawTerminalEvent = true
			if streamResponse.Response != nil {
				if streamResponse.Response.Usage != nil {
					incomingUsage := relayconvert.NormalizeResponsesUsage(streamResponse.Response.Usage)
					usage = dto.MergeUsageNonZero(usage, incomingUsage)
				}
				if !imageCommitted {
					if relaycommon.IsNonBillableResponsesStatus(streamResponse.Response.Status) {
						imageCounter.Reset()
						imageCounter.Commit(info)
						imageCommitted = true
					} else {
						for i := range streamResponse.Response.Output {
							idx := i
							imageCounter.Observe(&streamResponse.Response.Output[i], &idx)
						}
						imageCounter.Commit(info)
						imageCommitted = true
					}
				}
			} else if !imageCommitted {
				imageCounter.Commit(info)
				imageCommitted = true
			}
		case "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
			sawTerminalEvent = true
			if !imageCommitted {
				imageCounter.Reset()
				imageCounter.Commit(info)
				imageCommitted = true
			}
		case "response.output_text.delta":
			// 处理输出文本
			responseTextBuilder.WriteString(streamResponse.Delta)
		case dto.ResponsesOutputTypeItemDone:
			if streamResponse.Item != nil {
				switch streamResponse.Item.Type {
				case dto.BuildInCallWebSearchCall:
					info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
					hasToolCall = true
				case dto.BuildInCallFileSearchCall:
					info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
					hasToolCall = true
				case dto.BuildInCallFunctionCall:
					info.CountBillableToolCall(dto.BuildInCallFunctionCall, streamResponse.Item.Name)
					hasToolCall = true
				case dto.ResponsesOutputTypeImageGenerationCall:
					if !imageCommitted {
						imageCounter.Observe(streamResponse.Item, streamResponse.OutputIndex)
					}
				}
			}
		}
	})
	if streamErr != nil {
		if service.GetOpsCyberPolicy(c) != nil {
			return usage, streamErr
		}
		return nil, streamErr
	}

	if usage.CompletionTokens == 0 {
		// 计算输出文本的 token 数量
		tempStr := responseTextBuilder.String()
		if len(tempStr) > 0 {
			// 非正常结束，使用输出文本的 token 数量
			completionTokens := service.CountTextToken(tempStr, info.UpstreamModelName)
			usage.CompletionTokens = completionTokens
		}
	}

	if usage.PromptTokens == 0 && usage.CompletionTokens != 0 {
		usage.PromptTokens = info.GetEstimatePromptTokens()
	}

	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	if usage.BillingUsage != nil {
		usage.BillingUsage = dto.CloneBillingUsageWithEstimatedCompletion(usage.BillingUsage, usage.CompletionTokens)
	}

	// 流以 EOF 结束，但从未发送终端事件（response.completed/done/failed/
	// incomplete/cancelled）。Responses 协议有显式终端事件，缺少它就说明上游
	// 在完成前关闭了流——即使已经产出了部分内容（客户端文案即
	// "stream closed before response.completed"），也必须标记为失败，而不是静默
	// 记一笔成功。已提交响应时补发合成 response.failed 事件，让客户端（如 Codex
	// CLI）走重试；未提交时直接返回错误，由 relay 按普通上游错误处理/重试。
	if !sawTerminalEvent {
		return usage, incompleteResponsesStreamError(c, c.Writer.Written(), responseTextBuilder.Len() > 0 || hasToolCall || imageCounter.Count() > 0)
	}

	return usage, nil
}

func usageFromResponsesResponse(response *dto.OpenAIResponsesResponse) *dto.Usage {
	usage := &dto.Usage{}
	if response == nil || response.Usage == nil {
		return usage
	}
	usage.PromptTokens = response.Usage.InputTokens
	usage.InputTokens = response.Usage.InputTokens
	usage.CompletionTokens = response.Usage.OutputTokens
	usage.OutputTokens = response.Usage.OutputTokens
	usage.TotalTokens = response.Usage.TotalTokens
	if response.Usage.InputTokensDetails != nil {
		usage.PromptTokensDetails.CachedTokens = response.Usage.InputTokensDetails.CachedTokens
		usage.PromptTokensDetails.CacheWriteTokens = response.Usage.InputTokensDetails.CacheWriteTokens
	}
	return usage
}

// incompleteResponsesStreamError marks a Responses stream that ended without a
// terminal event as an upstream failure, so the channel is evicted from the
// affinity binding instead of being re-bound as healthy. hadOutput separates a
// stream that delivered nothing (mirroring emptyChatCompletionError on the chat
// path) from one truncated after partial output; both are failures, and the
// message reports which case occurred.
//
// Only the zero-output case injects a synthetic response.failed event, because
// there the client has nothing to act on. When partial output was already
// delivered, the content stays committed and the client's own protocol check
// ("stream closed before response.completed") drives the retry, so the stream
// is left untouched.
func incompleteResponsesStreamError(c *gin.Context, committed bool, hadOutput bool) *types.NewAPIError {
	options := make([]types.NewAPIErrorOptions, 0, 3)
	message := "upstream returned empty final content"
	if hadOutput {
		// Partial output proves the upstream had already started producing, so
		// this is not an "empty" response: flag it as an upstream failure.
		options = append(options, types.ErrOptionWithUpstreamFailure())
		message = "upstream stream ended before response.completed"
	} else {
		options = append(options, types.ErrOptionWithEmptyOutput())
	}
	if committed {
		options = append(options, types.ErrOptionWithSkipRetry())
	}
	apiErr := types.NewOpenAIError(
		errors.New(message),
		types.ErrorCode("server_error"),
		http.StatusBadGateway,
		options...,
	)
	if !committed || hadOutput {
		return apiErr
	}
	synthetic := dto.ResponsesStreamResponse{
		Type: "response.failed",
		Response: &dto.OpenAIResponsesResponse{
			Status: []byte(`"failed"`),
			Error: &types.OpenAIError{
				Type:    "server_error",
				Message: apiErr.Error(),
			},
		},
	}
	if data, err := common.Marshal(synthetic); err == nil {
		_ = helper.ResponseChunkData(c, synthetic, string(data))
		helper.Done(c)
	}
	return apiErr
}
