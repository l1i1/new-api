package openai

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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

	info.ObserveResponseModel(responsesResponse.Model)
	responseBody = rewriteSGLangResponsesCreatedAt(info, responseBody, "created_at", responsesResponse.CreatedAt)

	// 写入新的 response body
	service.IOCopyBytesGracefully(c, resp, responseBody)

	// compute usage
	service.ApplyResponsesUsage(usage, responsesResponse.Usage)
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

// deferredResponsesStreamEvent is a content-free Responses stream event held
// back until the stream produces real output. Forwarding a lifecycle event such
// as response.created commits the response; if the upstream then fails before
// producing anything (for example an overloaded Moonshot pool reports an
// engine-overloaded error ~15s into the stream), the relay can no longer fail
// over to another channel. Keeping the lifecycle events buffered preserves that
// retry window. Keep-alive pings still reach the client meanwhile, and the retry
// classifier ignores ping-only commits.
type deferredResponsesStreamEvent struct {
	payload dto.ResponsesStreamResponse
	data    string
}

// maxDeferredResponsesStreamEvents bounds the lifecycle buffer. An upstream that
// keeps emitting content-free events without ever producing output falls back to
// streaming instead of growing memory without limit.
const maxDeferredResponsesStreamEvents = 64

// responsesStreamEventCarriesNoOutput reports whether a Responses SSE event only
// announces stream state and carries no model output. Such events are buffered
// until the first output or terminal event, so an upstream that fails before any
// output can still be retried on another channel.
func responsesStreamEventCarriesNoOutput(eventType string) bool {
	switch eventType {
	case "response.created", "response.in_progress", "response.queued",
		"response.output_item.added", "response.content_part.added",
		"response.reasoning_summary_part.added":
		return true
	}
	return false
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, "invalid response or response body")
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	accumulator := service.NewResponsesUsageAccumulator(info)
	responseDataSent := false
	sawTerminalEvent := false
	hadOutput := false
	var streamErr *types.NewAPIError
	policyUsage := &dto.Usage{}
	// Lifecycle events are forwarded only once the stream produces output (or a
	// terminal event). Until then they stay here, so a pre-output upstream
	// failure leaves the response uncommitted and the relay can retry another
	// channel (see responsesStreamEventCarriesNoOutput).
	deferred := make([]deferredResponsesStreamEvent, 0, 4)
	flushDeferred := func() {
		if len(deferred) == 0 {
			return
		}
		for _, event := range deferred {
			sendResponsesStreamData(c, event.payload, event.data)
		}
		deferred = deferred[:0]
		responseDataSent = true
	}

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
		if streamResponse.Response != nil {
			data = string(rewriteSGLangResponsesCreatedAt(info, []byte(data), "response.created_at", streamResponse.Response.CreatedAt))
			if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
				logger.LogError(c, "failed to unmarshal rewritten stream response: "+err.Error())
				sr.Error(err)
				return
			}
		}
		accumulator.Observe(&streamResponse)
		policyUsage = dto.MergeUsageNonZero(policyUsage, usageFromResponsesResponse(streamResponse.Response))
		if cyberErr := service.NewOpenAICyberPolicyError(c, common.StringToByteSlice(data), resp.StatusCode, true, policyUsage); cyberErr != nil {
			streamErr = cyberErr
			// Keep the protocol ordering: a held-back response.created must not
			// arrive after the event that terminates the stream.
			flushDeferred()
			sendResponsesStreamData(c, streamResponse, data)
			responseDataSent = true
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
		// Hold content-free lifecycle events until the stream produces output.
		// Nothing has been written yet, so an upstream failure in between keeps
		// the response uncommitted and lets the relay retry another channel.
		// Once payload has been committed the buffer has no retry value left, so
		// later lifecycle events (e.g. a second output_item.added) pass through
		// immediately instead of being delayed by one event.
		if !responseDataSent && responsesStreamEventCarriesNoOutput(streamResponse.Type) && len(deferred) < maxDeferredResponsesStreamEvents {
			deferred = append(deferred, deferredResponsesStreamEvent{payload: streamResponse, data: data})
			return
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
			if oaiError == nil && (streamResponse.Code != "" || streamResponse.Message != "") {
				// Standard SSE error events carry these fields at the top level.
				// Preserve the code for request-outcome classification.
				oaiError = &types.OpenAIError{Code: streamResponse.Code, Message: streamResponse.Message, Param: streamResponse.Param}
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
			// The held-back lifecycle events belong to the failed attempt: drop
			// them instead of letting a retry inherit another channel's events,
			// but remember how to replay them with this failure in case the relay
			// runs out of retries.
			pending := deferred
			deferred = nil
			deferResponsesStreamReplay(c, pending, func() {
				sendResponsesStreamData(c, streamResponse, data)
			})
			sr.Stop(streamErr)
			return
		}
		flushDeferred()
		sendResponsesStreamData(c, streamResponse, data)
		if streamErr != nil {
			sr.Stop(streamErr)
			return
		}
		responseDataSent = true
		switch streamResponse.Type {
		case "response.completed", "response.done", "response.incomplete", "response.cancelled", "response.canceled":
			sawTerminalEvent = true
		case "response.output_text.delta", "response.function_call_arguments.delta",
			"response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.refusal.delta":
			hadOutput = true
		case dto.ResponsesOutputTypeItemDone:
			hadOutput = hadOutput || streamResponse.Item != nil
		}
	})
	common.SetContextKey(c, constant.ContextKeyResponseStreamStatus, info.StreamStatus)
	if info.StreamStatus != nil {
		info.StreamStatus.RequireTerminal()
	}
	usage := accumulator.Finish()
	if streamErr != nil {
		if service.GetOpsCyberPolicy(c) != nil {
			return policyUsage, streamErr
		}
		return usage, streamErr
	}

	// A caller that disconnected is not an upstream protocol failure. The
	// accumulator has already settled the observed usage, so do not synthesize
	// a 502 for a response nobody can receive.
	if info.StreamStatus != nil && info.StreamStatus.EndReason == relaycommon.StreamEndReasonClientGone ||
		(c.Request != nil && c.Request.Context().Err() != nil) {
		return usage, nil
	}

	// Responses requires an explicit terminal event. A clean EOF without one
	// is an upstream truncation and must remain visible to retry/affinity logic.
	if !sawTerminalEvent {
		// Only real payload commits the response. A stream that sent nothing but
		// keep-alive pings, or only held-back lifecycle events, can still be
		// retried, matching the relay's own commit-state classification.
		committed := helper.ResponseCommitStateOf(c) == helper.ResponsePayloadWritten
		apiErr := incompleteResponsesStreamError(c, committed, hadOutput)
		if !committed && len(deferred) > 0 {
			// If the relay cannot retry, replay the held-back events plus the
			// synthetic response.failed that a committed truncation would have
			// received, keeping the client-visible stream unchanged.
			pending := deferred
			deferResponsesStreamReplay(c, pending, func() {
				writeSyntheticResponsesStreamFailure(c, apiErr)
			})
		}
		return usage, apiErr
	}

	return usage, nil
}

func rewriteSGLangResponsesCreatedAt(info *relaycommon.RelayInfo, payload []byte, path string, createdAt dto.IntValue) []byte {
	if info == nil || info.GetChannelType() != constant.ChannelTypeSGLang {
		return payload
	}
	if !gjson.GetBytes(payload, path).Exists() {
		return payload
	}
	patched, err := sjson.SetBytes(payload, path, int(createdAt))
	if err != nil {
		return payload
	}
	return patched
}

func usageFromResponsesResponse(response *dto.OpenAIResponsesResponse) *dto.Usage {
	usage := &dto.Usage{}
	if response == nil || response.Usage == nil {
		return usage
	}
	service.ApplyResponsesUsage(usage, response.Usage)
	return usage
}

// incompleteResponsesStreamError marks a Responses stream that ended without a
// terminal event as an upstream failure, so the channel is evicted from the
// affinity binding instead of being re-bound as healthy. A missing terminal
// event is a protocol truncation even when the stream carried no output; it is
// not an empty-completion validation and must use the same upstream-failure
// classification for every output shape.
//
// Only a committed zero-output stream gets a synthetic response.failed event,
// because an uncommitted stream can still be retried before the client sees it.
// When partial output was already delivered, the content stays committed and the
// client's own protocol check ("stream closed before response.completed") drives
// the retry, so the stream is left untouched.
func incompleteResponsesStreamError(c *gin.Context, committed bool, hadOutput bool) *types.NewAPIError {
	options := []types.NewAPIErrorOptions{types.ErrOptionWithUpstreamFailure()}
	message := "upstream stream ended before response.completed"
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
	writeSyntheticResponsesStreamFailure(c, apiErr)
	return apiErr
}

// writeSyntheticResponsesStreamFailure emits the protocol-shaped response.failed
// event that terminates a zero-output Responses stream. It is used when the
// stream is already committed, and by the relay's deferred replay when a retry
// is no longer possible.
func writeSyntheticResponsesStreamFailure(c *gin.Context, apiErr *types.NewAPIError) {
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
}

// deferResponsesStreamReplay stores a closure that replays the lifecycle events
// held back by this attempt together with its terminal failure. The relay calls
// it only when it cannot retry anymore, so the client receives the same stream
// it would have gotten before the lifecycle events were deferred.
func deferResponsesStreamReplay(c *gin.Context, pending []deferredResponsesStreamEvent, writeTerminal func()) {
	if c == nil || len(pending) == 0 || writeTerminal == nil {
		return
	}
	c.Set(string(constant.ContextKeyResponsesStreamDeferredReplay), func() {
		for _, event := range pending {
			sendResponsesStreamData(c, event.payload, event.data)
		}
		writeTerminal()
	})
}
