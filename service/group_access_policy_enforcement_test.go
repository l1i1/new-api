package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetRequestAutoGroupsExcludesPolicyBlockedGroups(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalRatios := ratio_setting.GroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalRatios))
	})
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default","vip"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":1}`))

	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, constant.ContextKeyGroupAccessPolicy, model.GroupAccessPolicySnapshot{
		GroupName:     "default",
		BlockedGroups: model.GroupAccessPolicyStringList{"vip"},
	})

	require.Equal(t, []string{"default"}, GetRequestAutoGroups(ctx, "default"))
}

func TestGroupAccessPolicySpecificChannelRequiresAllowedTargetGroup(t *testing.T) {
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
	})
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP"}`))
	previousDB := model.DB
	previousMemoryCacheEnabled := common.MemoryCacheEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousMemoryCacheEnabled
	})

	channel := &model.Channel{Group: "vip", Models: "gpt-5.5", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group: channel.GetGroups()[0], Model: "gpt-5.5", ChannelId: channel.Id,
		Enabled: true, Priority: pointerInt64(1),
	}).Error)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
	common.SetContextKey(ctx, constant.ContextKeyGroupAccessPolicy, model.GroupAccessPolicySnapshot{
		GroupName:     "default",
		BlockedGroups: model.GroupAccessPolicyStringList{"vip"},
	})
	assert.False(t, GroupAccessPolicyAllowsSpecificChannel(ctx, channel, "gpt-5.5", "/v1/chat/completions"))
	assert.False(t, GroupAccessPolicyAllowsTaskChannel(ctx, channel, "vip", "gpt-5.5", "/v1/chat/completions"))

	common.SetContextKey(ctx, constant.ContextKeyGroupAccessPolicy, model.GroupAccessPolicySnapshot{GroupName: "default"})
	assert.True(t, GroupAccessPolicyAllowsSpecificChannel(ctx, channel, "gpt-5.5", "/v1/chat/completions"))
	assert.True(t, GroupAccessPolicyAllowsTaskChannel(ctx, channel, "vip", "gpt-5.5", "/v1/chat/completions"))
}

func pointerInt64(value int64) *int64 {
	return &value
}
