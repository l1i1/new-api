package openai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// chatContinuationPrompt is the instruction appended after the truncated
// assistant message. It is deliberately neutral so it fits both an answer that
// was cut mid-sentence and a structured (for example numbered) output.
const chatContinuationPrompt = "上一条回复因输出长度限制被截断。请严格从中断处继续，不要重复已输出的内容，不要添加任何前言或解释。"

// chatThinkingContinuationPrompt is used by the abnormal-finish mode. The
// captured reasoning is replayed as the assistant body because upstreams
// ignore reasoning_content in the message history (verified 2026-10-27).
const chatThinkingContinuationPrompt = "上面是你在上一轮被上游截断的思考过程（当时还没有开始输出正文）。请从中断处继续思考，不要重复已经写过的内容，完成思考后直接给出最终答案。"

// chatAnswerContinuationPrompt is used by the abnormal/missing modes, where the
// previous round already burned the provider's whole wall (900s for these
// channels) on thinking and produced no visible answer. Asking it to continue
// thinking just walks into the same wall again, so the round must answer from
// the captured trace instead.
const chatAnswerContinuationPrompt = "上面是你上一轮被上游截断的思考过程。现在不要再继续思考，请立即基于上面的内容直接输出最终答案（可以简短），必须给出正文。"

// BuildChatAnswerContinuationBody rebuilds a request that resumes a cut-off
// round in answer mode: the captured reasoning goes into the assistant content
// and the round runs with the cheapest thinking budget the upstream accepts, so
// it produces the answer instead of spending another full wall on reasoning.
func BuildChatAnswerContinuationBody(baseJSON []byte, reasoning string, remainingTokens int) ([]byte, error) {
	out, err := buildChatContinuationBody(baseJSON, reasoning, chatAnswerContinuationPrompt, remainingTokens)
	if err != nil {
		return nil, err
	}
	if !gjson.GetBytes(out, "reasoning_effort").Exists() {
		return out, nil
	}
	var modelName string
	if request, ok := gjsonParseModel(baseJSON); ok {
		modelName = request
	}
	effort := kimiK3AggregatorDisabledEffortFor(modelName)
	if effort == "" {
		return out, nil
	}
	patched, patchErr := sjson.SetBytes(out, "reasoning_effort", effort)
	if patchErr != nil {
		return nil, patchErr
	}
	return patched, nil
}

// gjsonParseModel returns the request's model name, used to pick the cheapest
// thinking effort the upstream accepts.
func gjsonParseModel(body []byte) (string, bool) {
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		return "", false
	}
	return model, true
}

// BuildChatContinuationBody rebuilds the final upstream chat-completions body
// into a continuation request: the original messages, the visible answer so
// far as an assistant message, the continuation instruction, and the remaining
// output budget. Keeping the original body (after mapping and param overrides)
// means the continuation inherits the same model, tools, sampling parameters
// and channel-specific rewrites as the first round.
func BuildChatContinuationBody(baseJSON []byte, partialContent string, remainingTokens int) ([]byte, error) {
	return buildChatContinuationBody(baseJSON, partialContent, chatContinuationPrompt, remainingTokens)
}

// BuildChatThinkingContinuationBody rebuilds a request that resumes a cut-off
// thinking round. The captured reasoning is placed in the assistant content
// (not reasoning_content, which upstreams ignore) and the instruction asks the
// model to continue from that point.
func BuildChatThinkingContinuationBody(baseJSON []byte, reasoning string, remainingTokens int) ([]byte, error) {
	return buildChatContinuationBody(baseJSON, reasoning, chatThinkingContinuationPrompt, remainingTokens)
}

func buildChatContinuationBody(baseJSON []byte, partialContent, instruction string, remainingTokens int) ([]byte, error) {
	if remainingTokens <= 0 {
		return nil, fmt.Errorf("continuation: remaining output budget is %d", remainingTokens)
	}
	messages := gjson.GetBytes(baseJSON, "messages")
	if !messages.IsArray() {
		return nil, fmt.Errorf("continuation: base request has no messages array")
	}
	rawMessages := make([]json.RawMessage, 0, len(messages.Array())+2)
	for _, message := range messages.Array() {
		rawMessages = append(rawMessages, json.RawMessage(message.Raw))
	}
	assistantMessage, err := common.Marshal(map[string]any{"role": "assistant", "content": partialContent})
	if err != nil {
		return nil, err
	}
	instructionMessage, err := common.Marshal(map[string]any{"role": "user", "content": instruction})
	if err != nil {
		return nil, err
	}
	rawMessages = append(rawMessages, json.RawMessage(assistantMessage), json.RawMessage(instructionMessage))
	encodedMessages, err := common.Marshal(rawMessages)
	if err != nil {
		return nil, err
	}
	out, err := sjson.SetRawBytes(baseJSON, "messages", encodedMessages)
	if err != nil {
		return nil, err
	}
	maxUpdated := false
	if gjson.GetBytes(out, "max_completion_tokens").Exists() {
		out, err = sjson.SetBytes(out, "max_completion_tokens", remainingTokens)
		if err != nil {
			return nil, err
		}
		maxUpdated = true
	}
	if gjson.GetBytes(out, "max_tokens").Exists() {
		out, err = sjson.SetBytes(out, "max_tokens", remainingTokens)
		if err != nil {
			return nil, err
		}
		maxUpdated = true
	}
	if !maxUpdated {
		out, err = sjson.SetBytes(out, "max_tokens", remainingTokens)
		if err != nil {
			return nil, err
		}
	}
	if out, err = sjson.SetBytes(out, "stream", true); err != nil {
		return nil, err
	}
	if out, err = sjson.SetBytes(out, "stream_options.include_usage", true); err != nil {
		return nil, err
	}
	return out, nil
}

// chatContinuationCandidate decides whether the first round ended at an
// upstream output cap while the client still had budget left.
//
// Only a terminal finish_reason=length qualifies: a stop/tool_calls/content_filter
// already completed the answer, and a client whose own max_tokens was exhausted
// (completion >= ClientMaxTokens) must not receive more output than it asked for.
// The visible answer must be non-empty (a reasoning-only round has nothing to
// resume), no tool call may have been emitted, and the V4/K3 fit paths (which
// own their terminal frames) stay untouched.
func chatContinuationCandidate(info *relaycommon.RelayInfo, usage *dto.Usage, contentChars, toolCalls, choiceCount int) bool {
	if info == nil || usage == nil || info.ChatContinuation == nil ||
		info.ChatContinuation.Send == nil || info.ChatContinuation.MaxSteps <= 0 {
		return false
	}
	if !info.IsStream || info.RelayFormat != types.RelayFormatOpenAI || toolCalls > 0 || choiceCount > 1 {
		return false
	}
	if deepSeekV4FitEnabled(info) {
		return false
	}
	if strings.ToLower(strings.TrimSpace(info.StreamFinishReason)) != "length" {
		return false
	}
	if contentChars <= 0 || info.ClientMaxTokens <= 0 || usage.CompletionTokens >= info.ClientMaxTokens {
		return false
	}
	if info.StreamStatus != nil && info.StreamStatus.ResponseFailed() {
		return false
	}
	return true
}

// chatAbnormalContinuationCandidate decides whether a stream that died with an
// abnormal finish_reason can be resumed from its captured reasoning. It is the
// 900s cut-off shape: no visible content, a large reasoning trace, an abnormal
// finish, and a second upstream attempt available.
func chatAbnormalContinuationCandidate(info *relaycommon.RelayInfo, usage *dto.Usage, contentChars, reasoningChars, toolCalls, choiceCount int) bool {
	if info == nil || usage == nil || info.ChatContinuation == nil || info.ChatContinuation.Abnormal == nil {
		return false
	}
	cfg := info.ChatContinuation.Abnormal
	if cfg.Send == nil || cfg.MaxSteps <= 0 {
		return false
	}
	if !info.IsStream || info.RelayFormat != types.RelayFormatOpenAI || toolCalls > 0 || choiceCount > 1 {
		return false
	}
	if deepSeekV4FitEnabled(info) {
		return false
	}
	if info.ChannelSetting.ThinkingToContent || shouldSuppressReasoningContent(info) {
		return false
	}
	if contentChars > 0 {
		return false
	}
	minChars := cfg.MinReasoningChars
	if minChars <= 0 {
		minChars = 512
	}
	if reasoningChars < minChars {
		return false
	}
	if info.StreamStatus == nil || !info.StreamStatus.ResponseFailed() {
		return false
	}
	if !relaycommon.IsAbnormalFinishReason(info.StreamFinishReason) {
		return false
	}
	if info.ClientMaxTokens <= 0 || usage.CompletionTokens >= info.ClientMaxTokens {
		return false
	}
	return true
}

// chatMissingContinuationCandidate decides whether a stream that ended at
// EOF/[DONE] without any finish_reason can be resumed from its reasoning. It is
// the 60s silent cut shape: no visible content, a reasoning prefix, no terminal
// frame, and a second upstream attempt available.
func chatMissingContinuationCandidate(info *relaycommon.RelayInfo, usage *dto.Usage, contentChars, reasoningChars, toolCalls, choiceCount int, streamFinishSeen bool) bool {
	if info == nil || usage == nil || info.ChatContinuation == nil || info.ChatContinuation.Missing == nil {
		return false
	}
	cfg := info.ChatContinuation.Missing
	if cfg.Send == nil || cfg.MaxSteps <= 0 {
		return false
	}
	if !info.IsStream || info.RelayFormat != types.RelayFormatOpenAI || toolCalls > 0 || choiceCount > 1 {
		return false
	}
	if deepSeekV4FitEnabled(info) {
		return false
	}
	if info.ChannelSetting.ThinkingToContent || shouldSuppressReasoningContent(info) {
		return false
	}
	if streamFinishSeen || strings.TrimSpace(info.StreamFinishReason) != "" {
		return false
	}
	if contentChars > 0 {
		return false
	}
	minChars := cfg.MinReasoningChars
	if minChars <= 0 {
		minChars = 64
	}
	if reasoningChars < minChars {
		return false
	}
	if info.StreamStatus == nil || info.StreamStatus.EndReason == relaycommon.StreamEndReasonClientGone {
		return false
	}
	if info.ClientMaxTokens <= 0 || usage.CompletionTokens >= info.ClientMaxTokens {
		return false
	}
	return true
}

// chatLengthThinkingContinuationCandidate decides whether a stream that hit the
// provider output cap (finish_reason=length) before any visible content can be
// resumed from its captured reasoning. This is the 32k cap landing during the
// thinking phase: the prefix-based length continuation has no visible answer to
// replay, so the thinking continuation path has to take over.
func chatLengthThinkingContinuationCandidate(info *relaycommon.RelayInfo, usage *dto.Usage, contentChars, reasoningChars, toolCalls, choiceCount int) bool {
	if info == nil || usage == nil || info.ChatContinuation == nil || info.ChatContinuation.Abnormal == nil {
		return false
	}
	cfg := info.ChatContinuation.Abnormal
	if cfg.Send == nil || cfg.MaxSteps <= 0 {
		return false
	}
	if !info.IsStream || info.RelayFormat != types.RelayFormatOpenAI || toolCalls > 0 || choiceCount > 1 {
		return false
	}
	if deepSeekV4FitEnabled(info) {
		return false
	}
	if info.ChannelSetting.ThinkingToContent || shouldSuppressReasoningContent(info) {
		return false
	}
	if strings.ToLower(strings.TrimSpace(info.StreamFinishReason)) != "length" {
		return false
	}
	if contentChars > 0 {
		return false
	}
	minChars := cfg.MinReasoningChars
	if minChars <= 0 {
		minChars = 512
	}
	if reasoningChars < minChars {
		return false
	}
	if info.ClientMaxTokens <= 0 || usage.CompletionTokens >= info.ClientMaxTokens {
		return false
	}
	return true
}

// runLengthThinkingChatContinuation resumes a thinking round that died on the
// provider output cap. It reuses the reasoning continuation sender; the initial
// finish stays "length" so a round that ends without an answer is not reported
// as a normal completion.
func runLengthThinkingChatContinuation(c *gin.Context, info *relaycommon.RelayInfo, baseUsage *dto.Usage, reasoning string, meta chatContinuationMeta) chatContinuationResult {
	return runThinkingContinuation(c, info, baseUsage, reasoning, info.ChatContinuation.Abnormal, "length", meta)
}

// runAbnormalChatContinuation resumes a cut-off thinking round. The captured
// reasoning is replayed as the assistant body (upstreams ignore
// reasoning_content in history); intermediate terminal/usage frames are
// suppressed so the client sees one stream and one terminal frame.
func runAbnormalChatContinuation(c *gin.Context, info *relaycommon.RelayInfo, baseUsage *dto.Usage, reasoning string, meta chatContinuationMeta) chatContinuationResult {
	return runThinkingContinuation(c, info, baseUsage, reasoning, info.ChatContinuation.Abnormal, info.StreamFinishReason, meta)
}

func runMissingChatContinuation(c *gin.Context, info *relaycommon.RelayInfo, baseUsage *dto.Usage, reasoning string, meta chatContinuationMeta) chatContinuationResult {
	return runThinkingContinuation(c, info, baseUsage, reasoning, info.ChatContinuation.Missing, "", meta)
}

func runThinkingContinuation(c *gin.Context, info *relaycommon.RelayInfo, baseUsage *dto.Usage, reasoning string, cfg *relaycommon.ChatAbnormalContinuationConfig, initialFinish string, meta chatContinuationMeta) chatContinuationResult {
	total := cloneUsage(baseUsage)
	result := chatContinuationResult{usage: total, finish: initialFinish}
	contextText := reasoning
	for step := 0; step < cfg.MaxSteps; step++ {
		remaining := info.ClientMaxTokens - total.CompletionTokens
		if remaining <= 0 {
			break
		}
		resp, err := sendThinkingContinuationWithKeepAlive(c, info, cfg, contextText, remaining)
		if err != nil {
			logger.LogWarn(c, "thinking continuation request failed: "+err.Error())
			break
		}
		if resp == nil {
			break
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			logger.LogWarn(c, fmt.Sprintf("thinking continuation upstream status %d", resp.StatusCode))
			break
		}
		roundUsage, finish, roundContent, deltaSeen, roundErr := consumeChatContinuationRound(c, info, resp, meta)
		_ = resp.Body.Close()
		result.rounds++
		if roundUsage != nil {
			total = mergeContinuationUsage(total, roundUsage)
		}
		if finish != "" {
			result.finish = finish
		}
		contextText += roundContent
		result.contentChars += len(roundContent)
		if roundErr != nil {
			logger.LogWarn(c, "thinking continuation stream failed: "+roundErr.Error())
			break
		}
		if !deltaSeen {
			break
		}
		if finish == "" || relaycommon.IsAbnormalFinishReason(finish) || finish != "length" {
			break
		}
	}
	result.usage = total
	return result
}

func cloneUsage(in *dto.Usage) *dto.Usage {
	if in == nil {
		return &dto.Usage{}
	}
	cloned := *in
	if in.OutputTokensDetails != nil {
		details := *in.OutputTokensDetails
		cloned.OutputTokensDetails = &details
	}
	if in.InputTokensDetails != nil {
		details := *in.InputTokensDetails
		cloned.InputTokensDetails = &details
	}
	if in.BillingUsage != nil {
		cloned.BillingUsage = dto.CloneBillingUsage(in.BillingUsage)
	}
	return &cloned
}

type chatContinuationResult struct {
	usage        *dto.Usage
	finish       string
	rounds       int
	contentChars int
}

// chatContinuationMeta carries the first round's envelope fields. Continuation
// rounds are separate upstream requests with their own chunk ids, so every
// forwarded delta is rewritten to the first round's id/created/model/fingerprint.
// Clients must see one logical response, not a mid-stream id change.
type chatContinuationMeta struct {
	ID                string
	Created           int64
	Model             string
	SystemFingerprint string
}

// runChatContinuation stitches up to MaxSteps continuation rounds into the
// current client stream. Content/reasoning deltas of every round are forwarded
// as they arrive; intermediate terminal frames and usage events are suppressed
// because the client must see one continuous response with one terminal frame.
func runChatContinuation(c *gin.Context, info *relaycommon.RelayInfo, baseUsage *dto.Usage, partial string, meta chatContinuationMeta) chatContinuationResult {
	total := cloneUsage(baseUsage)
	result := chatContinuationResult{usage: total, finish: "length"}
	content := partial
	for step := 0; step < info.ChatContinuation.MaxSteps; step++ {
		remaining := info.ClientMaxTokens - total.CompletionTokens
		if remaining <= 0 {
			break
		}
		resp, err := info.ChatContinuation.Send(content, remaining)
		if err != nil {
			logger.LogWarn(c, "chat continuation request failed: "+err.Error())
			break
		}
		if resp == nil {
			break
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			logger.LogWarn(c, fmt.Sprintf("chat continuation upstream status %d", resp.StatusCode))
			break
		}
		roundUsage, finish, roundContent, deltaSeen, roundErr := consumeChatContinuationRound(c, info, resp, meta)
		_ = resp.Body.Close()
		result.rounds++
		if roundUsage != nil {
			total = mergeContinuationUsage(total, roundUsage)
		}
		if finish != "" {
			result.finish = finish
		}
		content += roundContent
		if roundErr != nil {
			logger.LogWarn(c, "chat continuation stream failed: "+roundErr.Error())
			break
		}
		if result.finish != "length" {
			break
		}
		if !deltaSeen {
			break
		}
		if total.CompletionTokens >= info.ClientMaxTokens {
			break
		}
	}
	result.usage = total
	return result
}

// consumeChatContinuationRound forwards one continuation round's deltas and
// returns that round's usage, terminal finish reason and visible content. The
// terminal frame and usage events are not forwarded: the caller renders one
// final frame with the merged usage.
func consumeChatContinuationRound(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response, meta chatContinuationMeta) (usage *dto.Usage, finish string, content string, deltaSeen bool, err error) {
	if resp == nil || resp.Body == nil {
		return nil, "", "", false, fmt.Errorf("continuation response has no body")
	}
	scanner := helper.NewStreamScanner(resp.Body)
	scanner.Split(bufio.ScanLines)
	var builder strings.Builder
	toolSeen := make(map[string]struct{})
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[len("data:"):])
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var chunk dto.ChatCompletionsStreamResponse
		if unmarshalErr := common.UnmarshalJsonStr(data, &chunk); unmarshalErr != nil {
			continue
		}
		if chunk.Usage != nil {
			// Usage events inside one round are cumulative snapshots, not
			// increments: merge fill-only so a repeated terminal/usage snapshot
			// cannot double-count the round.
			usage = dto.MergeUsage(usage, chunk.Usage)
		}
		forward := false
		for _, choice := range chunk.Choices {
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finish = *choice.FinishReason
			}
			delta := choice.Delta
			if delta.Content != nil && *delta.Content != "" {
				builder.WriteString(*delta.Content)
				forward = true
			}
			if delta.ReasoningContent != nil && *delta.ReasoningContent != "" {
				forward = true
			}
			for i, toolCall := range delta.ToolCalls {
				// Identity-dedupe exactly like round one: providers repeat the
				// function name on every chunk of one streamed call, and billing
				// is per call, not per chunk.
				if !isValidStreamFunctionToolCall(toolCall) {
					continue
				}
				name := strings.TrimSpace(toolCall.Function.Name)
				toolIdx := i
				if toolCall.Index != nil {
					toolIdx = *toolCall.Index
				}
				fallbackKey := fmt.Sprintf("index\x00%d\x00%d\x00%s", choice.Index, toolIdx, name)
				activeKey := fmt.Sprintf("active\x00%d\x00%d\x00%s", choice.Index, toolIdx, name)
				callID := strings.TrimSpace(toolCall.ID)
				if callID != "" {
					idKey := fmt.Sprintf("id\x00%d\x00%s", choice.Index, callID)
					if _, ok := toolSeen[idKey]; ok {
						continue
					}
					toolSeen[idKey] = struct{}{}
					toolSeen[activeKey] = struct{}{}
					if _, delayedID := toolSeen[fallbackKey]; delayedID {
						delete(toolSeen, fallbackKey)
						continue
					}
				} else {
					if _, ok := toolSeen[fallbackKey]; ok {
						continue
					}
					if _, ok := toolSeen[activeKey]; ok {
						continue
					}
					toolSeen[fallbackKey] = struct{}{}
					toolSeen[activeKey] = struct{}{}
				}
				info.CountBillableToolCall(dto.BuildInCallFunctionCall, name)
			}
			if len(delta.ToolCalls) > 0 {
				forward = true
			}
		}
		if !forward {
			continue
		}
		deltaSeen = true
		forwardData, stripErr := stripStreamUsageData(data)
		if stripErr != nil {
			forwardData = data
		}
		// A chunk may carry both a delta and a terminal finish_reason; forward
		// only the delta and let the synthetic terminal frame be the sole
		// finish marker.
		for i := range chunk.Choices {
			// The client already received the assistant role in round one; a
			// second role delta is a protocol seam.
			forwardData, _ = sjson.Delete(forwardData, fmt.Sprintf("choices.%d.delta.role", i))
			if chunk.Choices[i].FinishReason != nil && *chunk.Choices[i].FinishReason != "" {
				forwardData, _ = sjson.Delete(forwardData, fmt.Sprintf("choices.%d.finish_reason", i))
			}
		}
		forwardData = rewriteContinuationChunkMeta(forwardData, meta)
		// A Kimi K3 official-fit caller must not see the aggregator's chunk
		// shape just because this delta came from a continuation round: run the
		// same per-event fit the ordinary path applies, so the resumed segment
		// stays indistinguishable from one continuous official stream (key set,
		// empty-field stripping, reasoning delta normalization). The fit is
		// skipped for every other caller, whose frames stay byte-identical.
		if kimiK3FitEnabled(info) {
			forwardData = FitKimiK3StreamEventForAdapters(c, info, forwardData, nil, false)
			if forwardData == "" {
				continue
			}
		}
		helper.ExtendWriteDeadline(c)
		if sendErr := sendStreamData(c, info, forwardData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); sendErr != nil {
			return usage, finish, builder.String(), deltaSeen, sendErr
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return usage, finish, builder.String(), deltaSeen, scanErr
	}
	return usage, finish, builder.String(), deltaSeen, nil
}

// rewriteContinuationChunkMeta normalizes one continuation delta to the first
// round's envelope. Empty first-round fields are left alone; the fingerprint is
// deleted when the first round did not carry one, so clients cannot observe the
// second upstream's value.
func rewriteContinuationChunkMeta(data string, meta chatContinuationMeta) string {
	if data == "" {
		return data
	}
	var err error
	if meta.ID != "" {
		data, err = sjson.Set(data, "id", meta.ID)
		if err != nil {
			return data
		}
	}
	if meta.Created != 0 {
		data, err = sjson.Set(data, "created", meta.Created)
		if err != nil {
			return data
		}
	}
	if meta.Model != "" {
		data, err = sjson.Set(data, "model", meta.Model)
		if err != nil {
			return data
		}
	}
	if meta.SystemFingerprint != "" {
		data, _ = sjson.Set(data, "system_fingerprint", meta.SystemFingerprint)
	} else {
		data, _ = sjson.Delete(data, "system_fingerprint")
	}
	return data
}

// flushContinuationCoalescer flushes buffered deltas of the continuation
// rounds before the terminal frame. The normal stream path flushes right after
// the upstream body ends; a continuation returns early, so it must flush here
// or the client receives the terminal frame before the buffered content.
func flushContinuationCoalescer(c *gin.Context, info *relaycommon.RelayInfo) {
	if info == nil || !info.StreamCoalescer.Enabled() {
		return
	}
	for _, merged := range info.StreamCoalescer.Close() {
		helper.ExtendWriteDeadline(c)
		if err := writeStreamData(c, info, merged, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
			logger.LogError(c, "error writing coalesced continuation event: "+err.Error())
		}
	}
}

// sendChatContinuationTerminal renders the single terminal frame for a
// transparently continued response. Usage is emitted separately by
// HandleFinalResponse so the client sees exactly one terminal frame and at most
// one usage event, matching an ordinary stream.
//
// A Kimi K3 official-fit caller must keep the official stream shape: the
// terminal chunk carries the merged usage inside choices[0].usage and, when the
// client asked for stream usage, is followed by the choices:[] usage-only event.
// The generic frame below would leak the platform's internal usage shape and,
// because HandleFinalResponse suppresses its generic usage frame under the fit,
// leave the caller with no usage at all. So the fit owns both terminal frames
// for those callers; every other caller keeps the exact bytes it saw before.
func sendChatContinuationTerminal(c *gin.Context, info *relaycommon.RelayInfo, responseId string, createAt int64, model, finish string, usage *dto.Usage) {
	helper.ExtendWriteDeadline(c)
	if finish == "" {
		finish = "length"
	}
	if responseId == "" {
		responseId = "chatcmpl-continuation"
	}
	if createAt == 0 {
		createAt = time.Now().Unix()
	}
	if model == "" && info != nil {
		model = info.UpstreamModelName
	}
	if info != nil && kimiK3FitEnabled(info) {
		// The fit caller sees the official envelope order, and the fit moves the
		// merged usage into choices[0].usage (append keeps it last, where the
		// official endpoint puts it). A marshal failure falls through to the
		// generic frame rather than losing the terminal marker entirely.
		idJSON, idErr := common.Marshal(responseId)
		modelJSON, modelErr := common.Marshal(model)
		finishJSON, finishErr := common.Marshal(finish)
		if idErr == nil && modelErr == nil && finishErr == nil {
			terminalData := fmt.Sprintf(`{"id":%s,"object":"chat.completion.chunk","created":%d,"model":%s,"choices":[{"index":0,"delta":{},"finish_reason":%s}]}`, idJSON, createAt, modelJSON, finishJSON)
			if fitted := FitKimiK3StreamEventForAdapters(c, info, terminalData, usage, true); fitted != "" {
				terminalData = fitted
			}
			_ = sendStreamData(c, info, terminalData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent)
			if usageOnly := FitKimiK3StreamUsageOnlyChunk(info, terminalData); usageOnly != "" {
				helper.ExtendWriteDeadline(c)
				_ = sendStreamData(c, info, usageOnly, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent)
			}
			return
		}
	}
	payload := map[string]any{
		"id":      responseId,
		"object":  "chat.completion.chunk",
		"created": createAt,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": finish,
		}},
	}
	data, err := common.Marshal(payload)
	if err != nil {
		logger.LogError(c, "chat continuation terminal marshal failed: "+err.Error())
		return
	}
	_ = sendStreamData(c, info, string(data), info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent)
}

// mergeContinuationUsage folds one continuation round into the settled totals.
// Output tokens of every round are billable (they were forwarded to the
// client), but prompt/input tokens belong to the original request only: the
// continuation body replays the original input plus the reasoning we already
// produced, and billing that again would double-charge input.
func mergeContinuationUsage(total, round *dto.Usage) *dto.Usage {
	if round == nil {
		return total
	}
	if total == nil {
		return cloneUsage(round)
	}
	total.CompletionTokens += round.CompletionTokens
	total.OutputTokens += round.OutputTokens
	total.CompletionTokenDetails.TextTokens += round.CompletionTokenDetails.TextTokens
	total.CompletionTokenDetails.ReasoningTokens += round.CompletionTokenDetails.ReasoningTokens
	total.CompletionTokenDetails.AudioTokens += round.CompletionTokenDetails.AudioTokens
	total.CompletionTokenDetails.ImageTokens += round.CompletionTokenDetails.ImageTokens
	if total.OutputTokensDetails != nil && round.OutputTokensDetails != nil {
		total.OutputTokensDetails.TextTokens += round.OutputTokensDetails.TextTokens
		total.OutputTokensDetails.ReasoningTokens += round.OutputTokensDetails.ReasoningTokens
	}
	total.TotalTokens = total.PromptTokens + total.CompletionTokens
	// Settlement prefers BillingUsage when present. Rebuild the OpenAI-shaped
	// copy from the merged top-level usage so the second round's output is
	// billable too; any other semantic is dropped and settlement falls back to
	// the merged top-level numbers.
	if total.BillingUsage != nil {
		if total.BillingUsage.OpenAIUsage != nil {
			estimated := total.BillingUsage.Estimated
			total.BillingUsage = dto.NewOpenAIChatBillingUsage(total)
			if total.BillingUsage != nil {
				total.BillingUsage.Estimated = estimated
			}
		} else {
			total.BillingUsage = nil
		}
	}
	return total
}

func closeLateThinkingContinuationResponse(result <-chan thinkingContinuationSendResult) {
	r := <-result
	if r.resp != nil && r.resp.Body != nil {
		_ = r.resp.Body.Close()
	}
}

// writeThinkingContinuationPing writes an SSE comment without going through
// helper.PingData: the ping shares the request with the in-flight upstream call,
// and PingData touches RelayInfo counters that the sender may also touch. The
// comment only needs the client writer.
func writeThinkingContinuationPing(c *gin.Context) error {
	if c == nil || c.Writer == nil {
		return nil
	}
	helper.ExtendWriteDeadline(c)
	if _, err := c.Writer.WriteString(": PING\n\n"); err != nil {
		return err
	}
	helper.RecordKeepAliveBytes(c, len(": PING\n\n"))
	if err := http.NewResponseController(c.Writer).Flush(); err != nil {
		return err
	}
	return nil
}

// sendThinkingContinuationWithKeepAlive issues one continuation upstream call
// while writing SSE ping comments, so a client does not sit silent during the
// second attempt's prefill/header wait. The ping goroutine owns the client
// writer until Send returns; body deltas are only forwarded after that, so the
// writer is never touched concurrently.
// thinkingContinuationPingInterval is a variable so tests can shorten the
// keep-alive window without waiting ten seconds.
var thinkingContinuationPingInterval = 10 * time.Second

type thinkingContinuationSendResult struct {
	resp *http.Response
	err  error
}

func sendThinkingContinuationWithKeepAlive(c *gin.Context, info *relaycommon.RelayInfo, cfg *relaycommon.ChatAbnormalContinuationConfig, content string, remaining int) (*http.Response, error) {
	result := make(chan thinkingContinuationSendResult, 1)
	helper.ExtendWriteDeadline(c)
	allowPing := info == nil || !info.DisablePing
	prevDisablePing := false
	if info != nil {
		prevDisablePing = info.DisablePing
		// doRequest would start its own SSE pinger for the second attempt and
		// touch the same writer from another goroutine. The handler owns the
		// keepalive for continuation rounds, so suppress that pinger.
		info.DisablePing = true
	}
	go func() {
		resp, err := cfg.Send(content, remaining)
		result <- thinkingContinuationSendResult{resp: resp, err: err}
	}()
	ticker := time.NewTicker(thinkingContinuationPingInterval)
	defer ticker.Stop()
	for {
		select {
		case r := <-result:
			if info != nil {
				info.DisablePing = prevDisablePing
			}
			return r.resp, r.err
		case <-ticker.C:
			// Even when pings are disabled, keep the connection write deadline
			// alive during the second attempt's header wait.
			helper.ExtendWriteDeadline(c)
			if !allowPing {
				continue
			}
			if err := writeThinkingContinuationPing(c); err != nil {
				go closeLateThinkingContinuationResponse(result)
				return nil, err
			}
		case <-c.Request.Context().Done():
			go closeLateThinkingContinuationResponse(result)
			return nil, c.Request.Context().Err()
		}
	}
}
