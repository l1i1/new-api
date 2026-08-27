package helper

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
)

func GetAndValidateRequest(c *gin.Context, format types.RelayFormat) (request dto.Request, err error) {
	relayMode := relayconstant.Path2RelayMode(c.Request.URL.Path)

	switch format {
	case types.RelayFormatOpenAI:
		request, err = GetAndValidateTextRequest(c, relayMode)
	case types.RelayFormatGemini:
		if strings.Contains(c.Request.URL.Path, ":embedContent") {
			request, err = GetAndValidateGeminiEmbeddingRequest(c)
		} else if strings.Contains(c.Request.URL.Path, ":batchEmbedContents") {
			request, err = GetAndValidateGeminiBatchEmbeddingRequest(c)
		} else {
			request, err = GetAndValidateGeminiRequest(c)
		}
	case types.RelayFormatClaude:
		request, err = GetAndValidateClaudeRequest(c)
	case types.RelayFormatOpenAIResponses:
		request, err = GetAndValidateResponsesRequest(c)
	case types.RelayFormatOpenAIResponsesCompaction:
		request, err = GetAndValidateResponsesCompactionRequest(c)
	case types.RelayFormatOpenAIAlphaSearch:
		request, err = GetAndValidateAlphaSearchRequest(c)

	case types.RelayFormatOpenAIImage:
		request, err = GetAndValidOpenAIImageRequest(c, relayMode)
	case types.RelayFormatEmbedding:
		request, err = GetAndValidateEmbeddingRequest(c, relayMode)
	case types.RelayFormatRerank:
		request, err = GetAndValidateRerankRequest(c)
	case types.RelayFormatOpenAIAudio:
		request, err = GetAndValidAudioRequest(c, relayMode)
	case types.RelayFormatOpenAIRealtime:
		request = &dto.BaseRequest{}
	default:
		return nil, fmt.Errorf("unsupported relay format: %s", format)
	}
	return request, err
}

func GetAndValidAudioRequest(c *gin.Context, relayMode int) (*dto.AudioRequest, error) {
	audioRequest := &dto.AudioRequest{}
	err := common.UnmarshalBodyReusable(c, audioRequest)
	if err != nil {
		return nil, err
	}
	switch relayMode {
	case relayconstant.RelayModeAudioSpeech:
		if audioRequest.Model == "" {
			return nil, errors.New("model is required")
		}
	default:
		if audioRequest.Model == "" {
			return nil, errors.New("model is required")
		}
		if audioRequest.ResponseFormat == "" {
			audioRequest.ResponseFormat = "json"
		}
	}
	return audioRequest, nil
}

func GetAndValidateRerankRequest(c *gin.Context) (*dto.RerankRequest, error) {
	var rerankRequest *dto.RerankRequest
	err := common.UnmarshalBodyReusable(c, &rerankRequest)
	if err != nil {
		logger.LogError(c, fmt.Sprintf("getAndValidateTextRequest failed: %s", err.Error()))
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	if rerankRequest.Query == "" {
		return nil, types.NewError(fmt.Errorf("query is empty"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if len(rerankRequest.Documents) == 0 {
		return nil, types.NewError(fmt.Errorf("documents is empty"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	return rerankRequest, nil
}

func GetAndValidateEmbeddingRequest(c *gin.Context, relayMode int) (*dto.EmbeddingRequest, error) {
	var embeddingRequest *dto.EmbeddingRequest
	err := common.UnmarshalBodyReusable(c, &embeddingRequest)
	if err != nil {
		logger.LogError(c, fmt.Sprintf("getAndValidateTextRequest failed: %s", err.Error()))
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	if embeddingRequest.Input == nil {
		return nil, fmt.Errorf("input is empty")
	}
	if relayMode == relayconstant.RelayModeModerations && embeddingRequest.Model == "" {
		embeddingRequest.Model = "omni-moderation-latest"
	}
	if relayMode == relayconstant.RelayModeEmbeddings && embeddingRequest.Model == "" {
		embeddingRequest.Model = c.Param("model")
	}
	return embeddingRequest, nil
}

// maxTokensLimit bounds user-supplied max token fields. These values feed
// pre-consume quota math (preConsumedTokens * ratio); an unbounded value can
// overflow the conversion and corrupt billing.
const maxTokensLimit = math.MaxInt32 / 2

func exceedsMaxTokensLimit(values ...*uint) bool {
	for _, v := range values {
		if lo.FromPtrOr(v, uint(0)) > maxTokensLimit {
			return true
		}
	}
	return false
}

func GetAndValidateResponsesRequest(c *gin.Context) (*dto.OpenAIResponsesRequest, error) {
	request := &dto.OpenAIResponsesRequest{}
	err := common.UnmarshalBodyReusable(c, request)
	if err != nil {
		return nil, err
	}
	if request.Model == "" {
		return nil, errors.New("model is required")
	}
	if request.Input == nil {
		return nil, errors.New("input is required")
	}
	if exceedsMaxTokensLimit(request.MaxOutputTokens) {
		return nil, errors.New("max_output_tokens is invalid")
	}
	return request, nil
}

func GetAndValidateAlphaSearchRequest(c *gin.Context) (*dto.AlphaSearchRequest, error) {
	request := &dto.AlphaSearchRequest{}
	if err := common.UnmarshalBodyReusable(c, request); err != nil {
		return nil, err
	}
	if request.Model == "" {
		return nil, errors.New("model is required")
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	rawBody, err := storage.Bytes()
	if err != nil {
		return nil, err
	}
	request.RawBody = rawBody
	return request, nil
}

func GetAndValidateResponsesCompactionRequest(c *gin.Context) (*dto.OpenAIResponsesCompactionRequest, error) {
	request := &dto.OpenAIResponsesCompactionRequest{}
	if err := common.UnmarshalBodyReusable(c, request); err != nil {
		return nil, err
	}
	if request.Model == "" {
		return nil, errors.New("model is required")
	}
	return request, nil
}

func GetAndValidOpenAIImageRequest(c *gin.Context, relayMode int) (*dto.ImageRequest, error) {
	imageRequest := &dto.ImageRequest{}

	switch relayMode {
	case relayconstant.RelayModeImagesEdits:
		if strings.Contains(c.Request.Header.Get("Content-Type"), "multipart/form-data") {
			form, err := common.ParseMultipartFormReusable(c)
			if err != nil {
				return nil, fmt.Errorf("failed to parse image edit form request: %w", err)
			}
			formData := url.Values(form.Value)
			c.Request.MultipartForm = form
			c.Request.PostForm = formData
			imageRequest.Prompt = formData.Get("prompt")
			imageRequest.Model = formData.Get("model")
			if nValue := strings.TrimSpace(formData.Get("n")); nValue != "" {
				n, err := strconv.Atoi(nValue)
				if err != nil || n < 0 || n > dto.MaxImageN {
					return nil, fmt.Errorf("n must be an integer between 1 and %d", dto.MaxImageN)
				}
				imageRequest.N = common.GetPointer(uint(n))
			}
			imageRequest.Quality = formData.Get("quality")
			imageRequest.Size = formData.Get("size")
			if streamValue := strings.TrimSpace(formData.Get("stream")); streamValue != "" {
				stream, err := strconv.ParseBool(streamValue)
				if err != nil {
					return nil, fmt.Errorf("invalid stream value: %w", err)
				}
				imageRequest.Stream = common.GetPointer(stream)
			}
			if imageValue := formData.Get("image"); imageValue != "" {
				imageRequest.Image, _ = common.Marshal(imageValue)
			}

			if imageRequest.Model == "gpt-image-1" {
				if imageRequest.Quality == "" {
					imageRequest.Quality = "standard"
				}
			}
			if imageRequest.N == nil || *imageRequest.N == 0 {
				imageRequest.N = common.GetPointer(uint(1))
			}

			hasWatermark := formData.Has("watermark")
			if hasWatermark {
				watermark := formData.Get("watermark") == "true"
				imageRequest.Watermark = &watermark
			}
			break
		}
		fallthrough
	default:
		err := common.UnmarshalBodyReusable(c, imageRequest)
		if err != nil {
			return nil, err
		}

		if imageRequest.Model == "" {
			//imageRequest.Model = "dall-e-3"
			return nil, errors.New("model is required")
		}

		if strings.Contains(imageRequest.Size, "×") {
			return nil, errors.New("size an unexpected error occurred in the parameter, please use 'x' instead of the multiplication sign '×'")
		}

		if imageRequest.N != nil && *imageRequest.N > dto.MaxImageN {
			return nil, fmt.Errorf("n must be an integer between 1 and %d", dto.MaxImageN)
		}

		// Not "256x256", "512x512", or "1024x1024"
		if imageRequest.Model == "dall-e-2" || imageRequest.Model == "dall-e" {
			if imageRequest.Size != "" && imageRequest.Size != "256x256" && imageRequest.Size != "512x512" && imageRequest.Size != "1024x1024" {
				return nil, errors.New("size must be one of 256x256, 512x512, or 1024x1024 for dall-e-2 or dall-e")
			}
			if imageRequest.Size == "" {
				imageRequest.Size = "1024x1024"
			}
		} else if imageRequest.Model == "dall-e-3" {
			if imageRequest.Size != "" && imageRequest.Size != "1024x1024" && imageRequest.Size != "1024x1792" && imageRequest.Size != "1792x1024" {
				return nil, errors.New("size must be one of 1024x1024, 1024x1792 or 1792x1024 for dall-e-3")
			}
			if imageRequest.Quality == "" {
				imageRequest.Quality = "standard"
			}
			if imageRequest.Size == "" {
				imageRequest.Size = "1024x1024"
			}
		} else if imageRequest.Model == "gpt-image-1" {
			if imageRequest.Quality == "" {
				imageRequest.Quality = "auto"
			}
		}

		//if imageRequest.Prompt == "" {
		//	return nil, errors.New("prompt is required")
		//}

		if imageRequest.N == nil || *imageRequest.N == 0 {
			imageRequest.N = common.GetPointer(uint(1))
		}
	}

	return imageRequest, nil
}

func GetAndValidateClaudeRequest(c *gin.Context) (textRequest *dto.ClaudeRequest, err error) {
	textRequest = &dto.ClaudeRequest{}
	err = common.UnmarshalBodyReusable(c, textRequest)
	if err != nil {
		return nil, err
	}
	if textRequest.Messages == nil || len(textRequest.Messages) == 0 {
		return nil, errors.New("field messages is required")
	}
	if textRequest.Model == "" {
		return nil, errors.New("field model is required")
	}
	if exceedsMaxTokensLimit(textRequest.MaxTokens, textRequest.MaxTokensToSample) {
		return nil, errors.New("max_tokens is invalid")
	}

	//if textRequest.Stream {
	//	relayInfo.IsStream = true
	//}

	return textRequest, nil
}

func GetAndValidateTextRequest(c *gin.Context, relayMode int) (*dto.GeneralOpenAIRequest, error) {
	textRequest := &dto.GeneralOpenAIRequest{}
	err := common.UnmarshalBodyReusable(c, textRequest)
	if err != nil {
		return nil, err
	}

	if relayMode == relayconstant.RelayModeModerations && textRequest.Model == "" {
		textRequest.Model = "text-moderation-latest"
	}
	if relayMode == relayconstant.RelayModeEmbeddings && textRequest.Model == "" {
		textRequest.Model = c.Param("model")
	}

	if exceedsMaxTokensLimit(textRequest.MaxTokens, textRequest.MaxCompletionTokens) {
		return nil, errors.New("max_tokens is invalid")
	}
	if textRequest.Model == "" {
		return nil, errors.New("model is required")
	}
	if textRequest.WebSearchOptions != nil {
		if textRequest.WebSearchOptions.SearchContextSize != "" {
			validSizes := map[string]bool{
				"high":   true,
				"medium": true,
				"low":    true,
			}
			if !validSizes[textRequest.WebSearchOptions.SearchContextSize] {
				return nil, errors.New("invalid search_context_size, must be one of: high, medium, low")
			}
		} else {
			textRequest.WebSearchOptions.SearchContextSize = "medium"
		}
	}
	switch relayMode {
	case relayconstant.RelayModeCompletions:
		if textRequest.Prompt == "" {
			return nil, errors.New("field prompt is required")
		}
	case relayconstant.RelayModeChatCompletions:
		profile, fitEnabled := officialFitProfile(c, textRequest.Model)
		if fitEnabled && profile.Validate {
			if err := validateDeepSeekV4OfficialFields(textRequest); err != nil {
				return nil, err
			}
			if err := validateDeepSeekV4Logprobs(textRequest); err != nil {
				return nil, err
			}
			if err := validateKimiK3OfficialFields(textRequest); err != nil {
				return nil, err
			}
		}
		markDeepSeekV4OfficialPin(c, textRequest)
		// For FIM (Fill-in-the-middle) requests with prefix/suffix, messages is optional
		// It will be filled by provider-specific adaptors if needed (e.g., SiliconFlow)。Or it is allowed by model vendor(s) (e.g., DeepSeek)
		if len(textRequest.Messages) == 0 && textRequest.Prefix == nil && textRequest.Suffix == nil {
			return nil, errors.New("field messages is required")
		}
	case relayconstant.RelayModeEmbeddings:
	case relayconstant.RelayModeModerations:
		if textRequest.Input == nil || textRequest.Input == "" {
			return nil, errors.New("field input is required")
		}
	case relayconstant.RelayModeEdits:
		if textRequest.Instruction == "" {
			return nil, errors.New("field instruction is required")
		}
	}
	return textRequest, nil
}

// DeepSeek V4 official validation messages. The official endpoint returns these
// verbatim; keep the wording in sync with api.deepseek.com when it drifts.
const (
	deepSeekV4ReasoningEffortMessage  = "Invalid reasoning_effort value, the valid values are [low, high, max]."
	deepSeekV4TopPMessage             = "Invalid top_p value, the valid range of top_p is (0, 1]."
	deepSeekV4TemperatureMessage      = "Invalid temperature value, the valid range of temperature is [0, 2]"
	deepSeekV4JsonObjectMessage       = "Prompt must contain the word 'json' in some form to use 'response_format' of type 'json_object'."
	deepSeekV4TopLogprobsPairMessage  = "Invalid top_logprobs and logprobs value, logprobs must be set to true if top_logprobs is used."
	deepSeekV4TopLogprobsRangeMessage = "Invalid top_logprobs value, the valid range of top_logprobs is [0, 20]."
)

// deepSeekV4MessagesText concatenates the visible text of every message. The
// official json_object validation scans the whole conversation (system and
// user turns alike, case-insensitive) for the word "json".
func deepSeekV4MessagesText(messages []dto.Message) string {
	var sb strings.Builder
	for i := range messages {
		sb.WriteString(messages[i].StringContent())
	}
	return sb.String()
}

func validateDeepSeekV4OfficialFields(request *dto.GeneralOpenAIRequest) error {
	if request == nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(request.Model)), "deepseek-v4-") {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(request.ReasoningEffort), "extreme") {
		return types.WithOpenAIError(types.OpenAIError{
			Message: deepSeekV4ReasoningEffortMessage,
			Type:    "invalid_request_error",
			Param:   nil,
			Code:    "invalid_request_error",
		}, http.StatusBadRequest)
	}
	if request.ResponseFormat != nil && request.ResponseFormat.Type == "json_object" &&
		!strings.Contains(strings.ToLower(deepSeekV4MessagesText(request.Messages)), "json") {
		return types.WithOpenAIError(types.OpenAIError{
			Message: deepSeekV4JsonObjectMessage,
			Type:    "invalid_request_error",
			Param:   nil,
			Code:    "invalid_request_error",
		}, http.StatusBadRequest)
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 2) {
		return types.WithOpenAIError(types.OpenAIError{
			Message: deepSeekV4TemperatureMessage,
			Type:    "invalid_request_error",
			Param:   nil,
			Code:    "invalid_request_error",
		}, http.StatusBadRequest)
	}
	if request.TopP != nil && (*request.TopP <= 0 || *request.TopP > 1) {
		return types.WithOpenAIError(types.OpenAIError{
			Message: deepSeekV4TopPMessage,
			Type:    "invalid_request_error",
			Param:   nil,
			Code:    "invalid_request_error",
		}, http.StatusBadRequest)
	}
	return nil
}

// markDeepSeekV4OfficialPin flags deepseek-v4 requests whose sampling
// parameters are known to drive aggregator upstreams into divergent behavior
// (the K08 reasoning-loop class). Only these requests are pinned to the
// official channel; ordinary fit-able requests keep normal aggregator routing.
func markDeepSeekV4OfficialPin(c *gin.Context, request *dto.GeneralOpenAIRequest) {
	if c == nil || request == nil {
		return
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(request.Model)), "deepseek-v4-") {
		return
	}
	pinned := false
	if request.Temperature != nil && *request.Temperature > 1.5 {
		pinned = true
	}
	if request.TopP != nil && *request.TopP < 0.3 {
		pinned = true
	}
	if request.FrequencyPenalty != nil && *request.FrequencyPenalty > 1.0 {
		pinned = true
	}
	if request.PresencePenalty != nil && *request.PresencePenalty > 1.0 {
		pinned = true
	}
	if pinned {
		common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
	}
}

func validateDeepSeekV4Logprobs(request *dto.GeneralOpenAIRequest) error {
	if request == nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(request.Model)), "deepseek-v4-") || request.TopLogProbs == nil {
		return nil
	}
	if request.LogProbs == nil || !*request.LogProbs {
		return types.WithOpenAIError(types.OpenAIError{
			Message: deepSeekV4TopLogprobsPairMessage,
			Type:    "invalid_request_error",
			Param:   nil,
			Code:    "invalid_request_error",
		}, http.StatusBadRequest)
	}
	if *request.TopLogProbs < 0 || *request.TopLogProbs > 20 {
		return types.WithOpenAIError(types.OpenAIError{
			Message: deepSeekV4TopLogprobsRangeMessage,
			Type:    "invalid_request_error",
			Param:   nil,
			Code:    "invalid_request_error",
		}, http.StatusBadRequest)
	}
	return nil
}

// officialFitProfile resolves the user's official-fit profile for a model from
// the request context. TokenAuth writes the user setting before any relay
// handler runs, so both validation (here) and error rendering (controller)
// consult the same profile.
func officialFitProfile(c *gin.Context, model string) (dto.OfficialFitProfile, bool) {
	if c == nil {
		return dto.OfficialFitProfile{}, false
	}
	setting, ok := common.GetContextKeyType[dto.UserSetting](c, constant.ContextKeyUserSetting)
	if !ok {
		return dto.OfficialFitProfile{}, false
	}
	return setting.OfficialFitProfileFor(model)
}

// Kimi K3 official validation texts, calibrated 2026-08-27 against the live
// api.moonshot.cn API. The official endpoint does NOT enum-check
// reasoning_effort strings and silently ignores the K2.x thinking field; only
// fixed sampling params, the logprobs pair and a specified tool_choice are
// rejected, with the exact wordings below.
const (
	kimiK3TemperatureMessage         = "invalid temperature: only 1 is allowed for this model"
	kimiK3TopPMessage                = "invalid top_p: only 0.95 is allowed for this model"
	kimiK3NMessage                   = "invalid n: only 1 is allowed for this model"
	kimiK3PresencePenaltyMessage     = "invalid presence_penalty: only 0 is allowed for this model"
	kimiK3FrequencyPenaltyMessage    = "invalid frequency_penalty: only 0 is allowed for this model"
	kimiK3LogprobsFalseMessage       = "invalid logprobs: only false is allowed for this model"
	kimiK3TopLogprobsPairMessage     = "Invalid request: logprobs must be set to true if top_logprobs is used"
	kimiK3ToolChoiceSpecifiedMessage = "tool_choice 'specified' is incompatible with thinking enabled"
	kimiK3ToolNameMessage            = "Invalid request: function name is invalid, must start with a letter and can contain letters, numbers, underscores, and dashes"
	kimiK3MessagesEmptyMessage       = "Invalid request: messages must not be empty"
)

func isKimiK3Model(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "kimi-k3")
}

// validateKimiK3OfficialFields mirrors the official Moonshot kimi-k3 request
// contract as observed live: non-fixed sampling values (temperature must be
// 1.0, top_p 0.95, n 1, both penalties 0), logprobs disabled by design, the
// top_logprobs pair requirement, no specified tool_choice and non-empty
// messages. Explicit fixed values are accepted exactly as the official
// endpoint does. Only requests targeting a kimi-k3 model are inspected; other
// fields (thinking, reasoning_effort strings, max_completion_tokens) are
// passed through as the official endpoint accepts them.
func validateKimiK3OfficialFields(request *dto.GeneralOpenAIRequest) error {
	if request == nil || !isKimiK3Model(request.Model) {
		return nil
	}
	if request.Temperature != nil && math.Abs(*request.Temperature-1.0) > 1e-9 {
		return kimiK3Error(kimiK3TemperatureMessage)
	}
	if request.TopP != nil && math.Abs(*request.TopP-0.95) > 1e-9 {
		return kimiK3Error(kimiK3TopPMessage)
	}
	if request.N != nil && *request.N != 1 {
		return kimiK3Error(kimiK3NMessage)
	}
	if request.PresencePenalty != nil && *request.PresencePenalty != 0 {
		return kimiK3Error(kimiK3PresencePenaltyMessage)
	}
	if request.FrequencyPenalty != nil && *request.FrequencyPenalty != 0 {
		return kimiK3Error(kimiK3FrequencyPenaltyMessage)
	}
	if request.LogProbs != nil && *request.LogProbs {
		return kimiK3Error(kimiK3LogprobsFalseMessage)
	}
	if request.TopLogProbs != nil && (request.LogProbs == nil || !*request.LogProbs) {
		return kimiK3Error(kimiK3TopLogprobsPairMessage)
	}
	if tc, ok := request.ToolChoice.(map[string]any); ok {
		if t, _ := tc["type"].(string); strings.EqualFold(t, "function") {
			if _, hasFn := tc["function"]; hasFn {
				return kimiK3Error(kimiK3ToolChoiceSpecifiedMessage)
			}
		}
	}
	for i := range request.Tools {
		if !validKimiK3FunctionName(request.Tools[i].Function.Name) {
			return kimiK3Error(kimiK3ToolNameMessage)
		}
	}
	if len(request.Messages) == 0 && request.Prefix == nil && request.Suffix == nil {
		return kimiK3Error(kimiK3MessagesEmptyMessage)
	}
	return nil
}

// validKimiK3FunctionName mirrors the official tool-name rule: must start with
// a letter and may contain letters, numbers, underscores and dashes.
func validKimiK3FunctionName(name string) bool {
	if name == "" || !isLetter(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		c := name[i]
		if !isLetter(c) && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func kimiK3Error(message string) error {
	return types.WithOpenAIError(types.OpenAIError{
		Message: message,
		Type:    "invalid_request_error",
		Param:   nil,
		Code:    "invalid_request_error",
	}, http.StatusBadRequest)
}

// IsStrictFitValidationMessage reports whether an error message carries one of
// the official request-validation texts (DeepSeek V4 and Kimi K3). Official
// error bodies do not append gateway request IDs, so callers keep such
// messages verbatim.
func IsStrictFitValidationMessage(message string) bool {
	for _, prefix := range []string{
		deepSeekV4ReasoningEffortMessage,
		deepSeekV4TopPMessage,
		deepSeekV4TemperatureMessage,
		deepSeekV4JsonObjectMessage,
		deepSeekV4TopLogprobsPairMessage,
		deepSeekV4TopLogprobsRangeMessage,
		kimiK3TemperatureMessage,
		kimiK3TopPMessage,
		kimiK3NMessage,
		kimiK3PresencePenaltyMessage,
		kimiK3FrequencyPenaltyMessage,
		kimiK3LogprobsFalseMessage,
		kimiK3TopLogprobsPairMessage,
		kimiK3ToolChoiceSpecifiedMessage,
		kimiK3ToolNameMessage,
		kimiK3MessagesEmptyMessage,
	} {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return false
}

func GetAndValidateGeminiRequest(c *gin.Context) (*dto.GeminiChatRequest, error) {
	request := &dto.GeminiChatRequest{}
	err := common.UnmarshalBodyReusable(c, request)
	if err != nil {
		return nil, err
	}
	if len(request.Contents) == 0 && len(request.Requests) == 0 {
		return nil, errors.New("contents is required")
	}
	if exceedsMaxTokensLimit(request.GenerationConfig.MaxOutputTokens) {
		return nil, errors.New("maxOutputTokens is invalid")
	}

	//if c.Query("alt") == "sse" {
	//	relayInfo.IsStream = true
	//}

	return request, nil
}

func GetAndValidateGeminiEmbeddingRequest(c *gin.Context) (*dto.GeminiEmbeddingRequest, error) {
	request := &dto.GeminiEmbeddingRequest{}
	err := common.UnmarshalBodyReusable(c, request)
	if err != nil {
		return nil, err
	}
	return request, nil
}

func GetAndValidateGeminiBatchEmbeddingRequest(c *gin.Context) (*dto.GeminiBatchEmbeddingRequest, error) {
	request := &dto.GeminiBatchEmbeddingRequest{}
	err := common.UnmarshalBodyReusable(c, request)
	if err != nil {
		return nil, err
	}
	return request, nil
}
