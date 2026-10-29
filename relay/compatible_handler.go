package relay

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaychannel "github.com/QuantumNous/new-api/relay/channel"
	openaichannel "github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/samber/lo"
	"github.com/tidwall/sjson"

	"github.com/gin-gonic/gin"
)

// continuationFallbackChannel picks a different channel for a continuation round.
// The round that was cut is usually still bound to the channel that cut it (the
// 900s wall is one upstream flow; a broken aggregator keeps erroring), so the one
// permitted continuation step should not be spent on the same channel. Returns
// nil when the pool offers no other candidate.
func continuationFallbackChannel(c *gin.Context, info *relaycommon.RelayInfo) *model.Channel {
	if c == nil || info == nil || c.Request == nil {
		return nil
	}
	group := info.TokenGroup
	if group == "" {
		group = c.GetString("group")
	}
	previous := info.ChannelId
	for attempt := 0; attempt < 3; attempt++ {
		retry := attempt
		selected, _, selectErr := service.SelectChannelForRequest(c, info.OriginModelName, &service.RetryParam{
			Ctx:         c,
			TokenGroup:  group,
			ModelName:   info.OriginModelName,
			RequestPath: c.Request.URL.Path,
			Retry:       &retry,
		})
		if selectErr != nil || selected == nil {
			return nil
		}
		if selected.Id != previous {
			return selected
		}
	}
	return nil
}

// adoptChannelForContinuation points the relay info at the fallback channel so the
// continuation request, its billing and its request log all name the channel that
// actually served it. Returns nil when the channel type has no adaptor.
func adoptChannelForContinuation(info *relaycommon.RelayInfo, channel *model.Channel) relaychannel.Adaptor {
	if info == nil || info.ChannelMeta == nil || channel == nil {
		return nil
	}
	// GetAdaptor takes an API type, not a channel type (channel type 1 would
	// resolve to the Claude adaptor), so map it first.
	apiType, apiTypeOK := common.ChannelType2APIType(channel.Type)
	if !apiTypeOK {
		return nil
	}
	channelAdaptor := GetAdaptor(apiType)
	if channelAdaptor == nil {
		return nil
	}
	info.ChannelId = channel.Id
	info.ChannelType = channel.Type
	info.ChannelBaseUrl = channel.GetBaseURL()
	info.ChannelSetting = channel.GetSetting()
	info.ParamOverride = nil
	info.ResponseOverride = nil
	info.HeadersOverride = nil
	// A multi-key channel must hand out one enabled key, not the raw Key field
	// (that is the joined credential list, which is not a valid bearer token).
	apiKey, keyIndex, keyErr := channel.GetNextEnabledKey()
	if keyErr != nil || strings.TrimSpace(apiKey) == "" {
		return nil
	}
	info.ApiKey = apiKey
	info.ChannelIsMultiKey = channel.ChannelInfo.IsMultiKey
	info.ChannelMultiKeyIndex = keyIndex
	if info.ChannelMeta != nil {
		info.ChannelMeta.ApiType = apiType
	}
	return channelAdaptor
}

// restoreChannelMeta rolls the relay info back to the channel that owned it, used
// when the fallback attempt itself fails and the request keeps its original
// attribution.
func restoreChannelMeta(previous relaycommon.ChannelMeta, info *relaycommon.RelayInfo) {
	if info == nil || info.ChannelMeta == nil {
		return
	}
	*info.ChannelMeta = previous
}

func TextHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)
	// A previous attempt of the same request must not leak its continuation hook
	// into this attempt (the channel whitelist can differ between attempts).
	info.ChatContinuation = nil

	textReq, ok := info.Request.(*dto.GeneralOpenAIRequest)
	if !ok {
		return types.NewErrorWithStatusCode(fmt.Errorf("invalid request type, expected dto.GeneralOpenAIRequest, got %T", info.Request), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	request, err := common.DeepCopy(textReq)
	if err != nil {
		return types.NewError(fmt.Errorf("failed to copy request to GeneralOpenAIRequest: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	if request.WebSearchOptions != nil {
		c.Set("chat_completion_web_search_context_size", request.WebSearchOptions.SearchContextSize)
	}

	err = helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}
	if err := helper.ApplyReasoningModelSuffix(c, info, request); err != nil {
		return newConvertRequestFailedError(c, info, err)
	}

	includeUsage := true
	// 判断用户是否需要返回使用情况
	if request.StreamOptions != nil {
		includeUsage = request.StreamOptions.IncludeUsage
	}
	// The client's explicit ask, read before the forcing below overwrites the
	// request. includeUsage above answers "should we bill from stream usage" and
	// is true by default; this one answers "did the client ask for a usage
	// event", which official endpoints treat as false when stream_options is
	// absent entirely. Response shaping that mirrors such a contract needs the
	// difference.
	info.ClientIncludeUsage = request.StreamOptions != nil && request.StreamOptions.IncludeUsage

	// 如果不支持StreamOptions，将StreamOptions设置为nil
	if !info.SupportStreamOptions || !lo.FromPtrOr(request.Stream, false) {
		request.StreamOptions = nil
	} else {
		// 如果支持StreamOptions，且请求中没有设置StreamOptions，根据配置文件设置StreamOptions
		if constant.ForceStreamOption {
			request.StreamOptions = &dto.StreamOptions{
				IncludeUsage: true,
			}
		}
	}

	info.ShouldIncludeUsage = includeUsage

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)

	passThroughRequestBody := shouldPassThroughOpenAIRequestBody(info)
	// Channel-agnostic payload normalization: move a long document from the last
	// user message into the system message before ANY adaptor sees the request,
	// so every OpenAI-format channel (official and aggregator alike) gets the
	// cache-friendly shape. Placed ahead of the chat→Responses branch below so a
	// scoped request converted to the Responses API is rewritten too, and gated
	// on chat-completions so the legacy /v1/completions path (which carries no
	// messages) never reaches it. See normalizePayloadForUpstream.
	if shouldNormalizeChatPayload(info, passThroughRequestBody) {
		request = normalizePayloadForUpstream(c, info, request)
	}
	if info.RelayMode == relayconstant.RelayModeChatCompletions &&
		!passThroughRequestBody &&
		service.ShouldChatCompletionsUseResponsesGlobal(info.ChannelId, info.ChannelType, info.OriginModelName) {
		applySystemPromptIfNeeded(c, info, request)
		usage, newApiErr := textRequestViaResponses(c, info, adaptor, request)
		if newApiErr != nil {
			return newApiErr
		}

		var containAudioTokens = usage.CompletionTokenDetails.AudioTokens > 0 || usage.PromptTokensDetails.AudioTokens > 0
		var containsAudioRatios = ratio_setting.ContainsAudioRatio(info.OriginModelName) || ratio_setting.ContainsAudioCompletionRatio(info.OriginModelName)

		if containAudioTokens && containsAudioRatios {
			service.PostAudioConsumeQuota(c, info, usage, "")
		} else {
			service.PostTextConsumeQuota(c, info, usage, nil)
		}
		return nil
	}

	// Internal-stream mode (dto.ChannelSettings.InternalStreamForNonStream): the
	// client asked for one JSON body, but this channel is served by calling the
	// upstream with stream=true and buffering the deltas back into a single
	// response - see relay/channel/openai.OaiChatBufferedStreamHandler for why.
	//
	// info.IsStream is deliberately left false here. It gates
	// relay/channel/api_request.go's SetEventStreamHeaders, and the client must
	// still receive a plain JSON body; the streaming happens only on the upstream
	// leg. Pass-through channels are excluded because their body is forwarded
	// verbatim and cannot be rewritten.
	internalStreamForNonStream := info.ChannelSetting.InternalStreamForNonStream &&
		!info.IsStream &&
		!passThroughRequestBody &&
		info.RelayMode == relayconstant.RelayModeChatCompletions &&
		info.RelayFormat == types.RelayFormatOpenAI

	var requestBody io.Reader
	// continuationBase is the final upstream body snapshot used to build
	// transparent continuation rounds for an output-capped stream. Nil unless
	// the feature is enabled and this channel is whitelisted.
	var continuationBase []byte

	if passThroughRequestBody {
		requestBody, err = getPassThroughRequestBody(c, info)
		if err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
	} else {
		// Payload normalization already ran above, before the endpoint branch:
		// one owner, every OpenAI-format path.
		convertedRequest, err := adaptor.ConvertOpenAIRequest(c, info, request)
		if err != nil {
			return newConvertRequestFailedError(c, info, err)
		}
		relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)

		if req, ok := convertedRequest.(*dto.GeneralOpenAIRequest); ok {
			applySystemPromptIfNeeded(c, info, req)
		}

		jsonData, err := common.Marshal(convertedRequest)
		if err != nil {
			return types.NewError(err, types.ErrorCodeJsonMarshalFailed, types.ErrOptionWithSkipRetry())
		}

		// remove disabled fields for OpenAI API
		jsonData, err = relaycommon.RemoveDisabledFields(jsonData, info.ChannelOtherSettings, info.ChannelSetting.PassThroughBodyEnabled)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}

		// apply param override
		if len(info.ParamOverride) > 0 {
			jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
			if err != nil {
				return newAPIErrorFromParamOverride(err)
			}
		}

		// Internal-stream mode rewrites the outbound body only. sjson (the same
		// writer the param-override engine uses) keeps the key order and the
		// untouched fields byte-identical, which matters because providers such
		// as Moonshot echo structured fields back and the round trip must survive.
		// include_usage matches what the platform already sends for real streaming
		// requests (relay/channel/openai/adaptor.go), so it introduces nothing an
		// upstream serving our streaming traffic has not already accepted.
		if internalStreamForNonStream {
			patched, patchErr := sjson.SetBytes(jsonData, "stream", true)
			if patchErr == nil {
				patched, patchErr = sjson.SetBytes(patched, "stream_options.include_usage", true)
			}
			if patchErr != nil {
				return types.NewError(patchErr, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
			}
			jsonData = patched
		}

		logger.LogDebug(c, "text request body: %s", jsonData)

		if relaycommon.ChatContinuationStepsForRequest(info) > 0 ||
			relaycommon.ChatAbnormalContinuationStepsForRequest(info) > 0 ||
			relaycommon.ChatMissingContinuationStepsForRequest(info) > 0 {
			continuationBase = append([]byte(nil), jsonData...)
		}

		body, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		defer closer.Close()
		jsonData = nil
		requestBody = body
	}

	// A provider-side output cap can end a chat stream with finish_reason=length
	// while the client still has budget left. When the feature is enabled for this
	// channel, hand the stream handler a sender that replays the same final
	// upstream body with the visible answer prefix plus the continuation
	// instruction, so the handler can stitch the rounds into one client response.
	if len(continuationBase) > 0 {
		steps := relaycommon.ChatContinuationStepsForRequest(info)
		abnormalSteps := relaycommon.ChatAbnormalContinuationStepsForRequest(info)
		missingSteps := relaycommon.ChatMissingContinuationStepsForRequest(info)
		// The rotation-era round budget: one total cap for every repair mode, with
		// the legacy per-mode step counts kept as the compatibility fallback.
		maxRounds := common.ChatContinuationMaxRounds
		if common.ChatContinuationMaxRoundsSet {
			if maxRounds <= 0 {
				// The rotation-era key is explicitly 0: transparent continuation is off.
				steps, abnormalSteps, missingSteps = 0, 0, 0
			}
		} else {
			// Key unset: the legacy per-mode step counts keep their old meaning.
			maxRounds = steps
			if abnormalSteps > maxRounds {
				maxRounds = abnormalSteps
			}
			if missingSteps > maxRounds {
				maxRounds = missingSteps
			}
		}
		base := continuationBase
		adaptorRef := adaptor
		// Tracks the channel of the latest round so continuation-capability telemetry
		// names the channel that actually resumed (cross-channel hops included).
		roundChannel := &relaycommon.ContinuationRoundChannel{}
		// One tried set per request: rounds rotate over channels this request has not
		// been cut on, shared by the length, abnormal and missing repair modes.
		triedChannels := &relaycommon.TriedChannels{}
		send := func(build func([]byte, string, int) ([]byte, error)) func(string, int) (*http.Response, error) {
			// sendOnce replays the continuation body through one channel.
			sendOnce := func(channelAdaptor relaychannel.Adaptor, partial string, remaining int) (*http.Response, error) {
				continuationJSON, buildErr := build(base, partial, remaining)
				if buildErr != nil {
					return nil, buildErr
				}
				body, bodyCloser, bodyErr := relaycommon.NewOutboundJSONBody(continuationJSON)
				if bodyErr != nil {
					return nil, bodyErr
				}
				defer bodyCloser.Close()
				respAny, requestErr := channelAdaptor.DoRequest(c, info, body)
				if requestErr != nil {
					return nil, requestErr
				}
				resp, ok := respAny.(*http.Response)
				if !ok || resp == nil {
					return nil, fmt.Errorf("continuation: adaptor returned %T", respAny)
				}
				return resp, nil
			}
			// Same-channel first: a resume on the channel that owns the prefix hits
			// its prompt cache. The runner spends the single cross-channel fallback
			// only when this sender cannot even start the round.
			triedChannels.Add(info.ChannelId)
			return func(partial string, remaining int) (*http.Response, error) {
				resp, err := sendOnce(adaptorRef, partial, remaining)
				if err == nil && resp != nil {
					roundChannel.Set(info.ChannelId)
				}
				return resp, err
			}
		}
		// The cross-channel fallback replays the same OpenAI continuation body and
		// parses an OpenAI stream, so it is only offered when this attempt's body is
		// OpenAI-shaped. A converted (Claude/Gemini) attempt keeps same-channel only.
		crossChannel := info.ApiType == constant.APITypeOpenAI
		fallback := func(build func([]byte, string, int) ([]byte, error)) func(string, int) (*http.Response, error) {
			if !crossChannel {
				return nil
			}
			return crossChannelContinuationFallback(c, info, base, build, roundChannel, triedChannels)
		}
		cfg := &relaycommon.ChatContinuationConfig{MaxSteps: steps, MaxRounds: maxRounds, RoundChannel: roundChannel, Tried: triedChannels}
		if steps > 0 {
			cfg.Send = send(openaichannel.BuildChatContinuationBody)
			cfg.Fallback = fallback(openaichannel.BuildChatContinuationBody)
		}
		if abnormalSteps > 0 {
			cfg.Abnormal = &relaycommon.ChatAbnormalContinuationConfig{
				MaxSteps:          abnormalSteps,
				MaxRounds:         maxRounds,
				Tried:             triedChannels,
				Send:              send(openaichannel.BuildChatAnswerContinuationBody),
				RoundChannel:      roundChannel,
				Fallback:          fallback(openaichannel.BuildChatAnswerContinuationBody),
				MaxReasoningBytes: common.ChatContinuationAbnormalMaxReasoningBytes,
				MinReasoningChars: common.ChatContinuationAbnormalMinReasoningChars,
			}
		}
		if missingSteps > 0 {
			cfg.Missing = &relaycommon.ChatAbnormalContinuationConfig{
				MaxSteps:          missingSteps,
				MaxRounds:         maxRounds,
				Tried:             triedChannels,
				Send:              send(openaichannel.BuildChatAnswerContinuationBody),
				RoundChannel:      roundChannel,
				Fallback:          fallback(openaichannel.BuildChatAnswerContinuationBody),
				MaxReasoningBytes: common.ChatContinuationMissingMaxReasoningBytes,
				MinReasoningChars: common.ChatContinuationMissingMinReasoningChars,
			}
		}
		info.ChatContinuation = cfg
	}

	var httpResp *http.Response
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")

	if resp != nil {
		httpResp = resp.(*http.Response)
		info.IsStream = info.IsStream || strings.HasPrefix(httpResp.Header.Get("Content-Type"), "text/event-stream")
		if httpResp.StatusCode != http.StatusOK {
			newApiErr := service.RelayErrorHandlerWithFormat(c, httpResp, false, info.RelayFormat)
			// reset status code 重置状态码
			service.ResetStatusCode(newApiErr, statusCodeMappingStr)
			sendMappedStreamError(c, info, newApiErr)
			return newApiErr
		}
	}

	// Internal-stream mode answers the non-streaming client from the buffered
	// upstream stream instead of letting DoResponse pick the streaming path: the
	// upstream is now emitting SSE while the client asked for one JSON body.
	// Nothing has been written to the client at this point, so a failure here
	// still leaves the response uncommitted and the retry loop can fail over.
	//
	// The content-type check is the fallback for an upstream that ignores
	// stream=true and answers with a plain JSON body: that is a normal
	// non-streaming response, so it goes down the ordinary path. info.IsStream is
	// forced back to false because the check above just set it from the upstream
	// content type - the client still asked for, and must receive, one JSON body.
	var usage any
	var newApiErr *types.NewAPIError
	if internalStreamForNonStream && httpResp != nil &&
		strings.HasPrefix(httpResp.Header.Get("Content-Type"), "text/event-stream") {
		info.IsStream = false
		usage, newApiErr = openaichannel.OaiChatBufferedStreamHandler(c, info, httpResp)
	} else {
		usage, newApiErr = adaptor.DoResponse(c, httpResp, info)
	}
	if newApiErr != nil {
		// A failed stream can still have produced billable upstream usage: either
		// the transport terminated after output, or the response was rejected as
		// a zero-output/upstream failure after the upstream had already reported
		// usage (its reasoning was dropped, or the caller disabled thinking while
		// the provider still billed reasoning tokens). Settle that observed usage
		// before returning; the outer relay defer is idempotent and will not
		// refund a settled billing session. Only upstream-reported tokens are
		// settled -- a genuinely empty response has nothing to bill, so the
		// caller is not charged for a completion they never received.
		// The pointer check is the real guard: a typed-nil *dto.Usage in the
		// interface still asserts ok=true, so `streamUsage != nil` below is what
		// keeps the dereference safe.
		streamUsage, _ := usage.(*dto.Usage)
		isTextCompletion := info.RelayMode == relayconstant.RelayModeChatCompletions ||
			info.RelayMode == relayconstant.RelayModeCompletions
		truncatedUpstream := info.StreamStatus != nil && !info.StreamStatus.IsNormalEnd() &&
			info.ReceivedResponseCount > 0
		rejectedAfterUsage := newApiErr.SettlesObservedUsageAfterError() &&
			streamUsage != nil && streamUsage.TotalTokens > 0
		if streamUsage != nil && (truncatedUpstream || rejectedAfterUsage) {
			// The caller did not receive a complete answer, so this settlement
			// must be zero even though the upstream reported usage. The raw
			// counts stay on the relay info for the consume log's audit marker.
			info.MarkUndelivered(relaycommon.UndeliveredUpstreamFailure, streamUsage)
		}
		if isTextCompletion && streamUsage != nil && (truncatedUpstream || rejectedAfterUsage) {
			containAudioTokens := streamUsage.CompletionTokenDetails.AudioTokens > 0 || streamUsage.PromptTokensDetails.AudioTokens > 0
			containsAudioRatios := ratio_setting.ContainsAudioRatio(info.OriginModelName) || ratio_setting.ContainsAudioCompletionRatio(info.OriginModelName)
			if containAudioTokens && containsAudioRatios {
				service.PostAudioConsumeQuota(c, info, streamUsage, "incomplete upstream stream")
			} else {
				service.PostTextConsumeQuota(c, info, streamUsage, []string{"incomplete upstream stream"})
			}
		}
		// reset status code 重置状态码
		service.ResetStatusCode(newApiErr, statusCodeMappingStr)
		return newApiErr
	}

	var containAudioTokens = usage.(*dto.Usage).CompletionTokenDetails.AudioTokens > 0 || usage.(*dto.Usage).PromptTokensDetails.AudioTokens > 0
	var containsAudioRatios = ratio_setting.ContainsAudioRatio(info.OriginModelName) || ratio_setting.ContainsAudioCompletionRatio(info.OriginModelName)

	if containAudioTokens && containsAudioRatios {
		service.PostAudioConsumeQuota(c, info, usage.(*dto.Usage), "")
	} else {
		service.PostTextConsumeQuota(c, info, usage.(*dto.Usage), nil)
	}
	return nil
}

// sendMappedStreamError 处理「上游非 2xx 错误被 status_code_mapping 映射为 2xx 的流式请求」。
// 此时响应头已被 doRequest 在流式请求下设为 text/event-stream（SetEventStreamHeaders），
// 但错误体是普通 JSON（无 data: 行），SSE 客户端会把它解析成一个空流（无 finish_reason、
// 无内容），从而出现 empty_model_response 类问题。这里向客户端发送标准格式的 SSE 错误事件，
// 让客户端读到明确的错误而非空响应。已写入（c.Writer.Written()==true）后，
// controller/relay.go 的 defer 不会再写重复的错误 JSON。
func sendMappedStreamError(c *gin.Context, info *relaycommon.RelayInfo, newApiErr *types.NewAPIError) {
	if newApiErr == nil || info == nil || !info.IsStream || c.Writer.Written() {
		return
	}
	statusCode := newApiErr.StatusCode
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return
	}
	switch info.RelayFormat {
	case types.RelayFormatClaude:
		_ = helper.ClaudeData(c, dto.ClaudeResponse{
			Type:  "error",
			Error: newApiErr.ToClaudeError(),
		})
	case types.RelayFormatGemini:
		message := newApiErr.Error()
		code := statusCode
		if code < 400 || code > 599 {
			code = http.StatusBadRequest
		}
		payload, marshalErr := common.Marshal(gin.H{"error": gin.H{
			"code":    code,
			"message": message,
			"status":  "UPSTREAM_ERROR",
		}})
		if marshalErr == nil {
			c.Render(-1, common.CustomEvent{Data: "data: " + string(payload) + "\n\n"})
			_ = helper.FlushWriter(c)
		}
	default:
		_ = helper.ObjectData(c, gin.H{"error": newApiErr.ToOpenAIError()})
		helper.Done(c)
	}
}
