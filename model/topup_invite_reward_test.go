package model

import (
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func enableInviteFirstTopUpRewardForTest(t *testing.T, start int64) {
	t.Helper()
	t.Setenv("INVITE_FIRST_TOPUP_REWARD_ENABLED", "true")
	t.Setenv("INVITE_FIRST_TOPUP_REWARD_START_TIMESTAMP", strconv.FormatInt(start, 10))
}

func createInviteRewardUser(t *testing.T, id int, quota int, createdAt int64, inviterId int, status int) {
	t.Helper()
	user := User{
		Id:        id,
		Username:  fmt.Sprintf("invite_reward_user_%d", id),
		AffCode:   fmt.Sprintf("ir%d", id),
		Quota:     quota,
		InviterId: inviterId,
		Status:    status,
		CreatedAt: createdAt,
	}
	require.NoError(t, DB.Create(&user).Error)
}

func TestCompleteEpayTopUpAppliesInviteFirstTopUpRewardOnce(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	enableInviteFirstTopUpRewardForTest(t, now-100)

	createInviteRewardUser(t, 801, 300, now-90, 0, common.UserStatusEnabled)
	createInviteRewardUser(t, 802, 100, now-80, 801, common.UserStatusEnabled)
	topUp := TopUp{
		UserId:          802,
		Amount:          2,
		Money:           9.99,
		TradeNo:         "invite-reward-epay-once",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		CreateTime:      now - 10,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, topUp.Insert())

	baseQuota := common.QuotaFromDecimal(decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)))
	rewardQuota, err := inviteFirstTopUpRewardQuota(baseQuota, InviteFirstTopUpRewardRateBasisPoints)
	require.NoError(t, err)

	require.NoError(t, CompleteEpayTopUp(topUp.TradeNo, "alipay", "9.99", "127.0.0.1"))
	require.NoError(t, CompleteEpayTopUp(topUp.TradeNo, "alipay", "9.990", "127.0.0.1"))

	completed := GetTopUpByTradeNo(topUp.TradeNo)
	require.NotNil(t, completed)
	assert.Equal(t, baseQuota, completed.CreditedQuota)
	assert.Equal(t, 100+baseQuota, getUserQuotaForPaymentGuardTest(t, 802))
	assert.Equal(t, 300+rewardQuota, getUserQuotaForPaymentGuardTest(t, 801))

	var reward InviteTopUpReward
	require.NoError(t, DB.Where("top_up_id = ?", completed.Id).First(&reward).Error)
	assert.Equal(t, InviteTopUpRewardStatusApplied, reward.Status)
	assert.Equal(t, baseQuota, reward.BaseQuota)
	assert.Equal(t, rewardQuota, reward.RewardQuota)
	assert.Equal(t, 802, reward.InviteeId)
	assert.Equal(t, 801, reward.InviterId)
	assert.Positive(t, reward.AppliedAt)

	var rewardCount int64
	require.NoError(t, DB.Model(&InviteTopUpReward{}).Count(&rewardCount).Error)
	assert.EqualValues(t, 1, rewardCount)

	applied, err := ProcessInviteFirstTopUpReward(completed.Id)
	require.NoError(t, err)
	assert.False(t, applied)
	assert.Equal(t, 300+rewardQuota, getUserQuotaForPaymentGuardTest(t, 801))
}

func TestHistoricalSuccessfulWalletTopUpBlocksLaterReward(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	enableInviteFirstTopUpRewardForTest(t, now-100)

	createInviteRewardUser(t, 811, 300, now-90, 0, common.UserStatusEnabled)
	createInviteRewardUser(t, 812, 100, now-80, 811, common.UserStatusEnabled)
	require.NoError(t, DB.Create(&TopUp{
		UserId:       812,
		Amount:       1,
		Money:        5,
		TradeNo:      "invite-reward-historical-first",
		CreateTime:   now - 70,
		CompleteTime: now - 60,
		Status:       common.TopUpStatusSuccess,
	}).Error)
	second := TopUp{
		UserId:          812,
		Amount:          2,
		Money:           9.99,
		TradeNo:         "invite-reward-later-second",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		CreateTime:      now - 10,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, second.Insert())

	require.NoError(t, CompleteEpayTopUp(second.TradeNo, "alipay", "9.99", "127.0.0.1"))

	var rewardCount int64
	require.NoError(t, DB.Model(&InviteTopUpReward{}).Count(&rewardCount).Error)
	assert.Zero(t, rewardCount)
	assert.Equal(t, 300, getUserQuotaForPaymentGuardTest(t, 811))
}

func TestSubscriptionShadowTopUpDoesNotBlockWalletFirstTopUpReward(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	enableInviteFirstTopUpRewardForTest(t, now-100)

	createInviteRewardUser(t, 813, 300, now-90, 0, common.UserStatusEnabled)
	createInviteRewardUser(t, 814, 100, now-80, 813, common.UserStatusEnabled)
	require.NoError(t, DB.Create(&TopUp{
		UserId:       814,
		Amount:       0,
		Money:        9.99,
		TradeNo:      "invite-reward-subscription-shadow",
		CreateTime:   now - 70,
		CompleteTime: now - 60,
		Status:       common.TopUpStatusSuccess,
	}).Error)
	walletTopUp := TopUp{
		UserId:          814,
		Amount:          2,
		Money:           9.99,
		TradeNo:         "invite-reward-first-wallet-topup",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		CreateTime:      now - 10,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, walletTopUp.Insert())

	require.NoError(t, CompleteEpayTopUp(walletTopUp.TradeNo, "alipay", "9.99", "127.0.0.1"))

	var reward InviteTopUpReward
	require.NoError(t, DB.Where("top_up_id = ?", walletTopUp.Id).First(&reward).Error)
	assert.Equal(t, InviteTopUpRewardStatusApplied, reward.Status)
	assert.Equal(t, 300+reward.RewardQuota, getUserQuotaForPaymentGuardTest(t, 813))
}

func TestInviteFirstTopUpRewardFailsClosedForIneligibleInvitees(t *testing.T) {
	tests := []struct {
		name        string
		enabled     bool
		createdAt   func(now int64) int64
		inviterId   int
		campaignGap int64
	}{
		{
			name:      "feature disabled",
			createdAt: func(now int64) int64 { return now - 10 },
			inviterId: 841,
		},
		{
			name:        "registration before campaign",
			enabled:     true,
			createdAt:   func(now int64) int64 { return now - 200 },
			inviterId:   841,
			campaignGap: 100,
		},
		{
			name:        "topup before campaign",
			enabled:     true,
			createdAt:   func(now int64) int64 { return now + 200 },
			inviterId:   841,
			campaignGap: -100,
		},
		{
			name:        "no inviter",
			enabled:     true,
			createdAt:   func(now int64) int64 { return now - 10 },
			campaignGap: 100,
		},
		{
			name:        "self invite",
			enabled:     true,
			createdAt:   func(now int64) int64 { return now - 10 },
			inviterId:   842,
			campaignGap: 100,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncateTables(t)
			now := time.Now().Unix()
			if test.enabled {
				enableInviteFirstTopUpRewardForTest(t, now-test.campaignGap)
			} else {
				t.Setenv("INVITE_FIRST_TOPUP_REWARD_ENABLED", "false")
				t.Setenv("INVITE_FIRST_TOPUP_REWARD_START_TIMESTAMP", strconv.FormatInt(now-100, 10))
			}

			createInviteRewardUser(t, 841, 300, now-20, 0, common.UserStatusEnabled)
			inviteeId := 842
			createInviteRewardUser(t, inviteeId, 100, test.createdAt(now), test.inviterId, common.UserStatusEnabled)
			topUp := TopUp{
				UserId:          inviteeId,
				Amount:          2,
				Money:           9.99,
				TradeNo:         "invite-reward-ineligible-" + test.name,
				PaymentMethod:   "alipay",
				PaymentProvider: PaymentProviderEpay,
				CreateTime:      now - 5,
				Status:          common.TopUpStatusPending,
			}
			require.NoError(t, topUp.Insert())
			require.NoError(t, CompleteEpayTopUp(topUp.TradeNo, "alipay", "9.99", "127.0.0.1"))

			var rewardCount int64
			require.NoError(t, DB.Model(&InviteTopUpReward{}).Count(&rewardCount).Error)
			assert.Zero(t, rewardCount)
			assert.Equal(t, 300, getUserQuotaForPaymentGuardTest(t, 841))
		})
	}
}

func TestInactiveInviterIsSkippedWithoutRollingBackTopUp(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	enableInviteFirstTopUpRewardForTest(t, now-100)

	createInviteRewardUser(t, 821, 300, now-90, 0, common.UserStatusDisabled)
	createInviteRewardUser(t, 822, 100, now-80, 821, common.UserStatusEnabled)
	topUp := TopUp{
		UserId:          822,
		Amount:          2,
		Money:           9.99,
		TradeNo:         "invite-reward-disabled-inviter",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		CreateTime:      now - 10,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, topUp.Insert())

	require.NoError(t, CompleteEpayTopUp(topUp.TradeNo, "alipay", "9.99", "127.0.0.1"))

	completed := GetTopUpByTradeNo(topUp.TradeNo)
	require.NotNil(t, completed)
	assert.Equal(t, common.TopUpStatusSuccess, completed.Status)
	assert.Positive(t, completed.CreditedQuota)
	assert.Equal(t, 300, getUserQuotaForPaymentGuardTest(t, 821))

	var reward InviteTopUpReward
	require.NoError(t, DB.Where("top_up_id = ?", completed.Id).First(&reward).Error)
	assert.Equal(t, InviteTopUpRewardStatusSkipped, reward.Status)
	assert.Equal(t, "inviter_disabled", reward.SkipReason)
}

func TestPendingInviteRewardRetriesAfterQuotaUpdateFailure(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	enableInviteFirstTopUpRewardForTest(t, now-100)

	createInviteRewardUser(t, 831, 300, now-90, 0, common.UserStatusEnabled)
	createInviteRewardUser(t, 832, 100, now-80, 831, common.UserStatusEnabled)
	topUp := TopUp{
		UserId:          832,
		Amount:          2,
		Money:           9.99,
		TradeNo:         "invite-reward-retry",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		CreateTime:      now - 10,
		CompleteTime:    now,
		CreditedQuota:   1000,
		Status:          common.TopUpStatusSuccess,
	}
	require.NoError(t, DB.Create(&topUp).Error)
	rewardQuota, err := inviteFirstTopUpRewardQuota(topUp.CreditedQuota, InviteFirstTopUpRewardRateBasisPoints)
	require.NoError(t, err)
	reward := InviteTopUpReward{
		CampaignId:      fmt.Sprintf("invite-first-topup-%d", now-100),
		CampaignStart:   now - 100,
		TopUpId:         topUp.Id,
		TradeNo:         topUp.TradeNo,
		InviteeId:       832,
		InviterId:       831,
		PaymentProvider: PaymentProviderEpay,
		BaseQuota:       topUp.CreditedQuota,
		RewardQuota:     rewardQuota,
		RewardRateBps:   InviteFirstTopUpRewardRateBasisPoints,
		Status:          InviteTopUpRewardStatusPending,
	}
	require.NoError(t, DB.Create(&reward).Error)

	callbackName := "test:fail-invite-reward-user-update"
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("forced inviter quota update failure"))
		}
	}))

	applied, err := ProcessInviteFirstTopUpReward(topUp.Id)
	require.Error(t, err)
	assert.False(t, applied)
	require.NoError(t, DB.Callback().Update().Remove(callbackName))

	require.NoError(t, DB.First(&reward, reward.Id).Error)
	assert.Equal(t, InviteTopUpRewardStatusPending, reward.Status)
	assert.Equal(t, common.TopUpStatusSuccess, GetTopUpByTradeNo(topUp.TradeNo).Status)
	assert.Equal(t, 300, getUserQuotaForPaymentGuardTest(t, 831))

	batch, err := ProcessPendingInviteTopUpRewards()
	require.NoError(t, err)
	assert.Equal(t, 1, batch.Pending)
	assert.Equal(t, 1, batch.Applied)
	assert.Equal(t, 300+rewardQuota, getUserQuotaForPaymentGuardTest(t, 831))
}

func TestWalletSettlementProvidersPersistExactCreditedQuota(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	now := time.Now().Unix()
	enableInviteFirstTopUpRewardForTest(t, now-100)

	tests := []struct {
		name          string
		provider      string
		amount        int64
		money         float64
		expectedQuota int
		settle        func(tradeNo string) error
	}{
		{
			name:          "epay",
			provider:      PaymentProviderEpay,
			amount:        2,
			money:         9.99,
			expectedQuota: common.QuotaFromDecimal(decimal.NewFromInt(2).Mul(decimal.NewFromFloat(common.QuotaPerUnit))),
			settle: func(tradeNo string) error {
				return CompleteEpayTopUp(tradeNo, "alipay", "9.99", "127.0.0.1")
			},
		},
		{
			name:          "stripe",
			provider:      PaymentProviderStripe,
			amount:        2,
			money:         2.5,
			expectedQuota: common.QuotaFromDecimal(decimal.NewFromFloat(2.5).Mul(decimal.NewFromFloat(common.QuotaPerUnit))),
			settle: func(tradeNo string) error {
				return Recharge(tradeNo, "stripe-customer", "127.0.0.1")
			},
		},
		{
			name:          "creem",
			provider:      PaymentProviderCreem,
			amount:        1234,
			money:         2.5,
			expectedQuota: 1234,
			settle: func(tradeNo string) error {
				return RechargeCreem(tradeNo, "", "", "127.0.0.1")
			},
		},
		{
			name:          "waffo",
			provider:      PaymentProviderWaffo,
			amount:        2,
			money:         2.5,
			expectedQuota: common.QuotaFromDecimal(decimal.NewFromInt(2).Mul(decimal.NewFromFloat(common.QuotaPerUnit))),
			settle: func(tradeNo string) error {
				return RechargeWaffo(tradeNo, "127.0.0.1", WaffoSettlement{Amount: "2.50", Currency: PaymentCurrencyUSD})
			},
		},
		{
			name:          "waffo pancake",
			provider:      PaymentProviderWaffoPancake,
			amount:        2,
			money:         2.5,
			expectedQuota: common.QuotaFromDecimal(decimal.NewFromInt(2).Mul(decimal.NewFromFloat(common.QuotaPerUnit))),
			settle:        RechargeWaffoPancake,
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			userId := 850 + index
			inviterId := 900 + index
			createInviteRewardUser(t, inviterId, 0, now-20, 0, common.UserStatusEnabled)
			createInviteRewardUser(t, userId, 0, now-10, inviterId, common.UserStatusEnabled)
			var inviter User
			require.NoError(t, DB.First(&inviter, inviterId).Error)
			require.NoError(t, populateUserCache(inviter))
			var invitee User
			require.NoError(t, DB.First(&invitee, userId).Error)
			require.NoError(t, populateUserCache(invitee))
			tradeNo := fmt.Sprintf("credited-quota-%d", index)
			topUp := TopUp{
				UserId:          userId,
				Amount:          test.amount,
				Money:           test.money,
				TradeNo:         tradeNo,
				PaymentMethod:   test.provider,
				PaymentProvider: test.provider,
				PaymentCurrency: PaymentCurrencyUSD,
				CreateTime:      now,
				Status:          common.TopUpStatusPending,
			}
			require.NoError(t, topUp.Insert())
			require.NoError(t, test.settle(tradeNo))

			completed := GetTopUpByTradeNo(tradeNo)
			require.NotNil(t, completed)
			assert.Equal(t, test.expectedQuota, completed.CreditedQuota)
			assert.Equal(t, test.expectedQuota, getUserQuotaForPaymentGuardTest(t, userId))
			rewardQuota, err := inviteFirstTopUpRewardQuota(test.expectedQuota, InviteFirstTopUpRewardRateBasisPoints)
			require.NoError(t, err)
			assert.Equal(t, rewardQuota, getUserQuotaForPaymentGuardTest(t, inviterId))
			cachedInviteeQuota, err := common.RDB.HGet(t.Context(), getUserCacheKey(userId), "Quota").Int()
			require.NoError(t, err)
			assert.Equal(t, test.expectedQuota, cachedInviteeQuota)
			cachedInviterQuota, err := common.RDB.HGet(t.Context(), getUserCacheKey(inviterId), "Quota").Int()
			require.NoError(t, err)
			assert.Equal(t, rewardQuota, cachedInviterQuota)

			var reward InviteTopUpReward
			require.NoError(t, DB.Where("top_up_id = ?", completed.Id).First(&reward).Error)
			assert.Equal(t, InviteTopUpRewardStatusApplied, reward.Status)
			assert.Equal(t, test.expectedQuota, reward.BaseQuota)
		})
	}
}

func TestInviteFirstTopUpRewardQuotaRoundsDown(t *testing.T) {
	reward, err := inviteFirstTopUpRewardQuota(1003, InviteFirstTopUpRewardRateBasisPoints)
	require.NoError(t, err)
	assert.Equal(t, 200, reward)
}

func TestGetInviteTopUpRewardsForInviterAggregatesAndPaginatesSafely(t *testing.T) {
	truncateTables(t)
	rows := []InviteTopUpReward{
		{CampaignId: "summary", CampaignStart: 1, TopUpId: 1001, TradeNo: "summary-1", InviteeId: 100, InviterId: 90, PaymentProvider: PaymentProviderEpay, BaseQuota: 1000, RewardQuota: 200, RewardRateBps: 2000, Status: InviteTopUpRewardStatusApplied, CreatedAt: 10, AppliedAt: 20},
		{CampaignId: "summary", CampaignStart: 1, TopUpId: 1002, TradeNo: "summary-2", InviteeId: 101, InviterId: 90, PaymentProvider: PaymentProviderStripe, BaseQuota: 2000, RewardQuota: 400, RewardRateBps: 2000, Status: InviteTopUpRewardStatusPending, CreatedAt: 30},
		{CampaignId: "summary", CampaignStart: 1, TopUpId: 1003, TradeNo: "summary-3", InviteeId: 102, InviterId: 90, PaymentProvider: PaymentProviderCreem, BaseQuota: 3000, RewardQuota: 600, RewardRateBps: 2000, Status: InviteTopUpRewardStatusSkipped, CreatedAt: 40},
		{CampaignId: "summary", CampaignStart: 1, TopUpId: 1004, TradeNo: "summary-4", InviteeId: 103, InviterId: 90, PaymentProvider: PaymentProviderWaffo, BaseQuota: 4000, RewardQuota: 800, RewardRateBps: 2000, Status: InviteTopUpRewardStatusApplied, CreatedAt: 50, AppliedAt: 60},
		{CampaignId: "other", CampaignStart: 1, TopUpId: 1005, TradeNo: "summary-other", InviteeId: 104, InviterId: 91, PaymentProvider: PaymentProviderEpay, BaseQuota: 5000, RewardQuota: 1000, RewardRateBps: 2000, Status: InviteTopUpRewardStatusApplied, CreatedAt: 70, AppliedAt: 80},
	}
	for index := range rows {
		require.NoError(t, DB.Create(&rows[index]).Error)
	}

	summary, items, total, err := GetInviteTopUpRewardsForInviter(90, 0, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 2, summary.AppliedCount)
	assert.EqualValues(t, 1, summary.PendingCount)
	assert.EqualValues(t, 1, summary.SkippedCount)
	assert.EqualValues(t, 1000, summary.TotalRewardQuota)
	assert.EqualValues(t, 4, total)
	require.Len(t, items, 2)
	assert.Equal(t, rows[3].Id, items[0].Id)
	assert.Equal(t, rows[2].Id, items[1].Id)

	_, secondPage, secondTotal, err := GetInviteTopUpRewardsForInviter(90, 2, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 4, secondTotal)
	require.Len(t, secondPage, 2)
	assert.Equal(t, rows[1].Id, secondPage[0].Id)
	assert.Equal(t, rows[0].Id, secondPage[1].Id)

	_, empty, emptyTotal, err := GetInviteTopUpRewardsForInviter(999, 0, 2)
	require.NoError(t, err)
	assert.Empty(t, empty)
	assert.Zero(t, emptyTotal)

	_, _, _, err = GetInviteTopUpRewardsForInviter(90, -1, 2)
	assert.Error(t, err)
	_, _, _, err = GetInviteTopUpRewardsForInviter(90, 0, 0)
	assert.Error(t, err)
}

func TestInsertWithTxPersistsInviterRelationship(t *testing.T) {
	truncateTables(t)
	user := User{
		Username: "oauth_invitee_with_relationship",
		Status:   common.UserStatusEnabled,
	}

	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return user.InsertWithTx(tx, 991)
	}))

	var persisted User
	require.NoError(t, DB.First(&persisted, user.Id).Error)
	assert.Equal(t, 991, persisted.InviterId)
}

func TestInviteRegistrationCountIsIndependentFromTopUpAndFixedQuota(t *testing.T) {
	truncateTables(t)

	previousQuotaForInviter := common.QuotaForInviter
	previousCompliance := operation_setting.GetPaymentSetting().ComplianceConfirmed
	previousTermsVersion := operation_setting.GetPaymentSetting().ComplianceTermsVersion
	t.Cleanup(func() {
		common.QuotaForInviter = previousQuotaForInviter
		operation_setting.GetPaymentSetting().ComplianceConfirmed = previousCompliance
		operation_setting.GetPaymentSetting().ComplianceTermsVersion = previousTermsVersion
	})

	// Production intentionally keeps the legacy fixed inviter quota at zero.
	// Registration attribution must still be visible in the wallet, without
	// creating a first-top-up reward before the invitee has paid.
	common.QuotaForInviter = 0
	operation_setting.GetPaymentSetting().ComplianceConfirmed = false
	operation_setting.GetPaymentSetting().ComplianceTermsVersion = ""

	inviter := User{
		Username: "invite_registration_inviter",
		AffCode:  "invite-registration-code",
		Status:   common.UserStatusEnabled,
	}
	require.NoError(t, DB.Create(&inviter).Error)

	invitee := User{
		Username: "invite_registration_invitee",
		Status:   common.UserStatusEnabled,
	}
	require.NoError(t, invitee.Insert(inviter.Id))

	var persisted User
	require.NoError(t, DB.First(&persisted, inviter.Id).Error)
	assert.Equal(t, 1, persisted.AffCount)
	assert.Zero(t, persisted.AffQuota)
	assert.Equal(t, inviter.Id, invitee.InviterId)

	oauthInvitee := User{
		Username: "invite_registration_oauth_invitee",
		Status:   common.UserStatusEnabled,
	}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return oauthInvitee.InsertWithTx(tx, inviter.Id)
	}))
	oauthInvitee.FinalizeOAuthUserCreation(inviter.Id)

	require.NoError(t, DB.First(&persisted, inviter.Id).Error)
	assert.Equal(t, 2, persisted.AffCount)
	assert.Equal(t, inviter.Id, oauthInvitee.InviterId)

	var rewardCount int64
	require.NoError(t, DB.Model(&InviteTopUpReward{}).Where("inviter_id = ?", inviter.Id).Count(&rewardCount).Error)
	assert.Zero(t, rewardCount)
}

func TestInviteRegistrationKeepsCompliantFixedQuotaReward(t *testing.T) {
	truncateTables(t)

	previousQuotaForInviter := common.QuotaForInviter
	previousCompliance := operation_setting.GetPaymentSetting().ComplianceConfirmed
	previousTermsVersion := operation_setting.GetPaymentSetting().ComplianceTermsVersion
	t.Cleanup(func() {
		common.QuotaForInviter = previousQuotaForInviter
		operation_setting.GetPaymentSetting().ComplianceConfirmed = previousCompliance
		operation_setting.GetPaymentSetting().ComplianceTermsVersion = previousTermsVersion
	})

	common.QuotaForInviter = 7
	operation_setting.GetPaymentSetting().ComplianceConfirmed = true
	operation_setting.GetPaymentSetting().ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion

	inviter := User{
		Username: "invite_fixed_quota_inviter",
		AffCode:  "invite-fixed-quota-code",
		Status:   common.UserStatusEnabled,
	}
	require.NoError(t, DB.Create(&inviter).Error)

	invitee := User{
		Username: "invite_fixed_quota_invitee",
		Status:   common.UserStatusEnabled,
	}
	require.NoError(t, invitee.Insert(inviter.Id))

	var persisted User
	require.NoError(t, DB.First(&persisted, inviter.Id).Error)
	assert.Equal(t, 1, persisted.AffCount)
	assert.Equal(t, 7, persisted.AffQuota)
	assert.Equal(t, 7, persisted.AffHistoryQuota)
}
