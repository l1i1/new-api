package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

// The admin surface for targeted request capture. Captures are files on the master, so these two
// endpoints read that directory rather than a database: an operator lists what is there, then reads
// one file. Both are admin-only, because the contents are customer prompts.

func ListRequestCaptures(c *gin.Context) {
	setting := operation_setting.GetRequestCaptureSetting()
	limit := 200
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	files, err := service.ListRequestCaptures(setting, limit)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"enabled":         setting.Enabled,
			"directory":       setting.EffectiveDirectory(),
			"retention_hours": setting.EffectiveRetentionHours(),
			"files":           files,
		},
	})
}

func GetRequestCapture(c *gin.Context) {
	name := c.Query("name")
	if name == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "missing param: name"})
		return
	}
	raw, err := service.ReadRequestCapture(operation_setting.GetRequestCaptureSetting(), name)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"name": name, "content": string(raw)}})
}

// DeleteRequestCaptures removes one capture by name, or all of them when no name is given. It is the
// counterpart to capturing: an operator who took a copy of a customer's request must be able to take
// it back without waiting out the retention window.
func DeleteRequestCaptures(c *gin.Context) {
	name := c.Query("name")
	removed, err := service.DeleteRequestCapture(operation_setting.GetRequestCaptureSetting(), name)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	scope := "all captures"
	if name != "" {
		scope = name
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"removed": removed, "scope": scope}})
}
