package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func GetContentModerationConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"success": true, "data": service.GetContentModerationConfigView()})
}

func UpdateContentModerationConfig(c *gin.Context) {
	var request service.ContentModerationConfig
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid content moderation configuration"})
		return
	}
	current := service.GetContentModerationConfig()
	if request.ClearAPIKeys {
		request.APIKey = ""
		request.APIKeys = nil
	} else if strings.TrimSpace(request.APIKey) == "" && len(request.APIKeys) == 0 {
		request.APIKey = current.APIKey
	}
	if err := service.UpdateContentModerationConfig(request); err != nil {
		if errors.Is(err, service.ErrContentModerationConfigPersistence) {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to persist content moderation configuration"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": service.GetContentModerationConfigView()})
}

func GetContentModerationLogs(c *gin.Context) {
	var userID *int
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid user_id"})
			return
		}
		userID = &value
	}
	pageInfo := common.GetPageQuery(c)
	offset, err := parseNonNegativeQueryInt(c, "offset")
	if err != nil {
		return
	}
	limit, err := parseNonNegativeQueryInt(c, "limit")
	if err != nil {
		return
	}
	if c.Query("offset") == "" {
		offset = pageInfo.GetStartIdx()
	}
	if c.Query("limit") == "" {
		limit = pageInfo.GetPageSize()
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	page := offset/limit + 1
	filter := model.ContentModerationLogFilter{UserID: userID, GroupName: strings.TrimSpace(c.Query("group")), ModelName: strings.TrimSpace(c.Query("model")), Protocol: strings.TrimSpace(c.Query("protocol")), RequestID: strings.TrimSpace(c.Query("request_id")), Offset: offset, Limit: limit}
	if raw := strings.TrimSpace(c.Query("flagged")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid flagged filter"})
			return
		}
		filter.Flagged = &value
	}
	if raw := strings.TrimSpace(c.Query("start_at")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid start_at"})
			return
		}
		filter.StartAt = value
	}
	if raw := strings.TrimSpace(c.Query("end_at")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid end_at"})
			return
		}
		filter.EndAt = value
	}
	logs, total, err := model.QueryContentModerationLogs(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to query content moderation logs"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": logs, "total": total, "offset": offset, "limit": limit, "page": page, "page_size": limit})
}

func UnbanContentModerationUser(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid user id"})
		return
	}
	if model.DB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "database is not initialized"})
		return
	}
	result := model.DB.Model(&model.User{}).
		Where("id = ? AND status <> ?", userID, common.UserStatusEnabled).
		Updates(map[string]interface{}{"status": common.UserStatusEnabled, "auth_version": gorm.Expr("auth_version + ?", 1)})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to unban user"})
		return
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := model.DB.Model(&model.User{}).Where("id = ?", userID).Count(&count).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to unban user"})
			return
		}
		if count == 0 {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "user not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}
	_ = model.InvalidateUserTokensCache(userID)
	_ = model.InvalidateUserCache(userID)
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func ResetContentModerationUserViolations(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid user id"})
		return
	}
	if model.DB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "database is not initialized"})
		return
	}
	var count int64
	if err := model.DB.Model(&model.User{}).Where("id = ?", userID).Count(&count).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to reset violation count"})
		return
	}
	if count == 0 {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "user not found"})
		return
	}
	if err := model.ResetContentModerationUserViolations(userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to reset violation count"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func parseNonNegativeQueryInt(c *gin.Context, key string) (int, error) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid " + key})
		return 0, errors.New("invalid " + key)
	}
	return value, nil
}
