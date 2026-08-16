package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupPaymentGatewaySettlementControllerTest(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousRedis := common.RedisEnabled
	previousMainType := common.MainDatabaseType()
	dsn := "file:payment_gateway_settlement_controller_" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}, &model.InviteTopUpReward{}, &model.Log{}, &model.PaymentGatewaySettlement{}))
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetMainDatabaseType(previousMainType)
		common.RedisEnabled = previousRedis
	})
}

func TestPaymentGatewaySettlementRequiresValidSignatureAndAcknowledgesAfterCommit(t *testing.T) {
	setupPaymentGatewaySettlementControllerTest(t)
	t.Setenv("HOTPAY_SETTLEMENT_SECRET", "gateway-controller-secret")
	gin.SetMode(gin.TestMode)
	user := &model.User{Id: 9211, Username: "gateway-controller-user", Status: common.UserStatusEnabled, Quota: 10}
	require.NoError(t, model.DB.Create(user).Error)
	topUp := &model.TopUp{
		UserId: user.Id, Amount: 10, Money: 9.99, TradeNo: "gateway-controller-order",
		PaymentMethod: "card", PaymentProvider: model.PaymentProviderWaffoPancake, PaymentProviderAccountID: "account-1", PaymentEnvironment: "test",
		PaymentGatewayOrderID: "gateway-controller-id",
		PaymentCurrency:       model.PaymentCurrencyUSD, Status: common.TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(topUp).Error)
	command := model.PaymentGatewaySettlementCommand{
		CommandID: "gateway-controller-command", OrderID: "gateway-controller-id", MerchantOrderID: topUp.TradeNo,
		BusinessType: model.PaymentGatewayBusinessWallet, UserID: "9211", AmountMinor: 999,
		Currency: model.PaymentCurrencyUSD, Provider: model.PaymentProviderWaffoPancake,
		ProviderAccountID: "account-1", Environment: "test", ProviderEventID: "gateway-controller-event", ProviderOrderID: "provider-order-1", ProviderTransactionID: "provider-tx-1", PaymentMethod: "card", QuotaAmount: 0,
		PriceSnapshot: map[string]any{"quota_amount": 10, "provider_amount": "9.99", "pricing_currency": model.PaymentCurrencyUSD},
		IssuedAt:      time.Now().UTC(),
	}
	payload, err := model.PaymentGatewaySettlementPayload(command)
	require.NoError(t, err)
	command.Signature = common.HmacSha256(string(payload), "gateway-controller-secret")
	body, err := common.Marshal(command)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/payment/settlements", strings.NewReader(string(body)))
	request.Header.Set("X-Gateway-Signature", command.Signature)
	request.Header.Set("X-Gateway-Command-ID", command.CommandID)
	request.Header.Set("Idempotency-Key", command.CommandID)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	PaymentGatewaySettlement(context)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"committed":true`)

	replayRecorder := httptest.NewRecorder()
	replayContext, _ := gin.CreateTestContext(replayRecorder)
	replayContext.Request = httptest.NewRequest(http.MethodPost, "/internal/v1/payment/settlements", strings.NewReader(string(body)))
	replayContext.Request.Header.Set("X-Gateway-Signature", command.Signature)
	PaymentGatewaySettlement(replayContext)
	require.Equal(t, http.StatusOK, replayRecorder.Code)
	require.Contains(t, replayRecorder.Body.String(), `"duplicate":true`)

	var storedUser model.User
	require.NoError(t, model.DB.First(&storedUser, user.Id).Error)
	require.Equal(t, int64(5000010), int64(storedUser.Quota))
}

func TestPaymentGatewaySettlementRejectsBadSignatureAndDoesNotMutate(t *testing.T) {
	setupPaymentGatewaySettlementControllerTest(t)
	t.Setenv("HOTPAY_SETTLEMENT_SECRET", "gateway-controller-secret")
	gin.SetMode(gin.TestMode)
	user := &model.User{Id: 9212, Username: "gateway-controller-invalid", Status: common.UserStatusEnabled, Quota: 10}
	require.NoError(t, model.DB.Create(user).Error)
	topUp := &model.TopUp{
		UserId: user.Id, Amount: 10, Money: 9.99, TradeNo: "gateway-controller-invalid-order",
		PaymentMethod: "card", PaymentProvider: model.PaymentProviderWaffoPancake, PaymentProviderAccountID: "account-1", PaymentEnvironment: "test",
		PaymentGatewayOrderID: "gateway-controller-invalid-id",
		PaymentCurrency:       model.PaymentCurrencyUSD, Status: common.TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(topUp).Error)
	command := model.PaymentGatewaySettlementCommand{
		CommandID: "gateway-controller-invalid-command", OrderID: "gateway-controller-invalid-id", MerchantOrderID: topUp.TradeNo,
		BusinessType: model.PaymentGatewayBusinessWallet, UserID: "9212", AmountMinor: 999,
		Currency: model.PaymentCurrencyUSD, Provider: model.PaymentProviderWaffoPancake,
		ProviderAccountID: "account-1", Environment: "test", ProviderEventID: "gateway-controller-invalid-event", ProviderOrderID: "provider-order-2", ProviderTransactionID: "provider-tx-2", PaymentMethod: "card", QuotaAmount: 0,
		PriceSnapshot: map[string]any{"quota_amount": 10, "provider_amount": "9.99", "pricing_currency": model.PaymentCurrencyUSD},
		IssuedAt:      time.Now().UTC(), Signature: "not-a-valid-signature",
	}
	body, err := common.Marshal(command)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/internal/v1/payment/settlements", strings.NewReader(string(body)))
	context.Request.Header.Set("X-Gateway-Signature", command.Signature)
	PaymentGatewaySettlement(context)
	require.Equal(t, http.StatusUnauthorized, recorder.Code)

	var storedUser model.User
	require.NoError(t, model.DB.First(&storedUser, user.Id).Error)
	require.Equal(t, int64(10), int64(storedUser.Quota))
}

func TestPaymentGatewaySettlementReturnsConflictForCurrencyMismatch(t *testing.T) {
	setupPaymentGatewaySettlementControllerTest(t)
	t.Setenv("HOTPAY_SETTLEMENT_SECRET", "gateway-controller-secret")
	gin.SetMode(gin.TestMode)
	user := &model.User{Id: 9213, Username: "gateway-controller-currency", Status: common.UserStatusEnabled, Quota: 10}
	require.NoError(t, model.DB.Create(user).Error)
	topUp := &model.TopUp{
		UserId: user.Id, Amount: 10, Money: 9.99, TradeNo: "gateway-controller-currency-order",
		PaymentMethod: "card", PaymentProvider: model.PaymentProviderWaffoPancake, PaymentProviderAccountID: "account-1", PaymentEnvironment: "test",
		PaymentGatewayOrderID: "gateway-controller-currency-id",
		PaymentCurrency:       model.PaymentCurrencyUSD, Status: common.TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(topUp).Error)
	command := model.PaymentGatewaySettlementCommand{
		CommandID: "gateway-controller-currency-command", OrderID: "gateway-controller-currency-id", MerchantOrderID: topUp.TradeNo,
		BusinessType: model.PaymentGatewayBusinessWallet, UserID: "9213", AmountMinor: 999,
		Currency: model.PaymentCurrencyCNY, Provider: model.PaymentProviderWaffoPancake,
		ProviderAccountID: "account-1", Environment: "test", ProviderEventID: "gateway-controller-currency-event", ProviderOrderID: "provider-order-3", ProviderTransactionID: "provider-tx-3", PaymentMethod: "card", QuotaAmount: 0,
		PriceSnapshot: map[string]any{"quota_amount": 10, "provider_amount": "9.99", "pricing_currency": model.PaymentCurrencyCNY},
		IssuedAt:      time.Now().UTC(),
	}
	payload, err := model.PaymentGatewaySettlementPayload(command)
	require.NoError(t, err)
	command.Signature = common.HmacSha256(string(payload), "gateway-controller-secret")
	body, err := common.Marshal(command)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/internal/v1/payment/settlements", strings.NewReader(string(body)))
	context.Request.Header.Set("X-Gateway-Signature", command.Signature)
	PaymentGatewaySettlement(context)
	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"code":"payment_mismatch"`)
}

func TestPaymentGatewaySettlementBoundsConfiguredReplayWindow(t *testing.T) {
	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	t.Setenv("HOTPAY_SETTLEMENT_MAX_AGE_SECONDS", "86401")
	require.False(t, paymentGatewaySettlementTimestampValid(now.Add(-25*time.Hour), now))
	require.True(t, paymentGatewaySettlementTimestampValid(now.Add(-4*time.Minute), now))
}
