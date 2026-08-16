package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
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

func TestHotPayQuotaAmountFailsClosedOnOverflow(t *testing.T) {
	quota, err := hotPayQuotaAmount(1)
	require.NoError(t, err)
	require.Equal(t, int64(common.QuotaPerUnit), quota)

	overflowAmount := int64(common.MaxQuota)/int64(common.QuotaPerUnit) + 1
	_, err = hotPayQuotaAmount(overflowAmount)
	require.ErrorIs(t, err, errHotPayQuotaOverflow)
}

func TestHotPayGatewayPermanentErrorClassification(t *testing.T) {
	require.True(t, hotPayGatewayErrorIsPermanent(&service.HotPayGatewayError{Code: "invalid_amount"}))
	require.True(t, hotPayGatewayErrorIsPermanent(&service.HotPayGatewayError{Code: "product_not_found"}))
	require.False(t, hotPayGatewayErrorIsPermanent(&service.HotPayGatewayError{Code: "provider_unavailable", StatusCode: 503}))
	require.False(t, hotPayGatewayErrorIsPermanent(&service.HotPayGatewayError{Code: "idempotency_conflict", StatusCode: 409}))
}
