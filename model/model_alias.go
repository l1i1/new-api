package model

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

func resolveCompactModelAlias(modelName string, isAvailable func(string, string) bool) (string, bool) {
	baseModel, hasCompactSuffix := ratio_setting.CompactBaseModelName(modelName)
	if !hasCompactSuffix {
		return modelName, false
	}

	if isModelNameAvailable(modelName, isAvailable) {
		return modelName, false
	}
	if isModelNameAvailable(baseModel, isAvailable) {
		return baseModel, true
	}
	return modelName, false
}

func isModelNameAvailable(modelName string, isAvailable func(string, string) bool) bool {
	if isAvailable(modelName, modelName) {
		return true
	}
	normalizedModel := ratio_setting.FormatMatchingModelName(modelName)
	return normalizedModel != modelName && isAvailable(normalizedModel, modelName)
}

func ResolveCompactModelAliasForGroup(group, modelName string) (string, bool) {
	return ResolveCompactModelAliasForGroupPath(group, modelName, "")
}

func ResolveCompactModelAliasForGroupPath(group, modelName, requestPath string) (string, bool) {
	if group == "" || modelName == "" {
		return modelName, false
	}

	if common.MemoryCacheEnabled {
		channelSyncLock.RLock()
		defer channelSyncLock.RUnlock()
		return resolveCompactModelAlias(modelName, func(candidate, requestModel string) bool {
			return len(filterChannelsByRequestPathAndModel(
				group2model2channels[group][candidate], requestPath, requestModel,
			)) > 0
		})
	}

	return resolveCompactModelAlias(modelName, func(candidate, requestModel string) bool {
		var abilities []Ability
		err := DB.Where(commonGroupCol+" = ? and model = ? and enabled = ?", group, candidate, true).
			Find(&abilities).Error
		if err != nil || len(abilities) == 0 {
			return false
		}
		return len(filterAbilitiesByRequestPathAndModel(abilities, requestPath, requestModel)) > 0
	})
}

func ResolveCompactModelAliasFromModels(modelName string, availableModels []string) (string, bool) {
	available := make(map[string]struct{}, len(availableModels))
	for _, availableModel := range availableModels {
		trimmedModel := strings.TrimSpace(availableModel)
		if trimmedModel != "" {
			available[trimmedModel] = struct{}{}
		}
	}
	return resolveCompactModelAlias(modelName, func(candidate, _ string) bool {
		_, ok := available[candidate]
		return ok
	})
}

func ResolveCompactModelAliasForChannel(channel *Channel, modelName, requestPath string) (string, bool) {
	if channel == nil || modelName == "" {
		return modelName, false
	}

	models := channel.GetModels()
	return resolveCompactModelAlias(modelName, func(candidate, requestModel string) bool {
		for _, configuredModel := range models {
			if strings.TrimSpace(configuredModel) != candidate {
				continue
			}
			if requestPath == "" || channel.Type != constant.ChannelTypeAdvancedCustom {
				return true
			}
			config := channel.GetOtherSettings().AdvancedCustom
			return config != nil && config.SupportsPathForModel(requestPath, requestModel)
		}
		return false
	})
}
