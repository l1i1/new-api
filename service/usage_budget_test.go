package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPostTextConsumeQuotaClampsCompletionToBudget pins the Phase-B billing
// rule on the text settlement path: when the upstream reports a completion
// count above the request's declared max_tokens, the settlement bills (and the
// consume log records) the capped count, while the alarm-only deviation marker
// keeps the raw upstream value so accumulation is still visible.
func TestPostTextConsumeQuotaClampsCompletionToBudget(t *testing.T) {
	setupAnomalyLogDB(t)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	user := model.User{Username: "budget_clamp", Quota: 10_000_000, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "budget-clamp-test", Name: "budget", RemainQuota: 10_000_000, Status: common.TokenStatusEnabled}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{Name: "budget-clamp", Key: "unused", Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(&channel).Error)
	t.Cleanup(func() {
		require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Delete(&model.Log{}).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&token).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&user).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&channel).Error)
	})

	exprStr := `tier("base", p * 2 + c * 8)`
	newInfo := func(clientMax int) *relaycommon.RelayInfo {
		relayInfo := newAnomalyTestRelayInfo(exprStr, 1)
		relayInfo.UserId = user.Id
		relayInfo.TokenId = token.Id
		relayInfo.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: channel.Id}
		relayInfo.UserGroup = "default"
		relayInfo.UsingGroup = "default"
		relayInfo.RelayFormat = types.RelayFormatOpenAI
		relayInfo.UserSetting = dto.UserSetting{BillingPreference: "wallet_only"}
		relayInfo.ClientMaxTokens = clientMax
		relayInfo.SetEstimatePromptTokens(50)
		return relayInfo
	}

	// Accumulated shape: the upstream sums several internal rounds into one
	// usage (completion 457) although the request declared max_tokens=100.
	accumulatedInfo := newInfo(100)
	PostTextConsumeQuota(ctx, accumulatedInfo, &dto.Usage{PromptTokens: 50, CompletionTokens: 457, TotalTokens: 507}, nil)
	require.Equal(t, 457, accumulatedInfo.UpstreamRawCompletionTokens)

	// Control: the same admitted output billed without accumulation.
	PostTextConsumeQuota(ctx, newInfo(0), &dto.Usage{PromptTokens: 50, CompletionTokens: 100, TotalTokens: 150}, nil)

	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Order("id asc").Find(&logs).Error)
	require.Len(t, logs, 2)

	clamped, control := logs[0], logs[1]
	assert.Equal(t, 50, clamped.PromptTokens)
	assert.Equal(t, 100, clamped.CompletionTokens, "the settle path must bill the capped completion")
	assert.Equal(t, control.Quota, clamped.Quota, "capped settlement must cost exactly what an honest 100-token answer costs")

	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(clamped.Other, &other))
	adminInfo, ok := other["admin_info"].(map[string]any)
	require.True(t, ok)
	deviation, ok := adminInfo["usage_deviation"].(map[string]any)
	require.True(t, ok, "accumulation must still be flagged after the clamp")
	assert.Equal(t, "completion_over_budget", deviation["kind"])
	assert.Equal(t, "observed", deviation["action"])
	assert.Equal(t, float64(457), deviation["value"], "marker must carry the raw upstream count")
	assert.Equal(t, float64(100), deviation["baseline"])
}
