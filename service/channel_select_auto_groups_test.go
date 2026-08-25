package service

import (
	"fmt"
	"net/http/httptest"
	"strings"
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

func setupChannelSelectAutoGroupsTest(t *testing.T) *gorm.DB {
	t.Helper()

	originalDB := model.DB
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalRetryTimes := common.RetryTimes
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalGroupRatios := ratio_setting.GroupRatio2JSONString()
	originalMaxTokenAutoGroups := setting.GetMaxTokenAutoGroups()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	common.RetryTimes = 0

	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`[]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":2}`))
	require.NoError(t, setting.UpdateMaxTokenAutoGroups("2"))

	t.Cleanup(func() {
		model.DB = originalDB
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.RetryTimes = originalRetryTimes
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalGroupRatios))
		require.NoError(t, setting.UpdateMaxTokenAutoGroups(fmt.Sprintf("%d", originalMaxTokenAutoGroups)))

		if originalMemoryCacheEnabled && originalDB != nil &&
			originalDB.Migrator().HasTable(&model.Channel{}) && originalDB.Migrator().HasTable(&model.Ability{}) {
			model.InitChannelCache()
		}
		sqlDB, err := db.DB()
		if err == nil {
			require.NoError(t, sqlDB.Close())
		}
	})

	return db
}

func createChannelSelectAutoGroupsChannel(t *testing.T, db *gorm.DB, id int, group, modelName string) {
	t.Helper()
	priority := int64(0)
	weight := uint(100)
	require.NoError(t, db.Create(&model.Channel{
		Id:       id,
		Type:     constant.ChannelTypeOpenAI,
		Key:      fmt.Sprintf("key-%d", id),
		Status:   common.ChannelStatusEnabled,
		Name:     fmt.Sprintf("channel-%d", id),
		Weight:   &weight,
		Models:   modelName,
		Group:    group,
		Priority: &priority,
	}).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group:     group,
		Model:     modelName,
		ChannelId: id,
		Enabled:   true,
		Priority:  &priority,
		Weight:    weight,
	}).Error)
}

func useChannelSelectAutoGroupsDatabasePath(t *testing.T) {
	t.Helper()

	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalLogDB := model.LOG_DB
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()
	common.MemoryCacheEnabled = false
	t.Setenv("LOG_SQL_DSN", "")
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, model.InitLogDB())
	t.Cleanup(func() {
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		model.LOG_DB = originalLogDB
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
	})
}

func TestCacheGetRandomSatisfiedChannelUsesTokenOrderWithinGlobalAllowlist(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "auto-groups-runtime-model"
	createChannelSelectAutoGroupsChannel(t, db, 2101, "vip", modelName)
	createChannelSelectAutoGroupsChannel(t, db, 2102, "default", modelName)
	model.InitChannelCache()
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default","vip"]`))

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"vip", "default"})
	common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, true)

	retry := 0
	param := &RetryParam{
		Ctx:         ctx,
		TokenGroup:  "auto",
		ModelName:   modelName,
		RequestPath: "/v1/chat/completions",
		Retry:       &retry,
	}

	first, selectedGroup, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, 2101, first.Id)
	assert.Equal(t, "vip", selectedGroup)
	assert.Equal(t, "vip", common.GetContextKeyString(ctx, constant.ContextKeyAutoGroup))

	param.IncreaseRetry()
	second, selectedGroup, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.Equal(t, 2102, second.Id)
	assert.Equal(t, "default", selectedGroup)
	assert.Equal(t, "default", common.GetContextKeyString(ctx, constant.ContextKeyAutoGroup))

	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`[]`))
	revokedCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(revokedCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(revokedCtx, constant.ContextKeyTokenAutoGroups, []string{"vip", "default"})
	revokedRetry := 0
	revoked, _, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx:         revokedCtx,
		TokenGroup:  "auto",
		ModelName:   modelName,
		RequestPath: "/v1/chat/completions",
		Retry:       &revokedRetry,
	})
	assert.Nil(t, revoked)
	assert.EqualError(t, err, "auto groups is not enabled")
}

func TestCacheGetRandomSatisfiedChannelResolvesCompactAliasPerAutoGroup(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	createChannelSelectAutoGroupsChannel(t, db, 2201, "vip", "gpt-5.6-sol")
	model.InitChannelCache()
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["vip"]`))

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"vip"})

	retry := 0
	channel, selectedGroup, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx:         ctx,
		TokenGroup:  "auto",
		ModelName:   "gpt-5.6-sol-openai-compact",
		RequestPath: "/v1/responses/compact",
		Retry:       &retry,
	})
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 2201, channel.Id)
	assert.Equal(t, "vip", selectedGroup)
	assert.Equal(t, "gpt-5.6-sol", common.GetContextKeyString(ctx, constant.ContextKeyResolvedModel))
}

func TestCacheGetRandomSatisfiedChannelPrefersExactCompactModel(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	createChannelSelectAutoGroupsChannel(t, db, 2301, "default", "gpt-5.5")
	createChannelSelectAutoGroupsChannel(t, db, 2302, "default", "gpt-5.5-openai-compact")
	model.InitChannelCache()

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	retry := 0
	channel, selectedGroup, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx:         ctx,
		TokenGroup:  "default",
		ModelName:   "gpt-5.5-openai-compact",
		RequestPath: "/v1/responses/compact",
		Retry:       &retry,
	})
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 2302, channel.Id)
	assert.Equal(t, "default", selectedGroup)
	assert.Empty(t, common.GetContextKeyString(ctx, constant.ContextKeyResolvedModel))
}

func TestCacheGetRandomSatisfiedChannelRestartsAtHighestRemainingPriorityAfterExclusion(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "retry-exclusion-priority-model"
	for _, channel := range []struct {
		id       int
		priority int64
	}{
		{id: 2351, priority: 9},
		{id: 2352, priority: 8},
		{id: 2353, priority: 7},
	} {
		weight := uint(100)
		require.NoError(t, db.Create(&model.Channel{
			Id: channel.id, Type: constant.ChannelTypeOpenAI, Key: fmt.Sprintf("key-%d", channel.id),
			Status: common.ChannelStatusEnabled, Name: fmt.Sprintf("channel-%d", channel.id),
			Weight: &weight, Models: modelName, Group: "default", Priority: &channel.priority,
		}).Error)
		require.NoError(t, db.Create(&model.Ability{
			Group: "default", Model: modelName, ChannelId: channel.id, Enabled: true, Priority: &channel.priority, Weight: weight,
		}).Error)
	}
	model.InitChannelCache()

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	retry := 1
	param := &RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: modelName, Retry: &retry}
	param.ExcludeChannel(2351)

	channel, selectedGroup, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, "default", selectedGroup)
	assert.Equal(t, 2352, channel.Id)

	useChannelSelectAutoGroupsDatabasePath(t)
	databaseRetry := 1
	databaseParam := &RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: modelName, Retry: &databaseRetry}
	databaseParam.ExcludeChannel(2351)
	channel, selectedGroup, err = CacheGetRandomSatisfiedChannel(databaseParam)
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, "default", selectedGroup)
	assert.Equal(t, 2352, channel.Id)

	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default"]`))
	autoCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(autoCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(autoCtx, constant.ContextKeyTokenAutoGroups, []string{"default"})
	autoRetry := 1
	autoParam := &RetryParam{Ctx: autoCtx, TokenGroup: "auto", ModelName: modelName, Retry: &autoRetry}
	autoParam.ExcludeChannel(2351)

	channel, selectedGroup, err = CacheGetRandomSatisfiedChannel(autoParam)
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, "default", selectedGroup)
	assert.Equal(t, 2352, channel.Id)

	policyCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(policyCtx, constant.ContextKeyGroupAccessPolicy, model.GroupAccessPolicySnapshot{
		GroupName:         "default",
		BlockedChannelIDs: model.GroupAccessPolicyIntList{2351},
	})
	policyRetry := 1
	channel, selectedGroup, err = CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx: policyCtx, TokenGroup: "default", ModelName: modelName, Retry: &policyRetry,
	})
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, "default", selectedGroup)
	assert.Equal(t, 2353, channel.Id)
}
