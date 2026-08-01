package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupGroupModelRateLimitControllerTest(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousRedisEnabled := common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.GroupModelRateLimit{}))
	model.DB = db
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedisEnabled
	})
}

func groupModelRateLimitContext(t *testing.T, method, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, "/api/group-model-rate-limits", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, recorder
}

func TestGetGroupModelRateLimitsReturnsSavedRules(t *testing.T) {
	setupGroupModelRateLimitControllerTest(t)
	_, err := model.ReplaceGroupModelRateLimits([]model.GroupModelRateLimit{{
		GroupName: "default", ModelName: "gpt-test", WindowSeconds: 60, MaxRequests: 5, Enabled: true,
	}})
	require.NoError(t, err)
	c, recorder := groupModelRateLimitContext(t, http.MethodGet, "")
	GetGroupModelRateLimits(c)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "gpt-test")
}

func TestReplaceGroupModelRateLimitsRejectsDuplicateRulesWithoutPartialWrite(t *testing.T) {
	setupGroupModelRateLimitControllerTest(t)
	c, recorder := groupModelRateLimitContext(t, http.MethodPut, `{"rules":[{"group_name":"default","model_name":"gpt-test","window_seconds":60,"max_requests":1,"enabled":true},{"group_name":"default","model_name":"gpt-test","window_seconds":60,"max_requests":2,"enabled":true}]}`)
	ReplaceGroupModelRateLimits(c)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	var count int64
	require.NoError(t, model.DB.Model(&model.GroupModelRateLimit{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestReplaceGroupModelRateLimitsRejectsMissingRulesArray(t *testing.T) {
	setupGroupModelRateLimitControllerTest(t)
	c, recorder := groupModelRateLimitContext(t, http.MethodPut, `{}`)
	ReplaceGroupModelRateLimits(c)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}
