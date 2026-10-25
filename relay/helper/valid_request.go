package helper

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/officialfit"
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

// ExceedsMaxTokensLimit checks token limits before they reach billing arithmetic.
func ExceedsMaxTokensLimit(values ...*uint) bool {
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
	if ExceedsMaxTokensLimit(request.MaxOutputTokens) {
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
			if parameters := formData.Get("parameters"); parameters != "" {
				imageRequest.Extra = map[string]json.RawMessage{"parameters": json.RawMessage(parameters)}
			}
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
			return nil, errors.New("model is required")
		}

		if strings.Contains(imageRequest.Size, "×") {
			return nil, errors.New("size an unexpected error occurred in the parameter, please use 'x' instead of the multiplication sign '×'")
		}

		if imageRequest.N != nil && *imageRequest.N > dto.MaxImageN {
			return nil, fmt.Errorf("n must be an integer between 1 and %d", dto.MaxImageN)
		}

		if err := imageRequest.NormalizeLegacyDalleImageRequest(); err != nil {
			return nil, err
		}
		if imageRequest.Model == "gpt-image-1" {
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

	// Provider parameters can override the top-level count. Validate before
	// pricing so malformed multipliers return a client error, not a pricing
	// failure after reservation has started.
	if raw, exists := imageRequest.Extra["parameters"]; exists {
		parameters := &dto.ImageBillingParameters{}
		if err := common.Unmarshal(raw, parameters); err != nil {
			return nil, fmt.Errorf("invalid image parameters: %w", err)
		}
		imageRequest.BillingParameters = parameters
	}
	if _, err := imageRequest.ImageCount(false); err != nil {
		return nil, err
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
	if ExceedsMaxTokensLimit(textRequest.MaxTokens, textRequest.MaxTokensToSample) {
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

	if ExceedsMaxTokensLimit(textRequest.MaxTokens, textRequest.MaxCompletionTokens) {
		return nil, errors.New("max_tokens is invalid")
	}
	if ExceedsMaxTokensLimit(textRequest.MinTokens) {
		return nil, errors.New("min_tokens is invalid")
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
		// OpenAI's allowed_tools tool_choice is translated into the classic
		// filtered-declarations + string form before the official-fit validators
		// run: the current upstreams ignore the object shape, and mirroring its
		// strictest reading locally would reject a request both upstreams accept
		// (see the helper for the live probe).
		normalizeAllowedToolsToolChoice(textRequest)
		profile, fitEnabled := officialFitProfile(c, textRequest.Model)
		if fitEnabled && profile.Validate {
			// The official deserialization texts end in a location suffix
			// derived from the request bytes; resolve it once per request.
			loc := serdeLocationForRequest(c)
			if err := validateDeepSeekV4OfficialFields(textRequest, loc); err != nil {
				return nil, err
			}
			if err := validateDeepSeekV4Logprobs(textRequest, loc); err != nil {
				return nil, err
			}
			if err := validateDeepSeekV4ToolCallChain(textRequest, loc); err != nil {
				return nil, err
			}
			if err := validateKimiK3OfficialFields(textRequest); err != nil {
				return nil, err
			}
			if err := validateGlm53OfficialFields(textRequest); err != nil {
				return nil, err
			}
			// The GLM contract is stricter than the live Zhipu endpoint for an
			// explicit tool_choice="none": the upstream ignores it and calls
			// the tool anyway, so the declarations are withheld (see the
			// helper for the probe evidence).
			if err := applyGlm53ToolChoiceNone(c, textRequest); err != nil {
				return nil, err
			}
		}
		mapDeepSeekV4ReasoningEffort(textRequest)
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
	deepSeekV4MaxTokensUpperLimit = 393216
	deepSeekV4StopArrayLimit      = 16
	// Live-probed 2026-09-01 (audit r6): the official endpoint also rejects
	// these with their own texts; the penalty ranges surfaced via
	// presence/frequency_penalty 2.5 probes, the rest via dedicated shapes.
	deepSeekV4JSONSchemaMessage         = "This response_format type is unavailable now"
	deepSeekV4ToolChoiceThinkingMessage = "Thinking mode does not support this tool_choice"
	deepSeekV4EmptyMessagesMessage      = "Empty input messages"
	deepSeekV4ImageUnsupportedMessage   = "This model does not support image"
	// deepSeekV4ImageDownloadFailedMarker identifies the official text for a
	// content part whose remote image could not be fetched (live-probed
	// 2026-09-15: flash 400, pro 200 — the model difference is real and
	// per-model). The path carries both indices (`.messages[0].image[0]: ...`),
	// so it is matched by shape rather than by prefix. Recognising it keeps the
	// message, error code and media type byte-identical instead of masking the
	// URL and appending a gateway request id.
	deepSeekV4ImageDownloadFailedMarker = "].image["
	deepSeekV4ImageDownloadFailedText   = ": Failed to download image from "
	// Official role serde enum (live-probed): `developer` is rejected with the
	// standard deserialization text listing five variants, latest_reminder
	// included.
	deepSeekV4RoleDeserMessagePrefix            = "Failed to deserialize the JSON body into the target type: messages["
	deepSeekV4ContentPartDeserPrefix            = "Failed to deserialize the JSON body into the target type: messages["
	deepSeekV4ToolChoiceDeserMessage            = "Failed to deserialize the JSON body into the target type: tool_choice: expected one of `none`, `auto`, `required` or a tool"
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
	deepSeekV4ReasoningPassbackText = "The `reasoning_content` in the thinking mode must be passed back to the API."
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
	case officialfit.FamilyOf(model) == officialfit.FamilyDeepSeekV4:
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

func validateDeepSeekV4OfficialFields(request *dto.GeneralOpenAIRequest, loc *serdeLocation) error {
	if request == nil || officialfit.FamilyOf(request.Model) != officialfit.FamilyDeepSeekV4 {
		return nil
	}
	if effort := strings.TrimSpace(request.ReasoningEffort); effort != "" && !deepSeekV4ReasoningEffortAllowed[strings.ToLower(effort)] {
		return deepSeekV4ValidationError(loc.withLocation(deepSeekV4ReasoningEffortDeserMessage(effort), "reasoning_effort"))
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
	// Penalty ranges, live-probed 2026-09-01 (audit r6): presence 2.5 →
	// `Invalid presence_penalty value, the valid range of presence_penalty is
	// [-2, 2]`, same shape for frequency_penalty.
	if msg := deepSeekV4PenaltyRangeMessage("presence_penalty", request.PresencePenalty); msg != "" {
		return types.WithOpenAIError(types.OpenAIError{
			Message: msg, Type: "invalid_request_error", Param: nil, Code: "invalid_request_error",
		}, http.StatusBadRequest)
	}
	if msg := deepSeekV4PenaltyRangeMessage("frequency_penalty", request.FrequencyPenalty); msg != "" {
		return types.WithOpenAIError(types.OpenAIError{
			Message: msg, Type: "invalid_request_error", Param: nil, Code: "invalid_request_error",
		}, http.StatusBadRequest)
	}
	// json_schema is rejected for every V4 model, vision included (the docs
	// never advertised it; probed 2026-09-01).
	if request.ResponseFormat != nil && request.ResponseFormat.Type == "json_schema" {
		return types.WithOpenAIError(types.OpenAIError{
			Message: deepSeekV4JSONSchemaMessage,
			Type:    "invalid_request_error",
			Param:   nil,
			Code:    "invalid_request_error",
		}, http.StatusBadRequest)
	}
	if err := validateDeepSeekV4ToolChoice(request, loc); err != nil {
		return err
	}
	if err := validateDeepSeekV4Messages(request, loc); err != nil {
		return err
	}
	if err := validateDeepSeekV4Thinking(request, loc); err != nil {
		return err
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

// deepSeekV4PenaltyRangeMessage renders the official out-of-range text for a
// penalty field, or "" when the value is inside [-2, 2] (or absent).
func deepSeekV4PenaltyRangeMessage(field string, value *float64) string {
	if value == nil || (*value >= -2 && *value <= 2) {
		return ""
	}
	return "Invalid " + field + " value, the valid range of " + field + " is [-2, 2]"
}

// validateDeepSeekV4ToolChoice mirrors the official tool_choice contract
// (live-probed 2026-09-01): strings outside the `none`/`auto`/`required` enum
// are a deserialization failure; `required` and the named-object form are
// rejected in thinking mode (anything except thinking disabled / effort none)
// whenever the request actually declares tools — without tools every choice
// is accepted.
func validateDeepSeekV4ToolChoice(request *dto.GeneralOpenAIRequest, loc *serdeLocation) error {
	switch choice := request.ToolChoice.(type) {
	case nil:
		return nil
	case string:
		switch choice {
		case "auto", "none":
			return nil
		case "required":
			if len(request.Tools) > 0 && !deepSeekV4ThinkingOff(request) {
				return deepSeekV4ValidationError(deepSeekV4ToolChoiceThinkingMessage)
			}
			return nil
		default:
			return deepSeekV4ValidationError(loc.withLocation(deepSeekV4ToolChoiceDeserMessage, "tool_choice"))
		}
	case map[string]any:
		if len(request.Tools) > 0 && !deepSeekV4ThinkingOff(request) {
			if kind, ok := choice["type"].(string); ok && strings.EqualFold(kind, "function") {
				return deepSeekV4ValidationError(deepSeekV4ToolChoiceThinkingMessage)
			}
		}
		return nil
	default:
		return nil
	}
}

// validateDeepSeekV4Messages mirrors the official per-message contract: the
// role serde enum accepts exactly system/user/assistant/tool/latest_reminder
// (developer and friends are deserialization failures) and an empty message
// list is `Empty input messages`. Image content parts are accepted on every
// V4 model: live-probed 2026-09-06, official renders them 200 even on
// text-only pro/flash (the pre-2026-09 "This model does not support image"
// 400 no longer exists), so the parts forward to the upstream untouched.
//
// The content-part enum is internally tagged by `type` and admits exactly
// text/image_url/file (live-probed 2026-10-21 against api.deepseek.com): any
// other variant — video_url above all, which multi-modal clients send to every
// model — is a deserialization failure the official endpoint reports as HTTP
// 422, not 400. Rejecting it here keeps the official status and text instead of
// letting the request fall through to channel selection, where the video part
// narrowed the candidate set to nothing and the client received the gateway's
// internal "cannot prove exhaustion" 500.
func validateDeepSeekV4Messages(request *dto.GeneralOpenAIRequest, loc *serdeLocation) error {
	if len(request.Messages) == 0 {
		return deepSeekV4ValidationError(deepSeekV4EmptyMessagesMessage)
	}
	for i := range request.Messages {
		role := request.Messages[i].Role
		switch role {
		case "system", "user", "assistant", "tool", "latest_reminder":
		default:
			message := deepSeekV4RoleDeserMessagePrefix + strconv.Itoa(i) +
				"].role: unknown variant `" + role + "`, expected one of `system`, `user`, `assistant`, `tool`, `latest_reminder`"
			return deepSeekV4ValidationError(loc.withLocation(message, "messages", i, "role"))
		}
		if err := validateDeepSeekV4ContentParts(request.Messages[i], i, loc); err != nil {
			return err
		}
	}
	return nil
}

// deepSeekV4ContentPartTypes is the official content-part enum for the V4
// models. `file` is part of it even though the docs do not advertise it; the
// live endpoint lists it among the expected variants, so it must not be
// rejected here.
var deepSeekV4ContentPartTypes = map[string]bool{
	"text":      true,
	"image_url": true,
	"file":      true,
}

// validateDeepSeekV4ContentParts rejects an unknown content-part type the way
// the official endpoint does: the deserialization text names the *message*
// index (not the part index) and the status is 422.
func validateDeepSeekV4ContentParts(message dto.Message, messageIndex int, loc *serdeLocation) error {
	// The raw array is walked instead of ParseContent(): that helper keeps only
	// the part types the gateway itself understands, so an unknown variant (the
	// thing this rule exists to reject) never survives it. Content is a plain
	// interface here because the request body is decoded generically, but a
	// pre-parsed list is honoured too so the rule cannot be bypassed by another
	// entry point.
	raw, ok := message.Content.([]any)
	if !ok {
		return nil
	}
	for partIndex, item := range raw {
		part, ok := item.(map[string]any)
		if !ok {
			continue
		}
		partType, ok := part["type"].(string)
		if !ok || partType == "" || deepSeekV4ContentPartTypes[partType] {
			continue
		}
		text := deepSeekV4ContentPartDeserPrefix + strconv.Itoa(messageIndex) +
			"]: unknown variant `" + partType + "`, expected one of `text`, `image_url`, `file`"
		return deepSeekV4ValidationError(loc.withVariantLocation(text, messageIndex, partIndex))
	}
	return nil
}

// validateDeepSeekV4Thinking mirrors the official thinking contract
// (live-probed 2026-09-02, buyer round 6): the object's type enum is exactly
// adaptive/enabled/disabled (adaptive behaves like enabled and unknown extra
// fields are ignored), a missing type key and scalar thinking values each get
// their own deserialization text, a non-string type is a plain-text body
// parse failure, and a null thinking value is ignored.
func validateDeepSeekV4Thinking(request *dto.GeneralOpenAIRequest, loc *serdeLocation) error {
	if len(request.THINKING) == 0 {
		return nil
	}
	if string(bytes.TrimSpace(request.THINKING)) == "null" {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(request.THINKING, &fields); err != nil {
		return deepSeekV4ThinkingScalarError(request.THINKING, loc)
	}
	typeRaw, hasType := fields["type"]
	if !hasType {
		return deepSeekV4ValidationError(loc.withContainerLocation("Failed to deserialize the JSON body into the target type: thinking: missing field `type`", "thinking"))
	}
	typeValue := bytes.TrimSpace(typeRaw)
	if len(typeValue) == 0 || typeValue[0] != '"' {
		return deepSeekV4ValidationError(deepSeekV4ThinkingParseExpectedValueText)
	}
	var thinkingType string
	if err := json.Unmarshal(typeValue, &thinkingType); err != nil {
		return deepSeekV4ValidationError(deepSeekV4ThinkingParseExpectedValueText)
	}
	switch thinkingType {
	case "adaptive", "enabled", "disabled":
		return nil
	default:
		message := "Failed to deserialize the JSON body into the target type: thinking.type: unknown variant `" +
			thinkingType + "`, expected one of `adaptive`, `enabled`, `disabled`"
		return deepSeekV4ValidationError(loc.withLocation(message, "thinking", "type"))
	}
}

// deepSeekV4ThinkingParseExpectedValueText is the official plain-text body
// rendered (no JSON error wrapper) when thinking.type is not a string
// (number/bool/object/array; live-probed 2026-09-02).
const deepSeekV4ThinkingParseExpectedValueText = "Failed to parse the request body as JSON: thinking.type: expected value"

// deepSeekV4ThinkingScalarError renders the official deserialization text for
// a scalar thinking value: string/boolean/integer/floating point each get a
// type-specific wording; null never reaches here (accepted upstream of this
// check).
func deepSeekV4ThinkingScalarError(raw json.RawMessage, loc *serdeLocation) error {
	value := bytes.TrimSpace(raw)
	invalid := "Failed to deserialize the JSON body into the target type: thinking: invalid type: "
	message := ""
	switch {
	case len(value) > 0 && value[0] == '"':
		var scalar string
		if err := json.Unmarshal(value, &scalar); err != nil {
			message = invalid + "string, expected struct ThinkingOptions"
			break
		}
		message = invalid + "string \"" + scalar + "\", expected struct ThinkingOptions"
	case string(value) == "true" || string(value) == "false":
		message = invalid + "boolean `" + string(value) + "`, expected struct ThinkingOptions"
	case len(value) > 0 && value[0] == '[':
		message = invalid + "sequence, expected struct ThinkingOptions"
	case len(value) > 0 && value[0] == '{':
		message = invalid + "map, expected struct ThinkingOptions"
	default:
		if strings.ContainsAny(string(value), ".eE") {
			message = invalid + "floating point `" + string(value) + "`, expected struct ThinkingOptions"
		} else {
			message = invalid + "integer `" + string(value) + "`, expected struct ThinkingOptions"
		}
	}
	return deepSeekV4ValidationError(loc.withLocation(message, "thinking"))
}

// (docs thinking_mode table: medium and xhigh both map to high) so every
// upstream — including ollama-backed aggregators with a narrower effort enum —
// observes official-equivalent behavior. Only the exact lowercase variants are
// mapped; anything else keeps the deserialization contract of the enum check.
func mapDeepSeekV4ReasoningEffort(request *dto.GeneralOpenAIRequest) {
	if request == nil || officialfit.FamilyOf(request.Model) != officialfit.FamilyDeepSeekV4 {
		return
	}
	switch strings.TrimSpace(request.ReasoningEffort) {
	case "medium", "xhigh":
		request.ReasoningEffort = "high"
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
func validateDeepSeekV4ToolCallChain(request *dto.GeneralOpenAIRequest, loc *serdeLocation) error {
	if request == nil || officialfit.FamilyOf(request.Model) != officialfit.FamilyDeepSeekV4 {
		return nil
	}
	pending := map[string]bool{}
	for i := range request.Messages {
		msg := &request.Messages[i]
		switch msg.Role {
		case "assistant":
			if len(pending) > 0 {
				return deepSeekV4ValidationError(deepSeekV4InsufficientToolMsgsText)
			}
			pending = deepSeekV4ToolCallIDs(msg.ToolCalls)
		case "tool":
			if len(pending) == 0 {
				return deepSeekV4ValidationError(deepSeekV4OrphanToolMessage)
			}
			id := strings.TrimSpace(msg.ToolCallId)
			if id == "" {
				message := deepSeekV4MissingToolCallIDPrefix +
					strconv.Itoa(i) + "]: missing field `tool_call_id`"
				return deepSeekV4ValidationError(loc.withContainerLocation(message, "messages", i))
			}
			if !pending[id] {
				return deepSeekV4ValidationError(deepSeekV4UnansweredToolCallIDsText + id)
			}
			delete(pending, id)
		default:
			if len(pending) > 0 {
				return deepSeekV4ValidationError(deepSeekV4InsufficientToolMsgsText)
			}
		}
	}
	if len(pending) > 0 {
		return deepSeekV4ValidationError(deepSeekV4InsufficientToolMsgsText)
	}
	// Thinking-mode passback (live-probed 2026-09-01): pro and flash reject a
	// request that makes the model continue a tool loop — last message is a
	// tool response — unless the nearest preceding assistant (the tool-call
	// issuer) carries a reasoning_content field. The check is presence-based:
	// empty and blank strings satisfy it, absent/null does not. Ending on a
	// bare assistant or user turn is exempt, and the vision variant never
	// enforces the rule at all, while the state machine above applies to
	// every deepseek-v4* variant (the v4 and v4.1 lines alike).
	if last := request.Messages[len(request.Messages)-1]; last.Role == "tool" &&
		!deepSeekV4ThinkingOff(request) && !deepSeekV4PassbackExemptModel(request.Model) {
		for i := len(request.Messages) - 1; i >= 0; i-- {
			msg := &request.Messages[i]
			if msg.Role != "assistant" {
				continue
			}
			if msg.ReasoningContent == nil {
				return deepSeekV4ValidationError(deepSeekV4ReasoningPassbackText)
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

// deepSeekV4ValidationError renders an official request-validation body, with
// the status the live endpoint uses for that class (direct-official baseline
// captured against api.deepseek.com on 2026-10-20; re-probed 2026-10-21):
//
//   - "Failed to deserialize the JSON body into the target type: ..." → 422,
//     application/json: the body parsed and a field failed to deserialize
//     (reasoning_effort/thinking/tool_choice enums, content-part variants,
//     message fields);
//   - "Failed to parse the request body as JSON: ..." → 400, bare text: the
//     body itself could not be parsed;
//   - business texts (ranges, orphan tool messages, passback, ...) → 400.
//
// The prefix decides, so a message cannot drift onto the wrong status when a
// caller is added.
func deepSeekV4ValidationError(message string) error {
	status := http.StatusBadRequest
	if strings.HasPrefix(message, "Failed to deserialize the JSON body into the target type") {
		status = http.StatusUnprocessableEntity
	}
	return types.WithOpenAIError(types.OpenAIError{
		Message: message,
		Type:    "invalid_request_error",
		Param:   nil,
		Code:    "invalid_request_error",
	}, status)
}

func validateDeepSeekV4Logprobs(request *dto.GeneralOpenAIRequest, loc *serdeLocation) error {
	if request == nil || officialfit.FamilyOf(request.Model) != officialfit.FamilyDeepSeekV4 || request.TopLogProbs == nil {
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
		// fail before the range check with the deserialization text (422).
		return deepSeekV4ValidationError(loc.withLocation(deepSeekV4TopLogprobsDeserMessage(*request.TopLogProbs), "top_logprobs"))
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

// Kimi K3 official validation texts, re-probed live against api.moonshot.cn on
// 2026-09-21 (the 2026-08-27 set had drifted: `logprobs: only false` and the
// single fixed temperature no longer match the endpoint) and again on
// 2026-09-22 for the thinking state and tool_choice. Facts pinned by those
// probes:
//   - the fixed temperature follows the thinking state: enabled (the default)
//     pins 1.0, thinking off pins 0.6 — two distinct texts;
//   - "thinking off" has two controls: `thinking.type=disabled`, or
//     `reasoning_effort: "none"` when thinking carries no recognized type; an
//     explicit thinking type outranks the effort field in both directions;
//   - `logprobs: true` is ACCEPTED in both states and returns logprobs data;
//     only `top_logprobs > 0` without `logprobs: true` is rejected;
//   - tool_choice's "specified" class is gated on the thinking state: while
//     thinking is on, both the named-function object and a string outside the
//     enum answer the "specified" text; with thinking off the object is
//     accepted and really forces the call, while such a string answers the
//     "unknown tool choice strategy" text naming the value;
//   - `required` needs at least one tool (request-level or dynamic) in both
//     thinking states, and that check comes before the thinking-state one;
//   - the tool-call chain has its own state machine and texts;
//   - dynamic tools, response_format and empty messages have their own texts.
//
// Everything else the endpoint tolerates (reasoning_effort strings, the K2.x
// thinking field, max_completion_tokens, strict=false tools, missing tool
// parameters) must pass through unvalidated.
const (
	kimiK3TemperatureThinkingMessage = "invalid temperature: only 1 is allowed for this model"
	kimiK3TemperatureDisabledMessage = "invalid temperature: only 0.6 is allowed for this model"
	kimiK3TopPMessage                = "invalid top_p: only 0.95 is allowed for this model"
	kimiK3NMessage                   = "invalid n: only 1 is allowed for this model"
	kimiK3PresencePenaltyMessage     = "invalid presence_penalty: only 0 is allowed for this model"
	kimiK3FrequencyPenaltyMessage    = "invalid frequency_penalty: only 0 is allowed for this model"
	kimiK3TopLogprobsPairMessage     = "Invalid request: logprobs must be set to true if top_logprobs is used"
	// Stop-sequence contract, live-probed against the official endpoint on
	// 2026-10-25: `stop` is a string or an array of strings (null and [] mean no
	// stop); at most 5 sequences, each at most 32 BYTES (11 CJK characters are 33
	// bytes and are rejected); a null item inside the array is tolerated. The
	// three rejection classes are deserialization-class for a wrong JSON type and
	// semantic for the two caps. Ordering matters: the endpoint answers a bad
	// temperature before any stop class, so the stop checks run last.
	kimiK3StopArrayTooLongPrefix     = "Invalid request: stop array too long. Expected an array with maximum length 5, but got an array with length "
	kimiK3StopArrayTooLongSuffix     = " instead"
	kimiK3StopSequenceTooLongPrefix  = "Invalid request: stop sequence must not be longer than 32, but got "
	kimiK3StopSequenceTooLongSuffix  = " instead"
	kimiK3StopItemTypePrefix         = "Invalid request: the `stop` field in the request (expected type string) is illegal, and "
	kimiK3StopFieldTypePrefix        = "Invalid request: the `stop` field in the request (expected type []string) is illegal, and "
	kimiK3StopTypeSuffix             = " is not acceptable"
	kimiK3ToolChoiceSpecifiedMessage = "tool_choice 'specified' is incompatible with thinking enabled"
	kimiK3ToolChoiceRequiredMessage  = "Invalid request: tool_choice 'required' requires at least one tool"
	// The unknown-strategy text interrupts the value with the endpoint's own
	// (slightly off) list of supported strategies; mirrored verbatim.
	kimiK3UnknownToolChoicePrefix    = "Invalid request: unknown tool choice strategy: "
	kimiK3UnknownToolChoiceSuffix    = ", currently only auto and none are supported"
	kimiK3ToolNameMessage            = "Invalid request: function name is invalid, must start with a letter and can contain letters, numbers, underscores, and dashes"
	kimiK3MessagesEmptyMessage       = "Invalid request: messages must not be empty"
	kimiK3ReasoningEffortTypeMessage = "Invalid request: the `reasoning_effort` field in the request (expected type string) is illegal, and number is not acceptable"
	// Tool-call chain. The not-found text carries two spaces after
	// `tool_call_id`, exactly as the official endpoint renders it.
	kimiK3ToolCallIDNotFoundMessage = "Invalid request: tool_call_id  is not found"
	kimiK3UnansweredToolCallsPrefix = "Invalid request: an assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id'. The following tool_call_ids did not have response messages: "
	kimiK3DuplicateToolCallIDPrefix = "Invalid request: tool call id "
	kimiK3DuplicateToolCallIDSuffix = " is duplicated"
	// kimiK3TokenizationFailedMessage is what the official endpoint answers
	// when the same pending call is answered twice.
	kimiK3TokenizationFailedMessage = "Invalid request: tokenization failed"
	// Dynamic tools (message-level declarations).
	kimiK3MessageToolsPositionPrefix = "Invalid request: message tools at position "
	kimiK3MessageToolsRoleSuffix     = " must be used with role 'system'"
	kimiK3MessageToolsContentSuffix  = " cannot be used with content"
	kimiK3MessageToolsInvalidSuffix  = " is invalid: "
	kimiK3MessageToolsTypePrefix     = "Invalid request: the `messages.tools` field in the request (expected type []object) is illegal, and "
	kimiK3FunctionNameInvalidText    = "function name is invalid, must start with a letter and can contain letters, numbers, underscores, and dashes"
	kimiK3FunctionNameDuplicateText  = "function name %s is duplicated"
	kimiK3UnknownToolTypePrefix      = "unknown tool type: "
	kimiK3UnknownToolTypeSuffix      = ", currently only function and plugin are supported"
	kimiK3DuplicateToolNamePrefix    = "Invalid request: duplicate tool name "
	kimiK3DuplicateBetweenMessages   = " between message tools at positions "
	kimiK3DuplicateWithGlobalTools   = " between request-level tools and message tools at message position "
	// response_format.
	kimiK3ResponseFormatTypeMessage       = "Invalid request: response_format.type must be one of 'text', 'json_object', 'json_schema'"
	kimiK3ResponseFormatMissingSchemaName = "Invalid request: missing required parameter: 'response_format.json_schema.name'"
	kimiK3ResponseFormatEmptySchema       = "Invalid request: schema must not be empty"
	kimiK3ResponseFormatMissingSchema     = "Invalid request: missing required parameter: 'response_format.json_schema'"
	kimiK3ResponseFormatStrictTypePrefix  = "Invalid request: the `response_format.json_schema.strict` field in the request (expected type bool) is illegal, and "
	kimiK3ResponseFormatSchemaTypePrefix  = "Invalid request: the `response_format.json_schema.schema` field in the request (expected type dict[string,interface]) is illegal, and "
)

// GLM-5.3 official validation texts calibrated 2026-08-28 against the live
// open.bigmodel.cn/api/paas/v4 endpoint. The error wire format differs from
// OpenAI: {"error":{"code":"1210","message":"..."}} (1210=parameter,
// 1211=unknown model, 1214=input) with NO Content-Type header. GLM-5.3 accepts
// n, penalties, logprobs, top_logprobs, tool names, specified tool_choice,
// json_object without the "json" keyword and explicit default sampling
// values — those must NEVER be rejected locally.
//
// Two values drifted after the 2026-08-28 calibration and were re-measured
// live on 2026-09-15 (MEMORY.md family contracts + glm-v53 coverageNotes):
//   - unknown model: was "1214 / modelCode：不存在", now "1211 /
//     模型不存在，请检查模型代码。";
//   - business 400 media type: was application/json; charset=UTF-8, now the
//     endpoint sends no Content-Type at all.
const (
	glm53ThinkingMessage      = "该模型始终思考，不支持关闭思考；请使用 low、high 或 max。"
	glm53TemperatureMessage   = "temperature参数非法：限制数值范围[0,1]"
	glm53TopPMessage          = "top_p参数非法：限制数值范围[0,1]"
	glm53ModelNotFoundMessage = "模型不存在，请检查模型代码。"
	glm53EmptyMessagesMessage = "输入不能为空"
	// glm53ReasoningEffortTypeMessage mirrors the live Zhipu wording for a
	// non-string reasoning_effort (the numeric probe returned code 1210 with
	// the enum-list text, unlike string values which get the thinking text).
	glm53ReasoningEffortTypeMessage = "reasoning_effort 参数值非法，可选值为：none、minimal、low、medium、high、xhigh、max"
	// glm53MaxTokensRangeMessage mirrors the official wording for a max_tokens
	// outside the [1, 131072] model limit (128K = 131072, live-probed 2026-10-19
	// against open.bigmodel.cn/api/paas/v4: 131072 is accepted with 200; 0 and
	// 131073 return HTTP 400 with code 1210 and this message).
	glm53MaxTokensRangeMessage = "max_tokens参数非法：限制数值范围[1,131072]"
	glm53MaxTokensUpperLimit   = 131072
)

func isGlm53Model(model string) bool {
	return officialfit.FamilyOf(model) == officialfit.FamilyGlm53
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
	return officialfit.FamilyOf(model) == officialfit.FamilyKimiK3
}

// validateKimiK3OfficialFields mirrors the official Moonshot kimi-k3 request
// contract as re-probed live on 2026-09-21. The validator is deliberately
// narrow: every rule below was observed on the endpoint, and the fields it
// does not inspect (reasoning_effort strings, the K2.x thinking field,
// max_completion_tokens, tool parameters, strict=false) are tolerated there.
//
// kimiK3TemperatureAllowedWhileThinking reports whether a temperature is one
// the endpoint accepts with thinking enabled. Re-probed live 2026-10-16
// through the official upstream (same key pinned to the official channel):
// 1.0 is accepted, while 0.0, 0.6 and 1.1 are each rejected with
// kimiK3TemperatureThinkingMessage. The upstream's own vendor verifier instead
// requires 0.0 to be accepted, which the live endpoint does not honour -
// following the verifier here made the gateway answer 200 where the official
// endpoint answers 400 (cdp suite case K3-021), so the live contract wins.
func kimiK3TemperatureAllowedWhileThinking(value float64) bool {
	return math.Abs(value-1.0) <= 1e-9
}

func validateKimiK3OfficialFields(request *dto.GeneralOpenAIRequest) error {
	if request == nil || !isKimiK3Model(request.Model) {
		return nil
	}
	thinkingEnabled := kimiK3ThinkingEnabled(request)
	if request.Temperature != nil {
		// The endpoint pins one value per thinking state: 1.0 while thinking, 0.6 alone when
		// it is off (re-probed live 2026-10-16). Each rejection text names that same value,
		// which is the endpoint's own wording; the vendor verifier's additional 0.0
		// expectation is not part of the live contract.
		if thinkingEnabled && !kimiK3TemperatureAllowedWhileThinking(*request.Temperature) {
			return kimiK3Error(kimiK3TemperatureThinkingMessage)
		}
		if !thinkingEnabled && math.Abs(*request.Temperature-0.6) > 1e-9 {
			return kimiK3Error(kimiK3TemperatureDisabledMessage)
		}
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
	// logprobs=true is allowed (and returns logprobs); only a positive
	// top_logprobs without it is rejected.
	if request.TopLogProbs != nil && *request.TopLogProbs > 0 && (request.LogProbs == nil || !*request.LogProbs) {
		return kimiK3Error(kimiK3TopLogprobsPairMessage)
	}
	if err := validateKimiK3ToolChoice(request); err != nil {
		return err
	}
	if err := validateKimiK3DynamicTools(request); err != nil {
		return err
	}
	if err := validateKimiK3ToolCallChain(request); err != nil {
		return err
	}
	if err := validateKimiK3ResponseFormat(request); err != nil {
		return err
	}
	if err := validateKimiK3Stop(request.Stop); err != nil {
		return err
	}
	if len(request.Messages) == 0 && request.Prefix == nil && request.Suffix == nil {
		return kimiK3Error(kimiK3MessagesEmptyMessage)
	}
	return nil
}

// validateKimiK3Stop reproduces the official K3 stop contract (live-probed
// 2026-10-25; see the constants for the probe facts). A nil stop and an empty
// array both mean "no stop". An array is type-checked before its length and the
// per-sequence byte cap, which is the order the endpoint answers in: a 6-item
// array of numbers reports the item type, not the array length. Null items are
// tolerated both by the type pass and by the byte pass.
func validateKimiK3Stop(stop any) error {
	switch value := stop.(type) {
	case nil:
		return nil
	case string:
		return kimiK3StopSequenceCap(value)
	case []any:
		for _, item := range value {
			if item == nil {
				continue
			}
			if _, ok := item.(string); !ok {
				return kimiK3Error(kimiK3StopItemTypePrefix + kimiK3StopJSONType(item) + kimiK3StopTypeSuffix)
			}
		}
		if len(value) > 5 {
			return kimiK3Error(kimiK3StopArrayTooLongPrefix + fmt.Sprintf("%d", len(value)) + kimiK3StopArrayTooLongSuffix)
		}
		for _, item := range value {
			if item == nil {
				continue
			}
			if err := kimiK3StopSequenceCap(item.(string)); err != nil {
				return err
			}
		}
		return nil
	default:
		return kimiK3Error(kimiK3StopFieldTypePrefix + kimiK3StopJSONType(stop) + kimiK3StopTypeSuffix)
	}
}

// kimiK3StopSequenceCap applies the endpoint's 32-byte cap to one sequence.
func kimiK3StopSequenceCap(sequence string) error {
	if length := len(sequence); length > 32 {
		return kimiK3Error(kimiK3StopSequenceTooLongPrefix + fmt.Sprintf("%d", length) + kimiK3StopSequenceTooLongSuffix)
	}
	return nil
}

// kimiK3StopJSONType spells a decoded JSON value's type the way the endpoint's
// deserialization rejection names it (its Rust serde names: "bool", "number",
// "object", "array").
func kimiK3StopJSONType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "number"
	}
}

// kimiK3ThinkingEnabled reports whether the request runs in thinking mode.
// Official K3 defaults to enabled, and two fields can turn it off: a
// thinking.type of disabled, or reasoning_effort "none". A recognized
// thinking.type outranks the effort field in both directions (live-probed
// 2026-09-22: thinking "enabled" with effort "none" stays enabled, and thinking
// "disabled" with effort "high" stays disabled), so the effort field only
// decides when thinking carries no usable type — absent, null, an empty object,
// or a value that is not an object at all.
//
// Matching is case-insensitive and trims nothing around reasoning_effort (live
// probe: effort "NONE" flips the state, " none " does not) but does trim around
// the thinking type. That trim is the one deliberate divergence: the endpoint
// answers a padded type with its own deserialization-class rejection
// (`invalid value for thinking type: "  disabled  "`), which this predicate does
// not reproduce — a padded value is classified as the disabled state here and
// the request reaches the upstream, which still rejects it. Both paths fail the
// request, so the divergence costs a message text on malformed input, never a
// wrong outcome.
func kimiK3ThinkingEnabled(request *dto.GeneralOpenAIRequest) bool {
	if request == nil {
		return true
	}
	if raw := request.THINKING; len(raw) > 0 {
		var thinking map[string]any
		if err := json.Unmarshal(raw, &thinking); err == nil {
			if t, ok := thinking["type"].(string); ok {
				return !strings.EqualFold(strings.TrimSpace(t), "disabled")
			}
		}
	}
	return !strings.EqualFold(request.ReasoningEffort, "none")
}

// validateKimiK3ToolChoice mirrors the official tool_choice contract
// (live-probed 2026-09-22). `required` demands at least one tool — request-level
// or dynamic — in either thinking state, and that check comes first: with a tool
// declared, `required` is accepted even while thinking (unlike DeepSeek V4,
// which rejects it there). The "specified" class is gated on the thinking state.
// While thinking is off, the named-function object is legal and really forces
// the call (a name that is not declared gets its own upstream "not found" 400),
// and a string outside auto/none/required answers the "unknown tool choice
// strategy" text naming the value. While thinking is on, both shapes answer the
// "specified" text, whose wording is what ties the rejection to that state.
func validateKimiK3ToolChoice(request *dto.GeneralOpenAIRequest) error {
	thinkingEnabled := kimiK3ThinkingEnabled(request)
	switch choice := request.ToolChoice.(type) {
	case nil:
		return nil
	case string:
		switch choice {
		case "":
			// The endpoint treats an empty string as absent.
			return nil
		case "auto", "none":
			return nil
		case "required":
			if len(request.Tools) == 0 && kimiK3DynamicToolCount(request) == 0 {
				return kimiK3Error(kimiK3ToolChoiceRequiredMessage)
			}
			return nil
		default:
			if thinkingEnabled {
				return kimiK3Error(kimiK3ToolChoiceSpecifiedMessage)
			}
			return kimiK3Error(kimiK3UnknownToolChoicePrefix + choice + kimiK3UnknownToolChoiceSuffix)
		}
	case map[string]any:
		if thinkingEnabled {
			return kimiK3Error(kimiK3ToolChoiceSpecifiedMessage)
		}
		return nil
	default:
		// Non-string, non-object shapes are the deserialization class; the
		// "specified" text is the closest observed answer and is what the
		// endpoint renders for every other unexpected shape, so they keep it.
		return kimiK3Error(kimiK3ToolChoiceSpecifiedMessage)
	}
}

// kimiK3DynamicToolCount counts tools declared on messages, which participate
// in tool_choice and duplicate-name checks exactly like request-level tools.
func kimiK3DynamicToolCount(request *dto.GeneralOpenAIRequest) int {
	count := 0
	for i := range request.Messages {
		if len(request.Messages[i].Tools) == 0 {
			continue
		}
		var tools []dto.ToolCallRequest
		if err := json.Unmarshal(request.Messages[i].Tools, &tools); err == nil {
			count += len(tools)
		}
	}
	return count
}

// validateKimiK3DynamicTools mirrors the dynamic-tool loading contract: tools
// may only appear on a system message whose content is empty, their names must
// satisfy the same rule as request-level tools, the type must be function (or
// plugin), and no name may repeat within or across declarations — including
// against the request-level tools.
func validateKimiK3DynamicTools(request *dto.GeneralOpenAIRequest) error {
	seenByMessage := make(map[string]int)
	for i := range request.Messages {
		message := &request.Messages[i]
		if len(message.Tools) == 0 {
			continue
		}
		var tools []map[string]any
		if err := json.Unmarshal(message.Tools, &tools); err != nil {
			// The declaration is not an array of objects. Official names the
			// offending JSON type; unknown types fall through to the upstream.
			if typeName, ok := kimiK3JSONTypeName(message.Tools); ok {
				return kimiK3Error(kimiK3MessageToolsTypePrefix + typeName + " is not acceptable")
			}
			return nil
		}
		if !strings.EqualFold(message.Role, "system") {
			return kimiK3Error(kimiK3MessageToolsPositionPrefix + strconv.Itoa(i) + kimiK3MessageToolsRoleSuffix)
		}
		if content, ok := message.Content.(string); ok && strings.TrimSpace(content) != "" {
			return kimiK3Error(kimiK3MessageToolsPositionPrefix + strconv.Itoa(i) + kimiK3MessageToolsContentSuffix)
		}
		namesInMessage := make(map[string]bool)
		for _, entry := range tools {
			toolType, _ := entry["type"].(string)
			function, _ := entry["function"].(map[string]any)
			name := ""
			if function != nil {
				name, _ = function["name"].(string)
			}
			if toolType != "function" && toolType != "plugin" {
				return kimiK3Error(
					kimiK3MessageToolsPositionPrefix + strconv.Itoa(i) + kimiK3MessageToolsInvalidSuffix +
						kimiK3UnknownToolTypePrefix + toolType + kimiK3UnknownToolTypeSuffix,
				)
			}
			if !validKimiK3FunctionName(name) {
				return kimiK3Error(
					kimiK3MessageToolsPositionPrefix + strconv.Itoa(i) + kimiK3MessageToolsInvalidSuffix + kimiK3FunctionNameInvalidText,
				)
			}
			if namesInMessage[name] {
				return kimiK3Error(
					kimiK3MessageToolsPositionPrefix + strconv.Itoa(i) + kimiK3MessageToolsInvalidSuffix +
						fmt.Sprintf(kimiK3FunctionNameDuplicateText, name),
				)
			}
			namesInMessage[name] = true
		}
		for name := range namesInMessage {
			if first, ok := seenByMessage[name]; ok {
				return kimiK3Error(kimiK3DuplicateToolNamePrefix + name + kimiK3DuplicateBetweenMessages + strconv.Itoa(first) + " and " + strconv.Itoa(i))
			}
			seenByMessage[name] = i
		}
	}
	for i := range request.Tools {
		name := request.Tools[i].Function.Name
		if !validKimiK3FunctionName(name) {
			return kimiK3Error(kimiK3ToolNameMessage)
		}
		if position, ok := seenByMessage[name]; ok {
			return kimiK3Error(kimiK3DuplicateToolNamePrefix + name + kimiK3DuplicateWithGlobalTools + strconv.Itoa(position))
		}
	}
	return nil
}

// kimiK3ToolCallRef is one declared tool call, in declaration order.
type kimiK3ToolCallRef struct {
	id   string
	rank int
}

// validateKimiK3ToolCallChain mirrors the official tool-call state machine,
// which is sequential (live-probed 2026-09-21 across eight shapes):
//   - ids must not repeat within one assistant message;
//   - a following tool message must name an id that is currently pending —
//     an unknown id, a missing id and an orphan tool message all answer with
//     the not-found text;
//   - answering the same pending id twice fails tokenization;
//   - any other message (including the end of the conversation) while calls
//     are still pending reports them as unanswered, labelled
//     `<function name>:<index>` in declaration order;
//   - re-declaring an id in a later assistant message is allowed.
func validateKimiK3ToolCallChain(request *dto.GeneralOpenAIRequest) error {
	var (
		pending  []kimiK3DeclaredCall
		consumed map[string]bool
	)
	unanswered := func() error {
		labels := make([]string, 0, len(pending))
		for _, call := range pending {
			labels = append(labels, call.name+":"+strconv.Itoa(call.rank))
		}
		return kimiK3Error(kimiK3UnansweredToolCallsPrefix + strings.Join(labels, ", "))
	}
	for i := range request.Messages {
		message := &request.Messages[i]
		switch strings.ToLower(message.Role) {
		case "assistant":
			if len(pending) > 0 {
				return unanswered()
			}
			calls, err := kimiK3DeclaredToolCalls(message.ToolCalls)
			if err != nil {
				return nil
			}
			seen := make(map[string]bool)
			for _, call := range calls {
				if seen[call.id] {
					return kimiK3Error(kimiK3DuplicateToolCallIDPrefix + call.id + kimiK3DuplicateToolCallIDSuffix)
				}
				seen[call.id] = true
			}
			pending = calls
			consumed = make(map[string]bool)
		case "tool":
			id := strings.TrimSpace(message.ToolCallId)
			index := -1
			for i, call := range pending {
				if call.id == id && id != "" {
					index = i
					break
				}
			}
			if index < 0 {
				if consumed[id] {
					// A second answer to the same call fails the official
					// tokenizer rather than the chain check.
					return kimiK3Error(kimiK3TokenizationFailedMessage)
				}
				return kimiK3Error(kimiK3ToolCallIDNotFoundMessage)
			}
			consumed[id] = true
			pending = append(pending[:index], pending[index+1:]...)
		default:
			if len(pending) > 0 {
				return unanswered()
			}
		}
	}
	if len(pending) > 0 {
		return unanswered()
	}
	return nil
}

// kimiK3JSONTypeName names the JSON type of a raw value the way the official
// deserializer reports it in a type-mismatch text. ok=false for shapes the
// gateway does not know how to name, which leaves the request to the upstream.
func kimiK3JSONTypeName(raw json.RawMessage) (string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "", false
	}
	switch trimmed[0] {
	case '{':
		return "object", true
	case '"':
		return "string", true
	case 't', 'f':
		return "boolean", true
	case 'n':
		return "null", true
	default:
		return "", false
	}
}

// kimiK3DeclaredCall is one tool call with the official <name>:<index> label
// parts. er=false is ignored by the caller: a malformed declaration is left
// for the upstream to judge.
type kimiK3DeclaredCall struct {
	id   string
	name string
	rank int
}

func kimiK3DeclaredToolCalls(raw json.RawMessage) ([]kimiK3DeclaredCall, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var calls []map[string]any
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil, err
	}
	declared := make([]kimiK3DeclaredCall, 0, len(calls))
	for rank, entry := range calls {
		id, _ := entry["id"].(string)
		name := ""
		if function, ok := entry["function"].(map[string]any); ok {
			name, _ = function["name"].(string)
		}
		declared = append(declared, kimiK3DeclaredCall{id: id, name: name, rank: rank})
	}
	return declared, nil
}

// validateKimiK3ResponseFormat mirrors the response_format contract: the type
// must be one of the three documented values, json_schema requires a name and
// an object schema (strict, when present, must be a boolean).
func validateKimiK3ResponseFormat(request *dto.GeneralOpenAIRequest) error {
	format := request.ResponseFormat
	if format == nil {
		return nil
	}
	switch format.Type {
	case "", "text", "json_object":
		return nil
	case "json_schema":
	default:
		return kimiK3Error(kimiK3ResponseFormatTypeMessage)
	}
	if len(format.JsonSchema) == 0 {
		return kimiK3Error(kimiK3ResponseFormatMissingSchema)
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(format.JsonSchema, &schema); err != nil {
		return kimiK3Error(kimiK3ResponseFormatMissingSchema)
	}
	if name, ok := schema["name"]; !ok || !isNonEmptyJSONString(name) {
		return kimiK3Error(kimiK3ResponseFormatMissingSchemaName)
	}
	rawSchema, ok := schema["schema"]
	if !ok || !isNonEmptyJSONObject(rawSchema) {
		return kimiK3Error(kimiK3ResponseFormatEmptySchema)
	}
	if strict, ok := schema["strict"]; ok {
		switch strings.TrimSpace(string(strict)) {
		case "true", "false":
		default:
			return kimiK3Error(kimiK3ResponseFormatStrictTypePrefix + strings.TrimSpace(string(strict)) + " is not acceptable")
		}
	}
	return nil
}

// isNonEmptyJSONObject reports whether raw is a JSON object with at least one
// member.
func isNonEmptyJSONObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return len(trimmed) > 2 && trimmed[0] == '{'
}

// isNonEmptyJSONString reports whether raw is a JSON string with non-empty
// content ("" and whitespace-only count as empty). Mirrors the predicate the
// DeepSeek fit layer uses; the helper package cannot import that one without a
// cycle.
func isNonEmptyJSONString(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return len(trimmed) > 2 && trimmed[0] == '"'
}

// WriteZhipuBusinessError renders a Zhipu business rejection the way the live
// official endpoint does: the body is {"error":{"code","message"}} and the
// response carries NO Content-Type header at all (live-measured 2026-09-15 —
// MEMORY.md's family-contract table and the glm-v53 suite's `absent` media
// class; the 2026-08-28 calibration still had application/json;
// charset=UTF-8, which is the drift this writer retires).
//
// Setting the header entry to nil is deliberate: it both skips gin's render
// helpers (which would set a JSON type) and suppresses net/http's content
// sniffing, so the wire really has no Content-Type. Used by the glm-5.3
// unknown-model rejection in the distributor and by the strict-fit validation
// branch in the relay controller.
func WriteZhipuBusinessError(c *gin.Context, status int, code, message string) {
	body, marshalErr := common.Marshal(gin.H{"error": gin.H{"code": code, "message": message}})
	if marshalErr != nil {
		// Unreachable for two string fields; keep the response honest rather
		// than panicking on a marshal failure.
		body = []byte(`{"error":{"code":"1210","message":"invalid request"}}`)
	}
	c.Writer.Header()["Content-Type"] = nil
	c.Writer.WriteHeader(status)
	_, _ = c.Writer.Write(body)
	c.Abort()
}

// validateGlm53OfficialFields mirrors the live Zhipu glm-5.3 contract: the
// endpoint rejects only excluded thinking, a reasoning_effort outside
// low/high/max (any other string gets the thinking text), empty messages and
// unknown model ids. Everything the official endpoint tolerates — n,
// penalties, logprobs, top_logprobs with or without logprobs, arbitrary tool
// names, specified tool_choice, json_object without the "json" keyword,
// explicit default sampling values, and (since the 2026-09-15 live re-check)
// temperature/top_p outside [0,1] — must pass. The customer contract adds the
// orphan-tool rule below as a deliberate exception to upstream tolerance.
//
// The sampling-range rejection was retired on 2026-10-12: the 2026-08-28
// calibration had the endpoint answering 400 for temperature 2.5/-0.1/1.2 and
// top_p 1.5, but the live endpoint has accepted them since 2026-09-15 (recorded
// in MEMORY.md and in the glm-v53 suite's coverageNotes, whose cases
// GLM-022/023/024/026 expect 200). Keeping the check made fit mode *stricter*
// than the official endpoint and produced four avoidable SEVERE diffs; a
// 30-second A/B on 2026-10-12 (validate off, everything else on) confirmed the
// official channel answers 200 for those values.
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
	if thinking := compileGlmThinking(request.THINKING); thinking != nil {
		if t, _ := thinking["type"].(string); !strings.EqualFold(t, "enabled") {
			return glm53Error("1210", glm53ThinkingMessage)
		}
	}
	// Validate the value the adaptor actually forwards, not one field: the Zhipu
	// conversion forwards request.GetMaxTokens(), which prefers
	// max_completion_tokens over max_tokens (relay/channel/zhipu_4v/relay-zhipu_v4.go:72-76).
	// Checking max_tokens alone let {"max_completion_tokens": 131073} through to
	// the upstream, whose 400 was then retried and answered 200 by the fallback
	// channel — the exact failover this rule exists to prevent.
	if request.MaxTokens != nil || request.MaxCompletionTokens != nil {
		if effective := request.GetMaxTokens(); effective == 0 || effective > glm53MaxTokensUpperLimit {
			return glm53Error("1210", glm53MaxTokensRangeMessage)
		}
	}
	if glm53OrphanToolMessage(request.Messages) {
		// 1214 is the family's generic invalid-request code (the same one the
		// empty-messages rule uses); there is no Zhipu code for this class
		// because Zhipu never rejects it — see glm53OrphanToolMessageText.
		return glm53Error("1214", glm53OrphanToolMessageText)
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

// glm53OrphanToolMessageText is the tool-call state-machine text the glm-5.3
// family emits for a `tool` turn that answers nothing. There is no Zhipu text
// to mirror: live-probed 2026-10-12 through both the official channel (7) and
// the aggregator channel (78), the Zhipu endpoint ACCEPTS an orphan tool
// message with HTTP 200 and runs inference — which is exactly what the
// customer instruction-check "孤儿 tool 消息校验" fails. The buyer demanded
// gateway-side state-machine enforcement for the DeepSeek family on 2026-09-01
// (see validateDeepSeekV4ToolCallChain); the same requirement now covers
// glm-5.3, so this family emits the canonical sentence too. Only the orphan
// rule is enforced, including a second response to an already answered id.
// Unknown/missing response ids and calls left unanswered at the end of an
// exchange remain upstream concerns; they must not consume another call and
// make its subsequent valid response look like an orphan.
const glm53OrphanToolMessageText = "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'"

// glm53OrphanToolMessage reports whether the conversation carries a `tool` turn
// with no pending assistant tool_calls declaration to answer.
func glm53OrphanToolMessage(messages []dto.Message) bool {
	var pending, answered map[string]bool
	for i := range messages {
		msg := &messages[i]
		switch msg.Role {
		case "assistant":
			// The decoder is family-neutral (unparsable or absent tool_calls
			// yields no pending calls); reuse it instead of duplicating it.
			pending = deepSeekV4ToolCallIDs(msg.ToolCalls)
			answered = make(map[string]bool)
		case "tool":
			id := strings.TrimSpace(msg.ToolCallId)
			if len(pending) == 0 || answered[id] {
				return true
			}
			if pending[id] {
				delete(pending, id)
				answered[id] = true
			}
		default:
			// A user/system turn closes any open tool exchange, so a `tool`
			// message after it answers nothing.
			pending, answered = nil, nil
		}
	}
	return false
}

// applyGlm53ToolChoiceNone honours an explicit tool_choice="none" the upstream
// will not. Live-probed 2026-10-12: the official Zhipu channel (7) and the
// aggregator channel (78) both accept tool_choice="none" and still answer with
// a tool call, so the customer's "tool_choice=none 遵循" check fails on every
// channel we can route to — routing cannot fix it. Withholding the tool
// declarations is the only mechanism that makes the declared semantics true
// (the model then answers in text with finish_reason=stop and no tool_calls,
// measured on both channels including the streaming shape). tool_choice itself
// is left as sent: upstream it is inert once the tools are gone (200 on both
// channels) and it keeps the request recognisable. The caller gates this on the
// user's fit profile, like every other family contract rule. Clean both the
// typed request and its replay body: OpenAI-compatible pass-through attempts
// read the body instead of marshaling the DTO, including on channel retries.
func applyGlm53ToolChoiceNone(c *gin.Context, request *dto.GeneralOpenAIRequest) error {
	if request == nil || !isGlm53Model(request.Model) {
		return nil
	}
	strategy, ok := request.ToolChoice.(string)
	if !ok || !strings.EqualFold(strings.TrimSpace(strategy), "none") {
		return nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return err
	}
	body, err := storage.Bytes()
	if err != nil {
		return err
	}
	// RawMessage preserves unknown provider fields and exact number literals;
	// re-marshaling only the DTO would silently drop passthrough extensions.
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(body, &fields); err != nil {
		return err
	}
	delete(fields, "tools")
	delete(fields, "functions")
	delete(fields, "function_call")
	var messages []map[string]json.RawMessage
	if raw, ok := fields["messages"]; ok {
		if err := common.Unmarshal(raw, &messages); err != nil {
			return err
		}
		for _, message := range messages {
			delete(message, "tools")
		}
		fields["messages"], err = common.Marshal(messages)
		if err != nil {
			return err
		}
	}
	body, err = common.Marshal(fields)
	if err != nil {
		return err
	}
	cleanedStorage, err := common.CreateBodyStorage(body)
	if err != nil {
		return err
	}
	common.CleanupBodyStorage(c)
	c.Set(common.KeyBodyStorage, cleanedStorage)
	c.Set(common.KeyRequestBody, nil)
	c.Request.Body = io.NopCloser(cleanedStorage)
	c.Request.ContentLength = int64(len(body))
	request.Tools = nil
	request.Functions = nil
	request.FunctionCall = nil
	for i := range request.Messages {
		request.Messages[i].Tools = nil
	}
	return nil
}

// IsStrictGlmValidationMessage reports whether an error belongs to the GLM
// validation contract (official texts plus customer-required checks). The
// caller renders the Zhipu wire format ({error:{code,message}}) for them.
func IsStrictGlmValidationMessage(message string) bool {
	for _, prefix := range []string{
		glm53ThinkingMessage,
		glm53TemperatureMessage,
		glm53TopPMessage,
		glm53ModelNotFoundMessage,
		glm53EmptyMessagesMessage,
		glm53ReasoningEffortTypeMessage,
		glm53MaxTokensRangeMessage,
		glm53OrphanToolMessageText,
	} {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return false
}

// kimiK3FunctionNameMaxLength is the longest tool name the official endpoint
// accepts. Live-probed 2026-09-22 on both declaration positions: 128 characters
// pass (and a forced call with such a name works), 129 is rejected with the same
// "function name is invalid" text as a malformed name — the length limit is not
// a separate error class, which is why it lives inside the name predicate
// instead of getting its own message.
const kimiK3FunctionNameMaxLength = 128

// validKimiK3FunctionName mirrors the official tool-name rule: must start with
// a letter, may contain letters, numbers, underscores and dashes, and is at most
// kimiK3FunctionNameMaxLength characters.
func validKimiK3FunctionName(name string) bool {
	if name == "" || len(name) > kimiK3FunctionNameMaxLength || !isLetter(name[0]) {
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

// kimiK3Error builds a K3 validation error. The official endpoint renders every
// business rejection as {error:{message,type}} with Content-Type
// application/json (13/13 classes re-probed 2026-09-21), so the gateway keeps
// param/code out of the envelope: controller/relay.go drops them for this wire
// shape. The internal error object still carries the platform's usual Code so
// non-fit consumers keep their existing shape.
func kimiK3Error(message string) error {
	return types.WithOpenAIError(types.OpenAIError{
		Message: message,
		Type:    "invalid_request_error",
		Code:    "invalid_request_error",
	}, http.StatusBadRequest)
}

// StrictFitContentType reports the wire content type the official endpoint
// uses for a strict-fit validation message, as a property of the family's wire
// shape (live-probed 2026-09-01 for DeepSeek, 2026-09-21 for Moonshot):
// DeepSeek deserialization failures come back as application/json while its
// plain business rejections use application/octet-stream; Moonshot uses
// application/json for every business rejection (13/13 classes re-probed).
func StrictFitContentType(wireShape officialfit.WireShape, message string) string {
	if wireShape == officialfit.WireShapeMoonshot {
		return "application/json"
	}
	if strings.HasPrefix(message, "Failed to deserialize the JSON body into the target type") {
		return "application/json"
	}
	return "application/octet-stream"
}

// StrictFitRendersPlainText reports whether the official endpoint returns
// this strict-fit message as a bare-text body with no JSON error wrapper —
// the body-parse failure class (live-probed 2026-09-02: a non-string
// thinking.type renders as plain application/octet-stream text).
func StrictFitRendersPlainText(message string) bool {
	return strings.HasPrefix(message, "Failed to parse the request body as JSON:")
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
		"Invalid presence_penalty value, the valid range of presence_penalty is [-2, 2]",
		"Invalid frequency_penalty value, the valid range of frequency_penalty is [-2, 2]",
		deepSeekV4JSONSchemaMessage,
		deepSeekV4ToolChoiceThinkingMessage,
		deepSeekV4ToolChoiceDeserMessage,
		deepSeekV4EmptyMessagesMessage,
		deepSeekV4ImageUnsupportedMessage,
		deepSeekV4RoleDeserMessagePrefix,
		deepSeekV4ContentPartDeserPrefix,
		"Failed to deserialize the JSON body into the target type: thinking.type: unknown variant",
		"Failed to deserialize the JSON body into the target type: thinking: missing field",
		"Failed to deserialize the JSON body into the target type: thinking: invalid type:",
		deepSeekV4ThinkingParseExpectedValueText,
		kimiK3TemperatureThinkingMessage,
		kimiK3TemperatureDisabledMessage,
		kimiK3TopPMessage,
		kimiK3NMessage,
		kimiK3PresencePenaltyMessage,
		kimiK3FrequencyPenaltyMessage,
		kimiK3TopLogprobsPairMessage,
		kimiK3ToolChoiceSpecifiedMessage,
		kimiK3ToolChoiceRequiredMessage,
		kimiK3UnknownToolChoicePrefix,
		kimiK3ToolNameMessage,
		kimiK3MessagesEmptyMessage,
		kimiK3ReasoningEffortTypeMessage,
		kimiK3ToolCallIDNotFoundMessage,
		kimiK3UnansweredToolCallsPrefix,
		kimiK3DuplicateToolCallIDPrefix,
		kimiK3TokenizationFailedMessage,
		kimiK3MessageToolsPositionPrefix,
		kimiK3MessageToolsTypePrefix,
		kimiK3DuplicateToolNamePrefix,
		kimiK3ResponseFormatTypeMessage,
		kimiK3ResponseFormatMissingSchemaName,
		kimiK3ResponseFormatEmptySchema,
		kimiK3ResponseFormatMissingSchema,
		kimiK3ResponseFormatStrictTypePrefix,
		kimiK3ResponseFormatSchemaTypePrefix,
	} {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return isDeepSeekV4ImageDownloadFailedMessage(message)
}

// isDeepSeekV4ImageDownloadFailedMessage matches the official remote-image
// failure text, whose path embeds both indices (`.messages[0].image[0]: Failed
// to download image from <url>`), so it cannot be matched by a single prefix.
func isDeepSeekV4ImageDownloadFailedMessage(message string) bool {
	if !strings.HasPrefix(message, ".messages[") {
		return false
	}
	image := strings.Index(message, deepSeekV4ImageDownloadFailedMarker)
	if image < 0 {
		return false
	}
	return strings.Contains(message[image:], deepSeekV4ImageDownloadFailedText)
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
	if ExceedsMaxTokensLimit(request.GenerationConfig.MaxOutputTokens) {
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
