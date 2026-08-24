package service

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

func GetUserUsableGroups(userGroup string) map[string]string {
	groupsCopy := setting.GetUserUsableGroupsCopy()
	if userGroup != "" {
		specialSettings, b := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.Get(userGroup)
		if b {
			// 处理特殊可用分组
			for specialGroup, desc := range specialSettings {
				if strings.HasPrefix(specialGroup, "-:") {
					// 移除分组
					groupToRemove := strings.TrimPrefix(specialGroup, "-:")
					delete(groupsCopy, groupToRemove)
				} else if strings.HasPrefix(specialGroup, "+:") {
					// 添加分组
					groupToAdd := strings.TrimPrefix(specialGroup, "+:")
					groupsCopy[groupToAdd] = desc
				} else {
					// 直接添加分组
					groupsCopy[specialGroup] = desc
				}
			}
		}
		// 如果userGroup不在UserUsableGroups中，返回UserUsableGroups + userGroup
		if _, ok := groupsCopy[userGroup]; !ok {
			groupsCopy[userGroup] = "用户分组"
		}
	}
	return groupsCopy
}

func GroupInUserUsableGroups(userGroup, groupName string) bool {
	_, ok := GetUserUsableGroups(userGroup)[groupName]
	return ok
}

// GetUserUsableGroupsForContext applies the subject group's deny overlay on
// top of the existing GroupSpecialUsableGroup rules.
func GetUserUsableGroupsForContext(c *gin.Context, userGroup string) map[string]string {
	groups := GetUserUsableGroups(userGroup)
	policy, ok := GetGroupAccessPolicy(c)
	if !ok {
		return groups
	}
	for groupName := range groups {
		if policy.BlocksGroup(groupName) {
			delete(groups, groupName)
		}
	}
	return groups
}

func GroupInUserUsableGroupsForContext(c *gin.Context, userGroup, groupName string) bool {
	_, ok := GetUserUsableGroupsForContext(c, userGroup)[groupName]
	return ok
}

func IsUserSelectableGroup(userGroup, groupName string) bool {
	if groupName == "" || groupName == "auto" {
		return false
	}
	return GroupInUserUsableGroups(userGroup, groupName) && ratio_setting.ContainsGroupRatio(groupName)
}

func IsUserSelectableGroupForContext(c *gin.Context, userGroup, groupName string) bool {
	if groupName == "" || groupName == "auto" {
		return false
	}
	return GroupInUserUsableGroupsForContext(c, userGroup, groupName) && ratio_setting.ContainsGroupRatio(groupName)
}

// GetUserAutoGroup 根据用户分组获取自动分组设置
func GetUserAutoGroup(userGroup string) []string {
	return getUserAutoGroup(userGroup, nil)
}

func getUserAutoGroup(userGroup string, policy *model.GroupAccessPolicySnapshot) []string {
	autoGroups := make([]string, 0)
	seen := make(map[string]struct{})
	for _, group := range setting.GetAutoGroups() {
		if !IsUserSelectableGroup(userGroup, group) {
			continue
		}
		if policy != nil && policy.BlocksGroup(group) {
			continue
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		autoGroups = append(autoGroups, group)
	}
	return autoGroups
}

func GetUserAutoGroupForContext(c *gin.Context, userGroup string) []string {
	policy, ok := GetGroupAccessPolicy(c)
	if !ok {
		return GetUserAutoGroup(userGroup)
	}
	return getUserAutoGroup(userGroup, &policy)
}

// FilterUserTokenAutoGroups applies the current global Auto allowlist and user
// permissions before the current per-token limit. It does not fall back to the
// global order when an explicit token snapshot becomes empty.
func FilterUserTokenAutoGroups(userGroup string, groups []string) []string {
	return filterUserTokenAutoGroups(userGroup, groups, nil)
}

func filterUserTokenAutoGroups(userGroup string, groups []string, policy *model.GroupAccessPolicySnapshot) []string {
	maxCount := setting.GetMaxTokenAutoGroups()
	filtered := make([]string, 0, min(len(groups), maxCount))
	seen := make(map[string]struct{})
	for _, group := range groups {
		if !setting.ContainsAutoGroup(group) || !IsUserSelectableGroup(userGroup, group) {
			continue
		}
		if policy != nil && policy.BlocksGroup(group) {
			continue
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		filtered = append(filtered, group)
		if len(filtered) == maxCount {
			break
		}
	}
	return filtered
}

// GetRequestAutoGroups resolves the ordered Auto groups for the current token.
// The absence of the context value means that the token inherits the complete
// global Auto list; a present (even empty) value is an explicit token snapshot.
func GetRequestAutoGroups(c *gin.Context, userGroup string) []string {
	policy, policyLoaded := GetGroupAccessPolicy(c)
	value, ok := common.GetContextKey(c, constant.ContextKeyTokenAutoGroups)
	if !ok {
		if policyLoaded {
			return getUserAutoGroup(userGroup, &policy)
		}
		return GetUserAutoGroup(userGroup)
	}
	groups, ok := value.([]string)
	if !ok {
		return []string{}
	}
	if policyLoaded {
		return filterUserTokenAutoGroups(userGroup, groups, &policy)
	}
	return FilterUserTokenAutoGroups(userGroup, groups)
}

// GetGroupsEnabledModels 按 groups 顺序获取各分组启用的模型并去重
func GetGroupsEnabledModels(groups []string) []string {
	return getGroupsEnabledModels(groups, nil, nil)
}

func getGroupsEnabledModels(groups []string, blockedChannels map[int]struct{}, policy *model.GroupAccessPolicySnapshot) []string {
	seen := make(map[string]struct{})
	models := make([]string, 0)
	for _, group := range groups {
		if policy != nil && policy.BlocksGroup(group) {
			continue
		}
		for _, modelName := range model.GetGroupEnabledModelsWithBlockedChannels(group, blockedChannels) {
			if policy != nil && policy.BlocksModel(modelName) {
				continue
			}
			if _, ok := seen[modelName]; !ok {
				seen[modelName] = struct{}{}
				models = append(models, modelName)
			}
		}
	}
	return models
}

func GetGroupsEnabledModelsForContext(c *gin.Context, groups []string) []string {
	policy, ok := GetGroupAccessPolicy(c)
	if !ok {
		return GetGroupsEnabledModels(groups)
	}
	return getGroupsEnabledModels(groups, GroupAccessPolicyBlockedChannels(c), &policy)
}

// GetUserGroupRatio 获取用户使用某个分组的倍率
// userGroup 用户分组
// group 需要获取倍率的分组
func GetUserGroupRatio(userGroup, group string) float64 {
	ratio, ok := ratio_setting.GetGroupGroupRatio(userGroup, group)
	if ok {
		return ratio
	}
	return ratio_setting.GetGroupRatio(group)
}
