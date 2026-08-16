package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatWaffoPancakeAmount_UsesDisplayPriceString(t *testing.T) {
	testCases := []struct {
		name     string
		amount   float64
		expected string
	}{
		{name: "whole amount", amount: 29, expected: "29.00"},
		{name: "decimal amount", amount: 29.9, expected: "29.90"},
		{name: "round half up to cents", amount: 29.999, expected: "30.00"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, formatWaffoPancakeAmount(tc.amount))
		})
	}
}

func TestGetWaffoPancakeCNYAmount(t *testing.T) {
	originalPrice := operation_setting.Price
	originalQuotaDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	originalDiscounts := make(map[int]float64, len(operation_setting.GetPaymentSetting().AmountDiscount))
	for k, v := range operation_setting.GetPaymentSetting().AmountDiscount {
		originalDiscounts[k] = v
	}
	originalTopupGroupRatio := common.TopupGroupRatio2JSONString()

	t.Cleanup(func() {
		operation_setting.Price = originalPrice
		operation_setting.GetGeneralSetting().QuotaDisplayType = originalQuotaDisplayType
		operation_setting.GetPaymentSetting().AmountDiscount = originalDiscounts
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(originalTopupGroupRatio))
	})

	operation_setting.Price = 7
	operation_setting.GetPaymentSetting().AmountDiscount = map[int]float64{
		10:                           0.8,
		int(common.QuotaPerUnit * 3): 0.5,
		20:                           0,
	}
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1,"vip":1.2}`))

	testCases := []struct {
		name             string
		amount           int64
		group            string
		quotaDisplayType string
		expected         float64
	}{
		{
			name:             "currency display applies local price group ratio and discount",
			amount:           10,
			group:            "vip",
			quotaDisplayType: operation_setting.QuotaDisplayTypeUSD,
			expected:         67.2,
		},
		{
			name:             "tokens display converts quota to display units before pricing",
			amount:           int64(common.QuotaPerUnit * 3),
			group:            "vip",
			quotaDisplayType: operation_setting.QuotaDisplayTypeTokens,
			expected:         12.6,
		},
		{
			name:             "non-positive discount falls back to no discount",
			amount:           20,
			group:            "default",
			quotaDisplayType: operation_setting.QuotaDisplayTypeUSD,
			expected:         140,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			operation_setting.GetGeneralSetting().QuotaDisplayType = tc.quotaDisplayType
			actual := getWaffoPancakeCNYAmount(tc.amount, tc.group)
			require.InDelta(t, tc.expected, actual, 0.000001)
		})
	}
}

func TestGetWaffoPancakeCNYAmountUsesLocalPayableAmount(t *testing.T) {
	originalPrice := operation_setting.Price
	originalQuotaDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	originalDiscounts := operation_setting.GetPaymentSetting().AmountDiscount
	originalTopupGroupRatio := common.TopupGroupRatio2JSONString()

	t.Cleanup(func() {
		operation_setting.Price = originalPrice
		operation_setting.GetGeneralSetting().QuotaDisplayType = originalQuotaDisplayType
		operation_setting.GetPaymentSetting().AmountDiscount = originalDiscounts
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(originalTopupGroupRatio))
	})

	operation_setting.Price = 7
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	operation_setting.GetPaymentSetting().AmountDiscount = map[int]float64{}
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1}`))

	require.InDelta(t, 497, getWaffoPancakeCNYAmount(71, "default"), 0.000001)
}

func TestWaffoPancakeWalletCurrencySelection(t *testing.T) {
	originalPrice := operation_setting.Price
	originalExchangeRate := setting.WaffoPancakeExchangeRate
	originalUnitPrice := setting.WaffoPancakeUnitPrice
	originalDiscounts := operation_setting.GetPaymentSetting().AmountDiscount
	originalTopupGroupRatio := common.TopupGroupRatio2JSONString()

	t.Cleanup(func() {
		operation_setting.Price = originalPrice
		setting.WaffoPancakeExchangeRate = originalExchangeRate
		setting.WaffoPancakeUnitPrice = originalUnitPrice
		operation_setting.GetPaymentSetting().AmountDiscount = originalDiscounts
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(originalTopupGroupRatio))
	})

	operation_setting.Price = 7
	setting.WaffoPancakeExchangeRate = 7
	setting.WaffoPancakeUnitPrice = 1
	operation_setting.GetPaymentSetting().AmountDiscount = map[int]float64{}
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1}`))

	cases := []struct {
		name     string
		currency string
		expected float64
	}{
		{name: "native CNY", currency: model.PaymentCurrencyCNY, expected: 497},
		{name: "converted USD", currency: model.PaymentCurrencyUSD, expected: 71},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.InDelta(t, tc.expected, getWaffoPancakePaymentAmount(71, "default", tc.currency), 0.000001)
		})
	}

	for _, tc := range []struct {
		input    string
		expected string
	}{
		{input: "", expected: model.PaymentCurrencyCNY},
		{input: "cny", expected: model.PaymentCurrencyCNY},
		{input: "USD", expected: model.PaymentCurrencyUSD},
	} {
		t.Run("normalize_"+tc.input, func(t *testing.T) {
			actual, err := normalizeWaffoPancakeWalletCurrency(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.expected, actual)
		})
	}
	_, err := normalizeWaffoPancakeWalletCurrency("EUR")
	require.Error(t, err)
}

func TestHandleWaffoPancakeCompletedEvent_RetriesUnresolvedOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/waffo-pancake/webhook/prod", nil)

	handleWaffoPancakeCompletedEvent(c, &service.WaffoPancakeWebhookEvent{
		EventType: "order.completed",
		Data: service.WaffoPancakeWebhookData{
			OrderID:  "ORD_unresolved",
			Amount:   "10.00",
			Currency: "USD",
		},
	}, nil)

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Equal(t, "retry", recorder.Body.String())
}

func TestIsHotPayBoundWaffoPancakeOrderDoesNotDependOnPrefix(t *testing.T) {
	setupPaymentGatewaySettlementControllerTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.SubscriptionOrder{}))

	topup := &model.TopUp{
		UserId: 9101, TradeNo: "WAFFO_PANCAKE-legacy-shaped", PaymentProvider: model.PaymentProviderWaffoPancake,
		PaymentGatewayOrderID: "canonical-wallet-order", Status: common.TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(topup).Error)
	require.True(t, isHotPayBoundWaffoPancakeOrder(topup.TradeNo))

	legacy := &model.TopUp{UserId: 9101, TradeNo: "WAFFO_PANCAKE-old", PaymentProvider: model.PaymentProviderWaffoPancake, Status: common.TopUpStatusPending}
	require.NoError(t, model.DB.Create(legacy).Error)
	require.False(t, isHotPayBoundWaffoPancakeOrder(legacy.TradeNo))
}

func TestAdminCompleteTopUpRejectsGatewayBoundOrderWithoutHotPayPrefix(t *testing.T) {
	setupPaymentGatewaySettlementControllerTest(t)
	topup := &model.TopUp{
		UserId: 9102, TradeNo: "WAFFO_PANCAKE-legacy-shaped", PaymentProvider: model.PaymentProviderWaffoPancake,
		PaymentGatewayOrderID: "canonical-wallet-order", Status: common.TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(topup).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/topup/complete", strings.NewReader(`{"trade_no":"WAFFO_PANCAKE-legacy-shaped"}`))
	context.Request.Header.Set("Content-Type", "application/json")
	AdminCompleteTopUp(context)

	assert.Contains(t, recorder.Body.String(), "该订单由 HotPay 管理")
	assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo(topup.TradeNo).Status)
}
