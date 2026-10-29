package openai

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tidwall/sjson"

	rootcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
)

// gateToolCallChunk intercepts one outgoing SSE event when the channel buffers
// tool calls. Text and reasoning events pass through untouched; a tool-call delta
// is accumulated and held, and the terminal event is written only after the
// consolidated call set has been delivered (or held back when it is incomplete).
// The returned payload replaces the caller's data; an empty payload means "hold".
func gateToolCallChunk(c *gin.Context, info *relaycommon.RelayInfo, data string) (string, bool) {
	if info == nil || info.ToolCallBuffer == nil || !info.ToolCallBuffer.Enabled() {
		return data, false
	}
	if info.ToolCallsDelivered {
		// The consolidated call event is written through this same writer; it must
		// pass through without being observed and held again.
		return data, false
	}
	var chunk dto.ChatCompletionsStreamResponse
	if err := rootcommon.Unmarshal(rootcommon.StringToByteSlice(data), &chunk); err != nil {
		return data, false
	}
	hasCalls := false
	finish := ""
	for _, choice := range chunk.Choices {
		if len(choice.Delta.ToolCalls) > 0 {
			hasCalls = true
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			finish = *choice.FinishReason
		}
	}
	// A chunk may carry text or reasoning next to a call fragment. Strip only the
	// fragment: holding the whole event would silently drop the caller's text.
	stripped := data
	for index, choice := range chunk.Choices {
		if len(choice.Delta.ToolCalls) > 0 {
			stripped, _ = sjson.Delete(stripped, fmt.Sprintf("choices.%d.delta.tool_calls", index))
		}
	}
	forward := func(payload string) (string, bool) {
		if payload == "" {
			return "", true
		}
		return payload, true
	}
	if finish == "" {
		if !hasCalls {
			return data, false
		}
		// Call fragment without a terminal: hold it (the caller must never see half
		// an argument document); any text the same chunk carried still goes out.
		if stripped != data {
			return stripped, true
		}
		return "", true
	}
	// Terminal event: deliver the complete call set first, whatever chunk carries it,
	// or hold the terminal when the set cannot be reconstructed (the handler then
	// fails honestly instead of closing a call the caller cannot execute).
	if !info.ToolCallBuffer.Empty() {
		if err := info.ToolCallBuffer.Validate(); err != nil {
			info.ToolCallBuffer.MarkInvalid()
			if hasCalls {
				return forward(stripped)
			}
			return "", true
		}
		id := chunk.Id
		if id == "" {
			id = "chatcmpl-relay"
		}
		if consolidated := info.ToolCallBuffer.ConsolidatedChunk(id, chunk.Created, chunk.Model); consolidated != "" {
			info.ToolCallsDelivered = true
			if err := writeStreamData(c, info, consolidated, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
				logger.LogError(c, "error writing consolidated tool calls: "+err.Error())
				return "", true
			}
		}
	}
	if hasCalls {
		return forward(stripped)
	}
	return data, false
}

// flushBufferedToolCalls delivers a held tool-call set before a terminal frame
// that is written through a path the gate does not see (the synthesized
// tool_calls close and the regeneration path).
func flushBufferedToolCalls(c *gin.Context, info *relaycommon.RelayInfo, id string, created int64, model string) bool {
	if info == nil || info.ToolCallBuffer == nil || !info.ToolCallBuffer.Enabled() || info.ToolCallsDelivered {
		return true
	}
	if info.ToolCallBuffer.Empty() {
		return true
	}
	if err := info.ToolCallBuffer.Validate(); err != nil {
		info.ToolCallBuffer.MarkInvalid()
		return false
	}
	if id == "" {
		id = "chatcmpl-relay"
	}
	consolidated := info.ToolCallBuffer.ConsolidatedChunk(id, created, model)
	if consolidated == "" {
		info.ToolCallBuffer.MarkInvalid()
		return false
	}
	info.ToolCallsDelivered = true
	return writeStreamData(c, info, consolidated, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent) == nil
}

// mergeRegeneratedUsage merges the regenerated round's usage into the settlement:
// the prompt is counted once (the regeneration re-sends the same conversation, so
// its prompt replaces rather than adds), while the completion of the round is
// added to whatever the first round already produced.
func mergeRegeneratedUsage(base, regenerated *dto.Usage) *dto.Usage {
	if base == nil {
		return regenerated
	}
	if regenerated == nil {
		return base
	}
	merged := dto.MergeUsage(base, regenerated)
	return merged
}

// regenerateToolCalls recovers a cut tool call by re-running the original request
// once on another channel and delivering its complete call set.
//
// Visible answer text does NOT block it any more. A cut tool-call stream normally
// delivers a short preamble before the call starts, and refusing to regenerate
// because of those few characters turned a recoverable call into a 502
// (2026-10-30 03:15 ch91: 75 visible chars + an incomplete call, 900s wall, no
// repair attempted). The regenerated answer is only accepted when its content is
// empty, so the caller sees "preamble, consolidated call, tool_calls terminal" -
// the same shape a healthy stream produces. Nothing is stitched into the call
// arguments themselves.
//
// The switch must be on for the channel, a regenerate hook must be wired, and the
// answer must not already be closed: a forwarded terminal frame is final.
func regenerateToolCalls(c *gin.Context, info *relaycommon.RelayInfo, baseUsage *dto.Usage, visibleContentChars int, terminalForwarded bool, meta chatContinuationMeta) (*dto.Usage, bool) {
	cfg := info.ChatContinuation
	if cfg == nil || (cfg.RegenerateSame == nil && cfg.Regenerate == nil) || !rootcommon.ChatToolRepairChannelAllowed(info.ChannelId) {
		return nil, false
	}
	if info.ToolCallsDelivered || terminalForwarded {
		return nil, false
	}
	if deepSeekV4FitEnabled(info) || kimiK3FitEnabled(info) {
		// Fit-shape callers demand the official chunking; a synthesized consolidated
		// call would break that contract, so those requests keep the honest error.
		return nil, false
	}
	remaining := chatContinuationBudgetLeft(info, baseUsage)
	if remaining <= 0 {
		return nil, false
	}
	// Retry ladder: the original channel first (prompt cache warm, no selection
	// cost), then the cross-channel hop. A rung that returns an unusable call set
	// hands over to the next one instead of failing the request outright.
	//
	// The wall exception mirrors the continuation ladder: a request that already
	// spent a whole upstream wall (>=10 minutes) is not retried on the channel that
	// just hit it, because that channel will hit the same wall again - the retry
	// would burn the caller's remaining patience and then fail anyway
	// (2026-10-30 03:15 ch91 900s cut shape).
	rungs := make([]struct {
		fn    func(int) (*http.Response, error)
		label string
	}, 0, 2)
	if cfg.RegenerateSame != nil && (info.StartTime.IsZero() || time.Since(info.StartTime) < continuationHopFirstAfter) {
		rungs = append(rungs, struct {
			fn    func(int) (*http.Response, error)
			label string
		}{cfg.RegenerateSame, "same-channel"})
	}
	rungs = append(rungs, struct {
		fn    func(int) (*http.Response, error)
		label string
	}{cfg.Regenerate, "cross-channel"})
	for _, rung := range rungs {
		if rung.fn == nil {
			continue
		}
		if finalUsage, ok := regenerateToolCallsFromRung(c, info, baseUsage, remaining, rung.fn, rung.label, meta); ok {
			// The original channel cut this answer mid-call; the next request in the
			// conversation must not be pinned back to it just because the call was
			// recovered elsewhere. The next success re-records the binding.
			service.DropStickyRoutingAfterUpstreamFailure(c)
			return finalUsage, true
		}
		if c.Request != nil && c.Request.Context().Err() != nil {
			break
		}
	}
	return nil, false
}

// regenerateToolCallsFromRung runs one regeneration rung and, when its call set is
// complete, delivers it as the answer. A failed rung returns false so the caller
// can try the next retry priority.
func regenerateToolCallsFromRung(c *gin.Context, info *relaycommon.RelayInfo, baseUsage *dto.Usage, remaining int, regenerate func(int) (*http.Response, error), label string, meta chatContinuationMeta) (*dto.Usage, bool) {
	resp, err := regenerate(remaining)
	if err != nil || resp == nil || resp.Body == nil {
		rootcommon.SysLog(fmt.Sprintf("tool call regeneration rung failed: rung=%s ch=%d err=%v", label, info.ChannelId, err))
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		rootcommon.SysLog(fmt.Sprintf("tool call regeneration rung status %d: rung=%s ch=%d", resp.StatusCode, label, info.ChannelId))
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	var payload struct {
		ID      string `json:"id"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *dto.Usage `json:"usage"`
	}
	if err := rootcommon.Unmarshal(raw, &payload); err != nil || len(payload.Choices) == 0 {
		return nil, false
	}
	choice := payload.Choices[0]
	if strings.TrimSpace(choice.Message.Content) != "" || len(choice.Message.ToolCalls) == 0 {
		return nil, false
	}
	buffer := relaycommon.NewChatToolCallBuffer(true)
	deltas := make([]dto.ToolCallResponse, 0, len(choice.Message.ToolCalls))
	for position, call := range choice.Message.ToolCalls {
		index := position
		deltas = append(deltas, dto.ToolCallResponse{
			Index: &index,
			ID:    call.ID,
			Type:  call.Type,
			Function: dto.FunctionResponse{
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			},
		})
	}
	buffer.Observe([]dto.ChatCompletionsStreamResponseChoice{{Delta: dto.ChatCompletionsStreamResponseChoiceDelta{ToolCalls: deltas}}})
	if err := buffer.Validate(); err != nil {
		rootcommon.SysLog(fmt.Sprintf("tool call regeneration produced an unusable call set: ch=%d err=%v", info.ChannelId, err))
		return nil, false
	}
	id := payload.ID
	if id == "" {
		id = meta.ID
	}
	model := payload.Model
	if model == "" {
		model = meta.Model
	}
	created := payload.Created
	if created == 0 {
		created = meta.Created
	}
	consolidated := buffer.ConsolidatedChunk(id, created, model)
	if consolidated == "" {
		return nil, false
	}
	info.ToolCallsDelivered = true
	if err := writeStreamData(c, info, consolidated, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
		return nil, false
	}
	finalUsage := baseUsage
	if payload.Usage != nil {
		if !service.ValidUsage(payload.Usage) {
			return nil, false
		}
		finalUsage = mergeRegeneratedUsage(baseUsage, payload.Usage)
	}
	info.StreamFinishReason = constant.FinishReasonToolCalls
	if info.StreamStatus != nil {
		info.StreamStatus.MarkRecovered()
	}
	relaycommon.ClampUsageToClientBudget(info, finalUsage)
	helper.ExtendWriteDeadline(c)
	sendChatContinuationTerminal(c, info, meta.ID, meta.Created, meta.Model, constant.FinishReasonToolCalls, finalUsage)
	helper.ExtendWriteDeadline(c)
	HandleFinalResponse(c, info, "", meta.ID, meta.Created, meta.Model, meta.SystemFingerprint, finalUsage, false)
	rootcommon.SysLog(fmt.Sprintf("tool call regenerated on another channel: ch=%d calls=%d prompt=%d completion=%d", info.ChannelId, len(choice.Message.ToolCalls), finalUsage.PromptTokens, finalUsage.CompletionTokens))
	return finalUsage, true
}
