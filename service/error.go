package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

func MidjourneyErrorWrapper(code int, desc string) *taskdto.MidjourneyResponse {
	return &taskdto.MidjourneyResponse{
		Code:        code,
		Description: desc,
	}
}

func MidjourneyErrorWithStatusCodeWrapper(code int, desc string, statusCode int) *taskdto.MidjourneyResponseWithStatusCode {
	return &taskdto.MidjourneyResponseWithStatusCode{
		StatusCode: statusCode,
		Response:   *MidjourneyErrorWrapper(code, desc),
	}
}

//// OpenAIErrorWrapper wraps an error into an OpenAIErrorWithStatusCode
//func OpenAIErrorWrapper(err error, code string, statusCode int) *dto.OpenAIErrorWithStatusCode {
//	text := err.Error()
//	lowerText := strings.ToLower(text)
//	if !strings.HasPrefix(lowerText, "get file base64 from url") && !strings.HasPrefix(lowerText, "mime type is not supported") {
//		if strings.Contains(lowerText, "post") || strings.Contains(lowerText, "dial") || strings.Contains(lowerText, "http") {
//			common.SysLog(fmt.Sprintf("error: %s", text))
//			text = "请求上游地址失败"
//		}
//	}
//	openAIError := dto.OpenAIError{
//		Message: text,
//		Type:    "new_api_error",
//		Code:    code,
//	}
//	return &dto.OpenAIErrorWithStatusCode{
//		Error:      openAIError,
//		StatusCode: statusCode,
//	}
//}
//
//func OpenAIErrorWrapperLocal(err error, code string, statusCode int) *dto.OpenAIErrorWithStatusCode {
//	openaiErr := OpenAIErrorWrapper(err, code, statusCode)
//	openaiErr.LocalError = true
//	return openaiErr
//}

func ClaudeErrorWrapper(err error, code string, statusCode int) *dto.ClaudeErrorWithStatusCode {
	text := err.Error()
	lowerText := strings.ToLower(text)
	if !strings.HasPrefix(lowerText, "get file base64 from url") {
		if strings.Contains(lowerText, "post") || strings.Contains(lowerText, "dial") || strings.Contains(lowerText, "http") {
			common.SysLog(fmt.Sprintf("error: %s", text))
			text = "请求上游地址失败"
		}
	}
	claudeError := types.ClaudeError{
		Message: text,
		Type:    "new_api_error",
	}
	return &dto.ClaudeErrorWithStatusCode{
		Error:      claudeError,
		StatusCode: statusCode,
	}
}

func ClaudeErrorWrapperLocal(err error, code string, statusCode int) *dto.ClaudeErrorWithStatusCode {
	claudeErr := ClaudeErrorWrapper(err, code, statusCode)
	claudeErr.LocalError = true
	return claudeErr
}

func RelayErrorHandler(ctx context.Context, resp *http.Response, showBodyWhenFail bool) (newApiErr *types.NewAPIError) {
	return relayErrorHandler(ctx, resp, showBodyWhenFail, types.RelayFormatOpenAI)
}

// RelayErrorHandlerWithFormat preserves the selected compatibility protocol
// when a cyber-policy response is detected before the channel adaptor runs.
// Other upstream errors intentionally retain RelayErrorHandler's behavior.
func RelayErrorHandlerWithFormat(ctx context.Context, resp *http.Response, showBodyWhenFail bool, relayFormat types.RelayFormat) *types.NewAPIError {
	return relayErrorHandler(ctx, resp, showBodyWhenFail, relayFormat)
}

func relayErrorHandler(ctx context.Context, resp *http.Response, showBodyWhenFail bool, relayFormat types.RelayFormat) (newApiErr *types.NewAPIError) {
	defer func() {
		newApiErr = NormalizeDFlashLogprobCapabilityError(newApiErr)
	}()
	newApiErr = types.InitOpenAIError(types.ErrorCodeBadResponseStatusCode, resp.StatusCode)

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	CloseResponseBodyGracefully(resp)
	var errResponse dto.GeneralErrorResponse
	responseBodyText := string(responseBody)
	responseBodyPreview := common.DebugLogPreview(responseBodyText)
	buildErrWithBody := func(message string) error {
		if message == "" {
			return fmt.Errorf("bad response status code %d, body: %s", resp.StatusCode, responseBodyPreview)
		}
		return fmt.Errorf("bad response status code %d, message: %s, body: %s", resp.StatusCode, message, responseBodyPreview)
	}
	if ginCtx, ok := ctx.(*gin.Context); ok {
		if cyberErr := NewOpenAICyberPolicyError(ginCtx, responseBody, resp.StatusCode, false, nil); cyberErr != nil {
			writeCyberPolicyErrorForRelayFormat(ginCtx, resp, responseBody, cyberErr, relayFormat)
			MarkOpsCyberPolicyForwarded(ginCtx)
			return cyberErr
		}
	} else if hit, _, message := detectOpenAICyberPolicy(responseBody); hit {
		if message == "" {
			message = "upstream cyber policy interception"
		}
		return types.NewOpenAIError(errors.New(message), types.ErrorCode("cyber_policy"), resp.StatusCode,
			types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}

	err = common.Unmarshal(responseBody, &errResponse)
	if err != nil {
		if showBodyWhenFail {
			newApiErr.Err = buildErrWithBody("")
		} else {
			logger.LogError(ctx, fmt.Sprintf("bad response status code %d, body: %s", resp.StatusCode, responseBodyPreview))
			newApiErr.Err = fmt.Errorf("bad response status code %d", resp.StatusCode)
		}
		return
	}

	if common.GetJsonType(errResponse.Error) == "object" {
		// General format error (OpenAI, Anthropic, Gemini, etc.)
		oaiError := errResponse.TryToOpenAIError()
		if oaiError != nil {
			newApiErr = types.WithOpenAIError(*oaiError, resp.StatusCode)
			if showBodyWhenFail {
				newApiErr.Err = buildErrWithBody(newApiErr.Error())
			}
			return
		}
	}
	message := errResponse.ToMessage()
	if message == "" {
		// The body parsed as JSON but carried no usable error message; log the
		// raw body so the upstream failure remains diagnosable.
		logger.LogError(ctx, fmt.Sprintf("bad response status code %d with empty error message, body: %s", resp.StatusCode, responseBodyPreview))
	}
	newApiErr = types.NewOpenAIError(errors.New(message), types.ErrorCodeBadResponseStatusCode, resp.StatusCode)
	if showBodyWhenFail {
		newApiErr.Err = buildErrWithBody(newApiErr.Error())
	}
	return
}

// NormalizeDFlashLogprobCapabilityError keeps DFLASH capability failures
// retryable so channel selection can move to an upstream that returns real
// logprobs instead of disabling the incompatible channel.
func NormalizeDFlashLogprobCapabilityError(err *types.NewAPIError) *types.NewAPIError {
	if err == nil {
		return err
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "dflash") ||
		(!strings.Contains(message, "return_logprob") && !strings.Contains(message, "return_logprobs")) {
		return err
	}
	return types.NewErrorWithStatusCode(err, types.ErrorCodeChannelUnsupportedFeature, err.StatusCode)
}

// NormalizeOpenAIStreamError converts a top-level OpenAI-compatible SSE error
// event into the same error path used for a non-2xx upstream response. A few
// providers send an error object with HTTP 200 before closing the stream; that
// event must not be mistaken for an empty successful completion.
func NormalizeOpenAIStreamError(data []byte, upstreamStatus int) *types.NewAPIError {
	var errResponse dto.GeneralErrorResponse
	if err := common.Unmarshal(data, &errResponse); err != nil || len(errResponse.Error) == 0 {
		return nil
	}
	message := errResponse.ToMessage()
	if message == "" {
		return nil
	}
	openAIError := errResponse.TryToOpenAIError()
	statusCode := upstreamStatus
	if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices {
		statusCode = http.StatusBadGateway
	}
	if openAIError != nil {
		return NormalizeDFlashLogprobCapabilityError(types.WithOpenAIError(*openAIError, statusCode))
	}
	return NormalizeDFlashLogprobCapabilityError(types.NewOpenAIError(
		errors.New(message), types.ErrorCodeBadResponseStatusCode, statusCode,
	))
}

func writeCyberPolicyErrorForRelayFormat(c *gin.Context, resp *http.Response, body []byte, err *types.NewAPIError, relayFormat types.RelayFormat) {
	stream := resp != nil && strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	statusCode := http.StatusBadRequest
	if resp != nil && resp.StatusCode > 0 {
		statusCode = resp.StatusCode
	}
	switch relayFormat {
	case types.RelayFormatClaude:
		payload := gin.H{"type": "error", "error": err.ToClaudeError()}
		if stream {
			if data, marshalErr := common.Marshal(payload); marshalErr == nil {
				c.Render(-1, common.CustomEvent{Data: "event: error\n"})
				c.Render(-1, common.CustomEvent{Data: "data: " + string(data)})
				c.Writer.Flush()
			}
		} else {
			c.JSON(statusCode, payload)
		}
	case types.RelayFormatGemini:
		payload := gin.H{"error": gin.H{
			"code":    statusCode,
			"message": err.Error(),
			"status":  "CYBER_POLICY",
		}}
		if stream {
			if data, marshalErr := common.Marshal(payload); marshalErr == nil {
				c.Render(-1, common.CustomEvent{Data: "data: " + string(data)})
				c.Writer.Flush()
			}
		} else {
			c.JSON(statusCode, payload)
		}
	default:
		IOCopyBytesGracefully(c, resp, body)
	}
}

// NormalizeServerOverloadError maps Codex's non-retryable capacity marker to
// a transient server error so clients can retry or fail over to another model.
func NormalizeServerOverloadError(openAIError *types.OpenAIError) bool {
	if openAIError == nil {
		return false
	}
	code, ok := openAIError.Code.(string)
	if !ok {
		return false
	}
	switch code {
	case "server_is_overloaded", "server_overloaded", "serverOverloaded", "slow_down":
		openAIError.Code = "server_error"
		return true
	default:
		return false
	}
}

func ResetStatusCode(newApiErr *types.NewAPIError, statusCodeMappingStr string) {
	if newApiErr == nil {
		return
	}
	if statusCodeMappingStr == "" || statusCodeMappingStr == "{}" {
		return
	}
	statusCodeMapping := make(map[string]any)
	err := common.Unmarshal([]byte(statusCodeMappingStr), &statusCodeMapping)
	if err != nil {
		return
	}
	if newApiErr.StatusCode == http.StatusOK {
		return
	}
	codeStr := strconv.Itoa(newApiErr.StatusCode)
	if value, ok := statusCodeMapping[codeStr]; ok {
		intCode, ok := parseStatusCodeMappingValue(value)
		if !ok {
			return
		}
		newApiErr.StatusCode = intCode
	}
}

func parseStatusCodeMappingValue(value any) (int, bool) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return 0, false
		}
		statusCode, err := strconv.Atoi(v)
		if err != nil {
			return 0, false
		}
		return statusCode, true
	case float64:
		if v != math.Trunc(v) {
			return 0, false
		}
		return int(v), true
	case int:
		return v, true
	case json.Number:
		statusCode, err := strconv.Atoi(v.String())
		if err != nil {
			return 0, false
		}
		return statusCode, true
	default:
		return 0, false
	}
}

func TaskErrorWrapperLocal(err error, code string, statusCode int) *taskdto.TaskError {
	openaiErr := TaskErrorWrapper(err, code, statusCode)
	openaiErr.LocalError = true
	return openaiErr
}

func TaskErrorWrapper(err error, code string, statusCode int) *taskdto.TaskError {
	text := err.Error()
	lowerText := strings.ToLower(text)
	if strings.Contains(lowerText, "post") || strings.Contains(lowerText, "dial") || strings.Contains(lowerText, "http") {
		common.SysLog(fmt.Sprintf("error: %s", text))
		//text = "请求上游地址失败"
		text = common.MaskSensitiveInfo(text)
	}
	//避免暴露内部错误
	taskError := &taskdto.TaskError{
		Code:       code,
		Message:    text,
		StatusCode: statusCode,
		Error:      err,
	}

	return taskError
}

// TaskErrorFromAPIError 将 PreConsumeBilling 返回的 NewAPIError 转换为 TaskError。
func TaskErrorFromAPIError(apiErr *types.NewAPIError) *taskdto.TaskError {
	if apiErr == nil {
		return nil
	}
	return &taskdto.TaskError{
		Code:       string(apiErr.GetErrorCode()),
		Message:    apiErr.Err.Error(),
		StatusCode: apiErr.StatusCode,
		Error:      apiErr.Err,
	}
}
