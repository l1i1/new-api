package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestHotPayWalletMethodMatrix(t *testing.T) {
	method, err := hotPayWalletMethod(model.PaymentCurrencyCNY, "wxpay")
	require.NoError(t, err)
	require.Equal(t, "wechat_pay", method)

	method, err = hotPayWalletMethod(model.PaymentCurrencyUSD, "applepay")
	require.NoError(t, err)
	require.Equal(t, "apple_pay", method)

	_, err = hotPayWalletMethod(model.PaymentCurrencyCNY, "card")
	require.Error(t, err)
	_, err = hotPaySubscriptionMethod(model.PaymentCurrencyCNY, "wechat_pay")
	require.Error(t, err)
}

func TestHotPayMerchantOrderIDIsStablePerIdempotencyKey(t *testing.T) {
	first := hotPayMerchantOrderID("wallet", 42, "retry-key")
	second := hotPayMerchantOrderID("wallet", 42, "retry-key")
	require.Equal(t, first, second)
	require.NotEqual(t, first, hotPayMerchantOrderID("wallet", 42, "other-key"))
	require.NotEqual(t, first, hotPayMerchantOrderID("subscription", 42, "retry-key"))
}
