package model

import (
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupPaymentGatewaySettlementTest(t *testing.T, models ...any) {
	t.Helper()
	previousDB := DB
	previousLogDB := LOG_DB
	previousRedis := common.RedisEnabled
	previousMainType := common.MainDatabaseType()
	dsn := "file:payment_gateway_settlement_" + strconv.FormatInt(time.Now().UnixNano(), 10) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	DB = db
	LOG_DB = db
	require.NoError(t, db.AutoMigrate(models...))
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
		common.SetMainDatabaseType(previousMainType)
		common.RedisEnabled = previousRedis
	})
}

func gatewaySettlementCommand(tradeNo, businessType string) PaymentGatewaySettlementCommand {
	snapshot := map[string]any{
		"quota_amount": 10, "provider_amount": "9.99", "pricing_currency": PaymentCurrencyUSD,
	}
	if businessType == PaymentGatewayBusinessSubscription {
		snapshot = map[string]any{"plan_id": 1, "price_amount": "9.99", "currency": PaymentCurrencyUSD}
	}
	return PaymentGatewaySettlementCommand{
		CommandID:       "cmd-" + tradeNo,
		OrderID:         "gateway-order-" + tradeNo,
		MerchantOrderID: tradeNo,
		BusinessType:    businessType,
		UserID:          "9101",
		AmountMinor:     999,
		Currency:        PaymentCurrencyUSD,
		Provider:        PaymentProviderWaffoPancake,
		ProviderEventID: "event-" + tradeNo,
		PaymentMethod:   "card",
		QuotaAmount:     0,
		PriceSnapshot:   snapshot,
		IssuedAt:        time.Now().UTC(),
	}
}

func TestApplyPaymentGatewaySettlementWalletIsAtomicAndIdempotent(t *testing.T) {
	setupPaymentGatewaySettlementTest(t, &User{}, &TopUp{}, &InviteTopUpReward{}, &Log{}, &PaymentGatewaySettlement{})
	user := &User{Id: 9101, Username: "gateway-wallet-user", Status: common.UserStatusEnabled, Quota: 10}
	require.NoError(t, DB.Create(user).Error)
	topUp := &TopUp{
		UserId: user.Id, Amount: 10, Money: 9.99, TradeNo: "gateway-wallet-order",
		PaymentMethod: "card", PaymentProvider: PaymentProviderWaffoPancake,
		PaymentGatewayOrderID: "gateway-order-gateway-wallet-order",
		PaymentCurrency:       PaymentCurrencyUSD, Status: common.TopUpStatusPending,
	}
	require.NoError(t, DB.Create(topUp).Error)
	command := gatewaySettlementCommand(topUp.TradeNo, PaymentGatewayBusinessWallet)

	first, err := ApplyPaymentGatewaySettlement(command)
	require.NoError(t, err)
	require.False(t, first.Duplicate)
	require.Equal(t, "topup:"+itoa(topUp.Id), first.CreditedReference)

	second, err := ApplyPaymentGatewaySettlement(command)
	require.NoError(t, err)
	require.True(t, second.Duplicate)

	var storedUser User
	require.NoError(t, DB.First(&storedUser, user.Id).Error)
	require.Equal(t, int64(5000010), int64(storedUser.Quota))
	var storedTopUp TopUp
	require.NoError(t, DB.Where("trade_no = ?", topUp.TradeNo).First(&storedTopUp).Error)
	require.Equal(t, common.TopUpStatusSuccess, storedTopUp.Status)
	require.Equal(t, 5000000, storedTopUp.CreditedQuota)
	var settlementCount int64
	require.NoError(t, DB.Model(&PaymentGatewaySettlement{}).Where("settlement_key = ?", PaymentGatewayBusinessWallet+":"+topUp.TradeNo).Count(&settlementCount).Error)
	require.Equal(t, int64(1), settlementCount)
}

func TestApplyPaymentGatewaySettlementSubscriptionActivatesEntitlementOnce(t *testing.T) {
	setupPaymentGatewaySettlementTest(t, &User{}, &SubscriptionPlan{}, &SubscriptionOrder{}, &UserSubscription{}, &TopUp{}, &PaymentGatewaySettlement{})
	user := &User{Id: 9101, Username: "gateway-subscription-user", Status: common.UserStatusEnabled, Quota: 10}
	require.NoError(t, DB.Create(user).Error)
	plan := &SubscriptionPlan{Title: "Gateway plan", PriceAmount: 9.99, Currency: PaymentCurrencyUSD, DurationUnit: "month", DurationValue: 1, Enabled: true}
	require.NoError(t, DB.Create(plan).Error)
	order := &SubscriptionOrder{
		UserId: user.Id, PlanId: plan.Id, Money: 9.99, TradeNo: "gateway-subscription-order",
		PaymentMethod: "card", PaymentProvider: PaymentProviderWaffoPancake,
		PaymentGatewayOrderID: "gateway-order-gateway-subscription-order",
		PaymentCurrency:       PaymentCurrencyUSD, Status: common.TopUpStatusPending, CreateTime: common.GetTimestamp(),
	}
	require.NoError(t, DB.Create(order).Error)
	command := gatewaySettlementCommand(order.TradeNo, PaymentGatewayBusinessSubscription)

	first, err := ApplyPaymentGatewaySettlement(command)
	require.NoError(t, err)
	require.False(t, first.Duplicate)
	second, err := ApplyPaymentGatewaySettlement(command)
	require.NoError(t, err)
	require.True(t, second.Duplicate)

	var storedOrder SubscriptionOrder
	require.NoError(t, DB.Where("trade_no = ?", order.TradeNo).First(&storedOrder).Error)
	require.Equal(t, common.TopUpStatusSuccess, storedOrder.Status)
	var subscriptionCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ? AND plan_id = ?", user.Id, plan.Id).Count(&subscriptionCount).Error)
	require.Equal(t, int64(1), subscriptionCount)
}

func TestApplyPaymentGatewaySettlementRejectsMoneyMismatchWithoutMutation(t *testing.T) {
	setupPaymentGatewaySettlementTest(t, &User{}, &TopUp{}, &InviteTopUpReward{}, &Log{}, &PaymentGatewaySettlement{})
	user := &User{Id: 9101, Username: "gateway-mismatch-user", Status: common.UserStatusEnabled, Quota: 10}
	require.NoError(t, DB.Create(user).Error)
	topUp := &TopUp{
		UserId: user.Id, Amount: 10, Money: 9.99, TradeNo: "gateway-mismatch-order",
		PaymentMethod: "card", PaymentProvider: PaymentProviderWaffoPancake,
		PaymentGatewayOrderID: "gateway-order-gateway-mismatch-order",
		PaymentCurrency:       PaymentCurrencyUSD, Status: common.TopUpStatusPending,
	}
	require.NoError(t, DB.Create(topUp).Error)
	command := gatewaySettlementCommand(topUp.TradeNo, PaymentGatewayBusinessWallet)
	command.AmountMinor = 998
	_, err := ApplyPaymentGatewaySettlement(command)
	require.ErrorIs(t, err, ErrPaymentAmountMismatch)
	var storedUser User
	require.NoError(t, DB.First(&storedUser, user.Id).Error)
	require.Equal(t, int64(10), int64(storedUser.Quota))
	var storedTopUp TopUp
	require.NoError(t, DB.Where("trade_no = ?", topUp.TradeNo).First(&storedTopUp).Error)
	require.Equal(t, common.TopUpStatusPending, storedTopUp.Status)
}

func TestApplyPaymentGatewaySettlementTreatsCurrencyMismatchAsTerminal(t *testing.T) {
	setupPaymentGatewaySettlementTest(t, &User{}, &TopUp{}, &InviteTopUpReward{}, &Log{}, &PaymentGatewaySettlement{})
	user := &User{Id: 9102, Username: "gateway-currency-user", Status: common.UserStatusEnabled, Quota: 10}
	require.NoError(t, DB.Create(user).Error)
	topUp := &TopUp{
		UserId: user.Id, Amount: 10, Money: 9.99, TradeNo: "gateway-currency-order",
		PaymentMethod: "card", PaymentProvider: PaymentProviderWaffoPancake,
		PaymentGatewayOrderID: "gateway-order-gateway-currency-order",
		PaymentCurrency:       PaymentCurrencyUSD, Status: common.TopUpStatusPending,
	}
	require.NoError(t, DB.Create(topUp).Error)
	command := gatewaySettlementCommand(topUp.TradeNo, PaymentGatewayBusinessWallet)
	command.UserID = strconv.Itoa(user.Id)
	command.Currency = PaymentCurrencyCNY

	_, err := ApplyPaymentGatewaySettlement(command)
	require.ErrorIs(t, err, ErrPaymentGatewaySettlementMismatch)
	require.NotErrorIs(t, err, ErrPaymentGatewaySettlementRetryable)
	var settlementCount int64
	require.NoError(t, DB.Model(&PaymentGatewaySettlement{}).Where("settlement_key = ?", PaymentGatewayBusinessWallet+":"+topUp.TradeNo).Count(&settlementCount).Error)
	require.Equal(t, int64(0), settlementCount)
}

func TestApplyPaymentGatewaySettlementRejectsQuotaOverflowWithoutMutation(t *testing.T) {
	setupPaymentGatewaySettlementTest(t, &User{}, &TopUp{}, &InviteTopUpReward{}, &Log{}, &PaymentGatewaySettlement{})
	user := &User{Id: 9103, Username: "gateway-quota-overflow-user", Status: common.UserStatusEnabled, Quota: common.MaxQuota - 1}
	require.NoError(t, DB.Create(user).Error)
	topUp := &TopUp{
		UserId: user.Id, Amount: 10, Money: 9.99, TradeNo: "gateway-quota-overflow-order",
		PaymentMethod: "card", PaymentProvider: PaymentProviderWaffoPancake,
		PaymentGatewayOrderID: "gateway-order-gateway-quota-overflow-order",
		PaymentCurrency:       PaymentCurrencyUSD, Status: common.TopUpStatusPending,
	}
	require.NoError(t, DB.Create(topUp).Error)
	command := gatewaySettlementCommand(topUp.TradeNo, PaymentGatewayBusinessWallet)
	command.UserID = strconv.Itoa(user.Id)
	command.QuotaAmount = int64(common.QuotaPerUnit * 10)

	_, err := ApplyPaymentGatewaySettlement(command)
	require.ErrorIs(t, err, ErrPaymentGatewaySettlementInvalid)
	var storedUser User
	require.NoError(t, DB.First(&storedUser, user.Id).Error)
	require.Equal(t, common.MaxQuota-1, storedUser.Quota)
	var storedTopUp TopUp
	require.NoError(t, DB.Where("trade_no = ?", topUp.TradeNo).First(&storedTopUp).Error)
	require.Equal(t, common.TopUpStatusPending, storedTopUp.Status)
}

func TestApplyPaymentGatewaySettlementRequiresOrderIDAndRejectsSettlementKeyConflict(t *testing.T) {
	setupPaymentGatewaySettlementTest(t, &User{}, &TopUp{}, &InviteTopUpReward{}, &Log{}, &PaymentGatewaySettlement{})
	user := &User{Id: 9104, Username: "gateway-command-user", Status: common.UserStatusEnabled, Quota: 10}
	require.NoError(t, DB.Create(user).Error)
	topUp := &TopUp{
		UserId: user.Id, Amount: 10, Money: 9.99, TradeNo: "gateway-command-order",
		PaymentMethod: "card", PaymentProvider: PaymentProviderWaffoPancake,
		PaymentGatewayOrderID: "gateway-order-gateway-command-order",
		PaymentCurrency:       PaymentCurrencyUSD, Status: common.TopUpStatusPending,
	}
	require.NoError(t, DB.Create(topUp).Error)
	missingOrderID := gatewaySettlementCommand(topUp.TradeNo, PaymentGatewayBusinessWallet)
	missingOrderID.UserID = strconv.Itoa(user.Id)
	missingOrderID.OrderID = ""
	_, err := ApplyPaymentGatewaySettlement(missingOrderID)
	require.ErrorIs(t, err, ErrPaymentGatewaySettlementInvalid)

	first := gatewaySettlementCommand(topUp.TradeNo, PaymentGatewayBusinessWallet)
	first.UserID = strconv.Itoa(user.Id)
	_, err = ApplyPaymentGatewaySettlement(first)
	require.NoError(t, err)
	conflict := first
	conflict.CommandID = "cmd-conflicting-order"
	conflict.AmountMinor = 998
	_, err = ApplyPaymentGatewaySettlement(conflict)
	require.ErrorIs(t, err, ErrPaymentGatewaySettlementConflict)
}

func TestApplyPaymentGatewaySettlementRejectsWrongGatewayOrderID(t *testing.T) {
	setupPaymentGatewaySettlementTest(t, &User{}, &TopUp{}, &InviteTopUpReward{}, &Log{}, &PaymentGatewaySettlement{})
	user := &User{Id: 9105, Username: "gateway-order-id-user", Status: common.UserStatusEnabled, Quota: 10}
	require.NoError(t, DB.Create(user).Error)
	topUp := &TopUp{
		UserId: user.Id, Amount: 10, Money: 9.99, TradeNo: "gateway-order-id-order",
		PaymentMethod: "card", PaymentProvider: PaymentProviderWaffoPancake,
		PaymentGatewayOrderID: "canonical-order-1",
		PaymentCurrency:       PaymentCurrencyUSD, Status: common.TopUpStatusPending,
	}
	require.NoError(t, DB.Create(topUp).Error)
	command := gatewaySettlementCommand(topUp.TradeNo, PaymentGatewayBusinessWallet)
	command.UserID = strconv.Itoa(user.Id)
	command.OrderID = "canonical-order-2"
	_, err := ApplyPaymentGatewaySettlement(command)
	require.ErrorIs(t, err, ErrPaymentGatewaySettlementMismatch)
	var storedUser User
	require.NoError(t, DB.First(&storedUser, user.Id).Error)
	require.Equal(t, int64(10), int64(storedUser.Quota))
	var storedTopUp TopUp
	require.NoError(t, DB.Where("trade_no = ?", topUp.TradeNo).First(&storedTopUp).Error)
	require.Equal(t, common.TopUpStatusPending, storedTopUp.Status)
}

func TestBindPaymentGatewayOrderIDIsWriteOnce(t *testing.T) {
	setupPaymentGatewaySettlementTest(t, &User{}, &TopUp{}, &PaymentGatewaySettlement{})
	topUp := &TopUp{UserId: 9106, TradeNo: "gateway-bind-order", PaymentProvider: PaymentProviderWaffoPancake, Status: common.TopUpStatusPending}
	require.NoError(t, DB.Create(topUp).Error)
	require.NoError(t, BindPaymentGatewayOrderID(PaymentGatewayBusinessWallet, topUp.TradeNo, "canonical-order-1"))
	require.NoError(t, BindPaymentGatewayOrderID(PaymentGatewayBusinessWallet, topUp.TradeNo, "canonical-order-1"))
	require.ErrorIs(t, BindPaymentGatewayOrderID(PaymentGatewayBusinessWallet, topUp.TradeNo, "canonical-order-2"), ErrPaymentGatewaySettlementMismatch)
	var stored TopUp
	require.NoError(t, DB.Where("trade_no = ?", topUp.TradeNo).First(&stored).Error)
	require.Equal(t, "canonical-order-1", stored.PaymentGatewayOrderID)
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
