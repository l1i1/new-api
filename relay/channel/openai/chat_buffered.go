package openai

import (
	"bufio"
	"fmt"
	"net/http"
	"strings"
	"time"

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

// OaiChatBufferedStreamHandler answers a *non-streaming* client from an upstream
// chat *stream*, by folding the deltas into one chat.completion body.
//
// Why the relay ever does this: a non-streaming request is otherwise bounded by
// RELAY_RESPONSE_HEADER_TIMEOUT, because the relay waits for response headers
// and a non-streaming upstream sends none until the entire answer exists. That
// ceiling cut real customer traffic: a 29m55s non-streaming request returned 200,
// while a sibling that needed slightly longer hit the 1800s limit and failed with
// nothing to show for it. Once the upstream streams, the header wait ends
// immediately ("once the headers arrive, streaming is unaffected"), the relay can
// tell "slow to start" from "hung", and a failure before the buffer completes
// leaves the client response uncommitted - so the ordinary retry loop fails over
// instead of surfacing a 30-minute dead end.
//
// Enabled per channel via internal_stream_for_nonstream; the caller has already
// rewritten the outbound body to stream=true (+ include_usage).
func OaiChatBufferedStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	accumulator := relayconvert.NewChatBufferedAccumulator()
	var streamErr *types.NewAPIError
	var usage *dto.Usage
	sawDone := false

	scanner := helper.NewStreamScanner(resp.Body)
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 6 || line[:5] != "data:" {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			sawDone = true
			break
		}

		// Same error semantics as OaiStreamHandler: a provider-specific error
		// payload inside the stream is the request's failure, not a chunk.
		if upstreamErr := service.NormalizeOpenAIStreamError(common.StringToByteSlice(data), resp.StatusCode); upstreamErr != nil {
			streamErr = upstreamErr
			break
		}
		if cyberErr := service.NewOpenAICyberPolicyError(c, common.StringToByteSlice(data), resp.StatusCode, true, usage); cyberErr != nil {
			streamErr = cyberErr
			break
		}

		var chunk dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
			logger.LogError(c, "failed to unmarshal buffered chat stream chunk: "+err.Error())
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
			break
		}
		if chunk.Model != "" {
			info.ObserveResponseModel(chunk.Model)
		}
		if chunk.Usage != nil && service.ValidUsage(chunk.Usage) {
			usage = dto.MergeUsage(usage, chunk.Usage)
		}
		accumulator.ProcessChunk(&chunk)
	}
	if streamErr != nil {
		// Return whatever usage the upstream already reported: a stream that
		// produced billable work before failing must settle that work (the caller
		// bills observed usage on the error path).
		return usageOrAccumulated(usage, accumulator), streamErr
	}
	if err := scanner.Err(); err != nil {
		return usageOrAccumulated(usage, accumulator), types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	// A stream that produced no answer structure at all, and never signalled the
	// end of one, was cut short. Answering with a partial body would hide a
	// channel failure behind a plausible-looking 200, so fail instead - retryable,
	// uncommitted, which is exactly the failover this mode exists to enable.
	//
	// The two "empty" cases are deliberately judged differently: [DONE] with no
	// chunks is a legitimate empty answer (the platform treats empty visible
	// output as valid upstream output), while no [DONE] and no choices at all is
	// truncation. [DONE] is NOT sufficient on its own once choices exist - an
	// upstream can close the stream early - so every accumulated choice must also
	// have terminated, or a half-finished answer goes out.
	if !(accumulator.ChoiceCount() == 0 && sawDone) && !accumulator.AllChoicesFinished() {
		return usageOrAccumulated(usage, accumulator), types.NewOpenAIError(
			fmt.Errorf("upstream chat stream ended before completion"),
			types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	final := accumulator.BuildResponse(helper.GetResponseID(c), info.UpstreamModelName, time.Now().Unix())
	if len(final.Choices) == 0 {
		// A well-formed empty answer for a non-streaming client: choices must not
		// be an empty array, so state one empty completion rather than a shape no
		// SDK expects.
		final.Choices = []dto.OpenAITextResponseChoice{{
			Index:        0,
			Message:      dto.Message{Role: "assistant", Content: ""},
			FinishReason: "stop",
		}}
	}
	if u := usageOrAccumulated(usage, accumulator); u != nil {
		final.Usage = *u
		usage = u
	}
	if final.Usage.TotalTokens == 0 {
		// Upstream reported nothing usable; fall back to the platform's own
		// accounting so the request is still billed.
		if estimated := service.ResponseText2Usage(c, accumulator.Text(), info.UpstreamModelName, info.GetEstimatePromptTokens()); estimated != nil {
			final.Usage = *estimated
			usage = estimated
		}
	}
	if usage == nil {
		// The caller dereferences the typed pointer, so never hand back a nil.
		usage = &dto.Usage{}
	}

	responseBody, err := common.Marshal(final)
	if err != nil {
		return usage, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	// The upstream answered with SSE; a non-streaming client must not inherit that
	// content type, and IOCopyBytesGracefully copies upstream headers verbatim.
	// Dropping it also stops net/http from re-adding it ahead of the body.
	resp.Header.Del("Content-Type")
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	service.IOCopyBytesGracefully(c, resp, responseBody)
	return usage, nil
}

// usageOrAccumulated prefers the usage merged from top-level chunks and falls
// back to the accumulator's copy (Moonshot K3 attaches terminal usage to
// choices[0], which only the accumulator reads).
func usageOrAccumulated(usage *dto.Usage, accumulator *relayconvert.ChatBufferedAccumulator) *dto.Usage {
	if usage != nil && usage.TotalTokens > 0 {
		return usage
	}
	if accumulated := accumulator.Usage(); accumulated != nil && accumulated.TotalTokens > 0 {
		return accumulated
	}
	return usage
}
