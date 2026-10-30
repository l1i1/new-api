package openai

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	rootcommon "github.com/QuantumNous/new-api/common"
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

// chatContinuationRoundAttempts bounds how many times a resume round may be
// retried when it dies before forwarding a single delta. An upstream that has
// just hit its own wall (the 900s cut) often rejects the follow-up request
// immediately; without this retry the gateway abandoned a half-written answer
// and surfaced a 502 for work already tens of kilobytes into the caller's
// answer (2026-10-28 21:21-21:22, kimi-k3.1-pro). A round that already
// forwarded deltas is never retried: the caller would see duplicates.
const chatContinuationRoundAttempts = 3

// chatContinuationRetryBackoff is the linear backoff between resume attempts.
const chatContinuationRetryBackoff = 300 * time.Millisecond

// chatContinuationRetryBudget bounds the wall time one round may spend retrying:
// a slow-rejecting upstream must not stretch an already long request.
const chatContinuationRetryBudget = 5 * time.Second

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
func BuildChatAnswerContinuationBody(baseJSON []byte, reasoning string, remainingTokens int, prefill string) ([]byte, error) {
	if prefill != "" {
		// Reasoning-only resumes have no visible prefix to hand back, and every
		// measured prefill dialect (kimi partial in particular) restarts from
		// scratch when the assistant prefix is empty. Callers must not wire the
		// reasoning modes for a prefill channel; failing loudly here keeps a
		// mis-wired caller from spending a round that can only duplicate work.
		return nil, fmt.Errorf("continuation: reasoning-only resume is unavailable for prefill mode %q", prefill)
	}
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
func BuildChatContinuationBody(baseJSON []byte, partialContent, reasoning string, remainingTokens int, prefill string) ([]byte, error) {
	if prefill != "" && strings.TrimSpace(partialContent) != "" && !continuationBodyRequestsJSON(baseJSON) {
		return buildPrefillContinuationBody(baseJSON, partialContent, reasoning, prefill, remainingTokens)
	}
	return buildChatContinuationBody(baseJSON, partialContent, chatContinuationPrompt, remainingTokens)
}

// continuationBodyRequestsJSON reports whether the caller asked for a JSON
// object. Kimi's guide warns that Partial Mode must not be mixed with
// response_format=json_object, so that combination falls back to the
// instruction-style continuation instead of sending a prefilled assistant turn.
func continuationBodyRequestsJSON(baseJSON []byte) bool {
	switch strings.TrimSpace(gjson.GetBytes(baseJSON, "response_format.type").String()) {
	case "json_object", "json_schema":
		return true
	}
	return false
}

// buildPrefillContinuationBody hands the provider the answer prefix it already
// produced and lets it continue natively. Unlike the instruction-style body this
// adds no trailing user message: the assistant turn is the last message, which
// is exactly the shape Kimi Partial Mode, DeepSeek prefix completion and Qwen
// partial mode expect. The other request properties (model, tools, sampling,
// channel rewrites) stay untouched, so the round runs on the same model with the
// same budget mapping.
func buildPrefillContinuationBody(baseJSON []byte, content, reasoning, prefill string, remainingTokens int) ([]byte, error) {
	if remainingTokens <= 0 {
		return nil, fmt.Errorf("continuation: remaining output budget is %d", remainingTokens)
	}
	messages := gjson.GetBytes(baseJSON, "messages")
	if !messages.IsArray() {
		return nil, fmt.Errorf("continuation: base request has no messages array")
	}
	assistant := map[string]any{"role": "assistant", "content": content}
	switch prefill {
	case dto.PrefillKimiPartial:
		assistant["partial"] = true
		// Thinking mode needs the reasoning the model already emitted; the guide
		// passes it back next to the prefix so the resumed turn continues the
		// same chain instead of starting a fresh one.
		if strings.TrimSpace(reasoning) != "" {
			assistant["reasoning_content"] = reasoning
		}
	case dto.PrefillQwenPartial:
		assistant["partial"] = true
	case dto.PrefillDeepseekPrefix:
		assistant["prefix"] = true
	default:
		return nil, fmt.Errorf("continuation: unsupported prefill mode %q", prefill)
	}
	encodedAssistant, err := common.Marshal(assistant)
	if err != nil {
		return nil, err
	}
	rawMessages := make([]json.RawMessage, 0, len(messages.Array())+1)
	for _, message := range messages.Array() {
		rawMessages = append(rawMessages, json.RawMessage(message.Raw))
	}
	rawMessages = append(rawMessages, json.RawMessage(encodedAssistant))
	encodedMessages, err := common.Marshal(rawMessages)
	if err != nil {
		return nil, err
	}
	out, err := sjson.SetRawBytes(baseJSON, "messages", encodedMessages)
	if err != nil {
		return nil, err
	}
	return finishContinuationBody(out, remainingTokens)
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
	return finishContinuationBody(out, remainingTokens)
}

// finishContinuationBody applies the shared tail of every continuation body: the
// remaining output budget on whichever max-token field the caller used, and a
// streaming request that carries usage.
func finishContinuationBody(out []byte, remainingTokens int) ([]byte, error) {
	var err error
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

// undeclaredClientOutputBudget is the output budget one continuation round may
// spend when the request declared no max_tokens at all (ClientMaxTokens == 0).
//
// Such a request has no client-side ceiling: max_tokens is optional, and the
// real ceiling is the model's own output cap, which the gateway does not hold
// per model. Refusing the repair outright was wrong in production: 2026-10-28
// 18:21 one kimi-k3.1-pro cut reached the caller as a 502 purely because the
// guard read ClientMaxTokens <= 0 as "budget exhausted" even though only 52
// tokens had been produced. Bounding each round instead of the total keeps the
// declared-budget contract intact (a declared request is still hard-capped) and
// gives an undeclared one one bounded step per round, with the continuation
// configuration's MaxSteps bounding the number of rounds. The value matches the
// smallest output cap among the models this gateway serves, so a round can never
// ask an upstream for more output than the model can produce.
const undeclaredClientOutputBudget = 8192

// chatContinuationOutputBudget is the output budget a continuation may use:
// the declared max_tokens when there is one, otherwise the undeclared per-round
// step budget.
func chatContinuationOutputBudget(info *relaycommon.RelayInfo) int {
	if info == nil {
		return 0
	}
	if info.ClientMaxTokens > 0 {
		return info.ClientMaxTokens
	}
	return undeclaredClientOutputBudget
}

// chatContinuationBudgetLeft is the output budget a continuation round may still
// spend. A declared budget counts everything already produced against the
// ceiling the client asked for; an undeclared request gets a fresh per-round
// step, because output that was produced before the ceiling existed cannot have
// exhausted it.
// chatContinuationBudgetLeftWithContent bounds the next round by both the
// provider-reported completion tokens and a local estimate of the text already
// delivered. An upstream that ignores stream usage would otherwise let every round
// reuse the same remaining budget: the answer could exceed the caller's max_tokens
// while those extra tokens stayed unbilled (2026-10-29 review, H8).
func chatContinuationBudgetLeftWithContent(info *relaycommon.RelayInfo, usage *dto.Usage, deliveredChars int) int {
	remaining := chatContinuationBudgetLeft(info, usage)
	if deliveredChars <= 0 || info == nil || info.ClientMaxTokens <= 0 {
		return remaining
	}
	estimate := deliveredChars / 4
	if usage != nil && int(usage.CompletionTokens) > estimate {
		estimate = int(usage.CompletionTokens)
	}
	if byContent := info.ClientMaxTokens - estimate; byContent < remaining {
		remaining = byContent
	}
	if remaining < 0 {
		remaining = 0
	}
	return remaining
}

func chatContinuationBudgetLeft(info *relaycommon.RelayInfo, usage *dto.Usage) int {
	budget := chatContinuationOutputBudget(info)
	if budget <= 0 {
		return 0
	}
	if info.ClientMaxTokens <= 0 {
		return budget
	}
	used := 0
	if usage != nil {
		used = usage.CompletionTokens
	}
	if used >= budget {
		return 0
	}
	return budget - used
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
	// An ordinary output-cap resume only spends a budget the caller declared. An
	// undeclared request has no ceiling, so resuming a legitimate model cap would
	// generate (and bill) unbounded rounds; the abnormal and missing cut shapes
	// keep the per-round step for undeclared requests.
	if contentChars <= 0 || info.ClientMaxTokens <= 0 || usage.CompletionTokens >= info.ClientMaxTokens {
		return false
	}
	if info.StreamStatus != nil && info.StreamStatus.ResponseFailed() {
		return false
	}
	return true
}

// toolCallsBlockContinuation reports whether the tool-call fragment observed on
// this stream makes a continuation impossible. A fragment held by the channel's
// tool-call buffer never reached the caller, so a repair may drop it and resume
// the answer (the resumed round can deliver the complete call set); only a call
// the caller already received is final.
func toolCallsBlockContinuation(info *relaycommon.RelayInfo, toolCalls int) bool {
	if toolCalls <= 0 {
		return false
	}
	return !bufferedToolCallDiscardable(info, toolCalls)
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
	if !info.IsStream || info.RelayFormat != types.RelayFormatOpenAI || toolCallsBlockContinuation(info, toolCalls) || choiceCount > 1 {
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
	if chatContinuationBudgetLeft(info, usage) <= 0 {
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
	if !info.IsStream || info.RelayFormat != types.RelayFormatOpenAI || toolCallsBlockContinuation(info, toolCalls) || choiceCount > 1 {
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
	if chatContinuationBudgetLeft(info, usage) <= 0 {
		return false
	}
	return true
}

// chatMissingVisibleContinuationCandidate decides whether a stream that ended at
// EOF/[DONE] without any finish_reason can be resumed from the visible answer it
// already delivered.
//
// This is the cut-mid-answer shape (2026-10-28: a degraded kimi-k3.1-pro
// upstream truncated 1.4-2.4KB answers at 13-22s in production): the client is
// already reading a partial answer, so the reasoning resume cannot repair it
// (chatMissingContinuationCandidate refuses any visible content), and discarding
// the prefix surfaces a 502 for work that can still be completed. The visible
// prefix is continued through the same transparent continuation used for a
// provider length cap and for an abnormal finish after the answer started.
//
// Guards mirror those paths: one choice, no tool calls, a client budget left, and
// no client disconnect (that is the caller's own cancellation, not a truncation).
func chatMissingVisibleContinuationCandidate(info *relaycommon.RelayInfo, usage *dto.Usage, contentChars, toolCalls, choiceCount int, streamFinishSeen bool) bool {
	if info == nil || usage == nil || info.ChatContinuation == nil || info.ChatContinuation.Missing == nil {
		return false
	}
	// The visible prefix is continued through the output-cap runner, which sends
	// with the top-level sender; the Missing config is what opts this request and
	// channel into truncation repair at all.
	if info.ChatContinuation.Send == nil || info.ChatContinuation.MaxSteps <= 0 || info.ChatContinuation.Missing.MaxSteps <= 0 {
		return false
	}
	if !info.IsStream || info.RelayFormat != types.RelayFormatOpenAI || toolCallsBlockContinuation(info, toolCalls) || choiceCount > 1 {
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
	if contentChars <= 0 {
		return false
	}
	if info.StreamStatus == nil || info.StreamStatus.EndReason == relaycommon.StreamEndReasonClientGone {
		return false
	}
	if chatContinuationBudgetLeft(info, usage) <= 0 {
		return false
	}
	return true
}

// truncationRepairSkipReason explains why a truncated stream reached the caller
// instead of being repaired by one of the continuation modes. Every guard that
// silently refuses a repair is named here so the one production shape that keeps
// failing can be identified from a single log line instead of reverse-engineering
// the decision matrix (2026-10-28: the missing-finish-with-content gap took an
// hour to find because the only trace was "candidate=false").
//
// Returns "" when no guard is known to apply; the caller still logs the line, so
// an unnamed skip shows up as an empty reason rather than disappearing.
func truncationRepairSkipReason(info *relaycommon.RelayInfo, usage *dto.Usage, contentChars, reasoningChars, toolCalls, choiceCount int) string {
	if info == nil {
		return "no_relay_info"
	}
	if !info.IsStream || info.RelayFormat != types.RelayFormatOpenAI {
		return "unsupported_format"
	}
	if info.ChatContinuation == nil {
		return "continuation_not_configured"
	}
	if toolCallsBlockContinuation(info, toolCalls) {
		return "tool_calls_in_progress"
	}
	if choiceCount > 1 {
		return "multiple_choices"
	}
	if deepSeekV4FitEnabled(info) {
		return "deepseek_v4_fit_pin"
	}
	if info.ChannelSetting.ThinkingToContent || shouldSuppressReasoningContent(info) {
		return "reasoning_suppressed_or_folded"
	}
	if info.StreamStatus != nil && info.StreamStatus.EndReason == relaycommon.StreamEndReasonClientGone {
		return "client_gone"
	}
	if chatContinuationBudgetLeft(info, usage) <= 0 {
		return "client_budget_exhausted"
	}
	if info.ChatContinuation.Missing == nil && info.ChatContinuation.Abnormal == nil {
		return "continuation_not_configured"
	}
	if contentChars > 0 {
		if info.ChatContinuation.Send == nil || info.ChatContinuation.MaxSteps <= 0 {
			return "visible_continuation_disabled"
		}
		return ""
	}
	if reasoningChars <= 0 {
		return "nothing_to_continue"
	}
	return ""
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
	if chatContinuationBudgetLeft(info, usage) <= 0 {
		return false
	}
	return true
}

// runLengthThinkingChatContinuation resumes a thinking round that died on the
// provider output cap. It reuses the reasoning continuation sender; the initial
// finish stays "length" so a round that ends without an answer is not reported
// as a normal completion.
func runLengthThinkingChatContinuation(c *gin.Context, info *relaycommon.RelayInfo, baseUsage *dto.Usage, reasoning string, meta chatContinuationMeta) chatContinuationResult {
	if info == nil || info.ChatContinuation == nil {
		return chatContinuationResult{usage: cloneUsage(baseUsage)}
	}
	return runThinkingContinuation(c, info, baseUsage, reasoning, info.ChatContinuation.Abnormal, "length", meta)
}

// continuationRoundMaxElapsedDefaultSeconds is the fallback wall clock cap for a
// repair when the hot option is unset.
const continuationRoundMaxElapsedDefaultSeconds = 1500

// continuationPostCutToolCallGraceSeconds is the extra wall clock a repair gets
// when it resumes a request that was cut at the provider's stream wall while a
// tool call was being written. Such a request spent the whole round budget on
// thinking before the wall (2026-10-30: cuts at 1200-1209s with ~20k completion
// tokens and ~70k reasoning characters), so a start-anchored budget leaves only
// seconds for the resumed round - the round could never deliver the call. The
// grace applies only to that shape, which never repaired at all before, so an
// ordinary cut keeps the original budget exactly.
const continuationPostCutToolCallGraceSeconds = 600

// continuationElapsedCapReached reports whether this request has spent enough
// wall clock that another continuation round is not worth starting. The cap is
// counted from the request start (not from the cut), so three rounds on three
// dying channels cannot multiply the caller's wait by three walls.
func continuationElapsedCapReached(info *relaycommon.RelayInfo) bool {
	if info == nil || info.StartTime.IsZero() {
		return false
	}
	seconds := rootcommon.ChatContinuationRoundMaxElapsedSeconds
	if seconds <= 0 {
		seconds = continuationRoundMaxElapsedDefaultSeconds
	}
	limit := time.Duration(seconds) * time.Second
	if info.ToolCallFragmentDiscarded {
		limit += continuationPostCutToolCallGraceSeconds * time.Second
	}
	return time.Since(info.StartTime) > limit
}

// continuationRotationEnabled reports whether later rounds may rotate to another
// channel. Disabling it restores the legacy same-channel rounds.
func continuationRotationEnabled() bool {
	return rootcommon.ChatContinuationRotateChannels
}

// continuationRoundLimit resolves the round budget. An explicitly configured
// rotation-era budget wins (0 = transparent continuation off); when the key was
// never set, the deployment keeps its legacy per-mode round counts, so upgrading
// does not silently multiply the rounds a cut costs.
func continuationRoundLimit(cfgRounds, cfgSteps int) int {
	if rootcommon.ChatContinuationMaxRoundsSet {
		if rootcommon.ChatContinuationMaxRounds <= 0 {
			return 0
		}
		return rootcommon.ChatContinuationMaxRounds
	}
	if cfgRounds < 0 || cfgSteps < 0 {
		return 0
	}
	if cfgRounds > 0 {
		return cfgRounds
	}
	return cfgSteps
}

// continuationProviderOutputCap reports the provider output cap this usage hit,
// or 0 when it did not land on one. A stream that stops exactly at a configured
// cap after only reasoning is a by-design truncation: the provider ended the
// stream without saying finish_reason=length (2026-10-30 ch102: 32000 completion
// tokens, [DONE], no terminal frame), and the official endpoint answers that
// shape with a normal length completion.
func continuationProviderOutputCap(usage *dto.Usage) int {
	if usage == nil || usage.CompletionTokens <= 0 {
		return 0
	}
	completion := int(usage.CompletionTokens)
	for _, cap := range rootcommon.ChatContinuationOutputCaps {
		if cap > 0 && completion >= cap {
			return cap
		}
	}
	return 0
}

// continuationResultUsable reports whether a repair produced something the caller
// can use: either visible text closed by a real terminal frame, or a buffered
// tool-call set that validated and was delivered (the round may itself have lost
// its terminal frame in that case, which is why terminal is not required).
func continuationResultUsable(result chatContinuationResult) bool {
	if result.rounds <= 0 || result.finish == "" || relaycommon.IsAbnormalFinishReason(result.finish) {
		return false
	}
	if result.toolCallsDelivered {
		return true
	}
	return result.terminal && result.toolCalls == 0 && result.contentChars > 0
}

// continuationRoundChannelID names the channel that served the latest round.
func continuationRoundChannelID(info *relaycommon.RelayInfo, tracker *relaycommon.ContinuationRoundChannel) int {
	if tracker != nil {
		if id := tracker.Last(); id > 0 {
			return id
		}
	}
	if info == nil {
		return 0
	}
	return info.ChannelId
}

// continuationOutcomeChannel names the channel a round outcome belongs to: the last
// channel that actually served a round when tracked, otherwise the request channel.
func continuationOutcomeChannel(info *relaycommon.RelayInfo, tracker *relaycommon.ContinuationRoundChannel) int {
	return continuationRoundChannelID(info, tracker)
}

// continuationContentOutcomeName labels one visible-answer round for the log.
func continuationContentOutcomeName(ok bool, round contentRound, err error) string {
	switch {
	case errors.Is(err, relaycommon.ErrNoContinuationCandidate):
		return "no_candidate"
	case !ok:
		return "send_failed"
	case round.toolCalls > 0:
		return "tool_calls"
	case round.finish != "" && !relaycommon.IsAbnormalFinishReason(round.finish):
		return "terminal"
	case round.content == "":
		return "no_content"
	default:
		return "cut"
	}
}

// continuationThinkingOutcomeName labels one reasoning-resume round for the log.
func continuationThinkingOutcomeName(ok bool, round thinkingRound, err error) string {
	switch {
	case errors.Is(err, relaycommon.ErrNoContinuationCandidate):
		return "no_candidate"
	case !ok:
		return "send_failed"
	case round.toolCalls > 0:
		return "tool_calls"
	case round.finish != "" && !relaycommon.IsAbnormalFinishReason(round.finish):
		return "terminal"
	case round.content == "":
		return "no_answer"
	default:
		return "cut"
	}
}

// continuationShouldHopFirst reports whether this resume was triggered by a
// provider wall (the request has already been running for most of the wall) and
// a cross-channel sender is configured. A wall-triggered repair starts on another
// channel: the channel that just ended a generation at the wall cannot finish the
// answer inside it either.
const continuationHopFirstAfter = 600 * time.Second

func continuationShouldHopFirst(info *relaycommon.RelayInfo, hasFallback bool) bool {
	if info == nil || !hasFallback || info.StartTime.IsZero() {
		return false
	}
	return time.Since(info.StartTime) >= continuationHopFirstAfter
}

// runThinkingContinuation stitches reasoning-resume rounds into the current
// client stream: round 1 on the channel that produced the reasoning, then a
// rotation over channels this request has not tried. The round budget, the
// elapsed cap and the shared tried set bound the repair.
func runThinkingContinuation(c *gin.Context, info *relaycommon.RelayInfo, baseUsage *dto.Usage, reasoning string, cfg *relaycommon.ChatAbnormalContinuationConfig, initialFinish string, meta chatContinuationMeta) chatContinuationResult {
	total := cloneUsage(baseUsage)
	result := chatContinuationResult{usage: total, finish: initialFinish}
	if info == nil || cfg == nil {
		return result
	}
	if cfg.Tried != nil {
		cfg.Tried.Add(info.ChannelId)
	}
	rounds := continuationRoundLimit(cfg.MaxRounds, cfg.MaxSteps)
	rotate := continuationRotationEnabled() && cfg.Fallback != nil
	hopFirst := continuationShouldHopFirst(info, cfg.Fallback != nil)
	recovered := false
	contextText := reasoning
	visible := ""
	for round := 0; round < rounds; round++ {
		if continuationElapsedCapReached(info) {
			result.failedReason = relaycommon.ContinuationFailureElapsedCap
			break
		}
		remaining := chatContinuationBudgetLeftWithContent(info, total, result.contentChars)
		if remaining <= 0 {
			result.failedReason = relaycommon.ContinuationFailureBudgetExhausted
			break
		}
		sender, label := cfg.Send, "same-channel"
		if (round == 0 && hopFirst) || round > 0 {
			if rotate {
				sender, label = cfg.Fallback, "cross-channel"
			}
		}
		if sender == nil {
			result.failedReason = relaycommon.ContinuationFailureNoCandidate
			break
		}
		// Rotation is the retry mechanism for later rounds; the owning channel keeps
		// its bounded same-channel retries (it is the one with the prompt cache).
		attempts := 1
		if !rotate || (round == 0 && !hopFirst) {
			attempts = chatContinuationRoundAttempts
		}
		roundOut, ok, err := sendThinkingRound(c, info, sender, contextText, remaining, meta, label, attempts, newSeamDeduper(visible))
		if errors.Is(err, relaycommon.ErrNoContinuationCandidate) {
			result.failedReason = relaycommon.ContinuationFailureNoCandidate
			break
		}
		result.rounds++
		channel := continuationRoundChannelID(info, cfg.RoundChannel)
		result.channels = append(result.channels, channel)
		logger.LogWarn(c, fmt.Sprintf("chat continuation round: n=%d ch=%d outcome=%s remaining=%d", result.rounds, channel, continuationThinkingOutcomeName(ok, roundOut, err), remaining))
		if !ok {
			if errors.Is(err, relaycommon.ErrNoContinuationCandidate) {
				result.failedReason = relaycommon.ContinuationFailureNoCandidate
				break
			}
			result.failedReason = relaycommon.ContinuationFailureRoundIncomplete
			if !rotate {
				// No other channel is available: retrying the channel that just refused
				// the resume is what the old attempt loop did, and it never helped.
				break
			}
			continue
		}
		result.terminal = roundOut.terminal
		// Bill a resumed round only when it delivered something the caller can use.
		// A round that replayed reasoning and died burned provider tokens but gave the
		// caller nothing, so its usage is not merged (same rule as the pre-rotation
		// hop, 2026-10-29 request …Zit8of32).
		if roundOut.usage != nil && (roundOut.content != "" || roundOut.toolCalls > 0) {
			total = mergeContinuationUsage(total, roundOut.usage)
		}
		if roundOut.finish != "" {
			result.finish = roundOut.finish
		}
		result.toolCalls += roundOut.toolCalls
		contextText += roundOut.content
		visible += roundOut.content
		result.contentChars += len(roundOut.content)
		if roundOut.toolCalls > 0 {
			if roundOut.toolCallsDelivered {
				result.toolCallsDelivered = true
				result.finish = constant.FinishReasonToolCalls
				result.failedReason = ""
				recovered = true
				break
			}
			result.failedReason = relaycommon.ContinuationFailureToolCalls
			break
		}
		// A "length" finish means the resumed round was capped again: that is not a
		// success, the rotation continues. Any other real terminal reason ends the
		// repair - classify it before the content-only breaks, or a successful
		// resume that also delivered text would be labelled round_incomplete.
		if roundOut.finish != "" && roundOut.finish != "length" && !relaycommon.IsAbnormalFinishReason(roundOut.finish) {
			result.failedReason = ""
			recovered = true
			break
		}
		if roundOut.content != "" && roundOut.finish != "length" {
			// Visible text already reached the caller and this round died on top of it:
			// the cut is final. A reasoning repair must never continue on another
			// channel with a half-delivered answer (the caller would read it twice);
			// the caller closes the response honestly instead.
			result.failedReason = relaycommon.ContinuationFailureRoundIncomplete
			break
		}
		if roundOut.err != nil {
			result.failedReason = relaycommon.ContinuationFailureRoundIncomplete
			break
		}
		if roundOut.content == "" && roundOut.finish != "length" {
			result.failedReason = relaycommon.ContinuationFailureRoundIncomplete
			continue
		}
	}
	if result.failedReason == "" && !recovered {
		result.failedReason = relaycommon.ContinuationFailureRoundsExhausted
	}
	// Same affinity rule as the content runner: a hop-served recovery means the
	// original channel cannot finish this conversation's reasoning right now, so the
	// next request must not be pinned back to it.
	if recovered && cfg.RoundChannel != nil {
		if served := cfg.RoundChannel.Last(); served != 0 && served != info.ChannelId {
			service.DropStickyRoutingAfterUpstreamFailure(c)
		}
	}
	result.usage = total
	common.RecordContinuationOutcome(continuationOutcomeChannel(info, cfg.RoundChannel), result.rounds > 0 && (result.contentChars > 0 || result.toolCallsDelivered))
	return result
}

// sendThinkingRound spends one sender on one reasoning-resume round.
func sendThinkingRound(c *gin.Context, info *relaycommon.RelayInfo, sender func(string, int) (*http.Response, error), contextText string, remaining int, meta chatContinuationMeta, label string, attempts int, seam *seamDeduper) (thinkingRound, bool, error) {
	return sendAndReadThinkingRound(c, info, sender, contextText, remaining, meta, label, attempts, seam)
}

// runAbnormalChatContinuation resumes a cut-off thinking round. The captured
// reasoning is replayed as the assistant body (upstreams ignore
// reasoning_content in history); intermediate terminal/usage frames are
// suppressed so the client sees one stream and one terminal frame.
func runAbnormalChatContinuation(c *gin.Context, info *relaycommon.RelayInfo, baseUsage *dto.Usage, reasoning string, meta chatContinuationMeta) chatContinuationResult {
	if info == nil || info.ChatContinuation == nil {
		return chatContinuationResult{usage: cloneUsage(baseUsage)}
	}
	return runThinkingContinuation(c, info, baseUsage, reasoning, info.ChatContinuation.Abnormal, info.StreamFinishReason, meta)
}

func runMissingChatContinuation(c *gin.Context, info *relaycommon.RelayInfo, baseUsage *dto.Usage, reasoning string, meta chatContinuationMeta) chatContinuationResult {
	if info == nil || info.ChatContinuation == nil {
		return chatContinuationResult{usage: cloneUsage(baseUsage)}
	}
	return runThinkingContinuation(c, info, baseUsage, reasoning, info.ChatContinuation.Missing, "", meta)
}

// thinkingRound is one reasoning-resume round's outcome.
type thinkingRound struct {
	usage     *dto.Usage
	finish    string
	content   string
	deltaSeen bool
	toolCalls int
	// toolCallsDelivered reports that this round's buffered tool calls were
	// complete and have already been delivered, making the round a valid answer.
	toolCallsDelivered bool
	terminal           bool
	err                error
}

// runThinkingContinuationRound runs one reasoning-resume round: bounded
// same-channel attempts while nothing has reached the caller, then a single
// cross-channel attempt when the round forwarded only reasoning.
//
// The cross-channel phase exists because a cut can land on a channel that is
// dying: 2026-10-29 ch102 cut a 10-minute thinking stream, the same-channel
// resume forwarded another 16KB of reasoning and died 19s later, and the caller
// got a 502 for an answer another channel could still have produced. Reasoning
// is a continuation stream, so resuming it elsewhere cannot duplicate visible
// answer text - the one thing a resume must never do. A round that already
// forwarded visible content or part of a tool call keeps the old contract: no
// retry, no hop, the caller must retry.
// sendAndReadThinkingRound spends one sender on a reasoning resume and folds the
// SSE body into a round outcome. label prefixes the log lines so a same-channel
// attempt and a cross-channel hop are distinguishable in production.
func sendAndReadThinkingRound(c *gin.Context, info *relaycommon.RelayInfo, sender func(string, int) (*http.Response, error), contextText string, remaining int, meta chatContinuationMeta, label string, attempts int, seam *seamDeduper) (thinkingRound, bool, error) {
	round := thinkingRound{}
	if sender == nil {
		return round, false, relaycommon.ErrNoContinuationCandidate
	}
	if attempts < 1 {
		attempts = 1
	}
	retryStarted := time.Now()
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if c.Request != nil && c.Request.Context().Err() != nil {
				break
			}
			if time.Since(retryStarted) > chatContinuationRetryBudget {
				logger.LogWarn(c, label+" continuation retry budget exhausted")
				break
			}
			time.Sleep(time.Duration(attempt) * chatContinuationRetryBackoff)
			helper.ExtendWriteDeadline(c)
		}
		resp, err := sendThinkingContinuationWithKeepAlive(c, info, sender, contextText, remaining)
		if err != nil {
			// A send/transport failure is not the transient upstream rejection the
			// same-channel retry exists for; the driver rotates instead.
			logger.LogWarn(c, label+" continuation request failed: "+err.Error())
			round.err = err
			return round, false, err
		}
		if resp == nil {
			return round, false, nil
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			logger.LogWarn(c, fmt.Sprintf("%s continuation upstream status %d (attempt %d/%d)", label, resp.StatusCode, attempt+1, attempts))
			continue
		}
		return readThinkingRound(c, info, resp, meta, seam), true, nil
	}
	return round, false, round.err
}

// readThinkingRound consumes one resume round's SSE body into its outcome.
func readThinkingRound(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response, meta chatContinuationMeta, seam *seamDeduper) thinkingRound {
	round := thinkingRound{}
	defer resp.Body.Close()
	usage, finish, content, deltaSeen, toolCalls, toolCallsDelivered, terminal, err := consumeChatContinuationRound(c, info, resp, meta, seam)
	round.usage = usage
	round.finish = finish
	round.content = content
	round.deltaSeen = deltaSeen
	round.toolCalls = toolCalls
	round.toolCallsDelivered = toolCallsDelivered
	round.terminal = terminal
	round.err = err
	return round
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
	// terminal reports whether the LAST round ended with a provider terminal
	// frame. A round that forwards deltas then hits a clean EOF must not be
	// reported as recovered: the caller would be billed for a twice-cut answer.
	terminal bool
	// toolCalls counts tool-call deltas forwarded by resumed rounds. A round that
	// emits a tool call and dies has already sent half a call, so the answer must
	// not be closed as a clean truncation.
	toolCalls int
	// toolCallsDelivered reports that a round's buffered tool calls were complete
	// and were delivered to the caller: the repair produced a usable answer even
	// though the round itself lost its terminal frame.
	toolCallsDelivered bool
	// channels lists the channel that served each round, in order: round 1 is the
	// original channel, later entries are the rotation. It lands in the cut
	// diagnostics so an unrepaired request shows where the repair actually ran.
	channels []int
	// failedReason names why the rotation ended without a usable answer, empty on
	// success. Callers map it into the cut diagnostics.
	failedReason string
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

// runChatContinuation stitches continuation rounds into the current client
// stream: round 1 runs on the channel that produced the prefix (its prompt cache
// applies), every later round rotates to a channel this request has not tried
// yet, and each round is judged by the same gates. The round budget, the elapsed
// cap and the shared tried set bound the repair, so a dying channel cannot burn
// it by being re-selected.
// continuationContentSender is one rung of a resume round's retry ladder.
type continuationContentSender struct {
	fn    func(partialContent string, remainingTokens int) (*http.Response, error)
	label string
}

// sendContentRoundLadder walks a round's senders in priority order: the original
// (native prefill) body on the original channel, the instruction body on the same
// channel, then the cross-channel rungs. The first rung that actually produces
// output ends the round; a rung that returns nothing (refused, error, empty body)
// is skipped so the next priority gets its chance. This is the ordering the
// operator asked for: original before instruction, original channel before a
// channel switch, and no priority skipped.
func sendContentRoundLadder(c *gin.Context, info *relaycommon.RelayInfo, senders []continuationContentSender, meta chatContinuationMeta, content string, remaining int, attempts int) (contentRound, bool, error) {
	round := contentRound{}
	var lastErr error
	for position, sender := range senders {
		if sender.fn == nil {
			continue
		}
		// Only the first rung inherits the bounded transient-retry budget: the
		// alternate rungs are different requests, not retries of the same one.
		rungAttempts := 1
		if position == 0 {
			rungAttempts = attempts
		}
		out, ok, err := sendContentRound(c, info, sender.fn, meta, content, remaining, sender.label, rungAttempts)
		// A rung is productive when it said something: a delta, a terminal reason, a
		// buffered tool call, or visible text. A bare [DONE] with no finish_reason is
		// exactly what "this body shape was refused" looks like, so it must not stop
		// the ladder - that is what makes the instruction fallback run.
		if ok && (out.deltaSeen || out.finish != "" || out.content != "" || out.toolCalls > 0) {
			return out, true, err
		}
		if err != nil {
			lastErr = err
			round.err = err
		}
		logger.LogWarn(c, sender.label+" continuation rung produced nothing, trying next retry priority")
		if c.Request != nil && c.Request.Context().Err() != nil {
			break
		}
	}
	return round, false, lastErr
}

func runChatContinuation(c *gin.Context, info *relaycommon.RelayInfo, baseUsage *dto.Usage, partial string, meta chatContinuationMeta) chatContinuationResult {
	total := cloneUsage(baseUsage)
	result := chatContinuationResult{usage: total, finish: "length"}
	if info == nil || info.ChatContinuation == nil {
		return result
	}
	cfg := info.ChatContinuation
	if cfg.Tried != nil {
		cfg.Tried.Add(info.ChannelId)
	}
	rounds := continuationRoundLimit(cfg.MaxRounds, cfg.MaxSteps)
	rotate := continuationRotationEnabled() && cfg.Fallback != nil
	hopFirst := continuationShouldHopFirst(info, cfg.Fallback != nil)
	recovered := false
	content := partial
	for round := 0; round < rounds; round++ {
		if continuationElapsedCapReached(info) {
			result.failedReason = relaycommon.ContinuationFailureElapsedCap
			break
		}
		remaining := chatContinuationBudgetLeftWithContent(info, total, len(content)-len(partial))
		if remaining <= 0 {
			result.failedReason = relaycommon.ContinuationFailureBudgetExhausted
			break
		}
		sameRungs := make([]continuationContentSender, 0, 2)
		if cfg.Send != nil {
			sameRungs = append(sameRungs, continuationContentSender{fn: cfg.Send, label: "same-channel"})
		}
		if cfg.SendInstruction != nil {
			sameRungs = append(sameRungs, continuationContentSender{fn: cfg.SendInstruction, label: "same-channel-instruction"})
		}
		crossRungs := make([]continuationContentSender, 0, 2)
		if rotate {
			if cfg.Fallback != nil {
				crossRungs = append(crossRungs, continuationContentSender{fn: cfg.Fallback, label: "cross-channel"})
			}
			if cfg.FallbackInstruction != nil {
				crossRungs = append(crossRungs, continuationContentSender{fn: cfg.FallbackInstruction, label: "cross-channel-instruction"})
			}
		}
		// Priority order: original channel (native body, then instruction body), then
		// the channel switch (native, then instruction). A wall-triggered repair
		// (>=600s) is the one documented exception: the channel that already burned a
		// whole wall cannot finish inside it, so it starts on another channel and
		// falls back to the original one only if the hop produces nothing.
		var senders []continuationContentSender
		switch {
		case round == 0 && hopFirst:
			// Wall-triggered repair: the channel that already burned a whole wall
			// cannot finish inside it, so the cross-channel rungs run first.
			senders = append(senders, crossRungs...)
		case round == 0:
			// Priority ladder: original channel first (native body, then instruction
			// body), and only then the channel switch with the same two body forms.
			senders = append(senders, sameRungs...)
			senders = append(senders, crossRungs...)
		case rotate:
			// Rotation rounds are already the channel switch; a later round must not
			// fall back to the original channel (it is in the tried set precisely
			// because it cut the answer).
			senders = append(senders, crossRungs...)
		default:
			// Rotation disabled: later rounds keep the legacy same-channel contract.
			senders = append(senders, sameRungs...)
		}
		if len(senders) == 0 {
			result.failedReason = relaycommon.ContinuationFailureNoCandidate
			break
		}
		// The channel that owns the prefix keeps its bounded same-channel retries
		// (it is the one with the prompt cache); rotating rounds are single attempts
		// because the next round gets a different channel instead of a retry.
		attempts := 1
		if !rotate || (round == 0 && !hopFirst) {
			attempts = chatContinuationRoundAttempts
		}
		roundOut, ok, err := sendContentRoundLadder(c, info, senders, meta, content, remaining, attempts)
		if errors.Is(err, relaycommon.ErrNoContinuationCandidate) {
			result.failedReason = relaycommon.ContinuationFailureNoCandidate
			break
		}
		result.rounds++
		channel := continuationRoundChannelID(info, cfg.RoundChannel)
		result.channels = append(result.channels, channel)
		logger.LogWarn(c, fmt.Sprintf("chat continuation round: n=%d ch=%d outcome=%s remaining=%d", result.rounds, channel, continuationContentOutcomeName(ok, roundOut, err), remaining))
		if !ok {
			if errors.Is(err, relaycommon.ErrNoContinuationCandidate) {
				result.failedReason = relaycommon.ContinuationFailureNoCandidate
				break
			}
			result.failedReason = relaycommon.ContinuationFailureRoundIncomplete
			if !rotate {
				// No other channel is available: retrying the channel that just refused
				// the resume is what the old attempt loop did, and it never helped.
				break
			}
			continue
		}
		result.terminal = roundOut.terminal
		// Same billing rule as the reasoning modes: a round that delivered nothing
		// to the caller is not merged into the settled usage.
		if roundOut.usage != nil && (roundOut.content != "" || roundOut.toolCalls > 0) {
			total = mergeContinuationUsage(total, roundOut.usage)
		}
		if roundOut.finish != "" {
			result.finish = roundOut.finish
		}
		if roundOut.toolCalls > 0 {
			result.toolCalls += roundOut.toolCalls
			if roundOut.toolCallsDelivered {
				// The resume round switched to a tool call and lost its terminal frame,
				// but every argument document was complete: the consolidated call set
				// already reached the caller, so this round is a usable answer.
				result.toolCallsDelivered = true
				result.finish = constant.FinishReasonToolCalls
				result.failedReason = ""
				recovered = true
				break
			}
			result.failedReason = relaycommon.ContinuationFailureToolCalls
			break
		}
		if roundOut.content != "" {
			content += roundOut.content
		}
		if roundOut.finish != "" && roundOut.finish != "length" && !relaycommon.IsAbnormalFinishReason(roundOut.finish) {
			result.failedReason = ""
			recovered = true
			break
		}
		// A cut after visible text may still rotate: the next round replays the whole
		// delivered prefix as its assistant message, so the seam deduper keeps the
		// caller from reading the same sentence twice. Without rotation there is
		// nothing left to do on that channel, so the cut is final.
		if roundOut.content != "" && !rotate && roundOut.finish != "length" {
			result.failedReason = relaycommon.ContinuationFailureRoundIncomplete
			break
		}
		if roundOut.err != nil || (roundOut.content == "" && roundOut.finish != "length" && !roundOut.deltaSeen) {
			result.failedReason = relaycommon.ContinuationFailureRoundIncomplete
			if !rotate {
				break
			}
			continue
		}
		if chatContinuationBudgetLeftWithContent(info, total, len(content)-len(partial)) <= 0 {
			result.failedReason = relaycommon.ContinuationFailureBudgetExhausted
			break
		}
	}
	result.usage = total
	// A recovered answer served by a different channel proves the original channel
	// cannot finish this conversation's answers right now. Drop the conversation's
	// sticky binding so the next request does not walk back into the channel that
	// cut this one; a same-channel recovery keeps the binding (and its warm cache).
	if recovered && cfg.RoundChannel != nil {
		if served := cfg.RoundChannel.Last(); served != 0 && served != info.ChannelId {
			service.DropStickyRoutingAfterUpstreamFailure(c)
		}
	}
	if produced := len(content) - len(partial); produced > 0 {
		result.contentChars = produced
	}
	if result.failedReason == "" && !recovered {
		result.failedReason = relaycommon.ContinuationFailureRoundsExhausted
	}
	common.RecordContinuationOutcome(continuationOutcomeChannel(info, cfg.RoundChannel), result.rounds > 0 && result.contentChars > 0)
	return result
}

// contentRound is one output-cap resume round's outcome.
type contentRound struct {
	usage     *dto.Usage
	finish    string
	content   string
	deltaSeen bool
	toolCalls int
	// toolCallsDelivered reports that this round's buffered tool calls were
	// complete and have already been delivered to the caller, so the round is a
	// recoverable answer rather than a cut.
	toolCallsDelivered bool
	terminal           bool
	err                error
}

// runContentContinuationRound runs one output-cap resume round: bounded
// same-channel attempts while nothing new has reached the caller, then a single
// cross-channel attempt when the round failed before forwarding any visible text.
//
// The hop mirrors runThinkingContinuationRound: a resume that only streamed
// reasoning and died left the caller without new answer text, so another channel
// may still finish the answer instead of a 502. Once visible text or part of a
// tool call was forwarded the round is final - a second channel would duplicate
// it.
// sendAndReadContentRound spends one sender on a visible-answer resume and folds
// the SSE body into a round outcome, using a fresh seam deduper against the
// content delivered so far.
func sendAndReadContentRound(c *gin.Context, info *relaycommon.RelayInfo, sender func(string, int) (*http.Response, error), meta chatContinuationMeta, content string, remaining int, label string, attempts int) (contentRound, bool, error) {
	round := contentRound{}
	if sender == nil {
		return round, false, relaycommon.ErrNoContinuationCandidate
	}
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if c.Request != nil && c.Request.Context().Err() != nil {
				break
			}
			time.Sleep(time.Duration(attempt) * chatContinuationRetryBackoff)
			helper.ExtendWriteDeadline(c)
		}
		resp, err := sender(content, remaining)
		if err != nil {
			// A send/transport failure is not the transient upstream rejection the
			// same-channel retry exists for; the driver rotates instead.
			logger.LogWarn(c, label+" continuation request failed: "+err.Error())
			round.err = err
			return round, false, err
		}
		if resp == nil {
			return round, false, nil
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			logger.LogWarn(c, fmt.Sprintf("%s continuation upstream status %d (attempt %d/%d)", label, resp.StatusCode, attempt+1, attempts))
			continue
		}
		return readContentRound(c, info, resp, meta, newSeamDeduper(content)), true, nil
	}
	return round, false, round.err
}

// sendContentRound spends one sender on one visible-answer resume round.
func sendContentRound(c *gin.Context, info *relaycommon.RelayInfo, sender func(string, int) (*http.Response, error), meta chatContinuationMeta, content string, remaining int, label string, attempts int) (contentRound, bool, error) {
	return sendAndReadContentRound(c, info, sender, meta, content, remaining, label, attempts)
}

// readContentRound consumes one resume round and folds its result.
func readContentRound(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response, meta chatContinuationMeta, seam *seamDeduper) contentRound {
	round := contentRound{}
	defer resp.Body.Close()
	usage, finish, emitted, deltaSeen, toolCalls, toolCallsDelivered, terminal, err := consumeChatContinuationRound(c, info, resp, meta, seam)
	round.usage = usage
	round.finish = finish
	round.content = emitted
	round.deltaSeen = deltaSeen
	round.toolCalls = toolCalls
	round.toolCallsDelivered = toolCallsDelivered
	round.terminal = terminal
	round.err = err
	return round
}

// consumeChatContinuationRound forwards one continuation round's deltas and
// returns that round's usage, terminal finish reason and visible content. The
// terminal frame and usage events are not forwarded: the caller renders one
// final frame with the merged usage.
func consumeChatContinuationRound(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response, meta chatContinuationMeta, seam *seamDeduper) (usage *dto.Usage, finish string, content string, deltaSeen bool, toolCalls int, toolCallsDelivered bool, sawTerminal bool, err error) {
	if resp == nil || resp.Body == nil {
		return nil, "", "", false, 0, false, false, fmt.Errorf("continuation response has no body")
	}
	// A resumed round can change its mind and emit a tool call. Its deltas are held
	// here until the round ends, so a cut can never hand the caller half an
	// argument document; a complete set is delivered as one consolidated event and
	// turns the round into a usable answer (2026-10-30: 66 of 68 ch102 visible-cut
	// failures were the resume round itself emitting a cut tool call).
	calls := relaycommon.NewChatToolCallBuffer(true)
	toolCallsSalvaged := false
	scanner := helper.NewStreamScanner(resp.Body)
	scanner.Split(bufio.ScanLines)
	// delivered is the visible text this round actually handed the client: the
	// seam deduper may drop a repeated prefix, and the caller uses this text as
	// the assistant prefix of the next round and as the billed answer.
	var delivered strings.Builder
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
			sawTerminal = true
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
			usage = mergeUsageForChannel(info, usage, chunk.Usage)
		}
		forward := false
		// seamText holds the content to forward per choice position after seam
		// de-duplication; a choice whose delta had content but no entry here is
		// withheld (a held prefix, or a fully stripped duplicate).
		seamText := make(map[int]string, len(chunk.Choices))
		for _, choice := range chunk.Choices {
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finish = *choice.FinishReason
			}
			delta := choice.Delta
			if delta.Content != nil && *delta.Content != "" {
				text := *delta.Content
				if seam != nil {
					emit, ready := seam.feed(text)
					if ready && emit != "" {
						seamText[choice.Index] = emit
						delivered.WriteString(emit)
						forward = true
					}
				} else {
					delivered.WriteString(text)
					forward = true
				}
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
				toolCalls++
				calls.Observe([]dto.ChatCompletionsStreamResponseChoice{choice})
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
		if seam != nil {
			// The chunk may carry reasoning or tool calls next to the text, so the
			// decision has to be applied to the forwarded copy rather than by
			// dropping the whole chunk.
			for i, choice := range chunk.Choices {
				if choice.Delta.Content == nil || *choice.Delta.Content == "" {
					continue
				}
				if text, ok := seamText[i]; ok {
					forwardData, _ = sjson.Set(forwardData, fmt.Sprintf("choices.%d.delta.content", i), text)
				} else {
					forwardData, _ = sjson.Delete(forwardData, fmt.Sprintf("choices.%d.delta.content", i))
				}
			}
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
			if len(chunk.Choices[i].Delta.ToolCalls) > 0 {
				// The held call set is delivered later, as one complete event.
				forwardData, _ = sjson.Delete(forwardData, fmt.Sprintf("choices.%d.delta.tool_calls", i))
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
			return usage, finish, delivered.String(), deltaSeen, toolCalls, false, sawTerminal, sendErr
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return usage, finish, delivered.String(), deltaSeen, toolCalls, false, sawTerminal, scanErr
	}
	if seam != nil {
		// The round can end before the lookahead filled up; the held text still
		// has to reach the client, at most seamMaxLookahead characters once.
		if tail, ready := seam.finish(); ready && tail != "" {
			if sendErr := sendContinuationTextChunk(c, info, meta, tail); sendErr != nil {
				return usage, finish, delivered.String(), deltaSeen, toolCalls, false, sawTerminal, sendErr
			}
			delivered.WriteString(tail)
			deltaSeen = true
		}
	}
	if toolCalls > 0 {
		if err := calls.Validate(); err == nil {
			if consolidated := calls.ConsolidatedChunk(meta.ID, meta.Created, meta.Model); consolidated != "" {
				info.ToolCallsDelivered = true
				helper.ExtendWriteDeadline(c)
				if sendErr := sendStreamData(c, info, consolidated, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); sendErr == nil {
					toolCallsSalvaged = true
					finish = constant.FinishReasonToolCalls
				}
			}
		} else {
			common.SysLog(fmt.Sprintf("continuation round tool calls incomplete, dropped: ch=%d calls=%d err=%v", info.ChannelId, calls.CallCount(), err))
		}
	}
	return usage, finish, delivered.String(), deltaSeen, toolCalls, toolCallsSalvaged, sawTerminal, nil
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

// sendContinuationTextChunk forwards one synthesized content delta. It is used
// only for the seam deduper's held prefix when a resumed round ends before the
// lookahead filled up, so the text reaches the client in the same shape as any
// other continuation delta (first-round id/created/model, K3 fit applied when the
// caller is a fit-shaped one) instead of being dropped.
func sendContinuationTextChunk(c *gin.Context, info *relaycommon.RelayInfo, meta chatContinuationMeta, text string) error {
	if text == "" {
		return nil
	}
	payload, err := common.Marshal(map[string]any{
		"id":      meta.ID,
		"object":  "chat.completion.chunk",
		"created": meta.Created,
		"model":   meta.Model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{"content": text},
			"finish_reason": nil,
		}},
	})
	if err != nil {
		return err
	}
	data := rewriteContinuationChunkMeta(string(payload), meta)
	if kimiK3FitEnabled(info) {
		data = FitKimiK3StreamEventForAdapters(c, info, data, nil, false)
		if data == "" {
			return nil
		}
	}
	helper.ExtendWriteDeadline(c)
	return sendStreamData(c, info, data, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent)
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

func sendThinkingContinuationWithKeepAlive(c *gin.Context, info *relaycommon.RelayInfo, send func(string, int) (*http.Response, error), content string, remaining int) (*http.Response, error) {
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
		resp, err := send(content, remaining)
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
