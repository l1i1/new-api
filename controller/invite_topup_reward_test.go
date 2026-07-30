package controller

import (
	"fmt"
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

func setupInviteTopUpRewardControllerTest(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousDatabaseType := common.MainDatabaseType()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.InviteTopUpReward{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)

	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousDatabaseType)
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestGetInviteTopUpRewardsUsesAuthenticatedInviterAndSanitizesItems(t *testing.T) {
	db := setupInviteTopUpRewardControllerTest(t)
	t.Setenv("INVITE_FIRST_TOPUP_REWARD_ENABLED", "true")
	t.Setenv("INVITE_FIRST_TOPUP_REWARD_START_TIMESTAMP", "1")
	rewards := []model.InviteTopUpReward{
		{CampaignId: "campaign", CampaignStart: 1, TopUpId: 1, TradeNo: "private-one", InviteeId: 11, InviterId: 100, PaymentProvider: "epay", BaseQuota: 1000, RewardQuota: 200, RewardRateBps: 2000, Status: model.InviteTopUpRewardStatusApplied, CreatedAt: 10, UpdatedAt: 20, AppliedAt: 20},
		{CampaignId: "campaign", CampaignStart: 1, TopUpId: 2, TradeNo: "private-two", InviteeId: 12, InviterId: 100, PaymentProvider: "stripe", BaseQuota: 1500, RewardQuota: 300, RewardRateBps: 2000, Status: model.InviteTopUpRewardStatusPending, CreatedAt: 30, UpdatedAt: 30},
		{CampaignId: "campaign", CampaignStart: 1, TopUpId: 3, TradeNo: "private-three", InviteeId: 13, InviterId: 100, PaymentProvider: "creem", BaseQuota: 2000, RewardQuota: 400, RewardRateBps: 2000, Status: model.InviteTopUpRewardStatusSkipped, CreatedAt: 40, UpdatedAt: 40, SkipReason: "private-reason"},
		{CampaignId: "campaign-two", CampaignStart: 2, TopUpId: 4, TradeNo: "private-four", InviteeId: 14, InviterId: 100, PaymentProvider: "waffo", BaseQuota: 2500, RewardQuota: 500, RewardRateBps: 2000, Status: model.InviteTopUpRewardStatusApplied, CreatedAt: 50, UpdatedAt: 60, AppliedAt: 60},
		{CampaignId: "campaign", CampaignStart: 1, TopUpId: 5, TradeNo: "other-inviter", InviteeId: 15, InviterId: 200, PaymentProvider: "epay", BaseQuota: 5000, RewardQuota: 999, RewardRateBps: 2000, Status: model.InviteTopUpRewardStatusApplied, CreatedAt: 70, UpdatedAt: 80, AppliedAt: 80},
	}
	for index := range rewards {
		require.NoError(t, db.Create(&rewards[index]).Error)
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/user/invite-topup-rewards?p=1&page_size=2&inviter_id=200", nil)
	context.Set("id", 100)

	GetInviteTopUpRewards(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool                              `json:"success"`
		Data    inviteTopUpRewardOverviewResponse `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.True(t, response.Data.ProgramEnabled)
	assert.Equal(t, model.InviteFirstTopUpRewardRateBasisPoints, response.Data.RewardRateBps)
	assert.EqualValues(t, 2, response.Data.Summary.AppliedCount)
	assert.EqualValues(t, 1, response.Data.Summary.PendingCount)
	assert.EqualValues(t, 1, response.Data.Summary.SkippedCount)
	assert.EqualValues(t, 700, response.Data.Summary.TotalRewardQuota)
	assert.EqualValues(t, 4, response.Data.Total)
	require.Len(t, response.Data.Items, 2)
	assert.Equal(t, rewards[3].Id, response.Data.Items[0].Id)
	assert.Equal(t, rewards[2].Id, response.Data.Items[1].Id)
	assert.NotContains(t, recorder.Body.String(), "invitee_id")
	assert.NotContains(t, recorder.Body.String(), "trade_no")
	assert.NotContains(t, recorder.Body.String(), "payment_provider")
	assert.NotContains(t, recorder.Body.String(), "base_quota")
	assert.NotContains(t, recorder.Body.String(), "other-inviter")
}
