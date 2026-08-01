package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

type replaceGroupModelRateLimitsRequest struct {
	Rules []model.GroupModelRateLimit `json:"rules"`
}

func GetGroupModelRateLimits(c *gin.Context) {
	rules, err := model.GetGroupModelRateLimits()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, rules)
}

func ReplaceGroupModelRateLimits(c *gin.Context) {
	var request replaceGroupModelRateLimitsRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.Rules == nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "rules must be an array"})
		return
	}
	rules, err := model.ReplaceGroupModelRateLimits(request.Rules)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	common.ApiSuccess(c, rules)
}
