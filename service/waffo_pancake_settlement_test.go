package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupWaffoPancakeSettlementTestDB(t *testing.T) {
	t.Helper()

	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:waffo_pancake_settlement?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.TopUp{}, &model.SubscriptionOrder{}))
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})
}

func completedWaffoPancakeEvent(tradeNo, amount, currency string) *WaffoPancakeWebhookEvent {
	return &WaffoPancakeWebhookEvent{
		EventType: "order.completed",
		Data: WaffoPancakeWebhookData{
			OrderID:                 "ORD_test",
			OrderMerchantExternalID: tradeNo,
			Amount:                  amount,
			Currency:                currency,
		},
	}
}

func TestResolveWaffoPancakeTradeNo_UsesSubtotalWithoutBuyerIdentity(t *testing.T) {
	setupWaffoPancakeSettlementTestDB(t)

	topUp := &model.TopUp{
		UserId:          42,
		Amount:          10,
		Money:           12.505,
		TradeNo:         "WAFFO_PANCAKE-test-wallet",
		PaymentMethod:   model.PaymentMethodWaffoPancake,
		PaymentProvider: model.PaymentProviderWaffoPancake,
		PaymentCurrency: model.PaymentCurrencyCNY,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(topUp).Error)

	event := completedWaffoPancakeEvent(topUp.TradeNo, "13.76", "cny")
	event.Data.Subtotal = "12.510"
	event.Data.TaxAmount = "1.25"
	event.Data.Total = "13.76"
	tradeNo, err := ResolveWaffoPancakeTradeNo(event)
	require.NoError(t, err)
	assert.Equal(t, topUp.TradeNo, tradeNo)
}

func TestResolveWaffoPancakeTradeNo_FallsBackToLegacyAmount(t *testing.T) {
	setupWaffoPancakeSettlementTestDB(t)

	topUp := &model.TopUp{
		UserId:          42,
		Amount:          10,
		Money:           12.50,
		TradeNo:         "WAFFO_PANCAKE-test-legacy-amount",
		PaymentMethod:   model.PaymentMethodWaffoPancake,
		PaymentProvider: model.PaymentProviderWaffoPancake,
		PaymentCurrency: model.PaymentCurrencyCNY,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(topUp).Error)

	tradeNo, err := ResolveWaffoPancakeTradeNo(completedWaffoPancakeEvent(topUp.TradeNo, "12.500", "CNY"))
	require.NoError(t, err)
	assert.Equal(t, topUp.TradeNo, tradeNo)
}

func TestResolveWaffoPancakeTradeNo_RejectsSettlementMismatch(t *testing.T) {
	setupWaffoPancakeSettlementTestDB(t)

	topUp := &model.TopUp{
		UserId:          42,
		Amount:          10,
		Money:           12.50,
		TradeNo:         "WAFFO_PANCAKE-test-mismatch",
		PaymentMethod:   model.PaymentMethodWaffoPancake,
		PaymentProvider: model.PaymentProviderWaffoPancake,
		PaymentCurrency: model.PaymentCurrencyCNY,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(topUp).Error)

	testCases := []struct {
		name  string
		event *WaffoPancakeWebhookEvent
	}{
		{name: "wrong amount", event: completedWaffoPancakeEvent(topUp.TradeNo, "12.49", "CNY")},
		{name: "unsupported amount precision", event: completedWaffoPancakeEvent(topUp.TradeNo, "12.501", "CNY")},
		{name: "malformed amount", event: completedWaffoPancakeEvent(topUp.TradeNo, "not-an-amount", "CNY")},
		{name: "wrong currency", event: completedWaffoPancakeEvent(topUp.TradeNo, "12.50", "EUR")},
		{name: "inconsistent tax total", event: func() *WaffoPancakeWebhookEvent {
			event := completedWaffoPancakeEvent(topUp.TradeNo, "13.50", "CNY")
			event.Data.Subtotal = "12.50"
			event.Data.TaxAmount = "1.00"
			event.Data.Total = "14.00"
			return event
		}()},
		{name: "wrong event type", event: func() *WaffoPancakeWebhookEvent {
			event := completedWaffoPancakeEvent(topUp.TradeNo, "12.50", "CNY")
			event.EventType = "order.created"
			return event
		}()},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tradeNo, err := ResolveWaffoPancakeTradeNo(tc.event)
			assert.Error(t, err)
			assert.Empty(t, tradeNo)
		})
	}
}

func TestResolveWaffoPancakeTradeNo_RejectsWrongProvider(t *testing.T) {
	setupWaffoPancakeSettlementTestDB(t)

	topUp := &model.TopUp{
		UserId:          42,
		Amount:          10,
		Money:           12.50,
		TradeNo:         "WAFFO_PANCAKE-test-provider",
		PaymentMethod:   model.PaymentMethodWaffoPancake,
		PaymentProvider: model.PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(topUp).Error)

	tradeNo, err := ResolveWaffoPancakeTradeNo(completedWaffoPancakeEvent(topUp.TradeNo, "12.50", "USD"))
	assert.Error(t, err)
	assert.Empty(t, tradeNo)
}

func TestResolveWaffoPancakeSubscriptionTradeNo_ValidatesAmountAndCurrency(t *testing.T) {
	setupWaffoPancakeSettlementTestDB(t)

	order := &model.SubscriptionOrder{
		UserId:          42,
		PlanId:          7,
		Money:           29.90,
		TradeNo:         "WAFFO_PANCAKE_SUB-test",
		PaymentMethod:   model.PaymentMethodWaffoPancake,
		PaymentProvider: model.PaymentProviderWaffoPancake,
		PaymentCurrency: model.PaymentCurrencyUSD,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(order).Error)

	tradeNo, err := ResolveWaffoPancakeSubscriptionTradeNo(completedWaffoPancakeEvent(order.TradeNo, "29.9", "USD"))
	require.NoError(t, err)
	assert.Equal(t, order.TradeNo, tradeNo)

	tradeNo, err = ResolveWaffoPancakeSubscriptionTradeNo(completedWaffoPancakeEvent(order.TradeNo, "29.8", "USD"))
	assert.Error(t, err)
	assert.Empty(t, tradeNo)
}
