package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRechargeWaffoValidatesProviderSettlement(t *testing.T) {
	tests := []struct {
		name     string
		amount   string
		currency string
		valid    bool
	}{
		{name: "matching amount and currency", amount: "2.50", currency: PaymentCurrencyUSD, valid: true},
		{name: "case insensitive currency", amount: "2.50", currency: "usd", valid: true},
		{name: "missing amount", currency: PaymentCurrencyUSD},
		{name: "invalid amount", amount: "not-a-number", currency: PaymentCurrencyUSD},
		{name: "excess precision", amount: "2.501", currency: PaymentCurrencyUSD},
		{name: "amount mismatch", amount: "2.49", currency: PaymentCurrencyUSD},
		{name: "currency mismatch", amount: "2.50", currency: PaymentCurrencyCNY},
	}

	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			truncateTables(t)
			userID := 1100 + index
			require.NoError(t, DB.Create(&User{
				Id:       userID,
				Username: "waffo_settlement_user",
				Status:   common.UserStatusEnabled,
			}).Error)
			tradeNo := "waffo-settlement-" + testCase.name
			require.NoError(t, (&TopUp{
				UserId:          userID,
				Amount:          2,
				Money:           2.5,
				TradeNo:         tradeNo,
				PaymentMethod:   PaymentMethodWaffo,
				PaymentProvider: PaymentProviderWaffo,
				PaymentCurrency: PaymentCurrencyUSD,
				CreateTime:      1,
				Status:          common.TopUpStatusPending,
			}).Insert())

			err := RechargeWaffo(tradeNo, "127.0.0.1", WaffoSettlement{
				Amount:   testCase.amount,
				Currency: testCase.currency,
			})
			if testCase.valid {
				require.NoError(t, err)
				require.NoError(t, RechargeWaffo(tradeNo, "127.0.0.1", WaffoSettlement{}))
				assert.Equal(t, common.TopUpStatusSuccess, GetTopUpByTradeNo(tradeNo).Status)
				assert.Positive(t, getUserQuotaForPaymentGuardTest(t, userID))
				return
			}

			assert.Zero(t, getUserQuotaForPaymentGuardTest(t, userID))
		})
	}
}
