package service

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectChannelForRequestSkipsPolicyBlockedAffinity(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "policy-affinity-model"
	createChannelSelectAutoGroupsChannel(t, db, 2601, "default", modelName)
	createChannelSelectAutoGroupsChannel(t, db, 2602, "default", modelName)
	model.InitChannelCache()

	affinity := operation_setting.GetChannelAffinitySetting()
	previousAffinity := *affinity
	t.Cleanup(func() { *affinity = previousAffinity })
	rule := operation_setting.ChannelAffinityRule{
		Name:              "policy-affinity",
		ModelRegex:        []string{"^" + modelName + "$"},
		KeySources:        []operation_setting.ChannelAffinityKeySource{{Type: "request_header", Key: "X-Session"}},
		IncludeRuleName:   true,
		IncludeModelName:  true,
		IncludeUsingGroup: true,
		SessionMode:       "prefer",
	}
	ruleValue := t.Name()
	rulesJSON, err := common.Marshal([]operation_setting.ChannelAffinityRule{rule})
	require.NoError(t, err)
	snapshot, err := model.BuildRequestPolicy(map[string]string{
		"channel_affinity_setting.enabled":      "true",
		"channel_affinity_setting.session_mode": "prefer",
		"channel_affinity_setting.rules":        string(rulesJSON),
	})
	require.NoError(t, err)
	*affinity = snapshot.Affinity
	cacheKey := buildChannelAffinityCacheKeySuffix(rule, modelName, "default", ruleValue)
	cache := getChannelAffinityCache()
	require.NoError(t, cache.SetWithTTL(cacheKey, 2601, time.Minute))
	t.Cleanup(func() { _, _ = cache.DeleteMany([]string{cacheKey}) })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Request.Header.Set("X-Session", ruleValue)
	common.SetContextKey(ctx, constant.ContextKeyGroupAccessPolicy, model.GroupAccessPolicySnapshot{
		GroupName:         "default",
		BlockedChannelIDs: model.GroupAccessPolicyIntList{2601},
	})
	retry := 0
	selected, _, selectErr := SelectChannelForRequest(ctx, modelName, &RetryParam{
		Ctx: ctx, TokenGroup: "default", ModelName: modelName,
		RequestPath: "/v1/responses", Retry: &retry,
	})

	require.Nil(t, selectErr)
	require.NotNil(t, selected)
	assert.Equal(t, 2602, selected.Id)
	_, found, err := cache.Get(cacheKey)
	require.NoError(t, err)
	assert.False(t, found, "a policy-rejected affinity binding must be evicted")
}
