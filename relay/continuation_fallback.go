package relay

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// crossChannelContinuationFallback resumes a delivered answer on a different
// channel when the channel that produced it cannot even start the next round.
//
// The 2026-10-28 ch77 case is the reason this exists: the upstream kept closing
// with "Upstream completed without answer text, tool call or refusal", so every
// same-channel round failed while another channel would still have resumed from
// the caller's prefix. A resume that hops channels loses the prompt cache, so the
// caller tries its own channel first and only spends this fallback once.
//
// The hop is deliberately restricted to OpenAI-compatible channels: the
// continuation body is an OpenAI chat request and the round's response is parsed
// as an OpenAI stream, so a channel speaking another protocol cannot take it
// without a second conversion layer.
//
// Binding is complete, not partial (the earlier half-bound hop was reverted):
// selection -> SetupContextForSelectedChannel -> InitChannelMeta -> model mapping
// -> credential/multi-key selection -> header/param overrides -> concurrency slot.
// The live request info is restored before returning, so billing, request logs and
// the caller-visible stream stay attributed to the channel that served the answer.
func crossChannelContinuationFallback(c *gin.Context, info *relaycommon.RelayInfo, base []byte, build func([]byte, string, int) ([]byte, error), roundChannel *relaycommon.ContinuationRoundChannel) func(string, int) (*http.Response, error) {
	return func(partial string, remaining int) (*http.Response, error) {
		if c == nil || info == nil || c.Request == nil || len(base) == 0 {
			return nil, errors.New("cross-channel continuation unavailable")
		}
		group := info.TokenGroup
		if group == "" {
			group = c.GetString("group")
		}
		excluded := map[int]struct{}{info.ChannelId: {}}
		var selected *model.Channel
		for attempt := 0; attempt < 4; attempt++ {
			// retry>0 skips the session-affinity branch of the selector: affinity
			// would hand back the channel that just failed, and its returned channel
			// ignores the excluded set (2026-10-29 review, H1).
			retry := attempt + 1
			param := &service.RetryParam{
				Ctx:         c,
				TokenGroup:  group,
				ModelName:   info.OriginModelName,
				RequestPath: c.Request.URL.Path,
				Retry:       &retry,
			}
			for channelID := range excluded {
				param.ExcludeChannel(channelID)
			}
			candidate, _, selectErr := service.SelectChannelForRequest(c, info.OriginModelName, param)
			if selectErr != nil || candidate == nil {
				return nil, fmt.Errorf("cross-channel continuation: no candidate channel: %v", selectErr)
			}
			if _, seen := excluded[candidate.Id]; seen {
				// Keep looking instead of aborting: one stale preferred candidate must
				// not make the whole cross-channel resume impossible.
				continue
			}
			excluded[candidate.Id] = struct{}{}
			if common.ChannelInCooldown(candidate.Id) {
				continue
			}
			apiType, ok := common.ChannelType2APIType(candidate.Type)
			if !ok || apiType != constant.APITypeOpenAI {
				continue
			}
			selected = candidate
			break
		}
		if selected == nil {
			return nil, errors.New("cross-channel continuation: no OpenAI-compatible candidate")
		}
		logger.LogWarn(c, fmt.Sprintf("chat continuation hopping to channel %d after channel %d failed to start the round", selected.Id, info.ChannelId))
		resp, err := runContinuationRoundOnChannel(c, info, selected, base, build, partial, remaining)
		if err == nil && resp != nil {
			// Credit the hop channel for the round it actually served.
			roundChannel.Set(selected.Id)
		}
		if err != nil {
			// Only a genuine upstream failure counts as channel breakage: saturated
			// slots, local build/marshal errors and a caller that walked away must not
			// cool a healthy channel down (2026-10-29 review, H4).
			var upstreamFailure *continuationUpstreamFailure
			if errors.As(err, &upstreamFailure) && (c.Request == nil || c.Request.Context().Err() == nil) {
				common.RecordChannelFailure(selected.Id)
			}
		}
		return resp, err
	}
}

// continuationUpstreamFailure marks an error that came from the hop's upstream
// request (transport failure), as opposed to a local build/binding/slot error.
type continuationUpstreamFailure struct {
	err error
}

func (e *continuationUpstreamFailure) Error() string { return e.err.Error() }

func (e *continuationUpstreamFailure) Unwrap() error { return e.err }

// continuationContextKeys are the request-scoped context values that describe the
// selected channel. They are snapshotted before the hop and restored afterwards so
// billing, request logging and the remaining stream keep naming the channel that
// actually served the answer.
var continuationContextKeys = []constant.ContextKey{
	constant.ContextKeyChannelId,
	constant.ContextKeyChannelName,
	constant.ContextKeyChannelType,
	constant.ContextKeyChannelCreateTime,
	constant.ContextKeyChannelSetting,
	constant.ContextKeyChannelOtherSetting,
	constant.ContextKeyChannelBaseUrl,
	constant.ContextKeyChannelKey,
	constant.ContextKeyChannelIsMultiKey,
	constant.ContextKeyChannelMultiKeyIndex,
	constant.ContextKeyChannelParamOverride,
	constant.ContextKeyChannelResponseOverride,
	constant.ContextKeyChannelHeaderOverride,
	constant.ContextKeyChannelOrganization,
	constant.ContextKeyChannelAutoBan,
	constant.ContextKeyChannelModelMapping,
	constant.ContextKeyChannelStatusCodeMapping,
	constant.ContextKeyChannelCredentialId,
	constant.ContextKeyChannelProxyMode,
	constant.ContextKeyChannelEffectiveProxy,
	constant.ContextKeyChannelMultiKeyTried,
	constant.ContextKeySystemPromptOverride,
	constant.ContextKeySelectedChannel,
	constant.ContextKeyOriginalModel,
	constant.ContextKey("api_version"),
	constant.ContextKey("region"),
	constant.ContextKey("channel_organization"),
	constant.ContextKey("original_model"),
	constant.ContextKey("task_plugin_key"),
	constant.ContextKey("expected_task_plugin_key"),
	constant.ContextKey("platform"),
	constant.ContextKey("bot_id"),
}

func snapshotContinuationContext(c *gin.Context) map[string]any {
	snapshot := make(map[string]any, len(continuationContextKeys))
	for _, key := range continuationContextKeys {
		if value, exists := c.Get(string(key)); exists {
			snapshot[string(key)] = value
		}
	}
	return snapshot
}

func restoreContinuationContext(c *gin.Context, snapshot map[string]any) {
	for _, key := range continuationContextKeys {
		value, exists := snapshot[string(key)]
		if !exists {
			// The hop may have created a channel-scoped key that did not exist before
			// (channel_organization is only written when the channel has one). Leaving
			// it behind would describe the hop channel to later attempts, so clear it.
			c.Set(string(key), nil)
			continue
		}
		c.Set(string(key), value)
	}
}

// runContinuationRoundOnChannel sends one continuation round through the selected
// channel with a complete rebind and returns the upstream response untouched.
func runContinuationRoundOnChannel(c *gin.Context, info *relaycommon.RelayInfo, selected *model.Channel, base []byte, build func([]byte, string, int) ([]byte, error), partial string, remaining int) (*http.Response, error) {
	contextSnapshot := snapshotContinuationContext(c)
	previousMeta := *info.ChannelMeta
	defer func() {
		*info.ChannelMeta = previousMeta
		restoreContinuationContext(c, contextSnapshot)
	}()

	if setupErr := middleware.SetupContextForSelectedChannel(c, selected, info.OriginModelName); setupErr != nil {
		return nil, fmt.Errorf("cross-channel continuation: bind channel %d: %w", selected.Id, setupErr)
	}

	continuationJSON, buildErr := build(base, partial, remaining)
	if buildErr != nil {
		return nil, buildErr
	}
	var request dto.GeneralOpenAIRequest
	if unmarshalErr := common.Unmarshal(continuationJSON, &request); unmarshalErr != nil {
		return nil, fmt.Errorf("cross-channel continuation: parse body: %w", unmarshalErr)
	}

	// A throwaway RelayInfo clone carries the new channel identity for this round
	// only: the live info keeps its billing channel and client stream accounting.
	round := *info
	round.ChannelMeta = &relaycommon.ChannelMeta{}
	round.InitChannelMeta(c)
	round.SendResponseCount = 0
	round.Request = &request
	if mapErr := helper.ModelMappedHelper(c, &round, &request); mapErr != nil {
		return nil, fmt.Errorf("cross-channel continuation: map model: %w", mapErr)
	}

	apiType, ok := common.ChannelType2APIType(selected.Type)
	if !ok {
		return nil, fmt.Errorf("cross-channel continuation: channel %d has no api type", selected.Id)
	}
	channelAdaptor := GetAdaptor(apiType)
	if channelAdaptor == nil {
		return nil, fmt.Errorf("cross-channel continuation: channel %d has no adaptor", selected.Id)
	}
	channelAdaptor.Init(&round)

	release, saturated := service.AcquireChannelConcurrency(c, selected)
	if saturated {
		return nil, fmt.Errorf("cross-channel continuation: channel %d concurrency saturated", selected.Id)
	}
	if release == nil {
		release = func() {}
	}
	var releaseOnce sync.Once
	releaseSlot := func() { releaseOnce.Do(release) }

	jsonBody, marshalErr := common.Marshal(&request)
	if marshalErr != nil {
		releaseSlot()
		return nil, marshalErr
	}
	if jsonBody, marshalErr = relaycommon.ApplyParamOverrideWithRelayInfo(jsonBody, &round); marshalErr != nil {
		releaseSlot()
		return nil, marshalErr
	}
	body, bodyCloser, bodyErr := relaycommon.NewOutboundJSONBody(jsonBody)
	if bodyErr != nil {
		releaseSlot()
		return nil, bodyErr
	}
	defer bodyCloser.Close()

	respAny, requestErr := channelAdaptor.DoRequest(c, &round, body)
	if requestErr != nil {
		releaseSlot()
		return nil, &continuationUpstreamFailure{err: requestErr}
	}
	resp, ok := respAny.(*http.Response)
	if !ok || resp == nil {
		releaseSlot()
		return nil, fmt.Errorf("cross-channel continuation: adaptor returned %T", respAny)
	}
	if resp.Body == nil {
		releaseSlot()
		return nil, fmt.Errorf("cross-channel continuation: channel %d returned no body", selected.Id)
	}
	// The concurrency slot belongs to the round's body lifetime: hold it until the
	// caller finishes reading the round.
	resp.Body = &releaseOnCloseReadCloser{ReadCloser: resp.Body, release: releaseSlot}
	return resp, nil
}

type releaseOnCloseReadCloser struct {
	io.ReadCloser
	release func()
}

func (r *releaseOnCloseReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.release()
	return err
}
