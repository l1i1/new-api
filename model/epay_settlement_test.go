package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompleteEpayTopUp_AtomicallyCreditsAndIsIdempotent(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 501, 100)
	topUp := &TopUp{
		UserId:          501,
		Amount:          2,
		Money:           9.99,
		TradeNo:         "epay-topup-success",
		PaymentMethod:   "wxpay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, topUp.Insert())

	expectedCredit := common.QuotaFromDecimal(decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)))
	require.NoError(t, CompleteEpayTopUp(topUp.TradeNo, "alipay", "9.99", "127.0.0.1"))
	require.NoError(t, CompleteEpayTopUp(topUp.TradeNo, "alipay", "9.990", "127.0.0.1"))

	completed := GetTopUpByTradeNo(topUp.TradeNo)
	require.NotNil(t, completed)
	assert.Equal(t, common.TopUpStatusSuccess, completed.Status)
	assert.Equal(t, "alipay", completed.PaymentMethod)
	assert.Positive(t, completed.CompleteTime)
	assert.Equal(t, 100+expectedCredit, getUserQuotaForPaymentGuardTest(t, 501))

	var logCount int64
	require.NoError(t, DB.Model(&Log{}).Where("user_id = ? AND type = ?", 501, LogTypeTopup).Count(&logCount).Error)
	assert.EqualValues(t, 1, logCount)
}

func TestCompleteEpayTopUp_RejectsAmountMismatch(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 502, 100)
	insertTopUpForPaymentGuardTest(t, "epay-topup-amount-mismatch", 502, PaymentProviderEpay)

	err := CompleteEpayTopUp("epay-topup-amount-mismatch", "alipay", "9.98", "127.0.0.1")
	require.ErrorIs(t, err, ErrPaymentAmountMismatch)

	topUp := GetTopUpByTradeNo("epay-topup-amount-mismatch")
	require.NotNil(t, topUp)
	assert.Equal(t, common.TopUpStatusPending, topUp.Status)
	assert.Zero(t, topUp.CompleteTime)
	assert.Equal(t, 100, getUserQuotaForPaymentGuardTest(t, 502))
}

func TestCompleteEpayTopUp_RollsBackOrderWhenUserCreditFails(t *testing.T) {
	truncateTables(t)

	insertTopUpForPaymentGuardTest(t, "epay-topup-missing-user", 9999, PaymentProviderEpay)

	err := CompleteEpayTopUp("epay-topup-missing-user", "alipay", "9.99", "127.0.0.1")
	require.Error(t, err)

	topUp := GetTopUpByTradeNo("epay-topup-missing-user")
	require.NotNil(t, topUp)
	assert.Equal(t, common.TopUpStatusPending, topUp.Status)
	assert.Zero(t, topUp.CompleteTime)
}

func TestCompleteEpaySubscriptionOrder_ValidatesAmountAndIsIdempotent(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 503, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 603)
	insertSubscriptionOrderForPaymentGuardTest(t, "epay-subscription-success", 503, plan.Id, PaymentProviderEpay)

	require.NoError(t, CompleteEpaySubscriptionOrder("epay-subscription-success", `{"provider":"epay"}`, "alipay", "9.99"))
	require.NoError(t, CompleteEpaySubscriptionOrder("epay-subscription-success", `{"provider":"epay"}`, "alipay", "9.990"))

	order := GetSubscriptionOrderByTradeNo("epay-subscription-success")
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusSuccess, order.Status)
	assert.Equal(t, "alipay", order.PaymentMethod)
	assert.Positive(t, order.CompleteTime)
	assert.EqualValues(t, 1, countUserSubscriptionsForPaymentGuardTest(t, 503))

	topUp := GetTopUpByTradeNo("epay-subscription-success")
	require.NotNil(t, topUp)
	assert.Equal(t, common.TopUpStatusSuccess, topUp.Status)

	var logCount int64
	require.NoError(t, DB.Model(&Log{}).Where("user_id = ? AND type = ?", 503, LogTypeTopup).Count(&logCount).Error)
	assert.EqualValues(t, 1, logCount)
}

func TestCompleteEpaySubscriptionOrder_RejectsAmountMismatchWithoutPartialState(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 504, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 604)
	insertSubscriptionOrderForPaymentGuardTest(t, "epay-subscription-amount-mismatch", 504, plan.Id, PaymentProviderEpay)

	err := CompleteEpaySubscriptionOrder("epay-subscription-amount-mismatch", `{"provider":"epay"}`, "alipay", "10.00")
	require.ErrorIs(t, err, ErrPaymentAmountMismatch)

	order := GetSubscriptionOrderByTradeNo("epay-subscription-amount-mismatch")
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
	assert.Zero(t, order.CompleteTime)
	assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, 504))
	assert.Nil(t, GetTopUpByTradeNo("epay-subscription-amount-mismatch"))
}
