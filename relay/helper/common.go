package helper

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/officialfit"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// isDeepSeekV4StreamModel reports whether the relay is streaming a DeepSeek V4
// chat completion with the official-fit shape enabled, so protocol-shape
// details (SSE Content-Type, chunk fields) can mirror the official endpoint.
func isDeepSeekV4StreamModel(info *relaycommon.RelayInfo) bool {
	if info == nil || info.RelayMode != relayconstant.RelayModeChatCompletions {
		return false
	}
	if officialfit.FamilyOf(info.OriginModelName) != officialfit.FamilyDeepSeekV4 {
		return false
	}
	profile, ok := info.UserSetting.OfficialFitProfileFor(info.OriginModelName)
	return ok && profile.Shape
}

// pingProbe is the SSE comment frame written by PingData. Clients ignore it, so
// it does not count as delivered response content.
const pingProbe = ": PING\n\n"

// ginKeyKeepAliveBytes tracks how many bytes of the committed response are pure
// keep-alive output, so a retry decision can tell a ping-only commit apart from
// one that already delivered real data.
const ginKeyKeepAliveBytes = "helper_keepalive_written_bytes"

// RecordKeepAliveBytes accounts bytes that were written outside PingData but are
// still pure keep-alive output (the continuation writer-only ping). Retry
// classification uses this to keep a ping-only commit retryable.
func RecordKeepAliveBytes(c *gin.Context, n int) {
	if c == nil || n <= 0 {
		return
	}
	c.Set(ginKeyKeepAliveBytes, c.GetInt(ginKeyKeepAliveBytes)+n)
}

// ResponseCommitState describes what has already been committed to the client,
// which decides whether swapping channels for a retry is safe.
type ResponseCommitState int

const (
	// ResponseNotCommitted: neither headers nor body were committed.
	ResponseNotCommitted ResponseCommitState = iota
	// ResponseKeepAliveOnly: only keep-alive pings (or just the status/headers)
	// were committed. The client has no response content to act on, so another
	// channel can still serve this request.
	ResponseKeepAliveOnly
	// ResponsePayloadWritten: real response data reached the client. Retrying
	// would concatenate a second response onto a partial stream, so it must not
	// happen.
	ResponsePayloadWritten
)

// ResponseCommitStateOf classifies the current response commit state. It relies
// on the writer's byte count matching the tracked keep-alive byte count, which
// is exact because keep-alive output is only ever written by PingData and
// WhitespaceData (both go through writeKeepAliveProbe).
func ResponseCommitStateOf(c *gin.Context) ResponseCommitState {
	if c == nil || c.Writer == nil || !c.Writer.Written() {
		return ResponseNotCommitted
	}
	if c.Writer.Size() == c.GetInt(ginKeyKeepAliveBytes) {
		return ResponseKeepAliveOnly
	}
	return ResponsePayloadWritten
}

func FlushWriter(c *gin.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("flush panic recovered: %v", r)
		}
	}()

	if c == nil || c.Writer == nil {
		return nil
	}

	if requestContextDone(c) {
		return fmt.Errorf("request context done: %w", c.Request.Context().Err())
	}

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return errors.New("streaming error: flusher not found")
	}

	flusher.Flush()
	return nil
}

func requestContextDone(c *gin.Context) bool {
	return c != nil && c.Request != nil && c.Request.Context().Err() != nil
}

func SetEventStreamHeaders(c *gin.Context) {
	// 检查是否已经设置过头部
	if _, exists := c.Get("event_stream_headers_set"); exists {
		return
	}

	// 设置标志，表示头部已经设置过
	c.Set("event_stream_headers_set", true)

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
}

// relayInfoForClientUsage resolves the current relay info for the final
// client-usage clamp applied by the stream writers below.
func relayInfoForClientUsage(c *gin.Context) *relaycommon.RelayInfo {
	if c == nil {
		return nil
	}
	if info, ok := common.GetContextKeyType[*relaycommon.RelayInfo](c, constant.ContextKeyRelayInfoPtr); ok {
		return info
	}
	return nil
}

func ClaudeData(c *gin.Context, resp dto.ClaudeResponse) error {
	if requestContextDone(c) {
		return nil
	}

	clientResponse := ClaudeResponseForClient(&resp)
	jsonData, err := common.Marshal(clientResponse)
	if err != nil {
		common.SysError("error marshalling stream response: " + err.Error())
	} else {
		jsonData = relaycommon.ClampClientUsageJSON(relayInfoForClientUsage(c), jsonData)
		c.Render(-1, common.CustomEvent{Data: fmt.Sprintf("event: %s\n", resp.Type)})
		c.Render(-1, common.CustomEvent{Data: "data: " + string(jsonData)})
	}
	_ = FlushWriter(c)
	return nil
}

func ClaudeResponseForClient(resp *dto.ClaudeResponse) *dto.ClaudeResponse {
	if resp == nil {
		return nil
	}
	clientResponse := *resp
	if resp.Usage != nil {
		clientResponse.Usage = claudeUsageForClient(resp.Usage)
	}
	if resp.Message != nil && resp.Message.Usage != nil {
		clientMessage := *resp.Message
		clientMessage.Usage = claudeUsageForClient(resp.Message.Usage)
		clientResponse.Message = &clientMessage
	}
	return &clientResponse
}

// claudeUsageForClient strips the internal billing extension and zeroes the
// counts when the upstream reported an implausible usage (see client_usage.go).
func claudeUsageForClient(usage *dto.ClaudeUsage) *dto.ClaudeUsage {
	if usage == nil {
		return nil
	}
	if claudeUsageCountAnomaly(usage) != nil {
		return sanitizedClaudeUsageForClient(usage)
	}
	clientUsage := *usage
	clientUsage.BillingUsage = nil
	return &clientUsage
}

func GeminiResponseForClient(resp *dto.GeminiChatResponse) *dto.GeminiChatResponse {
	if resp == nil {
		return nil
	}
	clientResponse := *resp
	clientResponse.UsageMetadata = geminiUsageForClient(resp.UsageMetadata)
	return &clientResponse
}

// geminiUsageForClient strips the internal billing extension and zeroes the
// counts when the upstream reported an implausible usage.
func geminiUsageForClient(usage dto.GeminiUsageMetadata) dto.GeminiUsageMetadata {
	if geminiUsageCountAnomaly(&usage) != nil {
		return sanitizedGeminiUsageForClient(usage)
	}
	cloned := usage
	cloned.BillingUsage = nil
	return cloned
}

// UsageForClient renders the usage an OpenAI-shaped client sees: the internal
// billing extension is stripped, and an implausible upstream usage is reported
// as zeros instead of forwarding numbers that can break the client's own
// accounting (see client_usage.go).
func UsageForClient(usage *dto.Usage) *dto.Usage {
	if usage == nil {
		return nil
	}
	if UsageCountAnomaly(usage) != nil {
		return sanitizedUsageForClient(usage)
	}
	clientUsage := *usage
	clientUsage.BillingUsage = nil
	return &clientUsage
}

func OpenAITextResponseForClient(resp *dto.OpenAITextResponse) *dto.OpenAITextResponse {
	if resp == nil {
		return nil
	}
	clientResponse := *resp
	clientResponse.Usage = *UsageForClient(&resp.Usage)
	return &clientResponse
}

func OpenAIResponsesResponseForClient(resp *dto.OpenAIResponsesResponse) *dto.OpenAIResponsesResponse {
	if resp == nil {
		return nil
	}
	clientResponse := *resp
	clientResponse.Usage = UsageForClient(resp.Usage)
	return &clientResponse
}

func ChatCompletionsStreamResponseForClient(resp *dto.ChatCompletionsStreamResponse) *dto.ChatCompletionsStreamResponse {
	if resp == nil {
		return nil
	}
	clientResponse := resp.Copy()
	clientResponse.Usage = UsageForClient(resp.Usage)
	return clientResponse
}

func ResponsesStreamResponseForClient(resp *dto.ResponsesStreamResponse) *dto.ResponsesStreamResponse {
	if resp == nil {
		return nil
	}
	clientResponse := *resp
	clientResponse.Response = OpenAIResponsesResponseForClient(resp.Response)
	return &clientResponse
}

func ResponseForClient(response interface{}) interface{} {
	switch value := response.(type) {
	case dto.OpenAITextResponse:
		return *OpenAITextResponseForClient(&value)
	case *dto.OpenAITextResponse:
		return OpenAITextResponseForClient(value)
	case dto.OpenAIResponsesResponse:
		return *OpenAIResponsesResponseForClient(&value)
	case *dto.OpenAIResponsesResponse:
		return OpenAIResponsesResponseForClient(value)
	case dto.ChatCompletionsStreamResponse:
		return *ChatCompletionsStreamResponseForClient(&value)
	case *dto.ChatCompletionsStreamResponse:
		return ChatCompletionsStreamResponseForClient(value)
	case dto.ResponsesStreamResponse:
		return *ResponsesStreamResponseForClient(&value)
	case *dto.ResponsesStreamResponse:
		return ResponsesStreamResponseForClient(value)
	case dto.ClaudeResponse:
		return *ClaudeResponseForClient(&value)
	case *dto.ClaudeResponse:
		return ClaudeResponseForClient(value)
	case dto.GeminiChatResponse:
		return *GeminiResponseForClient(&value)
	case *dto.GeminiChatResponse:
		return GeminiResponseForClient(value)
	default:
		return response
	}
}

func ClaudeChunkData(c *gin.Context, resp dto.ClaudeResponse, data string) {
	if requestContextDone(c) {
		return
	}

	// Phase B last stop: the upstream message_delta may carry an accumulated
	// output_tokens above the client's max_tokens; cap it before rendering.
	data = string(relaycommon.ClampClientUsageJSON(relayInfoForClientUsage(c), common.StringToByteSlice(data)))
	// Claude-native SSE writer: scan the event for an implausible usage the
	// same way StringData does (see service.SanitizeClientUsageJSON). The
	// typed patch above only rewrites the fields this fork models; a Claude
	// event forwarded verbatim can still carry garbage counts in keys the
	// model does not know (found by the .52 matrix: message_start /
	// message_delta forwarded 1.39e15 verbatim).
	data = string(service.SanitizeClientUsageJSON(common.StringToByteSlice(data)))

	c.Render(-1, common.CustomEvent{Data: fmt.Sprintf("event: %s\n", resp.Type)})
	c.Render(-1, common.CustomEvent{Data: fmt.Sprintf("data: %s\n", data)})
	_ = FlushWriter(c)
}

func ResponseChunkData(c *gin.Context, resp dto.ResponsesStreamResponse, data string) error {
	if requestContextDone(c) {
		return fmt.Errorf("request context done: %w", c.Request.Context().Err())
	}

	data = string(relaycommon.ClampClientUsageJSON(relayInfoForClientUsage(c), common.StringToByteSlice(data)))
	// Shared Responses SSE writer: scan the event for an implausible usage the
	// same way StringData does (see service.SanitizeClientUsageJSON).
	data = string(service.SanitizeClientUsageJSON(common.StringToByteSlice(data)))

	c.Render(-1, common.CustomEvent{Data: fmt.Sprintf("event: %s\n", resp.Type)})
	c.Render(-1, common.CustomEvent{Data: fmt.Sprintf("data: %s", data)})
	return FlushWriter(c)
}

func StringData(c *gin.Context, str string) error {
	if c == nil || c.Writer == nil {
		return errors.New("context or writer is nil")
	}

	if requestContextDone(c) {
		return fmt.Errorf("request context done: %w", c.Request.Context().Err())
	}

	// Phase B last stop: cap an accumulated completion count to the request's
	// output budget before the chunk is rendered to the client. The clamp is
	// gated on a substring probe and a positive budget, so ordinary content
	// chunks are untouched.
	str = string(relaycommon.ClampClientUsageJSON(relayInfoForClientUsage(c), common.StringToByteSlice(str)))
	// Shared SSE writer: a chunk forwarded verbatim still has to be scanned for
	// an implausible usage (see service.SanitizeClientUsageJSON). The scan is
	// gated on a substring check, so ordinary content chunks are untouched.
	str = string(service.SanitizeClientUsageJSON(common.StringToByteSlice(str)))

	if err := (common.CustomEvent{Data: "data: " + str}).Render(c.Writer); err != nil {
		return fmt.Errorf("write stream data failed: %w", err)
	}
	return FlushWriter(c)
}

// writeKeepAliveProbe commits one keep-alive probe and books its bytes as
// non-content output. Both the SSE ping and the non-stream whitespace probe go
// through here so the retry decision cannot treat one of them as real content.
func writeKeepAliveProbe(c *gin.Context, probe, label string) error {
	writer, ok := c.Writer.(gin.ResponseWriter)
	if !ok {
		return errors.New("context or writer is nil")
	}
	return writeKeepAliveProbeTo(c, writer, probe, label)
}

func PingData(c *gin.Context) error {
	return writeKeepAliveProbe(c, pingProbe, "ping")
}

func ObjectData(c *gin.Context, object any) error {
	if object == nil {
		return errors.New("object is nil")
	}
	jsonData, err := common.Marshal(ResponseForClient(object))
	if err != nil {
		return fmt.Errorf("error marshalling object: %w", err)
	}
	return StringData(c, string(jsonData))
}

func Done(c *gin.Context) {
	_ = StringData(c, "[DONE]")
}

func WssString(c *gin.Context, ws *websocket.Conn, str string) error {
	if ws == nil {
		logger.LogError(c, "websocket connection is nil")
		return errors.New("websocket connection is nil")
	}
	//common.LogInfo(c, fmt.Sprintf("sending message: %s", str))
	return ws.WriteMessage(1, []byte(str))
}

func WssObject(c *gin.Context, ws *websocket.Conn, object any) error {
	jsonData, err := common.Marshal(object)
	if err != nil {
		return fmt.Errorf("error marshalling object: %w", err)
	}
	if ws == nil {
		logger.LogError(c, "websocket connection is nil")
		return errors.New("websocket connection is nil")
	}
	//common.LogInfo(c, fmt.Sprintf("sending message: %s", jsonData))
	return ws.WriteMessage(1, jsonData)
}

func WssError(c *gin.Context, ws *websocket.Conn, openaiError types.OpenAIError) {
	if ws == nil {
		return
	}
	errorObj := &dto.RealtimeEvent{
		Type:    "error",
		EventId: GetLocalRealtimeID(c),
		Error:   &openaiError,
	}
	_ = WssObject(c, ws, errorObj)
}

func GetResponseID(c *gin.Context) string {
	logID := c.GetString(common.RequestIdKey)
	return fmt.Sprintf("chatcmpl-%s", logID)
}

func GetLocalRealtimeID(c *gin.Context) string {
	logID := c.GetString(common.RequestIdKey)
	return fmt.Sprintf("evt_%s", logID)
}

func GenerateStartEmptyResponse(id string, createAt int64, model string, systemFingerprint *string) *dto.ChatCompletionsStreamResponse {
	return &dto.ChatCompletionsStreamResponse{
		Id:                id,
		Object:            "chat.completion.chunk",
		Created:           createAt,
		Model:             model,
		SystemFingerprint: systemFingerprint,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					Role:    "assistant",
					Content: common.GetPointer(""),
				},
			},
		},
	}
}

func GenerateStopResponse(id string, createAt int64, model string, finishReason string) *dto.ChatCompletionsStreamResponse {
	return &dto.ChatCompletionsStreamResponse{
		Id:                id,
		Object:            "chat.completion.chunk",
		Created:           createAt,
		Model:             model,
		SystemFingerprint: nil,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				FinishReason: &finishReason,
			},
		},
	}
}

func GenerateFinalUsageResponse(id string, createAt int64, model string, usage dto.Usage) *dto.ChatCompletionsStreamResponse {
	return &dto.ChatCompletionsStreamResponse{
		Id:                id,
		Object:            "chat.completion.chunk",
		Created:           createAt,
		Model:             model,
		SystemFingerprint: nil,
		Choices:           make([]dto.ChatCompletionsStreamResponseChoice, 0),
		Usage:             &usage,
	}
}
