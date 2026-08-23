package ollama

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

type ollamaChatStreamChunk struct {
	Model     string          `json:"model"`
	CreatedAt string          `json:"created_at"`
	Error     json.RawMessage `json:"error"`
	// chat
	Message *struct {
		Role      string           `json:"role"`
		Content   string           `json:"content"`
		Thinking  json.RawMessage  `json:"thinking"`
		ToolCalls []OllamaToolCall `json:"tool_calls"`
	} `json:"message"`
	// generate
	Response            string          `json:"response"`
	Thinking            json.RawMessage `json:"thinking"`
	Done                bool            `json:"done"`
	DoneReason          string          `json:"done_reason"`
	TotalDuration       int64           `json:"total_duration"`
	LoadDuration        int64           `json:"load_duration"`
	PromptEvalCount     int             `json:"prompt_eval_count"`
	PromptTokensDetails *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CachedTokens       *int  `json:"cached_tokens"`
	EvalCount          int   `json:"eval_count"`
	PromptEvalDuration int64 `json:"prompt_eval_duration"`
	EvalDuration       int64 `json:"eval_duration"`
}

func ollamaCachedTokens(chunk ollamaChatStreamChunk) (int, bool) {
	if chunk.PromptTokensDetails != nil && chunk.PromptTokensDetails.CachedTokens != nil {
		value := *chunk.PromptTokensDetails.CachedTokens
		if value < 0 {
			value = 0
		}
		return value, true
	}
	if chunk.CachedTokens != nil {
		value := *chunk.CachedTokens
		if value < 0 {
			value = 0
		}
		return value, true
	}
	return 0, false
}

func normalizeOllamaTokenCount(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

// saturatingOllamaTokenAdd prevents malformed upstream counts from wrapping
// around into a negative usage value and an accidental negative charge.
func saturatingOllamaTokenAdd(values ...int) int {
	maxInt := int(^uint(0) >> 1)
	total := 0
	for _, value := range values {
		value = normalizeOllamaTokenCount(value)
		if total > maxInt-value {
			return maxInt
		}
		total += value
	}
	return total
}

func normalizeOllamaUsage(promptTokens, completionTokens int) dto.Usage {
	promptTokens = normalizeOllamaTokenCount(promptTokens)
	completionTokens = normalizeOllamaTokenCount(completionTokens)
	return dto.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      saturatingOllamaTokenAdd(promptTokens, completionTokens),
	}
}

func ollamaErrorMessage(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return ""
	}
	var message string
	if err := common.Unmarshal(raw, &message); err == nil {
		return strings.TrimSpace(message)
	}
	var object struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := common.Unmarshal(raw, &object); err == nil {
		if strings.TrimSpace(object.Error) != "" {
			return strings.TrimSpace(object.Error)
		}
		if strings.TrimSpace(object.Message) != "" {
			return strings.TrimSpace(object.Message)
		}
	}
	return value
}

func newOllamaResponseError(message string) *types.NewAPIError {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "invalid response from Ollama"
	}
	message = common.LocalLogPreview(common.MaskSensitiveInfo(message))
	return types.NewOpenAIError(fmt.Errorf("ollama upstream response error: %s", message), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
}

func logOllamaResponseDecodeError(c *gin.Context, prefix, detail string) {
	logger.LogError(c, prefix+common.LocalLogPreview(common.MaskSensitiveInfo(detail)))
}

func writeOllamaClaudeData(c *gin.Context, response dto.ClaudeResponse) error {
	if c == nil || c.Writer == nil {
		return fmt.Errorf("context or writer is nil")
	}
	if c.Request != nil && c.Request.Context().Err() != nil {
		return c.Request.Context().Err()
	}
	data, err := common.Marshal(helper.ClaudeResponseForClient(&response))
	if err != nil {
		return err
	}
	if err := (common.CustomEvent{Data: "event: " + response.Type + "\n"}).Render(c.Writer); err != nil {
		return err
	}
	if err := (common.CustomEvent{Data: "data: " + string(data)}).Render(c.Writer); err != nil {
		return err
	}
	return helper.FlushWriter(c)
}

func normalizeOllamaCachedTokens(value, promptTokens int) int {
	value = normalizeOllamaTokenCount(value)
	promptTokens = normalizeOllamaTokenCount(promptTokens)
	if value > promptTokens {
		return promptTokens
	}
	return value
}

func ollamaThinkingText(raw json.RawMessage) string {
	rawText := strings.TrimSpace(string(raw))
	if rawText == "" || rawText == "null" {
		return ""
	}
	var text string
	if err := common.Unmarshal(raw, &text); err == nil {
		return text
	}
	return rawText
}

func ollamaResponseCacheMessage(content, thinking string, toolCalls []OllamaToolCall) *OllamaChatMessage {
	if content == "" && thinking == "" && len(toolCalls) == 0 {
		return nil
	}
	message := &OllamaChatMessage{Role: "assistant", Content: content, ToolCalls: toolCalls}
	if thinking != "" {
		if raw, err := common.Marshal(thinking); err == nil {
			message.Thinking = raw
		}
	}
	return message
}

func ollamaToolCallsToOpenAI(toolCalls []OllamaToolCall, startIndex int, includeIndex bool) ([]dto.ToolCallResponse, int) {
	if len(toolCalls) == 0 {
		return nil, startIndex
	}
	result := make([]dto.ToolCallResponse, 0, len(toolCalls))
	for _, tc := range toolCalls {
		var argBytes []byte
		var err error
		if tc.Function.Arguments == nil {
			argBytes = []byte("{}")
		} else {
			argBytes, err = common.Marshal(tc.Function.Arguments)
			if err != nil || len(argBytes) == 0 {
				argBytes = []byte("{}")
			}
		}
		toolCallID := tc.ID
		if toolCallID == "" {
			toolCallID = fmt.Sprintf("call_%d", startIndex)
		}
		tr := dto.ToolCallResponse{
			ID:   toolCallID,
			Type: "function",
			Function: dto.FunctionResponse{
				Name:      tc.Function.Name,
				Arguments: string(argBytes),
			},
		}
		if includeIndex {
			tr.SetIndex(startIndex)
		}
		startIndex++
		result = append(result, tr)
	}
	return result, startIndex
}

func toUnix(ts string) int64 {
	if ts == "" {
		return time.Now().Unix()
	}
	// try time.RFC3339 or with nanoseconds
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t2, err2 := time.Parse(time.RFC3339, ts)
		if err2 == nil {
			return t2.Unix()
		}
		return time.Now().Unix()
	}
	return t.Unix()
}

type ollamaCompletionsStreamChoice struct {
	Text         string  `json:"text"`
	Index        int     `json:"index"`
	FinishReason *string `json:"finish_reason"`
}

type ollamaCompletionsStreamResponse struct {
	Id      string                          `json:"id"`
	Object  string                          `json:"object"`
	Created int64                           `json:"created"`
	Model   string                          `json:"model"`
	Choices []ollamaCompletionsStreamChoice `json:"choices"`
	Usage   *dto.Usage                      `json:"usage,omitempty"`
}

type ollamaCompletionsResponseChoice struct {
	Text         string `json:"text"`
	Index        int    `json:"index"`
	FinishReason string `json:"finish_reason"`
}

type ollamaCompletionsResponse struct {
	Id      string                            `json:"id"`
	Object  string                            `json:"object"`
	Created int64                             `json:"created"`
	Model   string                            `json:"model"`
	Choices []ollamaCompletionsResponseChoice `json:"choices"`
	Usage   dto.Usage                         `json:"usage"`
}

func buildOllamaStreamDelta(responseID string, created int64, model string, chunk ollamaChatStreamChunk, toolCallIndex *int) (*dto.ChatCompletionsStreamResponse, bool) {
	content := chunk.Response
	thinking := chunk.Thinking
	var toolCalls []OllamaToolCall
	if chunk.Message != nil {
		content = chunk.Message.Content
		if len(chunk.Message.Thinking) > 0 {
			thinking = chunk.Message.Thinking
		}
		toolCalls = chunk.Message.ToolCalls
	}
	delta := &dto.ChatCompletionsStreamResponse{
		Id:      responseID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			Index: 0,
			Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Role: "assistant"},
		}},
	}
	if content != "" {
		delta.Choices[0].Delta.SetContentString(content)
	}
	if thinkingContent := ollamaThinkingText(thinking); thinkingContent != "" {
		delta.Choices[0].Delta.SetReasoningContent(thinkingContent)
	}
	if len(toolCalls) > 0 {
		delta.Choices[0].Delta.ToolCalls, *toolCallIndex = ollamaToolCallsToOpenAI(toolCalls, *toolCallIndex, true)
	}
	hasPayload := content != "" || ollamaThinkingText(thinking) != "" || len(toolCalls) > 0
	return delta, hasPayload
}

func ollamaRelayFormat(info *relaycommon.RelayInfo) types.RelayFormat {
	if info == nil || info.RelayFormat == "" {
		return types.RelayFormatOpenAI
	}
	return info.RelayFormat
}

func ollamaIsCompletions(info *relaycommon.RelayInfo) bool {
	return info != nil && (info.RelayMode == relayconstant.RelayModeCompletions || strings.Contains(info.RequestURLPath, "/v1/completions"))
}

func ollamaStreamAPIError(err error) *types.NewAPIError {
	if err == nil {
		return nil
	}
	return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
}

func ollamaToolCallIdentity(tc OllamaToolCall, index int) string {
	if tc.ID != "" {
		return "id:" + tc.ID
	}
	data, err := common.Marshal(struct {
		Name      string `json:"name"`
		Arguments any    `json:"arguments"`
	}{Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	if err != nil {
		return fmt.Sprintf("index:%d:name:%s:type:%T", index, tc.Function.Name, tc.Function.Arguments)
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("index:%d:value:%x", index, sum)
}

func commitOllamaToolCalls(info *relaycommon.RelayInfo, calls []OllamaToolCall) {
	if info == nil {
		return
	}
	for _, call := range calls {
		info.CountBillableToolCall(dto.BuildInCallFunctionCall, call.Function.Name)
	}
}

func newOllamaToolCalls(calls []OllamaToolCall, seen map[string]struct{}) []OllamaToolCall {
	if len(calls) == 0 {
		return nil
	}
	result := make([]OllamaToolCall, 0, len(calls))
	for index, call := range calls {
		key := ollamaToolCallIdentity(call, index)
		if seen != nil {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		result = append(result, call)
	}
	return result
}

func ollamaStreamError(c *gin.Context, apiErr *types.NewAPIError) *types.NewAPIError {
	if apiErr == nil || c == nil || c.Writer == nil || !c.Writer.Written() {
		return apiErr
	}
	return types.NewError(apiErr, apiErr.GetErrorCode(), types.ErrOptionWithSkipRetry())
}

func writeOllamaStreamChunk(c *gin.Context, info *relaycommon.RelayInfo, response *dto.ChatCompletionsStreamResponse) *types.NewAPIError {
	if response == nil {
		return newOllamaResponseError("empty response chunk")
	}
	switch {
	case ollamaRelayFormat(info) == types.RelayFormatClaude:
		if info == nil {
			return newOllamaResponseError("missing relay info for Claude response")
		}
		info.SendResponseCount++
		result, err := relayconvert.ConvertStreamResponse(c, info, types.RelayFormatClaude, response)
		if err != nil {
			return types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		claudeResponses, ok := result.Value.([]*dto.ClaudeResponse)
		if !ok {
			return types.NewOpenAIError(fmt.Errorf("expected Claude stream responses, got %T", result.Value), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		for _, claudeResponse := range claudeResponses {
			if claudeResponse == nil {
				continue
			}
			if err := writeOllamaClaudeData(c, *claudeResponse); err != nil {
				return ollamaStreamAPIError(err)
			}
		}
		if c != nil && c.Request != nil && c.Request.Context().Err() != nil {
			return ollamaStreamAPIError(c.Request.Context().Err())
		}
		return nil
	case ollamaIsCompletions(info):
		clientUsage := helper.UsageForClient(response.Usage)
		completion := ollamaCompletionsStreamResponse{
			Id:      response.Id,
			Object:  "text_completion",
			Created: response.Created,
			Model:   response.Model,
			Choices: make([]ollamaCompletionsStreamChoice, 0, len(response.Choices)),
			Usage:   clientUsage,
		}
		for _, choice := range response.Choices {
			text := choice.Delta.GetContentString()
			completion.Choices = append(completion.Choices, ollamaCompletionsStreamChoice{
				Text:         text,
				Index:        choice.Index,
				FinishReason: choice.FinishReason,
			})
		}
		data, err := common.Marshal(completion)
		if err != nil {
			return types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
		}
		if err := helper.StringData(c, string(data)); err != nil {
			return ollamaStreamAPIError(err)
		}
		return nil
	default:
		data, err := common.Marshal(helper.ChatCompletionsStreamResponseForClient(response))
		if err != nil {
			return types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
		}
		if err := helper.StringData(c, string(data)); err != nil {
			return ollamaStreamAPIError(err)
		}
		return nil
	}
}

func decodeOllamaChunks(body []byte, c *gin.Context) ([]ollamaChatStreamChunk, *types.NewAPIError) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, newOllamaResponseError("empty response body")
	}

	// A non-stream response is one JSON object and may be pretty-printed. Try
	// the complete body first so its internal newlines are not treated as bad
	// NDJSON records.
	var single ollamaChatStreamChunk
	if err := common.Unmarshal([]byte(trimmed), &single); err == nil {
		return []ollamaChatStreamChunk{single}, nil
	}

	scanner := helper.NewStreamScanner(bytes.NewReader(body))
	chunks := make([]ollamaChatStreamChunk, 0)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var chunk ollamaChatStreamChunk
		if err := common.Unmarshal([]byte(line), &chunk); err != nil {
			logOllamaResponseDecodeError(c, "ollama non-stream json decode error: ", line)
			return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
		chunks = append(chunks, chunk)
	}
	if err := scanner.Err(); err != nil {
		logOllamaResponseDecodeError(c, "ollama non-stream scan error: ", err.Error())
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	if len(chunks) == 0 {
		return nil, newOllamaResponseError("empty response body")
	}
	return chunks, nil
}

func ollamaStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("empty response"), types.ErrorCodeBadResponse, http.StatusBadRequest)
	}
	defer service.CloseResponseBodyGracefully(resp)

	scanner := helper.NewStreamScanner(resp.Body)
	usage := &dto.Usage{}
	var model = info.UpstreamModelName
	var responseId = common.GetUUID()
	var created = time.Now().Unix()
	var toolCallIndex int
	var responseBuilder strings.Builder
	var thinkingBuilder strings.Builder
	var responseToolCalls []OllamaToolCall
	responseToolCallSet := make(map[string]struct{})
	sentStart := false
	sawDone := false
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var chunk ollamaChatStreamChunk
		if err := common.Unmarshal([]byte(line), &chunk); err != nil {
			logOllamaResponseDecodeError(c, "ollama stream json decode error: ", err.Error()+" line="+line)
			return usage, ollamaStreamError(c, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway))
		}
		if message := ollamaErrorMessage(chunk.Error); message != "" {
			logOllamaResponseDecodeError(c, "ollama stream upstream error: ", message)
			return usage, ollamaStreamError(c, newOllamaResponseError(message))
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		created = toUnix(chunk.CreatedAt)
		// Mark the first valid upstream frame, including a done-only frame.
		info.SetFirstResponseTime()
		if !sentStart {
			helper.SetEventStreamHeaders(c)
			start := helper.GenerateStartEmptyResponse(responseId, created, model, nil)
			if apiErr := writeOllamaStreamChunk(c, info, start); apiErr != nil {
				return usage, ollamaStreamError(c, apiErr)
			}
			sentStart = true
		}

		var newToolCalls []OllamaToolCall
		if chunk.Message != nil {
			responseBuilder.WriteString(chunk.Message.Content)
			if thinkingContent := ollamaThinkingText(chunk.Message.Thinking); thinkingContent != "" {
				thinkingBuilder.WriteString(thinkingContent)
			}
			newToolCalls = newOllamaToolCalls(chunk.Message.ToolCalls, responseToolCallSet)
			responseToolCalls = append(responseToolCalls, newToolCalls...)
		} else {
			responseBuilder.WriteString(chunk.Response)
			if thinkingContent := ollamaThinkingText(chunk.Thinking); thinkingContent != "" {
				thinkingBuilder.WriteString(thinkingContent)
			}
		}

		deltaChunk := chunk
		if chunk.Message != nil {
			message := *chunk.Message
			message.ToolCalls = newToolCalls
			deltaChunk.Message = &message
		}
		delta, hasPayload := buildOllamaStreamDelta(responseId, created, model, deltaChunk, &toolCallIndex)
		if hasPayload {
			if apiErr := writeOllamaStreamChunk(c, info, delta); apiErr != nil {
				return usage, ollamaStreamError(c, apiErr)
			}
		}
		if !chunk.Done {
			continue
		}
		// done frame
		usageValue := normalizeOllamaUsage(chunk.PromptEvalCount, chunk.EvalCount)
		usage = &usageValue
		cachedTokens, cachedTokensPresent := ollamaCachedTokens(chunk)
		if cachedTokensPresent {
			usage.PromptTokensDetails.CachedTokens = normalizeOllamaCachedTokens(cachedTokens, usage.PromptTokens)
		}
		applyOllamaPromptCacheEstimationWithUpstreamUsage(info, usage, cachedTokensPresent, c)
		finishReason := chunk.DoneReason
		if finishReason == "" {
			finishReason = "stop"
		}
		if toolCallIndex > 0 {
			finishReason = constant.FinishReasonToolCalls
		}
		// emit stop delta
		stop := helper.GenerateStopResponse(responseId, created, model, finishReason)
		if ollamaRelayFormat(info) == types.RelayFormatClaude {
			stop.Usage = usage
		}
		if apiErr := writeOllamaStreamChunk(c, info, stop); apiErr != nil {
			return usage, ollamaStreamError(c, apiErr)
		}
		// emit usage frame
		if ollamaRelayFormat(info) != types.RelayFormatClaude && (info == nil || info.ShouldIncludeUsage) {
			final := helper.GenerateFinalUsageResponse(responseId, created, model, *usage)
			if apiErr := writeOllamaStreamChunk(c, info, final); apiErr != nil {
				return usage, ollamaStreamError(c, apiErr)
			}
		}
		// send [DONE]
		if ollamaRelayFormat(info) != types.RelayFormatClaude {
			if err := helper.StringData(c, "[DONE]"); err != nil {
				return usage, ollamaStreamError(c, ollamaStreamAPIError(err))
			}
		}
		commitOllamaToolCalls(info, responseToolCalls)
		recordOllamaPromptCacheResponse(info, usage, ollamaResponseCacheMessage(responseBuilder.String(), thinkingBuilder.String(), responseToolCalls), c)
		sawDone = true
		break
	}
	if err := scanner.Err(); err != nil {
		logOllamaResponseDecodeError(c, "ollama stream scan error: ", err.Error())
		return usage, ollamaStreamError(c, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway))
	}
	if !sawDone {
		return usage, ollamaStreamError(c, newOllamaResponseError("Ollama stream ended before done=true"))
	}
	return usage, nil
}

// non-stream handler for chat/generate
func ollamaChatHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("empty response"), types.ErrorCodeBadResponse, http.StatusBadGateway)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	service.CloseResponseBodyGracefully(resp)
	if common.DebugEnabled {
		logger.LogDebug(c, "ollama non-stream raw resp: %s", common.LocalLogPreview(common.MaskSensitiveInfo(string(body))))
	}

	chunks, apiErr := decodeOllamaChunks(body, c)
	if apiErr != nil {
		return nil, apiErr
	}
	var (
		aggContent        strings.Builder
		reasoningBuilder  strings.Builder
		lastChunk         ollamaChatStreamChunk
		toolCallIndex     int
		toolCalls         []dto.ToolCallResponse
		responseToolCalls []OllamaToolCall
		toolCallSet       = make(map[string]struct{})
		sawDone           bool
	)
	for _, ck := range chunks {
		if message := ollamaErrorMessage(ck.Error); message != "" {
			logOllamaResponseDecodeError(c, "ollama non-stream upstream error: ", message)
			return nil, newOllamaResponseError(message)
		}
		if sawDone {
			continue
		}
		lastChunk = ck
		thinking := ck.Thinking
		if ck.Message != nil && len(ck.Message.Thinking) > 0 {
			thinking = ck.Message.Thinking
		}
		if thinkingContent := ollamaThinkingText(thinking); thinkingContent != "" {
			reasoningBuilder.WriteString(thinkingContent)
		}
		if ck.Message != nil && ck.Message.Content != "" {
			aggContent.WriteString(ck.Message.Content)
		} else if ck.Response != "" {
			aggContent.WriteString(ck.Response)
		}
		if ck.Message != nil && len(ck.Message.ToolCalls) > 0 {
			newToolCalls := newOllamaToolCalls(ck.Message.ToolCalls, toolCallSet)
			responseToolCalls = append(responseToolCalls, newToolCalls...)
			var converted []dto.ToolCallResponse
			converted, toolCallIndex = ollamaToolCallsToOpenAI(newToolCalls, toolCallIndex, false)
			toolCalls = append(toolCalls, converted...)
		}
		if ck.Done {
			sawDone = true
		}
	}
	if !sawDone {
		return nil, newOllamaResponseError("Ollama response ended before done=true")
	}

	model := lastChunk.Model
	if model == "" {
		model = info.UpstreamModelName
	}
	created := toUnix(lastChunk.CreatedAt)
	usageValue := normalizeOllamaUsage(lastChunk.PromptEvalCount, lastChunk.EvalCount)
	usage := &usageValue
	cachedTokens, cachedTokensPresent := ollamaCachedTokens(lastChunk)
	if cachedTokensPresent {
		usage.PromptTokensDetails.CachedTokens = normalizeOllamaCachedTokens(cachedTokens, usage.PromptTokens)
	}
	applyOllamaPromptCacheEstimationWithUpstreamUsage(info, usage, cachedTokensPresent, c)
	content := aggContent.String()
	finishReason := lastChunk.DoneReason
	if finishReason == "" {
		finishReason = "stop"
	}
	if len(toolCalls) > 0 {
		finishReason = constant.FinishReasonToolCalls
	}

	msg := dto.Message{Role: "assistant"}
	if content != "" {
		msg.SetStringContent(content)
	}
	if len(toolCalls) > 0 {
		if rawToolCalls, err := common.Marshal(toolCalls); err == nil {
			msg.ToolCalls = rawToolCalls
		}
	}
	if rc := reasoningBuilder.String(); rc != "" {
		msg.ReasoningContent = &rc
	}
	full := dto.OpenAITextResponse{
		Id:      common.GetUUID(),
		Model:   model,
		Object:  "chat.completion",
		Created: created,
		Choices: []dto.OpenAITextResponseChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: finishReason,
		}},
		Usage: *usage,
	}
	var out []byte
	if ollamaRelayFormat(info) == types.RelayFormatClaude {
		result, convertErr := relayconvert.ConvertResponse(c, info, types.RelayFormatClaude, &full)
		if convertErr != nil {
			return usage, types.NewOpenAIError(convertErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		claudeResponse, ok := result.Value.(*dto.ClaudeResponse)
		if !ok {
			return usage, types.NewOpenAIError(fmt.Errorf("expected Claude response, got %T", result.Value), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		out, err = common.Marshal(helper.ClaudeResponseForClient(claudeResponse))
	} else if ollamaIsCompletions(info) {
		clientUsage := helper.UsageForClient(usage)
		completion := ollamaCompletionsResponse{
			Id:      full.Id,
			Object:  "text_completion",
			Created: created,
			Model:   model,
			Choices: []ollamaCompletionsResponseChoice{{
				Text:         content,
				Index:        0,
				FinishReason: finishReason,
			}},
			Usage: *clientUsage,
		}
		out, err = common.Marshal(completion)
	} else {
		out, err = common.Marshal(helper.OpenAITextResponseForClient(&full))
	}
	if err != nil {
		return usage, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	if err := service.IOCopyBytesGracefully(c, resp, out); err != nil {
		return usage, ollamaStreamError(c, ollamaStreamAPIError(err))
	}
	commitOllamaToolCalls(info, responseToolCalls)
	recordOllamaPromptCacheResponse(info, usage, ollamaResponseCacheMessage(content, reasoningBuilder.String(), responseToolCalls), c)
	return usage, nil
}
