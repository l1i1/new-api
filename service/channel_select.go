package service

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

func GetChannelConstraints(c *gin.Context) *dto.ChannelConstraints {
	if c == nil {
		return &dto.ChannelConstraints{}
	}
	if existing, ok := common.GetContextKeyType[*dto.ChannelConstraints](c, constant.ContextKeyChannelConstraints); ok && existing != nil {
		return existing
	}
	constraints := &dto.ChannelConstraints{}
	common.SetContextKey(c, constant.ContextKeyChannelConstraints, constraints)
	return constraints
}

func AppendTaskPluginIdentityFilter(c *gin.Context, pluginKey string) {
	if c == nil {
		return
	}
	channelTypes, pluginKeys := pinnedTaskPluginIdentities(c, pluginKey)
	GetChannelConstraints(c).AddFilter(dto.ChannelFilter{
		Kind:                   dto.FilterTaskPluginIdentity,
		TaskPluginKey:          pluginKey,
		TaskPluginChannelTypes: channelTypes,
		TaskPluginKeys:         pluginKeys,
	})
}

type RetryParam struct {
	Ctx                *gin.Context
	TokenGroup         string
	ModelName          string
	RequestPath        string
	Retry              *int
	resetNextTry       bool
	preferredChannelID int
	excludedChannelIDs map[int]struct{}
	// saturatedExclusions counts how many channels were excluded because
	// their concurrency limit was full. When the request then runs out of
	// selectable channels and this equals the total exclusion count, the
	// failure is a pure saturation outcome and is reported as 429.
	saturatedExclusions int
}

type ChannelSelectionFailureKind string

const (
	ChannelSelectionModelNotConfigured     ChannelSelectionFailureKind = "model_not_configured"
	ChannelSelectionTemporarilyUnavailable ChannelSelectionFailureKind = "temporarily_unavailable"
	ChannelSelectionInternalError          ChannelSelectionFailureKind = "internal_selection_error"
	ChannelSelectionAccessDenied           ChannelSelectionFailureKind = "access_denied"
)

type ChannelSelectionError struct {
	Kind  ChannelSelectionFailureKind
	Group string
	Model string
	Err   error
}

func (e *ChannelSelectionError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return string(e.Kind)
	}
	return e.Err.Error()
}

func (e *ChannelSelectionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (p *RetryParam) GetRetry() int {
	if p.Retry == nil {
		return 0
	}
	return *p.Retry
}

func (p *RetryParam) SetRetry(retry int) {
	p.Retry = &retry
}

func (p *RetryParam) IncreaseRetry() {
	if p.resetNextTry {
		p.resetNextTry = false
		return
	}
	if p.Retry == nil {
		p.Retry = new(int)
	}
	*p.Retry++
}

func (p *RetryParam) ResetRetryNextTry() {
	p.resetNextTry = true
}

func (p *RetryParam) CancelRetryReset() {
	p.resetNextTry = false
}

// PreferChannel keeps a key-rotation retry on the channel that just failed.
func (p *RetryParam) PreferChannel(channelID int) {
	if channelID > 0 {
		p.preferredChannelID = channelID
	}
}

func (p *RetryParam) PreferredChannelID() int {
	return p.preferredChannelID
}

func (p *RetryParam) ClearPreferredChannel() {
	p.preferredChannelID = 0
}

// ExcludeChannel prevents a non-key failure from selecting the same channel
// again during this request.
func (p *RetryParam) ExcludeChannel(channelID int) {
	if channelID <= 0 {
		return
	}
	if p.excludedChannelIDs == nil {
		p.excludedChannelIDs = make(map[int]struct{})
	}
	p.excludedChannelIDs[channelID] = struct{}{}
}

func (p *RetryParam) IsChannelExcluded(channelID int) bool {
	_, excluded := p.excludedChannelIDs[channelID]
	return excluded
}

// ExcludeSaturatedChannel excludes a channel whose concurrency limit is full
// for the rest of this request and remembers the saturation, so exhausting
// the remaining candidates can be classified as 429.
func (p *RetryParam) ExcludeSaturatedChannel(channelID int) {
	if channelID <= 0 || p.IsChannelExcluded(channelID) {
		return
	}
	p.saturatedExclusions++
	p.ExcludeChannel(channelID)
}

// HasSaturatedChannel reports whether every channel excluded so far was
// excluded for concurrency saturation (no real upstream failure happened),
// so running out of candidates is a pure saturation outcome. A request that
// also saw genuine channel errors keeps the generic failure classification.
func (p *RetryParam) HasSaturatedChannel() bool {
	return p.saturatedExclusions > 0 && p.saturatedExclusions == len(p.excludedChannelIDs)
}

// CacheGetRandomSatisfiedChannel tries to get a random channel that satisfies the requirements.
// 尝试获取一个满足要求的随机渠道。
//
// For "auto" tokenGroup with cross-group Retry enabled:
// 对于启用了跨分组重试的 "auto" tokenGroup：
//
//   - Each group will exhaust all its priorities before moving to the next group.
//     每个分组会用完所有优先级后才会切换到下一个分组。
//
//   - Uses ContextKeyAutoGroupIndex to track current group index.
//     使用 ContextKeyAutoGroupIndex 跟踪当前分组索引。
//
//   - Uses ContextKeyAutoGroupRetryIndex to track the global Retry count when current group started.
//     使用 ContextKeyAutoGroupRetryIndex 跟踪当前分组开始时的全局重试次数。
//
//   - priorityRetry = Retry - startRetryIndex, represents the priority level within current group.
//     priorityRetry = Retry - startRetryIndex，表示当前分组内的优先级级别。
//
//   - When GetRandomSatisfiedChannel returns nil (priorities exhausted), moves to next group.
//     当 GetRandomSatisfiedChannel 返回 nil（优先级用完）时，切换到下一个分组。
//
// Example flow (2 groups, each with 2 priorities, RetryTimes=3):
// 示例流程（2个分组，每个有2个优先级，RetryTimes=3）：
//
//	Retry=0: GroupA, priority0 (startRetryIndex=0, priorityRetry=0)
//	         分组A, 优先级0
//
//	Retry=1: GroupA, priority1 (startRetryIndex=0, priorityRetry=1)
//	         分组A, 优先级1
//
//	Retry=2: GroupA exhausted → GroupB, priority0 (startRetryIndex=2, priorityRetry=0)
//	         分组A用完 → 分组B, 优先级0
//
//	Retry=3: GroupB, priority1 (startRetryIndex=2, priorityRetry=1)
//	         分组B, 优先级1
//
// v4OfficialPin reports whether the request context marks this deepseek-v4
// request for the official-channel pin (extreme sampling parameters that
// aggregators cannot fit).
func v4OfficialPin(param *RetryParam) bool {
	return param != nil && param.Ctx != nil &&
		common.GetContextKeyBool(param.Ctx, constant.ContextKeyV4OfficialPin)
}

// videoRequestOnly reports whether this request carries a video part, which
// restricts selection to channels that declared they can read video. The flag
// lives in the request context so every retry keeps the restriction.
func videoRequestOnly(param *RetryParam) bool {
	return param != nil && param.Ctx != nil &&
		common.GetContextKeyBool(param.Ctx, constant.ContextKeyVideoRequest)
}

func CacheGetRandomSatisfiedChannel(param *RetryParam) (*model.Channel, string, error) {
	var channel *model.Channel
	var err error
	var lastAutoGroupSelectionErr error
	var lastAutoGroupSelectionErrGroup string
	selectGroup := param.TokenGroup
	userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)
	policy, policyLoaded := GetGroupAccessPolicy(param.Ctx)
	blockedChannels := GroupAccessPolicyBlockedChannels(param.Ctx)
	if len(param.excludedChannelIDs) > 0 {
		mergedBlockedChannels := make(map[int]struct{}, len(blockedChannels)+len(param.excludedChannelIDs))
		for channelID := range blockedChannels {
			mergedBlockedChannels[channelID] = struct{}{}
		}
		for channelID := range param.excludedChannelIDs {
			mergedBlockedChannels[channelID] = struct{}{}
		}
		blockedChannels = mergedBlockedChannels
	}
	if policyLoaded && policy.BlocksModel(param.ModelName) {
		return nil, selectGroup, &ChannelSelectionError{
			Kind:  ChannelSelectionAccessDenied,
			Group: selectGroup,
			Model: param.ModelName,
			Err:   errors.New("model is blocked by group access policy"),
		}
	}

	if param.TokenGroup == "auto" {
		autoGroups := GetRequestAutoGroups(param.Ctx, userGroup)
		if len(autoGroups) == 0 {
			return nil, selectGroup, &ChannelSelectionError{
				Kind:  ChannelSelectionAccessDenied,
				Group: selectGroup,
				Model: param.ModelName,
				Err:   errors.New("auto groups is not enabled"),
			}
		}

		// startGroupIndex: the group index to start searching from
		// startGroupIndex: 开始搜索的分组索引
		startGroupIndex := 0
		crossGroupRetry := common.GetContextKeyBool(param.Ctx, constant.ContextKeyTokenCrossGroupRetry)

		if lastGroupIndex, exists := common.GetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex); exists {
			if idx, ok := lastGroupIndex.(int); ok {
				startGroupIndex = idx
			}
		}

		for i := startGroupIndex; i < len(autoGroups); i++ {
			autoGroup := autoGroups[i]
			if policyLoaded && policy.BlocksGroup(autoGroup) {
				continue
			}
			routingModel, compactAlias := model.ResolveCompactModelAliasForGroupPathWithBlockedChannels(autoGroup, param.ModelName, param.RequestPath, blockedChannels)
			if policyLoaded && policy.BlocksModel(routingModel) {
				continue
			}
			// Calculate priorityRetry for current group
			// 计算当前分组的 priorityRetry
			priorityRetry := param.GetRetry()
			if len(param.excludedChannelIDs) > 0 {
				priorityRetry = 0
			}
			// If moved to a new group, reset priorityRetry and update startRetryIndex
			// 如果切换到新分组，重置 priorityRetry 并更新 startRetryIndex
			if i > startGroupIndex {
				priorityRetry = 0
			}
			logger.LogDebug(param.Ctx, "Auto selecting group: %s, priorityRetry: %d", autoGroup, priorityRetry)

			channel, err = model.GetRandomSatisfiedChannelPinned(autoGroup, routingModel, priorityRetry, param.RequestPath, blockedChannels, v4OfficialPin(param), videoRequestOnly(param))
			if err != nil {
				lastAutoGroupSelectionErr = err
				lastAutoGroupSelectionErrGroup = autoGroup
				channel = nil
			}
			if channel == nil {
				// Current group has no available channel for this model, try next group
				// 当前分组没有该模型的可用渠道，尝试下一个分组
				logger.LogDebug(param.Ctx, "No available channel in group %s for model %s at priorityRetry %d, trying next group", autoGroup, param.ModelName, priorityRetry)
				// 重置状态以尝试下一个分组
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i+1)
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupRetryIndex, 0)
				// Reset retry counter so outer loop can continue for next group
				// 重置重试计数器，以便外层循环可以为下一个分组继续
				param.SetRetry(0)
				continue
			}
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, autoGroup)
			setResolvedModelContext(param.Ctx, routingModel, compactAlias)
			selectGroup = autoGroup
			logger.LogDebug(param.Ctx, "Auto selected group: %s", autoGroup)

			// Prepare state for next retry
			// 为下一次重试准备状态
			if crossGroupRetry && priorityRetry >= common.RetryTimes {
				// Current group has exhausted all retries, prepare to switch to next group
				// This request still uses current group, but next retry will use next group
				// 当前分组已用完所有重试次数，准备切换到下一个分组
				// 本次请求仍使用当前分组，但下次重试将使用下一个分组
				logger.LogDebug(param.Ctx, "Current group %s retries exhausted (priorityRetry=%d >= RetryTimes=%d), preparing switch to next group for next retry", autoGroup, priorityRetry, common.RetryTimes)
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i+1)
				// Reset retry counter so outer loop can continue for next group
				// 重置重试计数器，以便外层循环可以为下一个分组继续
				param.SetRetry(0)
				param.ResetRetryNextTry()
			} else {
				// Stay in current group, save current state
				// 保持在当前分组，保存当前状态
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i)
			}
			break
		}
	} else {
		if policyLoaded && policy.BlocksGroup(param.TokenGroup) {
			return nil, param.TokenGroup, &ChannelSelectionError{
				Kind:  ChannelSelectionAccessDenied,
				Group: param.TokenGroup,
				Model: param.ModelName,
				Err:   errors.New("group is blocked by group access policy"),
			}
		}
		routingModel, compactAlias := model.ResolveCompactModelAliasForGroupPathWithBlockedChannels(param.TokenGroup, param.ModelName, param.RequestPath, blockedChannels)
		if policyLoaded && policy.BlocksModel(routingModel) {
			return nil, param.TokenGroup, &ChannelSelectionError{
				Kind:  ChannelSelectionAccessDenied,
				Group: param.TokenGroup,
				Model: param.ModelName,
				Err:   errors.New("model is blocked by group access policy"),
			}
		}
		selectionRetry := param.GetRetry()
		if len(param.excludedChannelIDs) > 0 {
			selectionRetry = 0
		}
		channel, err = model.GetRandomSatisfiedChannelPinned(param.TokenGroup, routingModel, selectionRetry, param.RequestPath, blockedChannels, v4OfficialPin(param), videoRequestOnly(param))
		if err != nil {
			return nil, param.TokenGroup, &ChannelSelectionError{
				Kind:  ChannelSelectionInternalError,
				Group: param.TokenGroup,
				Model: param.ModelName,
				Err:   err,
			}
		}
		if channel != nil {
			setResolvedModelContext(param.Ctx, routingModel, compactAlias)
		}
	}
	if channel == nil {
		if lastAutoGroupSelectionErr != nil {
			return nil, lastAutoGroupSelectionErrGroup, &ChannelSelectionError{
				Kind:  ChannelSelectionInternalError,
				Group: lastAutoGroupSelectionErrGroup,
				Model: param.ModelName,
				Err:   lastAutoGroupSelectionErr,
			}
		}
		return nil, selectGroup, classifyChannelSelectionFailure(param, selectGroup)
	}
	return channel, selectGroup, nil
}

func classifyChannelSelectionFailure(param *RetryParam, selectedGroup string) *ChannelSelectionError {
	groups := []string{selectedGroup}
	if param.TokenGroup == "auto" {
		groups = GetRequestAutoGroups(param.Ctx, common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup))
	}
	blockedChannels := GroupAccessPolicyBlockedChannels(param.Ctx)
	policy, policyLoaded := GetGroupAccessPolicy(param.Ctx)
	configured := false
	enabled := false
	accessDenied := false
	for _, group := range groups {
		routingModel, _ := model.ResolveCompactModelAliasForGroupPathWithBlockedChannels(group, param.ModelName, param.RequestPath, blockedChannels)
		if policyLoaded && (policy.BlocksGroup(group) || policy.BlocksModel(routingModel)) {
			accessDenied = true
			continue
		}
		groupConfigured, groupEnabled, permittedEnabled, err := model.GetGroupModelAvailabilityForPath(group, routingModel, param.RequestPath, blockedChannels)
		if err != nil {
			return &ChannelSelectionError{Kind: ChannelSelectionInternalError, Group: group, Model: param.ModelName, Err: err}
		}
		configured = configured || groupConfigured
		enabled = enabled || groupEnabled
		if permittedEnabled {
			return &ChannelSelectionError{Kind: ChannelSelectionTemporarilyUnavailable, Group: group, Model: param.ModelName}
		}
	}
	if enabled {
		return &ChannelSelectionError{Kind: ChannelSelectionAccessDenied, Group: selectedGroup, Model: param.ModelName}
	}
	if accessDenied {
		return &ChannelSelectionError{Kind: ChannelSelectionAccessDenied, Group: selectedGroup, Model: param.ModelName}
	}
	if configured {
		return &ChannelSelectionError{Kind: ChannelSelectionTemporarilyUnavailable, Group: selectedGroup, Model: param.ModelName}
	}
	return &ChannelSelectionError{Kind: ChannelSelectionModelNotConfigured, Group: selectedGroup, Model: param.ModelName}
}

func pinnedTaskPluginIdentities(c *gin.Context, expected string) ([]int, []string) {
	if c == nil || expected == "" {
		return nil, nil
	}
	if value, exists := c.Get(jsplugin.ContextKeyPinnedEndpoint); exists {
		pinned, ok := value.(jsplugin.PinnedEndpoint)
		if ok && pinned.Generation != nil && len(pinned.Candidates) > 1 {
			expectedFound := false
			channelTypes := make([]int, 0, len(pinned.Candidates))
			pluginKeys := make([]string, 0, len(pinned.Candidates))
			seen := make(map[int]struct{}, len(pinned.Candidates))
			for _, candidate := range pinned.Candidates {
				if candidate.Plugin == nil {
					continue
				}
				if candidate.Plugin.Meta.Key == expected {
					expectedFound = true
				}
				pluginKeys = append(pluginKeys, candidate.Plugin.Meta.Key)
				for _, channelType := range candidate.Plugin.Meta.ChannelTypes {
					if channelType == 0 || channelType == constant.ChannelTypeTaskPlugin {
						continue
					}
					if _, duplicate := seen[channelType]; duplicate {
						continue
					}
					if plugin, indexed := pinned.Generation.GetByChannelType(channelType); indexed && plugin == candidate.Plugin {
						seen[channelType] = struct{}{}
						channelTypes = append(channelTypes, channelType)
					}
				}
			}
			if expectedFound {
				return channelTypes, pluginKeys
			}
		}
	}
	value, exists := c.Get(jsplugin.ContextKeyPinnedPlugin)
	pinned, ok := value.(jsplugin.PinnedPlugin)
	if !exists || !ok || pinned.Generation == nil || pinned.Plugin == nil || pinned.Plugin.Meta.Key != expected {
		return nil, nil
	}
	channelTypes := make([]int, 0, len(pinned.Plugin.Meta.ChannelTypes))
	for _, channelType := range pinned.Plugin.Meta.ChannelTypes {
		if channelType == 0 || channelType == constant.ChannelTypeTaskPlugin {
			continue
		}
		channelTypes = append(channelTypes, channelType)
	}
	return channelTypes, []string{expected}
}

func setResolvedModelContext(c *gin.Context, modelName string, compactAlias bool) {
	if c == nil || !compactAlias {
		if c != nil {
			common.SetContextKey(c, constant.ContextKeyResolvedModel, "")
		}
		return
	}
	common.SetContextKey(c, constant.ContextKeyResolvedModel, modelName)
}

// ChannelSelectError explains why SelectChannelForRequest found no channel.
// Callers render it for their transport: the HTTP distributor localizes
// MessageID with its own helpers and the Responses WebSocket relay wraps it in
// a NewAPIError. Message is set instead of MessageID when the text is a fixed
// error code that clients match on.
type ChannelSelectError struct {
	StatusCode int
	Code       types.ErrorCode
	MessageID  string
	Params     map[string]any
	Message    string
	Err        error
	// FilterKind and Channel identify a candidate rejected by request filters.
	FilterKind dto.ChannelFilterKind
	Channel    *model.Channel
	// NoAvailableChannel marks the "no channel for this group and model"
	// outcome so the distributor can name the claiming task plugin.
	NoAvailableChannel bool
}

func (e *ChannelSelectError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Message
}

func (e *ChannelSelectError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// SelectRandomChannelForRequest selects a non-pinned candidate and applies all
// request-scoped channel filters before returning it. A candidate rejected by
// a filter is excluded only for this request, so the cache selector can
// continue through the remaining priorities and auto groups.
func SelectRandomChannelForRequest(c *gin.Context, modelName string, retry *RetryParam) (*model.Channel, string, *ChannelSelectError) {
	constraints := GetChannelConstraints(c)
	usingGroup := retry.TokenGroup
	for {
		channel, selectGroup, err := CacheGetRandomSatisfiedChannel(retry)
		if err != nil {
			showGroup := usingGroup
			if usingGroup == "auto" {
				showGroup = fmt.Sprintf("auto(%s)", selectGroup)
			}
			return nil, selectGroup, &ChannelSelectError{
				StatusCode: http.StatusServiceUnavailable, Code: types.ErrorCodeModelNotFound, MessageID: i18n.MsgDistributorGetChannelFailed,
				Params: map[string]any{"Group": showGroup, "Model": modelName, "Error": err.Error()}, Err: err,
			}
		}
		if channel == nil {
			return nil, selectGroup, &ChannelSelectError{
				StatusCode: http.StatusServiceUnavailable, Code: types.ErrorCodeModelNotFound, MessageID: i18n.MsgDistributorNoAvailableChannel,
				Params: map[string]any{"Group": usingGroup, "Model": modelName}, NoAvailableChannel: true,
			}
		}
		if ok, kind := model.ChannelSatisfiesFilters(channel, modelName, constraints.Filters); ok {
			return channel, selectGroup, nil
		} else {
			if retry.IsChannelExcluded(channel.Id) {
				return nil, selectGroup, &ChannelSelectError{
					StatusCode: http.StatusServiceUnavailable, Code: types.ErrorCodeModelNotFound, MessageID: i18n.MsgDistributorNoAvailableChannel,
					Params: map[string]any{"Group": usingGroup, "Model": modelName}, FilterKind: kind, NoAvailableChannel: true,
				}
			}
			retry.ExcludeChannel(channel.Id)
			logger.LogDebug(c, "channel %d rejected by request filter %s, trying another candidate", channel.Id, kind)
		}
	}
}

// SelectChannelForRequest resolves the channel for one attempt with the rules
// shared by the HTTP distributor and the Responses WebSocket relay: a pinned
// channel wins, then session affinity (first attempt only), then a random
// eligible channel; every candidate must satisfy the request's channel
// filters. The group the channel was chosen from is returned for auto-group
// callers. The caller still applies SetupContextForSelectedChannel.
func SelectChannelForRequest(c *gin.Context, modelName string, retry *RetryParam) (*model.Channel, string, *ChannelSelectError) {
	constraints := GetChannelConstraints(c)
	if pin, found, overridden := constraints.ResolvedPin(); found {
		for _, lost := range overridden {
			logger.LogWarn(c, fmt.Sprintf(
				"channel pin overridden: winning_source=%s winning_channel_id=%d overridden_source=%s overridden_channel_id=%d",
				pin.Source, pin.ChannelId, lost.Source, lost.ChannelId,
			))
		}
		channel, err := model.CacheGetChannel(pin.ChannelId)
		if err != nil {
			return nil, "", pinnedChannelUnavailable(pin, http.StatusBadRequest, i18n.MsgDistributorInvalidChannelId)
		}
		if channel.Status != common.ChannelStatusEnabled {
			return nil, "", pinnedChannelUnavailable(pin, http.StatusForbidden, i18n.MsgDistributorChannelDisabled)
		}
		if ok, kind := model.ChannelSatisfiesFilters(channel, modelName, constraints.Filters); !ok {
			return nil, "", &ChannelSelectError{
				StatusCode: http.StatusBadRequest, Code: types.ErrorCode(kind), MessageID: i18n.MsgDistributorNoAvailableChannel,
				Params:     map[string]any{"Group": common.GetContextKeyString(c, constant.ContextKeyUsingGroup), "Model": modelName},
				FilterKind: kind, Channel: channel,
			}
		}
		return channel, "", nil
	}

	usingGroup := retry.TokenGroup
	var channel *model.Channel
	var selectGroup string
	if retry.GetRetry() == 0 {
		if preferredChannelID, found := GetPreferredChannelByAffinity(c, modelName, usingGroup); found {
			affinityUsable := false
			preferred, err := model.CacheGetChannel(preferredChannelID)
			affinitySatisfied := false
			if err == nil && preferred != nil && preferred.Status == common.ChannelStatusEnabled &&
				!GroupAccessPolicyBlocksChannel(c, preferred.Id) {
				affinitySatisfied, _ = model.ChannelSatisfiesFilters(preferred, modelName, constraints.Filters)
			}
			if affinitySatisfied {
				if usingGroup == "auto" {
					userGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
					for _, g := range GetRequestAutoGroups(c, userGroup) {
						routingModel, compactAlias := model.ResolveCompactModelAliasForGroupPath(g, modelName, c.Request.URL.Path)
						if GroupAccessPolicyAllowsGroup(c, g) &&
							!GroupAccessPolicyBlocksModel(c, routingModel) &&
							model.IsChannelEnabledForGroupModel(g, routingModel, preferred.Id) {
							selectGroup = g
							common.SetContextKey(c, constant.ContextKeyAutoGroup, g)
							setResolvedModelContext(c, routingModel, compactAlias)
							channel = preferred
							affinityUsable = true
							MarkChannelAffinityUsed(c, g, preferred.Id)
							break
						}
					}
				} else {
					routingModel, compactAlias := model.ResolveCompactModelAliasForGroupPath(usingGroup, modelName, c.Request.URL.Path)
					if GroupAccessPolicyAllowsGroup(c, usingGroup) &&
						!GroupAccessPolicyBlocksModel(c, routingModel) &&
						model.IsChannelEnabledForGroupModel(usingGroup, routingModel, preferred.Id) {
						channel = preferred
						selectGroup = usingGroup
						setResolvedModelContext(c, routingModel, compactAlias)
						affinityUsable = true
						MarkChannelAffinityUsed(c, usingGroup, preferred.Id)
					}
				}
			}
			if !affinityUsable && !ShouldKeepChannelAffinityOnChannelDisabled() {
				ClearCurrentChannelAffinityCache(c)
			}
			if !affinityUsable && RequestPolicy(c).SessionMode == "strict" {
				return nil, "", &ChannelSelectError{StatusCode: http.StatusServiceUnavailable, Message: "strict_session_binding_unavailable"}
			}
		}
	}

	if channel == nil {
		var selectErr *ChannelSelectError
		channel, selectGroup, selectErr = SelectRandomChannelForRequest(c, modelName, retry)
		if selectErr != nil {
			return nil, selectGroup, selectErr
		}
	}
	if ok, kind := model.ChannelSatisfiesFilters(channel, modelName, constraints.Filters); !ok {
		return nil, selectGroup, &ChannelSelectError{
			StatusCode: http.StatusServiceUnavailable, Code: types.ErrorCodeModelNotFound, MessageID: i18n.MsgDistributorNoAvailableChannel,
			Params:     map[string]any{"Group": common.GetContextKeyString(c, constant.ContextKeyUsingGroup), "Model": modelName},
			FilterKind: kind, Channel: channel, NoAvailableChannel: true,
		}
	}
	return channel, selectGroup, nil
}

// Origin-task pins report a fixed code so task polling can tell a retired
// channel from a malformed request.
func pinnedChannelUnavailable(pin dto.ChannelPin, statusCode int, messageID string) *ChannelSelectError {
	if pin.Source == dto.PinSourceOriginTask {
		return &ChannelSelectError{StatusCode: http.StatusBadRequest, Code: "origin_task_channel_disabled", Message: "origin_task_channel_disabled"}
	}
	return &ChannelSelectError{StatusCode: statusCode, MessageID: messageID}
}

// AppendUsedChannel records an attempted channel in the request's channel
// trail, which the retry log and the consume log's admin_info both read.
func AppendUsedChannel(c *gin.Context, channelID int) {
	c.Set("use_channel", append(c.GetStringSlice("use_channel"), fmt.Sprintf("%d", channelID)))
}
