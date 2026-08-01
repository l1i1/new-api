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

func setupUserModelRateLimitControllerTest(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousRedisEnabled := common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserModelRateLimit{}))
	model.DB = db
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedisEnabled
	})
}

func userModelRateLimitContext(t *testing.T, role int, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Params = gin.Params{{Key: "id", Value: "2"}}
	c.Set("role", role)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/user/2/model-rate-limits", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, recorder
}

func TestUserModelRateLimitAdminAPIEnforcesTargetRole(t *testing.T) {
	setupUserModelRateLimitControllerTest(t)
	require.NoError(t, model.DB.Create(&model.User{Id: 2, Username: "admin-target", Role: common.RoleAdminUser}).Error)
	c, recorder := userModelRateLimitContext(t, common.RoleAdminUser, `{"rules":[]}`)
	GetUserModelRateLimits(c)
	assert.Equal(t, http.StatusForbidden, recorder.Code)
}

func TestReplaceUserModelRateLimitsRejectsDuplicateRulesWithoutPartialWrite(t *testing.T) {
	setupUserModelRateLimitControllerTest(t)
	require.NoError(t, model.DB.Create(&model.User{Id: 2, Username: "common-target", Role: common.RoleCommonUser}).Error)
	c, recorder := userModelRateLimitContext(t, common.RoleRootUser, `{"rules":[{"model_name":"gpt-test","window_seconds":60,"max_requests":1,"enabled":true},{"model_name":"gpt-test","window_seconds":60,"max_requests":2,"enabled":true}]}`)
	ReplaceUserModelRateLimits(c)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	var count int64
	require.NoError(t, model.DB.Model(&model.UserModelRateLimit{}).Where("user_id = ?", 2).Count(&count).Error)
	assert.Zero(t, count)
}
