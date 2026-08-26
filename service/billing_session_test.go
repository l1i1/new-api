package service

import (
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	billingSessionSubscriptionTablesOnce sync.Once
	billingSessionSubscriptionTablesErr  error
)

func newBillingSessionTestContext() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	return c
}

func ensureBillingSessionSubscriptionTables(t *testing.T) {
	t.Helper()
	billingSessionSubscriptionTablesOnce.Do(func() {
		billingSessionSubscriptionTablesErr = model.DB.AutoMigrate(&model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{})
	})
	require.NoError(t, billingSessionSubscriptionTablesErr)
}

func TestBillingSessionSettleExactMatchRecordsWalletUsage(t *testing.T) {
	truncate(t)

	const userID = 704
	seedUser(t, userID, 100_000)

	relayInfo := &common.RelayInfo{
		UserId:       userID,
		IsPlayground: true,
	}
	session := &BillingSession{
		relayInfo:        relayInfo,
		funding:          &WalletFunding{userId: userID},
		preConsumedQuota: 12_000,
	}
	session.FoldUsageIntoWalletSettle(12_000)

	require.NoError(t, session.Settle(12_000))

	var user model.User
	require.NoError(t, model.DB.Select("used_quota", "request_count").First(&user, userID).Error)
	assert.Equal(t, 12_000, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
}

func TestBillingSessionSettleRefundRecordsWalletUsage(t *testing.T) {
	truncate(t)

	const userID = 707
	seedUser(t, userID, 100_000)

	relayInfo := &common.RelayInfo{
		UserId:       userID,
		IsPlayground: true,
	}
	session := &BillingSession{
		relayInfo:        relayInfo,
		funding:          &WalletFunding{userId: userID},
		preConsumedQuota: 12_000,
	}
	session.FoldUsageIntoWalletSettle(8_000)

	require.NoError(t, session.Settle(8_000))

	var user model.User
	require.NoError(t, model.DB.Select("quota", "used_quota", "request_count").First(&user, userID).Error)
	assert.Equal(t, 104_000, user.Quota)
	assert.Equal(t, 8_000, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
}

func TestBillingSessionRefundAfterSettleIsNoOp(t *testing.T) {
	truncate(t)

	const userID = 709
	seedUser(t, userID, 100_000)

	relayInfo := &common.RelayInfo{
		UserId:       userID,
		IsPlayground: true,
	}
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), relayInfo, 12_000)
	require.Nil(t, apiErr)
	require.NoError(t, session.Settle(8_000))

	session.Refund(newBillingSessionTestContext())

	var user model.User
	require.NoError(t, model.DB.Select("quota").First(&user, userID).Error)
	assert.Equal(t, 92_000, user.Quota)
}

func TestBillingSessionSettleExactMatchRecordsZeroQuotaRequest(t *testing.T) {
	truncate(t)

	const userID = 708
	seedUser(t, userID, 100_000)

	session := &BillingSession{
		relayInfo:        &common.RelayInfo{UserId: userID, IsPlayground: true},
		funding:          &WalletFunding{userId: userID},
		preConsumedQuota: 0,
	}
	session.FoldUsageIntoWalletSettle(0)

	require.NoError(t, session.Settle(0))

	var user model.User
	require.NoError(t, model.DB.Select("used_quota", "request_count").First(&user, userID).Error)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
}

func TestNewBillingSessionSubscriptionFirstUsesWalletWhenNoSubscription(t *testing.T) {
	truncate(t)

	const userID = 705
	seedUser(t, userID, 100_000)

	relayInfo := &common.RelayInfo{
		UserId:       userID,
		IsPlayground: true,
	}
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), relayInfo, 12_000)

	require.Nil(t, apiErr)
	require.NotNil(t, session)
	assert.Equal(t, BillingSourceWallet, session.funding.Source())
	assert.Equal(t, 100_000, relayInfo.UserQuota)

	var user model.User
	require.NoError(t, model.DB.Select("quota").First(&user, userID).Error)
	assert.Equal(t, 88_000, user.Quota)
}

func TestNewBillingSessionSubscriptionFirstUsesActiveSubscription(t *testing.T) {
	truncate(t)
	ensureBillingSessionSubscriptionTables(t)

	const userID = 706
	const planID = 1706
	const subscriptionID = 2706
	seedUser(t, userID, 100_000)
	require.NoError(t, model.DB.Create(&model.SubscriptionPlan{
		Id:            planID,
		Title:         "billing-session-test-plan",
		DurationUnit:  model.SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   50_000,
		Enabled:       true,
	}).Error)
	seedSubscription(t, subscriptionID, userID, 50_000, 0)
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("id = ?", subscriptionID).Update("plan_id", planID).Error)

	relayInfo := &common.RelayInfo{
		UserId:          userID,
		OriginModelName: "billing-session-test-model",
		RequestId:       "billing-session-subscription-first",
		IsPlayground:    true,
	}
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), relayInfo, 12_000)

	require.Nil(t, apiErr)
	require.NotNil(t, session)
	assert.Equal(t, BillingSourceSubscription, session.funding.Source())
	assert.Equal(t, subscriptionID, relayInfo.SubscriptionId)
	assert.Equal(t, 12_000, relayInfo.FinalPreConsumedQuota)

	var user model.User
	require.NoError(t, model.DB.Select("quota").First(&user, userID).Error)
	assert.Equal(t, 100_000, user.Quota)
	var subscription model.UserSubscription
	require.NoError(t, model.DB.First(&subscription, subscriptionID).Error)
	assert.Equal(t, int64(12_000), subscription.AmountUsed)
}

func TestNewBillingSessionSubscriptionFirstFallsBackToWalletOnAllowedOverflow(t *testing.T) {
	truncate(t)
	ensureBillingSessionSubscriptionTables(t)

	const userID = 707
	const planID = 1707
	const subscriptionID = 2707
	allowWalletOverflow := true
	seedUser(t, userID, 100_000)
	require.NoError(t, model.DB.Create(&model.SubscriptionPlan{
		Id:                  planID,
		Title:               "billing-session-overflow-plan",
		DurationUnit:        model.SubscriptionDurationMonth,
		DurationValue:       1,
		TotalAmount:         1_000,
		Enabled:             true,
		AllowWalletOverflow: &allowWalletOverflow,
	}).Error)
	seedSubscription(t, subscriptionID, userID, 1_000, 1_000)
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("id = ?", subscriptionID).Updates(map[string]any{
		"plan_id":               planID,
		"allow_wallet_overflow": true,
	}).Error)

	relayInfo := &common.RelayInfo{
		UserId:          userID,
		OriginModelName: "billing-session-overflow-model",
		RequestId:       "billing-session-subscription-overflow",
		IsPlayground:    true,
	}
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), relayInfo, 12_000)

	require.Nil(t, apiErr)
	require.NotNil(t, session)
	assert.Equal(t, BillingSourceWallet, session.funding.Source())

	var user model.User
	require.NoError(t, model.DB.Select("quota").First(&user, userID).Error)
	assert.Equal(t, 88_000, user.Quota)
	var subscription model.UserSubscription
	require.NoError(t, model.DB.First(&subscription, subscriptionID).Error)
	assert.Equal(t, int64(1_000), subscription.AmountUsed)
}
