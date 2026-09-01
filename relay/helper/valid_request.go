package helper

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"encoding/json"

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
		if officialFitErr := officialFitEffortTypeError(c, err); officialFitErr != nil {
			return nil, officialFitErr
		}
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
			if err := validateDeepSeekV4ToolCallChain(textRequest); err != nil {
				return nil, err
			}
			if err := validateKimiK3OfficialFields(textRequest); err != nil {
				return nil, err
			}
			if err := validateGlm53OfficialFields(textRequest); err != nil {
				return nil, err
			}
		}
		mapDeepSeekV4ReasoningEffort(textRequest)
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
	deepSeekV4TopPMessage             = "Invalid top_p value, the valid range of top_p is (0, 1.0]"
	deepSeekV4TemperatureMessage      = "Invalid temperature value, the valid range of temperature is [0, 2]"
	deepSeekV4JsonObjectMessage       = "Prompt must contain the word 'json' in some form to use 'response_format' of type 'json_object'."
	deepSeekV4TopLogprobsPairMessage  = "Invalid top_logprobs and logprobs value, logprobs must be set to true if top_logprobs is used."
	deepSeekV4TopLogprobsRangeMessage = "Invalid top_logprobs value, the valid range of top_logprobs is [0, 20]."
	// deepSeekV4MaxTokensRangeMessage mirrors the official wording for a
	// max_tokens above the model limit (384K = 393216, probed live: 393216 is
	// accepted, 393217 returns this exact text).
	deepSeekV4MaxTokensRangeMessage = "Invalid max_tokens value, the valid range of max_tokens is [1, 393216]"
	// deepSeekV4MaxTokensUpperLimit is the official per-request output cap from
	// the model & pricing page (384K).
	deepSeekV4MaxTokensUpperLimit               = 393216
	deepSeekV4StopArrayLimit                    = 16
	deepSeekV4ReasoningEffortDeserMessagePrefix = "Failed to deserialize the JSON body into the target type: reasoning_effort: unknown variant"
	deepSeekV4ReasoningEffortDeserMessageSuffix = ", expected one of `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`"
	// Official tool-call state machine texts (live-probed 2026-09-01). A tool
	// message that responds to nothing gets the orphan text; a tool message
	// whose tool_call_id matches no pending call gets the unanswered-ids text
	// (note the comma after 'tool_call_id'); unanswered pending calls when the
	// conversation moves on or ends get the insufficient text (note the
	// period); a tool message without tool_call_id fails the official
	// deserialization with the message index (the body-specific
	// "at line 1 column N" suffix is dropped per the established convention).
	deepSeekV4OrphanToolMessage         = "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'"
	deepSeekV4UnansweredToolCallIDsText = "An assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id', The following tool_call_ids did not have response messages: "
	deepSeekV4InsufficientToolMsgsText  = "An assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id'. (insufficient tool messages following tool_calls message)"
	deepSeekV4MissingToolCallIDPrefix   = "Failed to deserialize the JSON body into the target type: messages["
	// deepSeekV4ReasoningPassbackText fires when a tool exchange is continued
	// (the last message is not a user turn) but the most recent assistant turn
	// carries no reasoning_content: the thinking-mode docs require passing the
	// chain of thought back for every prior turn once tools are involved
	// (live-probed 2026-09-01; conversations ending on a user turn and
	// tool-free conversations are exempt).
	deepSeekV4ReasoningPassbackText     = "The `reasoning_content` in the thinking mode must be passed back to the API."
	deepSeekV4UnknownModelMessagePrefix = "The supported API model names are deepseek-v4-pro, deepseek-v4-flash, and deepseek-v4-flash-vision-exp, but you passed "
	// deepSeekV4ReasoningEffortTypeErrorMessage mirrors the official wording for
	// a non-string reasoning_effort: the endpoint's own parser fails the type
	// before any enum check. The live response appends " at line 1 column N",
	// which is body-specific and dropped like the serde column suffix.
	deepSeekV4ReasoningEffortTypeErrorMessage = "Failed to parse the request body as JSON: reasoning_effort: expected value"
)

// deepSeekV4ReasoningEffortAllowed mirrors the official serde enum: the
// endpoint deserializes reasoning_effort into one of these variants and
// returns a deserialization error for anything else.
var deepSeekV4ReasoningEffortAllowed = map[string]bool{
	"none": true, "minimal": true, "low": true, "medium": true,
	"high": true, "xhigh": true, "max": true,
}

// deepSeekV4ReasoningEffortDeserMessage renders the official deserialization
// error for an unknown reasoning_effort variant (the gateway drops the
// request-specific "at line 1 column N" suffix the live endpoint appends).
func deepSeekV4ReasoningEffortDeserMessage(effort string) string {
	return deepSeekV4ReasoningEffortDeserMessagePrefix + " `" + effort +
		"`" + deepSeekV4ReasoningEffortDeserMessageSuffix
}

// deepSeekV4StopTooLongPrefix matches the official stop-array overflow text
// before the item count.
const deepSeekV4StopTooLongPrefix = "Stop string array too long: "

// deepSeekV4StopTooLongMessage renders the official error for a stop array
// above the 16-item cap (probed live: "Stop string array too long: 17").
func deepSeekV4StopTooLongMessage(count int) string {
	return deepSeekV4StopTooLongPrefix + fmt.Sprintf("%d", count)
}

// deepSeekV4StopArrayLength reports the item count when stop carries an array
// (JSON arrays decode to []any; []string covers programmatic callers). A bare
// string stop is always within the cap.
func deepSeekV4StopArrayLength(stop any) (int, bool) {
	switch value := stop.(type) {
	case []any:
		return len(value), true
	case []string:
		return len(value), true
	default:
		return 0, false
	}
}

// deepSeekV4TopLogprobsDeserMessage renders the official deserialization
// error for a negative top_logprobs: the wire type is `u8` so the endpoint
// fails before the range check (the [0, 20] range text applies to 21 and up).
func deepSeekV4TopLogprobsDeserMessage(value int) string {
	return fmt.Sprintf("Failed to deserialize the JSON body into the target type: top_logprobs: invalid value: integer `%d`, expected u8", value)
}

// DeepSeekV4OfficialModelNames is the exact model-id list the official
// api.deepseek.com endpoint accepts (from the live unknown-model error text).
var DeepSeekV4OfficialModelNames = []string{
	"deepseek-v4-pro", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp",
}

// IsDeepSeekV4OfficialModelName reports whether model is one of the exact
// official DeepSeek V4 model ids.
func IsDeepSeekV4OfficialModelName(model string) bool {
	for _, n := range DeepSeekV4OfficialModelNames {
		if strings.EqualFold(strings.TrimSpace(model), n) {
			return true
		}
	}
	return false
}

// DeepSeekV4UnknownModelMessage renders the official unknown-model error text.
func DeepSeekV4UnknownModelMessage(model string) string {
	return deepSeekV4UnknownModelMessagePrefix + model + "."
}

// officialFitEffortTypeError maps a JSON type error on reasoning_effort to the
// official per-family 400. The dto field is a Go string, so a numeric/bool
// value fails the decode and would otherwise surface as a gateway 500; the
// official endpoints reject the type with their own wording. Runs only in the
// decode-error path (it re-reads the stored body) and only when the user's
// official-fit profile enables validation for the request's model family —
// non-fit users keep the original decode error.
func officialFitEffortTypeError(c *gin.Context, unmarshalErr error) error {
	if unmarshalErr == nil || !strings.Contains(unmarshalErr.Error(), "reasoning_effort") {
		return nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil
	}
	if _, err := storage.Seek(0, io.SeekStart); err != nil {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(storage).Decode(&raw); err != nil {
		return nil
	}
	modelRaw, ok := raw["model"]
	if !ok || len(modelRaw) == 0 || modelRaw[0] != '"' {
		return nil
	}
	var model string
	if err := json.Unmarshal(modelRaw, &model); err != nil {
		return nil
	}
	effRaw, ok := raw["reasoning_effort"]
	if !ok || len(effRaw) == 0 || effRaw[0] == '"' {
		return nil
	}
	profile, fitEnabled := officialFitProfile(c, model)
	if !fitEnabled || !profile.Validate {
		return nil
	}
	switch {
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "deepseek-v4-"):
		return types.WithOpenAIError(types.OpenAIError{
			Message: deepSeekV4ReasoningEffortTypeErrorMessage,
			Type:    "invalid_request_error",
			Param:   nil,
			Code:    "invalid_request_error",
		}, http.StatusBadRequest)
	case isKimiK3Model(model):
		return kimiK3Error(kimiK3ReasoningEffortTypeMessage)
	case isGlm53Model(model):
		return glm53Error("1210", glm53ReasoningEffortTypeMessage)
	}
	return nil
}

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
	if effort := strings.TrimSpace(request.ReasoningEffort); effort != "" && !deepSeekV4ReasoningEffortAllowed[strings.ToLower(effort)] {
		return types.WithOpenAIError(types.OpenAIError{
			Message: deepSeekV4ReasoningEffortDeserMessage(effort),
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
	if count, isArray := deepSeekV4StopArrayLength(request.Stop); isArray && count > deepSeekV4StopArrayLimit {
		return types.WithOpenAIError(types.OpenAIError{
			Message: deepSeekV4StopTooLongMessage(count),
			Type:    "invalid_request_error",
			Param:   nil,
			Code:    "invalid_request_error",
		}, http.StatusBadRequest)
	}
	if request.MaxTokens != nil && *request.MaxTokens > deepSeekV4MaxTokensUpperLimit {
		return types.WithOpenAIError(types.OpenAIError{
			Message: deepSeekV4MaxTokensRangeMessage,
			Type:    "invalid_request_error",
			Param:   nil,
			Code:    "invalid_request_error",
		}, http.StatusBadRequest)
	}
	return nil
}

// mapDeepSeekV4ReasoningEffort applies the official effort mapping silently
// (docs thinking_mode table: medium and xhigh both map to high) so every
// upstream — including ollama-backed aggregators with a narrower effort enum —
// observes official-equivalent behavior. Only the exact lowercase variants are
// mapped; anything else keeps the deserialization contract of the enum check.
func mapDeepSeekV4ReasoningEffort(request *dto.GeneralOpenAIRequest) {
	if request == nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(request.Model)), "deepseek-v4-") {
		return
	}
	switch strings.TrimSpace(request.ReasoningEffort) {
	case "medium", "xhigh":
		request.ReasoningEffort = "high"
	}
}

// markDeepSeekV4OfficialPin flags deepseek-v4 requests whose parameters are
// known to drive aggregator upstreams into divergent behavior, so they must be
// served by the official channel: the K08 extreme-sampling class, plus explicit
// thinking toggles (aggregators may ignore thinking.type and leak the chain of
// thought the client asked to disable) and logprobs requests (aggregators drop
// the logprobs object entirely; the official response carries content and
// reasoning_content paths).
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
	if len(request.THINKING) > 0 {
		pinned = true
	}
	if request.LogProbs != nil && *request.LogProbs {
		pinned = true
	}
	if pinned {
		common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
	}
}

// validateDeepSeekV4ToolCallChain mirrors the official tool-call state machine
// before any channel is selected, so an invalid conversation fails fast with
// the official 400 instead of burning the retry cascade on aggregators that
// tolerate it (live incident 202609010547405945577378268d9d6IIbIbXcv: official
// and two aggregators rejected the orphan tool message while the ollama
// channel accepted it and ran inference).
//
// Rules, all live-probed against api.deepseek.com:
//   - a tool message with no pending tool_call is an orphan;
//   - a tool message whose tool_call_id matches no pending call lists that id;
//   - pending calls must all be answered before any non-tool message or the
//     end of the conversation;
//   - a tool message without tool_call_id is a deserialization failure;
//   - when the conversation ends on a tool response, the tool-call issuer's
//     reasoning_content must be present (pro/flash only, thinking mode, the
//     vision variant is exempt — see deepSeekV4PassbackExemptModel).
//
// Answering a call consumes it, so a duplicated answer degrades into the
// orphan rule exactly like the official endpoint.
func validateDeepSeekV4ToolCallChain(request *dto.GeneralOpenAIRequest) error {
	if request == nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(request.Model)), "deepseek-v4-") {
		return nil
	}
	pending := map[string]bool{}
	for i := range request.Messages {
		msg := &request.Messages[i]
		switch msg.Role {
		case "assistant":
			if len(pending) > 0 {
				return deepSeekV4ToolChainError(deepSeekV4InsufficientToolMsgsText)
			}
			pending = deepSeekV4ToolCallIDs(msg.ToolCalls)
		case "tool":
			if len(pending) == 0 {
				return deepSeekV4ToolChainError(deepSeekV4OrphanToolMessage)
			}
			id := strings.TrimSpace(msg.ToolCallId)
			if id == "" {
				return deepSeekV4ToolChainError(deepSeekV4MissingToolCallIDPrefix +
					strconv.Itoa(i) + "]: missing field `tool_call_id`")
			}
			if !pending[id] {
				return deepSeekV4ToolChainError(deepSeekV4UnansweredToolCallIDsText + id)
			}
			delete(pending, id)
		default:
			if len(pending) > 0 {
				return deepSeekV4ToolChainError(deepSeekV4InsufficientToolMsgsText)
			}
		}
	}
	if len(pending) > 0 {
		return deepSeekV4ToolChainError(deepSeekV4InsufficientToolMsgsText)
	}
	// Thinking-mode passback (live-probed 2026-09-01): pro and flash reject a
	// request that makes the model continue a tool loop — last message is a
	// tool response — unless the nearest preceding assistant (the tool-call
	// issuer) carries a reasoning_content field. The check is presence-based:
	// empty and blank strings satisfy it, absent/null does not. Ending on a
	// bare assistant or user turn is exempt, and the vision variant never
	// enforces the rule at all, while the state machine above applies to
	// every deepseek-v4-* variant.
	if last := request.Messages[len(request.Messages)-1]; last.Role == "tool" &&
		!deepSeekV4ThinkingOff(request) && !deepSeekV4PassbackExemptModel(request.Model) {
		for i := len(request.Messages) - 1; i >= 0; i-- {
			msg := &request.Messages[i]
			if msg.Role != "assistant" {
				continue
			}
			if msg.ReasoningContent == nil {
				return deepSeekV4ToolChainError(deepSeekV4ReasoningPassbackText)
			}
			break
		}
	}
	return nil
}

// deepSeekV4PassbackExemptModel reports whether the model variant skips the
// thinking-mode reasoning passback rule. Live-probed 2026-09-01:
// deepseek-v4-flash-vision-exp accepts tool-loop continuations without
// reasoning_content while pro and flash reject them.
func deepSeekV4PassbackExemptModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "vision")
}

// deepSeekV4ThinkingOff reports whether the request disables thinking
// (explicit thinking.type=disabled or the effort=none equivalent).
func deepSeekV4ThinkingOff(request *dto.GeneralOpenAIRequest) bool {
	if len(request.THINKING) > 0 {
		var thinking struct {
			Type string `json:"type"`
		}
		if err := common.Unmarshal(request.THINKING, &thinking); err == nil &&
			strings.EqualFold(strings.TrimSpace(thinking.Type), "disabled") {
			return true
		}
	}
	return strings.EqualFold(strings.TrimSpace(request.ReasoningEffort), "none")
}

// deepSeekV4ToolCallIDs extracts the ids an assistant message declares for
// tool calling. Unparsable or absent tool_calls yields no pending calls and
// the upstream sees the request verbatim.
func deepSeekV4ToolCallIDs(raw json.RawMessage) map[string]bool {
	ids := map[string]bool{}
	if len(raw) == 0 {
		return ids
	}
	var calls []struct {
		ID string `json:"id"`
	}
	if err := common.Unmarshal(raw, &calls); err != nil {
		return ids
	}
	for _, call := range calls {
		if id := strings.TrimSpace(call.ID); id != "" {
			ids[id] = true
		}
	}
	return ids
}

// deepSeekV4ToolChainError renders one of the official 400 bodies.
func deepSeekV4ToolChainError(message string) error {
	return types.WithOpenAIError(types.OpenAIError{
		Message: message,
		Type:    "invalid_request_error",
		Param:   nil,
		Code:    "invalid_request_error",
	}, http.StatusBadRequest)
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
	if *request.TopLogProbs < 0 {
		// The official endpoint deserializes top_logprobs as u8, so negatives
		// fail before the range check with the deserialization text.
		return types.WithOpenAIError(types.OpenAIError{
			Message: deepSeekV4TopLogprobsDeserMessage(*request.TopLogProbs),
			Type:    "invalid_request_error",
			Param:   nil,
			Code:    "invalid_request_error",
		}, http.StatusBadRequest)
	}
	if *request.TopLogProbs > 20 {
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
	kimiK3ReasoningEffortTypeMessage = "Invalid request: the `reasoning_effort` field in the request (expected type string) is illegal, and number is not acceptable"
)

// GLM-5.3 official validation texts calibrated 2026-08-28 against the live
// open.bigmodel.cn/api/paas/v4 endpoint. The error wire format differs from
// OpenAI: {"error":{"code":"1210","message":"..."}} (1210=parameter,
// 1214=model/input) with Content-Type application/json. GLM-5.3 accepts
// n, penalties, logprobs, top_logprobs, tool names, specified tool_choice,
// json_object without the "json" keyword and explicit default sampling
// values — those must NEVER be rejected locally.
const (
	glm53ThinkingMessage      = "该模型始终思考，不支持关闭思考；请使用 low、high 或 max。"
	glm53TemperatureMessage   = "temperature参数非法：限制数值范围[0,1]"
	glm53TopPMessage          = "top_p参数非法：限制数值范围[0,1]"
	glm53ModelNotFoundMessage = "modelCode：不存在"
	glm53EmptyMessagesMessage = "输入不能为空"
	// glm53ReasoningEffortTypeMessage mirrors the live Zhipu wording for a
	// non-string reasoning_effort (the numeric probe returned code 1210 with
	// the enum-list text, unlike string values which get the thinking text).
	glm53ReasoningEffortTypeMessage = "reasoning_effort 参数值非法，可选值为：none、minimal、low、medium、high、xhigh、max"
)

func isGlm53Model(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "glm-5.3")
}

// glm53Error wraps a GLM official validation error with its numeric code so
// the controller can render the Zhipu wire shape byte-identically.
func glm53Error(code, message string) error {
	return types.WithOpenAIError(types.OpenAIError{
		Message: message,
		Type:    "invalid_request_error",
		Param:   nil,
		Code:    code,
	}, http.StatusBadRequest)
}

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

// validateGlm53OfficialFields mirrors the live Zhipu glm-5.3 contract: the
// endpoint rejects only excluded thinking, a reasoning_effort outside
// low/high/max (any other string gets the thinking text), temperature/top_p
// outside [0,1], empty messages and unknown model ids. Everything the
// official endpoint tolerates — n, penalties, logprobs, top_logprobs with or
// without logprobs, arbitrary tool names, specified tool_choice, json_object
// without the "json" keyword, explicit default sampling values — must pass.
func validateGlm53OfficialFields(request *dto.GeneralOpenAIRequest) error {
	if request == nil || !isGlm53Model(request.Model) {
		return nil
	}
	if len(request.Messages) == 0 && request.Prefix == nil && request.Suffix == nil {
		return glm53Error("1214", glm53EmptyMessagesMessage)
	}
	if effort := strings.TrimSpace(request.ReasoningEffort); effort != "" &&
		!strings.Contains("|low|high|max|", "|"+strings.ToLower(effort)+"|") {
		return glm53Error("1210", glm53ThinkingMessage)
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 1) {
		return glm53Error("1210", glm53TemperatureMessage)
	}
	if request.TopP != nil && (*request.TopP < 0 || *request.TopP > 1) {
		return glm53Error("1210", glm53TopPMessage)
	}
	if thinking := compileGlmThinking(request.THINKING); thinking != nil {
		if t, _ := thinking["type"].(string); !strings.EqualFold(t, "enabled") {
			return glm53Error("1210", glm53ThinkingMessage)
		}
	}
	return nil
}

// compileGlmThinking decodes the raw thinking field; nil means absent or
// unparseable (the official endpoint ignores malformed types the same way).
func compileGlmThinking(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// IsStrictGlmValidationMessage reports whether an error message carries one
// of the official Zhipu validation texts; the caller renders the Zhipu wire
// format ({error:{code,message}}) for them and keeps them byte-identical.
func IsStrictGlmValidationMessage(message string) bool {
	for _, prefix := range []string{
		glm53ThinkingMessage,
		glm53TemperatureMessage,
		glm53TopPMessage,
		glm53ModelNotFoundMessage,
		glm53EmptyMessagesMessage,
		glm53ReasoningEffortTypeMessage,
	} {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return false
}

// Glm53ModelNotFoundText mirrors the official unknown-model text so the
// distributor can render it without importing package-internal constants.
var Glm53ModelNotFoundText = glm53ModelNotFoundMessage

// Glm53OfficialModelNames is the exact model-id list of the glm-5.3 family
// (glm-5.2 and older are not part of this fit family).
var Glm53OfficialModelNames = []string{"glm-5.3", "glm-5.3-flash"}

// IsGlm53OfficialModelName reports whether model is one of the glm-5.3
// family ids the official endpoint accepts.
func IsGlm53OfficialModelName(model string) bool {
	for _, n := range Glm53OfficialModelNames {
		if strings.EqualFold(strings.TrimSpace(model), n) {
			return true
		}
	}
	return false
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
		deepSeekV4ReasoningEffortDeserMessagePrefix,
		deepSeekV4ReasoningEffortTypeErrorMessage,
		deepSeekV4TopPMessage,
		deepSeekV4TemperatureMessage,
		deepSeekV4JsonObjectMessage,
		deepSeekV4TopLogprobsPairMessage,
		deepSeekV4TopLogprobsRangeMessage,
		"Failed to deserialize the JSON body into the target type: top_logprobs: invalid value: integer",
		deepSeekV4MaxTokensRangeMessage,
		deepSeekV4StopTooLongPrefix,
		deepSeekV4OrphanToolMessage,
		deepSeekV4UnansweredToolCallIDsText,
		deepSeekV4InsufficientToolMsgsText,
		deepSeekV4MissingToolCallIDPrefix,
		deepSeekV4ReasoningPassbackText,
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
		kimiK3ReasoningEffortTypeMessage,
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
