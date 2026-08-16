package controller

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
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
	request := signedEpayNotifyRequest(t, topUp.TradeNo, "9.99")
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
	request := signedEpayNotifyRequest(t, order.TradeNo, "9.99")
	request.URL.Path = "/api/subscription/epay/notify"
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	SubscriptionEpayNotify(context)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "success", recorder.Body.String())
}

func TestHotPayEpayNotifyRejectsUnsignedCommittedOrder(t *testing.T) {
	setupEpayNotifyTest(t)
	t.Setenv("HOTPAY_GATEWAY_URL", "https://pay.example.test")
	gin.SetMode(gin.TestMode)

	topUp := &model.TopUp{
		UserId: 704, Amount: 2, Money: 9.99, TradeNo: "HP_unsigned_notify",
		PaymentMethod: "card", PaymentProvider: model.PaymentProviderWaffoPancake,
		PaymentGatewayOrderID: "gateway-order-unsigned-notify", PaymentCurrency: model.PaymentCurrencyUSD,
		Status: common.TopUpStatusSuccess,
	}
	require.NoError(t, model.DB.Create(topUp).Error)
	request := httptest.NewRequest(http.MethodPost, "/api/user/epay/notify", nil)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	EpayNotify(context)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "fail", recorder.Body.String())
}

func TestHotPayEpayNotifyUsesGatewayVerifierAfterLegacyConfigRemoval(t *testing.T) {
	setupEpayNotifyTest(t)
	t.Setenv("HOTPAY_GATEWAY_URL", "https://pay.example.test")
	t.Setenv("HOTPAY_EPAY_PID", "gateway-pid")
	t.Setenv("HOTPAY_EPAY_KEY", "gateway-key")
	operation_setting.PayAddress = ""
	operation_setting.EpayId = ""
	operation_setting.EpayKey = ""
	gin.SetMode(gin.TestMode)

	topUp := &model.TopUp{
		UserId: 705, Amount: 2, Money: 9.99, TradeNo: "HP_gateway_verifier",
		PaymentMethod: "card", PaymentProvider: model.PaymentProviderWaffoPancake,
		PaymentGatewayOrderID: "gateway-order-verifier", PaymentCurrency: model.PaymentCurrencyUSD,
		Status: common.TopUpStatusSuccess,
	}
	require.NoError(t, model.DB.Create(topUp).Error)
	params := epay.GenerateParams(map[string]string{
		"pid": "gateway-pid", "type": "alipay", "trade_no": "provider-" + topUp.TradeNo,
		"out_trade_no": topUp.TradeNo, "money": "9.99", "trade_status": epay.StatusTradeSuccess,
		"notify_id": "notify-" + topUp.TradeNo,
	}, "gateway-key")
	form := url.Values{}
	for key, value := range params {
		form.Set(key, value)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/user/epay/notify", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	EpayNotify(context)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "success", recorder.Body.String())
}
