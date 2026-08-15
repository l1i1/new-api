package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestHotPayEpayWalletNotifyAcknowledgesOnlyCommittedGatewayOrder(t *testing.T) {
	setupEpayNotifyTest(t)
	t.Setenv("HOTPAY_GATEWAY_URL", "https://pay.example.test")
	gin.SetMode(gin.TestMode)

	topUp := &model.TopUp{
		UserId: 702, Amount: 2, Money: 9.99, TradeNo: "HP_wallet_notify",
		PaymentMethod: "card", PaymentProvider: model.PaymentProviderWaffoPancake,
		PaymentGatewayOrderID: "gateway-order-wallet-notify", PaymentCurrency: model.PaymentCurrencyUSD,
		Status: common.TopUpStatusSuccess,
	}
	require.NoError(t, model.DB.Create(topUp).Error)
	request := httptest.NewRequest(http.MethodPost, "/api/user/epay/notify", strings.NewReader("out_trade_no=HP_wallet_notify&trade_status=TRADE_SUCCESS"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	EpayNotify(context)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "success", recorder.Body.String())
}

func TestHotPayEpaySubscriptionNotifyAcknowledgesOnlyCommittedGatewayOrder(t *testing.T) {
	setupEpayNotifyTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.SubscriptionOrder{}))
	t.Setenv("HOTPAY_GATEWAY_URL", "https://pay.example.test")
	gin.SetMode(gin.TestMode)

	order := &model.SubscriptionOrder{
		UserId: 703, PlanId: 1, Money: 9.99, TradeNo: "SUBUSR703NOgateway",
		PaymentMethod: "card", PaymentProvider: model.PaymentProviderWaffoPancake,
		PaymentGatewayOrderID: "gateway-order-subscription-notify", PaymentCurrency: model.PaymentCurrencyUSD,
		Status: common.TopUpStatusSuccess,
	}
	require.NoError(t, model.DB.Create(order).Error)
	request := httptest.NewRequest(http.MethodPost, "/api/subscription/epay/notify", strings.NewReader("out_trade_no=SUBUSR703NOgateway&trade_status=TRADE_SUCCESS"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	SubscriptionEpayNotify(context)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "success", recorder.Body.String())
}
