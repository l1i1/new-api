package openai

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

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
	if !hasCalls && finish == "" {
		return data, false
	}
	if finish == "" {
		// Held: the caller must not see a fragment of the argument document.
		return "", true
	}
	// Terminal event: deliver the complete call set first, or hold the terminal
	// too when the set cannot be reconstructed (the handler then fails honestly
	// instead of closing a call the caller cannot execute).
	if info.ToolCallsDelivered {
		return data, false
	}
	if err := info.ToolCallBuffer.Validate(); err != nil {
		info.ToolCallBuffer.MarkInvalid()
		return "", true
	}
	id := chunk.Id
	if id == "" {
		id = "chatcmpl-relay"
	}
	model := chunk.Model
	created := chunk.Created
	if consolidated := info.ToolCallBuffer.ConsolidatedChunk(id, created, model); consolidated != "" {
		info.ToolCallsDelivered = true
		if err := writeStreamData(c, info, consolidated, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
			logger.LogError(c, "error writing consolidated tool calls: "+err.Error())
			return "", true
		}
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
// once on another channel and delivering its complete call set. It refuses to run
// when visible answer text already reached the caller (a regeneration would
// duplicate it), when the switch is off for this channel, or when no regenerate
// hook is wired.
func regenerateToolCalls(c *gin.Context, info *relaycommon.RelayInfo, baseUsage *dto.Usage, visibleContentChars int, meta chatContinuationMeta) (*dto.Usage, bool) {
	cfg := info.ChatContinuation
	if cfg == nil || cfg.Regenerate == nil || !rootcommon.ChatToolRepairChannelAllowed(info.ChannelId) {
		return nil, false
	}
	if visibleContentChars > 0 || info.ToolCallsDelivered {
		return nil, false
	}
	if info.SendResponseCount > 1 {
		// The caller already received frames of this answer (role/reasoning). A
		// regenerated tool-call answer can still be appended, but only when the
		// calls are the whole answer, which the model decides on the retry.
		return nil, false
	}
	remaining := chatContinuationBudgetLeft(info, baseUsage)
	if remaining <= 0 {
		return nil, false
	}
	resp, err := cfg.Regenerate(remaining)
	if err != nil || resp == nil || resp.Body == nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
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
