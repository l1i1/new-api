package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type userGroupsResponse struct {
	Success bool                              `json:"success"`
	Data    map[string]map[string]interface{} `json:"data"`
}

func configureGeoComplianceDiscoveryTest(t *testing.T) {
	t.Helper()

	originalMax := setting.GetMaxTokenAutoGroups()
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalRatios := ratio_setting.GroupRatio2JSONString()
	originalSpecialGroups := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.ReadAll()
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateMaxTokenAutoGroups(fmt.Sprintf("%d", originalMax)))
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalRatios))
		specialGroups := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup
		specialGroups.Clear()
		specialGroups.AddAll(originalSpecialGroups)
	})

	require.NoError(t, setting.UpdateMaxTokenAutoGroups("5"))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["gpt-premium","safe"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(
		`{"auto":"Auto","default":"Default","safe":"Safe","gpt-premium":"GPT","genpic":"Images"}`,
	))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(
		`{"default":1,"safe":1,"gpt-premium":1,"genpic":1}`,
	))
	ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.Clear()
}

func newGeoComplianceContext(target string, china bool) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, target, nil)
	if china {
		context.Request.Header.Set("CF-IPCountry", "CN")
	} else {
		context.Request.Header.Set("CF-IPCountry", "US")
		context.Request.RemoteAddr = ""
	}
	return context, recorder
}

func createGeoComplianceDiscoveryFixtures(t *testing.T) {
	t.Helper()

	require.NoError(t, model.DB.Create(&model.User{
		Id:       3101,
		Username: "geo-compliance-user",
		Password: "password",
		Group:    "default",
		Status:   common.UserStatusEnabled,
	}).Error)
	require.NoError(t, model.DB.Create(&[]model.Ability{
		{Group: "default", Model: "default-safe-model", ChannelId: 1, Enabled: true},
		{Group: "safe", Model: "safe-model", ChannelId: 1, Enabled: true},
		{Group: "safe", Model: "gpt-hidden-model", ChannelId: 2, Enabled: true},
		{Group: "gpt-premium", Model: "safe-model-in-restricted-group", ChannelId: 1, Enabled: true},
		{Group: "genpic", Model: "image-model", ChannelId: 1, Enabled: true},
	}).Error)
}

func TestCompliancePredicatesPreserveOrder(t *testing.T) {
	assert.True(t, isComplianceRestrictedModel("Vendor-GPT-Model"))
	assert.False(t, isComplianceRestrictedModel("deepseek-v4"))
	assert.True(t, isComplianceRestrictedGroup("GenPic-Pro"))
	assert.Equal(t, []string{"safe", "default"}, filterComplianceGroups(
		[]string{"gpt-premium", "safe", "genpic", "default"},
	))
	assert.Equal(t, []string{"safe-model", "image-model"}, filterComplianceModels(
		[]string{"gpt-5", "safe-model", "Claude-4", "image-model"},
	))
}

func TestCompliancePredicatesUseConfiguredKeywords(t *testing.T) {
	withComplianceOptions(t, map[string]string{
		setting.ComplianceGeoIPModelKeywordsOptionKey: "deepseek,qwen",
		setting.ComplianceGeoIPGroupKeywordsOptionKey: "vision,video",
	})

	assert.True(t, isComplianceRestrictedModel("deepseek-v4"))
	assert.False(t, isComplianceRestrictedModel("gpt-5"))
	assert.True(t, isComplianceRestrictedGroup("vision-premium"))
	assert.False(t, isComplianceRestrictedGroup("genpic"))
}

func TestGetUserGroupsFiltersChinaGroupsAndEmptyAuto(t *testing.T) {
	configureGeoComplianceDiscoveryTest(t)
	setupModelListControllerTestDB(t)
	createGeoComplianceDiscoveryFixtures(t)

	context, recorder := newGeoComplianceContext("/api/user/self/groups", true)
	context.Set("id", 3101)
	GetUserGroups(context)

	var response userGroupsResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Contains(t, response.Data, "default")
	assert.Contains(t, response.Data, "safe")
	assert.Contains(t, response.Data, "auto")
	assert.NotContains(t, response.Data, "gpt-premium")
	assert.NotContains(t, response.Data, "genpic")
	assert.Equal(t, "cn", recorder.Header().Get("X-Compliance-Filtered"))
	assert.Contains(t, recorder.Header().Get("Cache-Control"), "no-store")

	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["gpt-premium"]`))
	emptyContext, emptyRecorder := newGeoComplianceContext("/api/user/self/groups", true)
	emptyContext.Set("id", 3101)
	GetUserGroups(emptyContext)
	response = userGroupsResponse{}
	require.NoError(t, common.Unmarshal(emptyRecorder.Body.Bytes(), &response))
	assert.NotContains(t, response.Data, "auto")
}

func TestGetUserGroupsKeepsForeignGroups(t *testing.T) {
	configureGeoComplianceDiscoveryTest(t)
	setupModelListControllerTestDB(t)
	createGeoComplianceDiscoveryFixtures(t)

	context, recorder := newGeoComplianceContext("/api/user/self/groups", false)
	context.Set("id", 3101)
	GetUserGroups(context)

	var response userGroupsResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Contains(t, response.Data, "gpt-premium")
	assert.Contains(t, response.Data, "genpic")
	assert.Empty(t, recorder.Header().Get("X-Compliance-Filtered"))
}

func TestGetUserModelsFiltersChinaAutoGroupsAndModels(t *testing.T) {
	configureGeoComplianceDiscoveryTest(t)
	setupModelListControllerTestDB(t)
	createGeoComplianceDiscoveryFixtures(t)

	context, recorder := newGeoComplianceContext("/api/user/models?group=auto", true)
	context.Set("id", 3101)
	GetUserModels(context)

	models := decodeUserModelsResponse(t, recorder)
	assert.ElementsMatch(t, []string{"safe-model"}, models)
	assert.Equal(t, "cn", recorder.Header().Get("X-Compliance-Filtered"))

	restrictedContext, restrictedRecorder := newGeoComplianceContext("/api/user/models?group=gpt-premium", true)
	restrictedContext.Set("id", 3101)
	GetUserModels(restrictedContext)
	assert.Empty(t, decodeUserModelsResponse(t, restrictedRecorder))
}

func TestListModelsFiltersChinaAutoGroupsAndModels(t *testing.T) {
	configureGeoComplianceDiscoveryTest(t)
	setupModelListControllerTestDB(t)
	createGeoComplianceDiscoveryFixtures(t)
	withSelfUseModeEnabled(t)

	context, recorder := newGeoComplianceContext("/v1/models", true)
	common.SetContextKey(context, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(context, constant.ContextKeyTokenGroup, "auto")
	common.SetContextKey(context, constant.ContextKeyTokenAutoGroups, []string{"gpt-premium", "safe"})
	ListModels(context, constant.ChannelTypeOpenAI)

	models := decodeListModelsResponse(t, recorder)
	assert.Equal(t, map[string]struct{}{"safe-model": {}}, models)
	assert.Equal(t, "cn", recorder.Header().Get("X-Compliance-Filtered"))

	foreignContext, foreignRecorder := newGeoComplianceContext("/v1/models", false)
	common.SetContextKey(foreignContext, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(foreignContext, constant.ContextKeyTokenGroup, "auto")
	common.SetContextKey(foreignContext, constant.ContextKeyTokenAutoGroups, []string{"gpt-premium", "safe"})
	ListModels(foreignContext, constant.ChannelTypeOpenAI)
	foreignModels := decodeListModelsResponse(t, foreignRecorder)
	assert.Equal(t, map[string]struct{}{
		"safe-model":                     {},
		"gpt-hidden-model":               {},
		"safe-model-in-restricted-group": {},
	}, foreignModels)
}

func TestListModelsFiltersChinaAcrossProtocolRepresentations(t *testing.T) {
	configureGeoComplianceDiscoveryTest(t)
	setupModelListControllerTestDB(t)
	createGeoComplianceDiscoveryFixtures(t)
	withSelfUseModeEnabled(t)

	tests := []struct {
		name      string
		modelType int
	}{
		{name: "OpenAI", modelType: constant.ChannelTypeOpenAI},
		{name: "Anthropic", modelType: constant.ChannelTypeAnthropic},
		{name: "Gemini", modelType: constant.ChannelTypeGemini},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			context, recorder := newGeoComplianceContext("/v1/models", true)
			common.SetContextKey(context, constant.ContextKeyUserGroup, "default")
			common.SetContextKey(context, constant.ContextKeyTokenGroup, "auto")
			common.SetContextKey(context, constant.ContextKeyTokenAutoGroups, []string{"gpt-premium", "safe"})
			ListModels(context, test.modelType)

			var modelIDs []string
			switch test.modelType {
			case constant.ChannelTypeAnthropic:
				var response struct {
					Data []dto.AnthropicModel `json:"data"`
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				for _, item := range response.Data {
					modelIDs = append(modelIDs, item.ID)
				}
			case constant.ChannelTypeGemini:
				var response struct {
					Models []struct {
						Name string `json:"name"`
					} `json:"models"`
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				for _, item := range response.Models {
					modelIDs = append(modelIDs, item.Name)
				}
			default:
				response := decodeListModelsPayload(t, recorder)
				for _, item := range response.Data {
					modelIDs = append(modelIDs, item.Id)
				}
			}

			assert.Equal(t, []string{"safe-model"}, modelIDs)
			assert.Equal(t, "cn", recorder.Header().Get("X-Compliance-Filtered"))
		})
	}
}

func TestListModelsReturnsEmptyForChinaRestrictedTokenGroup(t *testing.T) {
	configureGeoComplianceDiscoveryTest(t)
	setupModelListControllerTestDB(t)
	createGeoComplianceDiscoveryFixtures(t)
	withSelfUseModeEnabled(t)

	context, recorder := newGeoComplianceContext("/v1/models", true)
	common.SetContextKey(context, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(context, constant.ContextKeyTokenGroup, "gpt-premium")
	ListModels(context, constant.ChannelTypeOpenAI)

	assert.Empty(t, decodeListModelsResponse(t, recorder))
}

func TestListModelsKeepsTokenModelLimitAfterChinaFiltering(t *testing.T) {
	configureGeoComplianceDiscoveryTest(t)
	setupModelListControllerTestDB(t)
	createGeoComplianceDiscoveryFixtures(t)
	withSelfUseModeEnabled(t)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "safe", Model: "safe-model-denied-by-token", ChannelId: 3, Enabled: true,
	}).Error)

	context, recorder := newGeoComplianceContext("/v1/models", true)
	common.SetContextKey(context, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(context, constant.ContextKeyTokenGroup, "auto")
	common.SetContextKey(context, constant.ContextKeyTokenAutoGroups, []string{"gpt-premium", "safe"})
	common.SetContextKey(context, constant.ContextKeyTokenModelLimitEnabled, true)
	common.SetContextKey(context, constant.ContextKeyTokenModelLimit, map[string]bool{
		"safe-model":       true,
		"gpt-hidden-model": true,
	})
	ListModels(context, constant.ChannelTypeOpenAI)

	assert.Equal(t, map[string]struct{}{"safe-model": {}}, decodeListModelsResponse(t, recorder))
}

func TestRetrieveModelHidesRestrictedModelForChina(t *testing.T) {
	const modelID = "gpt-compliance-direct-model"
	original, existed := openAIModelsMap[modelID]
	openAIModelsMap[modelID] = dto.OpenAIModels{Id: modelID, Object: "model"}
	t.Cleanup(func() {
		if existed {
			openAIModelsMap[modelID] = original
		} else {
			delete(openAIModelsMap, modelID)
		}
	})

	context, recorder := newGeoComplianceContext("/v1/models/"+modelID, true)
	context.Params = gin.Params{{Key: "model", Value: modelID}}
	RetrieveModel(context, constant.ChannelTypeOpenAI)

	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		ID string `json:"id"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "model_not_found", response.Error.Code)
	assert.Equal(t, "cn", recorder.Header().Get("X-Compliance-Filtered"))

	foreignContext, foreignRecorder := newGeoComplianceContext("/v1/models/"+modelID, false)
	foreignContext.Params = gin.Params{{Key: "model", Value: modelID}}
	RetrieveModel(foreignContext, constant.ChannelTypeOpenAI)
	response.Error.Code = ""
	response.ID = ""
	require.NoError(t, common.Unmarshal(foreignRecorder.Body.Bytes(), &response))
	assert.Equal(t, modelID, response.ID)
}
