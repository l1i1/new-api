package service

import (
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// DecideRelayRetry is the single retry decision for relay attempts. The reason
// is recorded in the request policy decision events of the log details.
func DecideRelayRetry(c *gin.Context, err *types.NewAPIError, retryTimes int) PolicyDecision {
	if err == nil {
		return PolicyDecision{Action: "stop", Reason: "request_completed", Source: "system"}
	}
	if ShouldSkipRetryAfterChannelAffinityFailure(c) {
		source := RequestPolicy(c).SessionModeSource
		if source == "" {
			source = "session_rule"
		}
		if !(c != nil && common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) &&
			ShouldRotateMultiKeyCredentialOn(err.StatusCode, err.Error())) {
			return PolicyDecision{Action: "stop", Reason: "strict_session", Source: source}
		}
	}
	if GetChannelConstraints(c).SuppressesRetry() {
		return PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}
	}
	if c != nil {
		if _, pinned := c.Get("specific_channel_id"); pinned {
			return PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}
		}
	}
	if types.IsChannelError(err) {
		return PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}
	}
	if retryTimes <= 0 {
		return PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}
	}
	// A request-level rejection is answered identically by every channel. Keep
	// this after the attempt budget check so an exhausted request reports the
	// same terminal reason as other failures, and before all configurable retry
	// rules so a matching never-retry keyword always wins.
	if IsNeverRetryUpstreamError(err) {
		return PolicyDecision{Action: "stop", Reason: "request_rejected", Source: "system"}
	}
	code := err.StatusCode
	if code >= 100 && code <= 599 && operation_setting.IsNeverRetryStatusCode(code) {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	// A malformed upstream response can succeed on another channel. Local
	// conversion failures carry an explicit skip-retry marker instead.
	// An operator-configured keyword is an explicit channel capability signal;
	// it takes precedence over a generic skip-retry marker.
	if operation_setting.MatchesAutomaticRetryKeywords(err.Error()) {
		if officialFitPinKeepsVerdict(c, err) {
			return PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}
		}
		return PolicyDecision{Action: "retry", Reason: "retry_keyword_matched", Source: "global"}
	}
	if types.IsSkipRetryError(err) {
		return PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}
	}
	if code >= 200 && code < 300 {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if code < 100 || code > 599 {
		return PolicyDecision{Action: "retry", Reason: "unrecognized_status", Source: "system"}
	}
	// Force-retry rules apply only to upstream errors. Local validation errors
	// must remain non-retryable even when their status is configured (for
	// example, HTTP 400).
	if err.GetErrorType() != types.ErrorTypeNewAPIError && operation_setting.ShouldRetryByStatusCode(code) {
		return PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}
	}
	return PolicyDecision{Action: "stop", Reason: "status_not_retryable", Source: "global"}
}

func ShouldRetryRelayError(c *gin.Context, openaiErr *types.NewAPIError, retryTimes int) bool {
	return DecideRelayRetry(c, openaiErr, retryTimes).Action == "retry"
}

// officialFitPinKeepsVerdict reports whether a retry-keyword match must NOT
// trigger channel failover because the request is pinned to an official-fit
// channel and retrying cannot produce a different answer.
//
// Why this is needed: the operator keyword list is written for aggregator
// traffic, where one upstream's wording is a channel capability gap that another
// channel can serve — that is the entire point of the list. An official-fit
// request is a different kind of traffic. Its pin is hard: channel selection
// keeps only channels that are official-behaving for the model
// (model.preferOfficialFitChannels), and a family may have a single such channel
// (Kimi K3, GLM 5.3). Failing over there cannot reach a second candidate, so the
// retry re-pins, excludes the only official channel, and the request dies with a
// routing error that REPLACES the official text — the exact opposite of what the
// pin exists for (the fit contract promises the official verdict passes through
// verbatim). Stopping keeps that verdict intact.
//
// The marker is set only for official-fit families
// (middleware.markV4OfficialPinFromDistributor classifies via the officialfit
// registry and needs both the user's Route dimension and a family predicate),
// so the pin flag alone is sufficient evidence here; no model lookup is needed.
//
// Credential rotation is the one retry that keeps this contract: the next key of
// the same official channel is the same official endpoint, so the request is
// still served the official way. It is therefore allowed — but only while another
// enabled credential actually remains (see anotherCredentialAvailable), because
// a one-key channel reported as multi-key would otherwise consume the retry,
// exhaust immediately, and reach the same routing error through the back door.
func officialFitPinKeepsVerdict(c *gin.Context, err *types.NewAPIError) bool {
	if c == nil || err == nil {
		return false
	}
	if !common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin) {
		return false
	}
	return !(ShouldRotateMultiKeyCredentialOn(err.StatusCode, err.Error()) && anotherCredentialAvailable(c))
}

// anotherCredentialAvailable reports whether the channel selected for this
// request still holds an enabled credential that this request has not used. It
// mirrors the selection side (model.selectMultiKeyCredential): for each key of
// the channel, skip the ones the status list marks disabled and the ones this
// request already tried, and report whether anything is left. A disabled
// credential is skipped rather than counted because rotation is only real when
// the next key can actually be handed to the upstream.
func anotherCredentialAvailable(c *gin.Context) bool {
	if !common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) {
		return false
	}
	channelID := common.GetContextKeyInt(c, constant.ContextKeyChannelId)
	if channelID <= 0 {
		return false
	}
	channel, err := model.CacheGetChannel(channelID)
	if err != nil || channel == nil || !channel.ChannelInfo.IsMultiKey {
		return false
	}
	keys := channel.Keys
	if len(keys) == 0 {
		keys = channel.GetKeys()
	}
	if len(keys) == 0 {
		return false
	}
	tried, _ := common.GetContextKeyType[map[int]map[string]struct{}](c, constant.ContextKeyChannelMultiKeyTried)
	triedForChannel := tried[channelID]
	statusList := channel.ChannelInfo.MultiKeyStatusList
	for index, key := range keys {
		if status, ok := statusList[index]; ok && status != common.ChannelStatusEnabled {
			continue
		}
		if _, used := triedForChannel[model.ChannelCredentialFingerprint(key)]; used {
			continue
		}
		return true
	}
	return false
}

func ProcessChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	if err == nil {
		return
	}
	// An upstream failure invalidates the affinity binding. This is shared by
	// HTTP and Responses WebSocket relay paths; otherwise a persistent WS retry
	// can keep selecting the same broken channel until the affinity TTL expires.
	EvictChannelAffinityOnUpstreamFailure(c, err)
	logger.LogError(c, fmt.Sprintf("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.MaskSensitiveErrorWithStatusCode())))
	if ShouldDisableChannel(err) && channelError.AutoBan {
		reason := err.MaskSensitiveErrorWithStatusCode()
		gopool.Go(func() {
			DisableChannel(channelError, reason)
		})
	}

	if constant.ErrorLogEnabled && types.IsRecordErrorLog(err) {
		userId := c.GetInt("id")
		tokenName := c.GetString("token_name")
		modelName := c.GetString("original_model")
		tokenId := c.GetInt("token_id")
		userGroup := c.GetString("group")
		other := model.NewLogOther()
		if c.Request != nil && c.Request.URL != nil {
			other.SetPublic("request_path", c.Request.URL.Path)
		}
		other.SetPublic("error_type", err.GetErrorType())
		other.SetPublic("error_code", err.GetErrorCode())
		other.SetPublic("status_code", err.StatusCode)
		AppendRelayLogAdminInfo(c, relayInfo, other)
		AppendResponseModelLogInfo(relayInfo, other)
		AppendTaskPluginContextAuditInfo(c, other)
		startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
		if startTime.IsZero() {
			startTime = time.Now()
		}
		useTimeSeconds := int(time.Since(startTime).Seconds())
		model.RecordErrorLog(c, userId, channelError.ChannelId, modelName, tokenName, err.MaskSensitiveErrorWithStatusCode(), tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
	}
}
