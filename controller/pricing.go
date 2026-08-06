package controller

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

var pricingChinaModelKeywords = []string{"gpt", "gemini", "claude", "grok"}

var pricingChinaGroupKeywords = []string{"gpt", "gemini", "claude", "grok", "genpic"}

func filterPricingByUsableGroups(pricing []model.Pricing, usableGroup map[string]string) []model.Pricing {
	if len(pricing) == 0 {
		return pricing
	}
	if len(usableGroup) == 0 {
		return []model.Pricing{}
	}

	filtered := make([]model.Pricing, 0, len(pricing))
	for _, item := range pricing {
		if common.StringsContains(item.EnableGroup, "all") {
			filtered = append(filtered, item)
			continue
		}
		for _, group := range item.EnableGroup {
			if _, ok := usableGroup[group]; ok {
				filtered = append(filtered, item)
				break
			}
		}
	}
	return filtered
}

func containsPricingKeyword(value string, keywords []string) bool {
	text := strings.ToLower(value)
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}

func filterPricingForChina(
	pricing []model.Pricing,
	vendors []model.PricingVendor,
	groupRatio map[string]float64,
	usableGroup map[string]string,
	autoGroups []string,
	supportedEndpoint map[string]common.EndpointInfo,
) ([]model.Pricing, []model.PricingVendor, map[string]float64, map[string]string, []string, map[string]common.EndpointInfo) {
	filteredPricing := make([]model.Pricing, 0, len(pricing))
	seenGroups := make(map[string]struct{})
	seenGroupOrder := make([]string, 0)
	seenVendorIDs := make(map[int]struct{})

	for _, item := range pricing {
		if containsPricingKeyword(item.ModelName, pricingChinaModelKeywords) {
			continue
		}

		bannedGroup := false
		for _, group := range item.EnableGroup {
			if containsPricingKeyword(group, pricingChinaGroupKeywords) {
				bannedGroup = true
				break
			}
		}
		if bannedGroup {
			continue
		}

		filteredPricing = append(filteredPricing, item)
		if item.VendorID != 0 {
			seenVendorIDs[item.VendorID] = struct{}{}
		}
		for _, group := range item.EnableGroup {
			if _, seen := seenGroups[group]; seen {
				continue
			}
			seenGroups[group] = struct{}{}
			seenGroupOrder = append(seenGroupOrder, group)
		}
	}

	filteredAutoGroups := make([]string, 0, len(autoGroups))
	allowedGroups := make(map[string]struct{})
	for _, group := range autoGroups {
		if _, seen := seenGroups[group]; !seen || containsPricingKeyword(group, pricingChinaGroupKeywords) {
			continue
		}
		if _, added := allowedGroups[group]; added {
			continue
		}
		filteredAutoGroups = append(filteredAutoGroups, group)
		allowedGroups[group] = struct{}{}
	}
	for _, group := range seenGroupOrder {
		if containsPricingKeyword(group, pricingChinaGroupKeywords) {
			continue
		}
		if _, added := allowedGroups[group]; added {
			continue
		}
		filteredAutoGroups = append(filteredAutoGroups, group)
		allowedGroups[group] = struct{}{}
	}

	filteredGroupRatio := make(map[string]float64)
	for group, ratio := range groupRatio {
		if _, allowed := allowedGroups[group]; allowed {
			filteredGroupRatio[group] = ratio
		}
	}
	filteredUsableGroup := make(map[string]string)
	for group, description := range usableGroup {
		if _, allowed := allowedGroups[group]; allowed {
			filteredUsableGroup[group] = description
		}
	}

	filteredVendors := make([]model.PricingVendor, 0, len(vendors))
	for _, vendor := range vendors {
		if _, seen := seenVendorIDs[vendor.ID]; seen {
			filteredVendors = append(filteredVendors, vendor)
		}
	}

	filteredEndpoints := make(map[string]common.EndpointInfo)
	if endpoint, ok := supportedEndpoint["openai"]; ok {
		filteredEndpoints["openai"] = endpoint
	}

	return filteredPricing, filteredVendors, filteredGroupRatio, filteredUsableGroup, filteredAutoGroups, filteredEndpoints
}

func GetPricing(c *gin.Context) {
	pricing := model.GetPricing()
	vendors := model.GetVendors()
	supportedEndpoint := model.GetSupportedEndpointMap()
	userId, exists := c.Get("id")
	usableGroup := map[string]string{}
	groupRatio := map[string]float64{}
	for s, f := range ratio_setting.GetGroupRatioCopy() {
		groupRatio[s] = f
	}
	var group string
	if exists {
		user, err := model.GetUserCache(userId.(int))
		if err == nil {
			group = user.Group
			for g := range groupRatio {
				ratio, ok := ratio_setting.GetGroupGroupRatio(group, g)
				if ok {
					groupRatio[g] = ratio
				}
			}
		}
	}

	usableGroup = service.GetUserUsableGroups(group)
	pricing = filterPricingByUsableGroups(pricing, usableGroup)
	// check groupRatio contains usableGroup
	for group := range ratio_setting.GetGroupRatioCopy() {
		if _, ok := usableGroup[group]; !ok {
			delete(groupRatio, group)
		}
	}
	autoGroups := service.GetUserAutoGroup(group)
	if isChinaPricingClient(c) {
		pricing, vendors, groupRatio, usableGroup, autoGroups, supportedEndpoint = filterPricingForChina(
			pricing,
			vendors,
			groupRatio,
			usableGroup,
			autoGroups,
			supportedEndpoint,
		)
		c.Header("X-Pricing-Filtered", "cn")
	}

	c.JSON(200, gin.H{
		"success":            true,
		"data":               pricing,
		"vendors":            vendors,
		"group_ratio":        groupRatio,
		"usable_group":       usableGroup,
		"supported_endpoint": supportedEndpoint,
		"auto_groups":        autoGroups,
		"pricing_version":    "a42d372ccf0b5dd13ecf71203521f9d2",
	})
}

func ResetModelRatio(c *gin.Context) {
	defaultStr := ratio_setting.DefaultModelRatio2JSONString()
	err := model.UpdateOption("ModelRatio", defaultStr)
	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	err = ratio_setting.UpdateModelRatioByJSONString(defaultStr)
	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	c.JSON(200, gin.H{
		"success": true,
		"message": "重置模型倍率成功",
	})
}
