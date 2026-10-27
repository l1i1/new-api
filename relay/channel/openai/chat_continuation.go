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
func chatContinuationCandidate(info *relaycommon.RelayInfo, usage *dto.Usage, contentChars, toolCalls int) bool {
	if info == nil || usage == nil || info.ChatContinuation == nil ||
		info.ChatContinuation.Send == nil || info.ChatContinuation.MaxSteps <= 0 {
		return false
	}
	if !info.IsStream || info.RelayFormat != types.RelayFormatOpenAI || toolCalls > 0 {
		return false
	}
	if deepSeekV4FitEnabled(info) || kimiK3FitEnabled(info) {
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
func chatAbnormalContinuationCandidate(info *relaycommon.RelayInfo, usage *dto.Usage, contentChars, reasoningChars, toolCalls int) bool {
	if info == nil || usage == nil || info.ChatContinuation == nil || info.ChatContinuation.Abnormal == nil {
		return false
	}
	cfg := info.ChatContinuation.Abnormal
	if cfg.Send == nil || cfg.MaxSteps <= 0 {
		return false
	}
	if !info.IsStream || info.RelayFormat != types.RelayFormatOpenAI || toolCalls > 0 {
		return false
	}
	if deepSeekV4FitEnabled(info) || kimiK3FitEnabled(info) {
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

// runAbnormalChatContinuation resumes a cut-off thinking round. The captured
// reasoning is replayed as the assistant body (upstreams ignore
// reasoning_content in history); intermediate terminal/usage frames are
// suppressed so the client sees one stream and one terminal frame.
func runAbnormalChatContinuation(c *gin.Context, info *relaycommon.RelayInfo, baseUsage *dto.Usage, reasoning string) chatContinuationResult {
	total := cloneUsage(baseUsage)
	result := chatContinuationResult{usage: total, finish: info.StreamFinishReason}
	cfg := info.ChatContinuation.Abnormal
	contextText := reasoning
	for step := 0; step < cfg.MaxSteps; step++ {
		remaining := info.ClientMaxTokens - total.CompletionTokens
		if remaining <= 0 {
			break
		}
		resp, err := cfg.Send(contextText, remaining)
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
		roundUsage, finish, roundContent, deltaSeen, roundErr := consumeChatContinuationRound(c, info, resp)
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
	return &cloned
}

type chatContinuationResult struct {
	usage        *dto.Usage
	finish       string
	rounds       int
	contentChars int
}

// runChatContinuation stitches up to MaxSteps continuation rounds into the
// current client stream. Content/reasoning deltas of every round are forwarded
// as they arrive; intermediate terminal frames and usage events are suppressed
// because the client must see one continuous response with one terminal frame.
func runChatContinuation(c *gin.Context, info *relaycommon.RelayInfo, baseUsage *dto.Usage, partial string) chatContinuationResult {
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
		roundUsage, finish, roundContent, deltaSeen, roundErr := consumeChatContinuationRound(c, info, resp)
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
func consumeChatContinuationRound(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (usage *dto.Usage, finish string, content string, deltaSeen bool, err error) {
	if resp == nil || resp.Body == nil {
		return nil, "", "", false, fmt.Errorf("continuation response has no body")
	}
	scanner := helper.NewStreamScanner(resp.Body)
	scanner.Split(bufio.ScanLines)
	var builder strings.Builder
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
			usage = addChatContinuationUsage(usage, chunk.Usage)
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
		if sendErr := sendStreamData(c, info, forwardData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); sendErr != nil {
			return usage, finish, builder.String(), deltaSeen, sendErr
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return usage, finish, builder.String(), deltaSeen, scanErr
	}
	return usage, finish, builder.String(), deltaSeen, nil
}

// sendChatContinuationTerminal renders the single terminal frame for a
// transparently continued response. Usage is emitted separately by
// HandleFinalResponse so the client sees exactly one terminal frame and at most
// one usage event, matching an ordinary stream.
func sendChatContinuationTerminal(c *gin.Context, info *relaycommon.RelayInfo, responseId string, createAt int64, model, finish string) {
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

// addChatContinuationUsage accumulates the usage events inside ONE upstream
// round (providers may repeat or split usage). It must not be used to combine
// independent rounds: input tokens are billed once by mergeContinuationUsage.
func addChatContinuationUsage(total, round *dto.Usage) *dto.Usage {
	if round == nil {
		return total
	}
	if total == nil {
		cloned := *round
		return &cloned
	}
	total.PromptTokens += round.PromptTokens
	total.CompletionTokens += round.CompletionTokens
	total.TotalTokens += round.TotalTokens
	total.InputTokens += round.InputTokens
	total.OutputTokens += round.OutputTokens
	total.PromptCacheHitTokens += round.PromptCacheHitTokens
	total.ClaudeCacheCreation5mTokens += round.ClaudeCacheCreation5mTokens
	total.ClaudeCacheCreation1hTokens += round.ClaudeCacheCreation1hTokens
	total.PromptTokensDetails.CachedTokens += round.PromptTokensDetails.CachedTokens
	total.PromptTokensDetails.CacheWriteTokens += round.PromptTokensDetails.CacheWriteTokens
	total.PromptTokensDetails.CachedCreationTokens += round.PromptTokensDetails.CachedCreationTokens
	total.CompletionTokenDetails.ReasoningTokens += round.CompletionTokenDetails.ReasoningTokens
	return total
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
			total.BillingUsage = dto.NewOpenAIChatBillingUsage(total)
		} else {
			total.BillingUsage = nil
		}
	}
	return total
}
