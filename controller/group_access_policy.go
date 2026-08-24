package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

type groupAccessPolicyRequest struct {
	BlockedChannelIDs         []int    `json:"blocked_channel_ids"`
	BlockedModels             []string `json:"blocked_models"`
	BlockedGroups             []string `json:"blocked_groups"`
	ContentModerationDisabled bool     `json:"content_moderation_disabled"`
}

// GetGroupAccessPolicy returns the normalized overlay for one subject group.
// An unconfigured policy is represented by empty deny lists and moderation
// enabled, so callers can use the response without a special null case.
func GetGroupAccessPolicy(c *gin.Context) {
	groupName := c.Param("group")
	if !model.IsConfiguredGroupAccessPolicyGroup(groupName) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "group is not configured"})
		return
	}
	policy, err := model.GetCachedGroupAccessPolicy(groupName)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, policy)
}

// ReplaceGroupAccessPolicy atomically replaces all policy fields for the
// subject group identified by the route parameter. The route group always
// wins over any body fields because the body intentionally has no group_name.
func ReplaceGroupAccessPolicy(c *gin.Context) {
	var request groupAccessPolicyRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid group access policy"})
		return
	}

	policy, err := model.ReplaceGroupAccessPolicy(model.GroupAccessPolicy{
		GroupName:                 c.Param("group"),
		BlockedChannelIDs:         model.GroupAccessPolicyIntList(request.BlockedChannelIDs),
		BlockedModels:             model.GroupAccessPolicyStringList(request.BlockedModels),
		BlockedGroups:             model.GroupAccessPolicyStringList(request.BlockedGroups),
		ContentModerationDisabled: request.ContentModerationDisabled,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	common.ApiSuccess(c, policy)
}
