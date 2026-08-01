package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

type replaceUserModelRateLimitsRequest struct {
	Rules []model.UserModelRateLimit `json:"rules"`
}

func GetUserModelRateLimits(c *gin.Context) {
	userId, ok := authorizeUserModelRateLimitTarget(c)
	if !ok {
		return
	}
	rules, err := model.GetUserModelRateLimits(userId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, rules)
}

func ReplaceUserModelRateLimits(c *gin.Context) {
	userId, ok := authorizeUserModelRateLimitTarget(c)
	if !ok {
		return
	}

	var request replaceUserModelRateLimitsRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.Rules == nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "rules must be an array"})
		return
	}
	rules, err := model.ReplaceUserModelRateLimits(userId, request.Rules)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	common.ApiSuccess(c, rules)
}

func authorizeUserModelRateLimitTarget(c *gin.Context) (int, bool) {
	userId, err := strconv.Atoi(c.Param("id"))
	if err != nil || userId <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid user id"})
		return 0, false
	}
	target, err := model.GetUserById(userId, false)
	if err != nil {
		common.ApiError(c, err)
		return 0, false
	}
	if !canManageTargetRole(c.GetInt("role"), target.Role) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "insufficient permission to manage this user"})
		return 0, false
	}
	return userId, true
}
