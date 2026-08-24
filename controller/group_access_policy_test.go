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

func setupGroupAccessPolicyControllerTest(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousRedisEnabled := common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.GroupAccessPolicy{}))
	model.DB = db
	common.RedisEnabled = false
	require.NoError(t, model.InvalidateGroupAccessPolicyCache("default"))
	t.Cleanup(func() {
		_ = model.InvalidateGroupAccessPolicyCache("default")
		model.DB = previousDB
		common.RedisEnabled = previousRedisEnabled
	})
}

func groupAccessPolicyContext(t *testing.T, method, path, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "group", Value: "default"}}
	return c, recorder
}

func TestReplaceAndGetGroupAccessPolicyReturnsNormalizedData(t *testing.T) {
	setupGroupAccessPolicyControllerTest(t)
	putContext, putRecorder := groupAccessPolicyContext(t, http.MethodPut, "/api/group-access-policies/default", `{"blocked_channel_ids":[3,1,3],"blocked_models":[" z-model ","a-model"],"blocked_groups":["vip"],"content_moderation_disabled":true}`)
	ReplaceGroupAccessPolicy(putContext)
	assert.Equal(t, http.StatusOK, putRecorder.Code)
	assert.Contains(t, putRecorder.Body.String(), `"blocked_channel_ids":[1,3]`)
	assert.Contains(t, putRecorder.Body.String(), `"content_moderation_disabled":true`)

	getContext, getRecorder := groupAccessPolicyContext(t, http.MethodGet, "/api/group-access-policies/default", "")
	GetGroupAccessPolicy(getContext)
	assert.Equal(t, http.StatusOK, getRecorder.Code)
	assert.Contains(t, getRecorder.Body.String(), `"blocked_models":["a-model","z-model"]`)
}

func TestReplaceGroupAccessPolicyRejectsUnknownGroupWithoutWrite(t *testing.T) {
	setupGroupAccessPolicyControllerTest(t)
	context, recorder := groupAccessPolicyContext(t, http.MethodPut, "/api/group-access-policies/default", `{"blocked_groups":["unknown"]}`)
	ReplaceGroupAccessPolicy(context)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	var count int64
	require.NoError(t, model.DB.Model(&model.GroupAccessPolicy{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestShouldExposeAutoGroupHidesPolicyBlockedAutoTargets(t *testing.T) {
	assert.False(t, shouldExposeAutoGroup(true, nil, ""))
	assert.True(t, shouldExposeAutoGroup(false, nil, ""))
	assert.True(t, shouldExposeAutoGroup(true, []string{"default"}, ""))
}
