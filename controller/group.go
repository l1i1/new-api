package controller

import (
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

func GetGroups(c *gin.Context) {
	groupNames := make([]string, 0)
	for groupName := range ratio_setting.GetGroupRatioCopy() {
		groupNames = append(groupNames, groupName)
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    groupNames,
	})
}

func GetUserGroups(c *gin.Context) {
	usableGroups := make(map[string]map[string]interface{})
	userGroup := ""
	userId := c.GetInt("id")
	policyLoaded := false
	userGroup, _ = model.GetUserGroup(userId, false)
	if userId > 0 {
		if err := service.LoadGroupAccessPolicy(c, userGroup); err != nil {
			common.SysLog(fmt.Sprintf("GetUserGroups GetCachedGroupAccessPolicy error: %v", err))
			common.ApiError(c, err)
			return
		}
		policyLoaded = true
	}
	userUsableGroups := service.GetUserUsableGroups(userGroup)
	if userId > 0 {
		userUsableGroups = service.GetUserUsableGroupsForContext(c, userGroup)
	}
	complianceCountry := complianceClientCountry(c)
	setDiscoveryComplianceHeaders(c, complianceCountry)
	for groupName, _ := range ratio_setting.GetGroupRatioCopy() {
		if complianceCountry != "" && isComplianceRestrictedGroup(groupName) {
			continue
		}
		// UserUsableGroups contains the groups that the user can use
		if desc, ok := userUsableGroups[groupName]; ok {
			usableGroups[groupName] = map[string]interface{}{
				"ratio": service.GetUserGroupRatio(userGroup, groupName),
				"desc":  desc,
			}
		}
	}
	if _, ok := userUsableGroups["auto"]; ok {
		autoGroups := service.GetUserAutoGroup(userGroup)
		if userId > 0 {
			autoGroups = service.GetUserAutoGroupForContext(c, userGroup)
		}
		if shouldExposeAutoGroup(policyLoaded, autoGroups, complianceCountry) {
			usableGroups["auto"] = map[string]interface{}{
				"ratio": "自动",
				"desc":  setting.GetUsableGroupDescription("auto"),
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    usableGroups,
	})
}

func shouldExposeAutoGroup(policyLoaded bool, autoGroups []string, complianceCountry string) bool {
	if policyLoaded && len(autoGroups) == 0 {
		return false
	}
	return complianceCountry == "" || len(filterComplianceGroups(autoGroups)) > 0
}
