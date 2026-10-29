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
// while a sibling that needed slightly longer hit the response-header limit of the
// time (1800s) and failed with nothing to show for it. Once the upstream streams, the header wait ends
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

	startedAt := time.Now()
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
	// A cut answer is resumed inside the buffered body before the request fails:
	// the caller asked for one JSON response and nothing has been committed yet,
	// so a transparent continuation is strictly better than a 502 for work the
	// provider already generated. This is the non-streaming face of the 900s
	// provider wall (2026-10-29 08:35: one request died at the wall on channels
	// 91, 106 and 102 in a row - 45 minutes - and returned 502 with no content).
	continuationText := ""
	if !(accumulator.ChoiceCount() == 0 && sawDone) && !accumulator.AllChoicesFinished() {
		extra, mergedUsage := continueBufferedChatAnswer(c, info, accumulator, usageOrAccumulated(usage, accumulator))
		if extra == "" {
			wallErr := types.NewOpenAIError(
				fmt.Errorf("upstream chat stream ended before completion"),
				types.ErrorCodeBadResponse, http.StatusInternalServerError)
			// The provider wall is deterministic: every channel stops at the same
			// wall, so retrying the request elsewhere spends another full wall and
			// still fails. Give the caller the failure once it is clear we already
			// burned one wall, instead of multiplying a 15-minute wait by the retry
			// budget.
			if time.Since(startedAt) >= bufferedChatWallSkipRetryAfter {
				common.SysLog(fmt.Sprintf("buffered cut not retried: wall exceeded ch=%d elapsed=%s", info.ChannelId, time.Since(startedAt).Round(time.Second)))
				wallErr = types.NewOpenAIError(
					fmt.Errorf("upstream chat stream ended before completion"),
					types.ErrorCodeBadResponse, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
			}
			return usageOrAccumulated(usage, accumulator), wallErr
		}
		continuationText = extra
		usage = mergedUsage
		channel := continuationOutcomeChannel(info, info.ChatContinuation.RoundChannel)
		common.RecordContinuationOutcome(channel, true)
		logger.LogInfo(c, fmt.Sprintf("chat continuation recovered: mode=buffered-nonstream rounds_appended=%d prompt=%d completion=%d",
			len(continuationText), mergedUsage.PromptTokens, mergedUsage.CompletionTokens))
	}

	final := accumulator.BuildResponse(helper.GetResponseID(c), info.UpstreamModelName, time.Now().Unix())
	if continuationText != "" && len(final.Choices) > 0 {
		// The resumed rounds were folded into text here, not into the accumulator,
		// so the caller sees a single answer whose content is the cut prefix plus
		// the resumed tail.
		switch content := final.Choices[0].Message.Content.(type) {
		case string:
			final.Choices[0].Message.Content = content + continuationText
		case nil:
			// A thinking-only round leaves the message body empty; the resumed
			// answer is the first visible content the caller sees.
			final.Choices[0].Message.Content = continuationText
		}
		if final.Choices[0].FinishReason == "" || relaycommon.IsAbnormalFinishReason(final.Choices[0].FinishReason) {
			final.Choices[0].FinishReason = "stop"
		}
	}
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

// bufferedChatWallSkipRetryAfter is the round time after which a cut is treated
// as the provider's deterministic generation wall instead of a transient channel
// fault. Every channel in this pool stops at the same wall, so retrying the
// request elsewhere buys another full wall and the same failure - the only
// repair that works is the continuation below.
const bufferedChatWallSkipRetryAfter = 10 * time.Minute

// continueBufferedChatAnswer resumes an answer the upstream cut before its
// terminal frame, folding the resumed text into the buffered body instead of
// surfacing a 502.
//
// It reuses the streaming repair senders: a cut after visible content replays
// that prefix (output-cap mode), while a cut inside thinking replays the
// captured reasoning and asks for the answer directly (the wall shape). A round
// that produces nothing leaves the caller on the ordinary error path, and the
// response is still uncommitted because nothing has been marshalled yet.
//
// Returns the text to append to the buffered answer and the merged usage. Input
// tokens are billed once (mergeContinuationUsage keeps the first round's prompt
// count and adds the resumed output).
func continueBufferedChatAnswer(c *gin.Context, info *relaycommon.RelayInfo, accumulator *relayconvert.ChatBufferedAccumulator, baseUsage *dto.Usage) (string, *dto.Usage) {
	if info == nil || info.ChatContinuation == nil || accumulator == nil {
		return "", baseUsage
	}
	// A cut inside a tool call or across several choices is not repairable by
	// replaying text; those must stay errors the caller can retry.
	if accumulator.ChoiceCount() > 1 || accumulator.HasToolCalls() {
		return "", baseUsage
	}

	visible := accumulator.ContentText()
	reasoning := accumulator.ReasoningText()
	var (
		sendFn   func(string, int) (*http.Response, error)
		maxSteps int
		input    = visible
		thinking bool
	)
	if visible != "" {
		if cfg := info.ChatContinuation; cfg.Send != nil && cfg.MaxSteps > 0 {
			sendFn, maxSteps = cfg.Send, cfg.MaxSteps
		}
	} else if reasoning != "" {
		// No visible content yet: the round died inside thinking. Replaying the
		// reasoning and asking for the answer is the only shape that can pass the
		// same wall; further reasoning would just hit it again.
		var cfg *relaycommon.ChatAbnormalContinuationConfig
		if info.ChatContinuation.Abnormal != nil {
			cfg = info.ChatContinuation.Abnormal
		} else {
			cfg = info.ChatContinuation.Missing
		}
		if cfg != nil && cfg.Send != nil && cfg.MaxSteps > 0 {
			minChars := cfg.MinReasoningChars
			if minChars <= 0 {
				minChars = 512
			}
			if len(reasoning) >= minChars {
				sendFn, maxSteps, input, thinking = cfg.Send, 1, reasoning, true
			}
		}
	}
	if sendFn == nil || maxSteps <= 0 {
		return "", baseUsage
	}

	total := baseUsage
	if total == nil {
		total = cloneUsage(nil)
	}
	// One deduper for the whole repair: a model that restarts from its own
	// sentence after the wall must not make the seam repeat.
	seam := newSeamDeduper(visible)
	var added strings.Builder
	for step := 0; step < maxSteps; step++ {
		remaining := chatContinuationBudgetLeftWithContent(info, total, len(input)+added.Len())
		if remaining <= 0 {
			break
		}
		roundInput := input
		if !thinking {
			roundInput = input + added.String()
		}
		resp, sendErr := sendFn(roundInput, remaining)
		if sendErr != nil || resp == nil {
			break
		}
		roundText, roundUsage, finish := readBufferedContinuationRound(c, resp)
		if roundUsage != nil {
			total = mergeContinuationUsage(total, roundUsage)
		}
		if roundText == "" {
			continue
		}
		if thinking {
			// The answer round is replayed from the captured reasoning, so its text
			// is an answer, not a continuation of a prefix: no seam to match.
			added.WriteString(roundText)
			break
		}
		if seam != nil {
			text, ready := seam.feed(roundText)
			if !ready {
				text, _ = seam.finish()
			}
			roundText = text
		}
		added.WriteString(roundText)
		if finish != "" && !relaycommon.IsAbnormalFinishReason(finish) {
			break
		}
	}
	if added.Len() == 0 {
		return "", baseUsage
	}
	return added.String(), total
}

// readBufferedContinuationRound folds one resumed round's SSE body into text,
// usage and its terminal finish reason. Provider-specific in-stream error
// payloads end the round without text: the caller keeps whatever previous rounds
// produced and falls back to the ordinary error path when nothing was produced.
func readBufferedContinuationRound(c *gin.Context, resp *http.Response) (string, *dto.Usage, string) {
	defer service.CloseResponseBodyGracefully(resp)

	var sb strings.Builder
	var usage *dto.Usage
	finish := ""
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
			break
		}
		if service.NormalizeOpenAIStreamError(common.StringToByteSlice(data), resp.StatusCode) != nil {
			break
		}
		var chunk dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
			logger.LogError(c, "failed to unmarshal buffered continuation chunk: "+err.Error())
			break
		}
		if chunk.Usage != nil && service.ValidUsage(chunk.Usage) {
			usage = dto.MergeUsage(usage, chunk.Usage)
		}
		for _, choice := range chunk.Choices {
			if text := choice.Delta.GetContentString(); text != "" {
				sb.WriteString(text)
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finish = *choice.FinishReason
			}
		}
	}
	return sb.String(), usage, finish
}
