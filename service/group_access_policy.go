package service

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// LoadGroupAccessPolicy loads one immutable snapshot for the authenticated
// user's stored base group. Routing callers should treat an error as a hard
// failure; moderation callers retain the default-deny behavior when loading
// the exemption is not possible.
func LoadGroupAccessPolicy(c *gin.Context, groupName string) error {
	if c == nil {
		return errors.New("request context is nil")
	}
	groupName = strings.TrimSpace(groupName)
	if groupName == "" {
		return errors.New("user group is empty")
	}
	policy, err := model.GetCachedGroupAccessPolicy(groupName)
	if err != nil {
		return err
	}
	common.SetContextKey(c, constant.ContextKeyGroupAccessPolicy, policy)
	return nil
}

func EnsureGroupAccessPolicy(c *gin.Context) error {
	if _, loaded := GetGroupAccessPolicy(c); loaded {
		return nil
	}
	groupName := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
	if strings.TrimSpace(groupName) == "" {
		return nil
	}
	return LoadGroupAccessPolicy(c, groupName)
}

// GetGroupAccessPolicy returns the request-scoped policy snapshot. A missing
// snapshot means the caller is outside the authenticated relay/discovery
// path, so existing behavior is preserved for internal/admin operations.
func GetGroupAccessPolicy(c *gin.Context) (model.GroupAccessPolicySnapshot, bool) {
	if c == nil {
		return model.GroupAccessPolicySnapshot{}, false
	}
	policy, ok := common.GetContextKeyType[model.GroupAccessPolicySnapshot](c, constant.ContextKeyGroupAccessPolicy)
	return policy, ok
}

func GroupAccessPolicyAllowsGroup(c *gin.Context, groupName string) bool {
	policy, ok := GetGroupAccessPolicy(c)
	return !ok || !policy.BlocksGroup(groupName)
}

func GroupAccessPolicyBlocksModel(c *gin.Context, modelName string) bool {
	policy, ok := GetGroupAccessPolicy(c)
	return ok && policy.BlocksModel(modelName)
}

func GroupAccessPolicyBlocksChannel(c *gin.Context, channelID int) bool {
	policy, ok := GetGroupAccessPolicy(c)
	return ok && policy.BlocksChannel(channelID)
}

// GroupAccessPolicySpecificChannelGroup resolves the target group for an
// admin-only channel pin using the same relationship as normal selection.
// An empty result means the channel cannot serve this request under policy.
func GroupAccessPolicySpecificChannelGroup(c *gin.Context, channel *model.Channel, requestModel, requestPath string) string {
	policy, loaded := GetGroupAccessPolicy(c)
	if channel == nil || (loaded && (policy.BlocksChannel(channel.Id) || policy.BlocksModel(requestModel))) {
		return ""
	}

	baseUserGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
	usingGroup := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	if usingGroup == "" {
		usingGroup = baseUserGroup
	}
	groups := []string{usingGroup}
	if usingGroup == "auto" {
		groups = GetRequestAutoGroups(c, baseUserGroup)
	}
	for _, groupName := range groups {
		if groupName == "" || (loaded && policy.BlocksGroup(groupName)) ||
			!GroupInUserUsableGroupsForContext(c, baseUserGroup, groupName) {
			continue
		}
		routingModel, _ := model.ResolveCompactModelAliasForChannel(channel, requestModel, requestPath)
		if loaded && policy.BlocksModel(routingModel) {
			continue
		}
		if channel.Type == constant.ChannelTypeAdvancedCustom {
			config := channel.GetOtherSettings().AdvancedCustom
			if config == nil || !config.SupportsPathForModel(requestPath, routingModel) {
				continue
			}
		}
		if model.IsChannelEnabledForGroupModel(groupName, routingModel, channel.Id) {
			return groupName
		}
	}
	return ""
}

// GroupAccessPolicyAllowsSpecificChannel checks the admin-only channel pin
// against the same user/group/model relationship used by normal selection.
// Without this check a channel ID could bypass blocked target groups.
func GroupAccessPolicyAllowsSpecificChannel(c *gin.Context, channel *model.Channel, requestModel, requestPath string) bool {
	return GroupAccessPolicySpecificChannelGroup(c, channel, requestModel, requestPath) != ""
}

// GroupAccessPolicyAllowsTaskChannel validates a channel saved on an existing
// task against that task's effective target group. A concrete task group must
// be checked directly; only legacy tasks without one use the current request's
// normal group resolution.
func GroupAccessPolicyAllowsTaskChannel(c *gin.Context, channel *model.Channel, taskGroup, requestModel, requestPath string) bool {
	policy, loaded := GetGroupAccessPolicy(c)
	if !loaded {
		return true
	}
	if channel == nil || policy.BlocksChannel(channel.Id) || policy.BlocksModel(requestModel) {
		return false
	}
	if strings.TrimSpace(taskGroup) == "" || taskGroup == "auto" {
		return GroupAccessPolicyAllowsSpecificChannel(c, channel, requestModel, requestPath)
	}
	if policy.BlocksGroup(taskGroup) || !GroupInUserUsableGroupsForContext(c, common.GetContextKeyString(c, constant.ContextKeyUserGroup), taskGroup) {
		return false
	}
	routingModel, _ := model.ResolveCompactModelAliasForChannel(channel, requestModel, requestPath)
	if policy.BlocksModel(routingModel) {
		return false
	}
	if channel.Type == constant.ChannelTypeAdvancedCustom {
		config := channel.GetOtherSettings().AdvancedCustom
		if config == nil || !config.SupportsPathForModel(requestPath, routingModel) {
			return false
		}
	}
	return model.IsChannelEnabledForGroupModel(taskGroup, routingModel, channel.Id)
}

// GroupAccessPolicyBlockedChannels returns a read-only-by-convention lookup
// used by both memory and database channel selection paths.
func GroupAccessPolicyBlockedChannels(c *gin.Context) map[int]struct{} {
	policy, ok := GetGroupAccessPolicy(c)
	if !ok || len(policy.BlockedChannelIDs) == 0 {
		return nil
	}
	blocked := make(map[int]struct{}, len(policy.BlockedChannelIDs))
	for _, channelID := range policy.BlockedChannelIDs {
		blocked[channelID] = struct{}{}
	}
	return blocked
}

func GroupAccessPolicyModerationDisabled(c *gin.Context) bool {
	policy, ok := GetGroupAccessPolicy(c)
	return ok && policy.ContentModerationDisabled
}

// GroupAccessPolicyAllowsModelForGroups keeps model discovery aligned with
// routing: a blocked model or a model served only by denied groups/channels is
// not advertised.
func GroupAccessPolicyAllowsModelForGroups(c *gin.Context, modelName string, groups []string) bool {
	policy, ok := GetGroupAccessPolicy(c)
	if !ok {
		return true
	}
	if policy.BlocksModel(modelName) {
		return false
	}
	if len(policy.BlockedChannelIDs) == 0 && len(policy.BlockedGroups) == 0 {
		return true
	}
	blockedChannels := policy.BlockedChannelSet()
	for _, groupName := range groups {
		if policy.BlocksGroup(groupName) {
			continue
		}
		resolvedModel, _ := model.ResolveCompactModelAliasForGroupPath(groupName, modelName, "")
		for _, enabledModel := range model.GetGroupEnabledModelsWithBlockedChannels(groupName, blockedChannels) {
			if model.GroupAccessPolicyModelsMatch(enabledModel, modelName) ||
				model.GroupAccessPolicyModelsMatch(enabledModel, resolvedModel) {
				return true
			}
		}
	}
	return false
}
