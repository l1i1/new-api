package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func manualVerificationTopUp(provider string) *model.TopUp {
	return &model.TopUp{
		TradeNo:         "manual-verification-order",
		Money:           9.99,
		PaymentMethod:   "alipay",
		PaymentProvider: provider,
	}
}

func requireVerificationState(t *testing.T, expected PaymentVerificationState, err error) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, expected, PaymentVerificationStateOf(err))
}

func TestValidatedEpayReconciliationURLFailsClosed(t *testing.T) {
	previousPayAddress := operation_setting.PayAddress
	t.Cleanup(func() { operation_setting.PayAddress = previousPayAddress })
	operation_setting.PayAddress = "https://pay.example.com"

	t.Setenv(epayReconciliationQueryURLEnv, "")
	_, err := validatedEpayReconciliationURL()
	require.Error(t, err)

	t.Setenv(epayReconciliationQueryURLEnv, "https://pay.example.com/api.php")
	_, err = validatedEpayReconciliationURL()
	require.Error(t, err)

	t.Setenv(epayReconciliationQueryURLEnv, "https://epay-internal.example.net/api.php")
	queryURL, err := validatedEpayReconciliationURL()
	require.NoError(t, err)
	assert.Equal(t, "epay-internal.example.net", queryURL.Hostname())

	t.Setenv(epayReconciliationQueryURLEnv, "http://10.198.0.167:18080/api.php")
	queryURL, err = validatedEpayReconciliationURL()
	require.NoError(t, err)
	assert.Equal(t, "10.198.0.167", queryURL.Hostname())

	t.Setenv(epayReconciliationQueryURLEnv, "http://epay-internal.example.net/api.php")
	_, err = validatedEpayReconciliationURL()
	require.Error(t, err)

	t.Setenv(epayReconciliationQueryURLEnv, "http://8.8.8.8/api.php")
	_, err = validatedEpayReconciliationURL()
	require.Error(t, err)
}

func TestValidateEpayProviderOrderRequiresPaidMatchingOrder(t *testing.T) {
	previousPayMethods := operation_setting.PayMethods
	t.Cleanup(func() { operation_setting.PayMethods = previousPayMethods })
	operation_setting.PayMethods = []map[string]string{
		{"type": "alipay"},
		{"type": "wxpay"},
	}

	topUp := manualVerificationTopUp(model.PaymentProviderEpay)
	valid := &epayOrderQueryResponse{
		Code:        1,
		Status:      1,
		PID:         1001,
		TradeNo:     "provider-order",
		OutTradeNo:  topUp.TradeNo,
		PaymentType: topUp.PaymentMethod,
		Money:       "9.990",
	}
	verified, err := validateEpayProviderOrder(topUp, 1001, valid)
	require.NoError(t, err)
	assert.Equal(t, "provider-order", verified.ProviderTradeNo)
	assert.Equal(t, "alipay", verified.PaymentMethod)

	differentAllowedMethod := *valid
	differentAllowedMethod.PaymentType = "wxpay"
	verified, err = validateEpayProviderOrder(topUp, 1001, &differentAllowedMethod)
	require.NoError(t, err)
	assert.Equal(t, "wxpay", verified.PaymentMethod)

	disallowedMethod := *valid
	disallowedMethod.PaymentType = "card"
	_, err = validateEpayProviderOrder(topUp, 1001, &disallowedMethod)
	requireVerificationState(t, PaymentVerificationMismatch, err)

	emptyMethod := *valid
	emptyMethod.PaymentType = " "
	_, err = validateEpayProviderOrder(topUp, 1001, &emptyMethod)
	requireVerificationState(t, PaymentVerificationMismatch, err)

	refunded := *valid
	refunded.Status = 2
	_, err = validateEpayProviderOrder(topUp, 1001, &refunded)
	requireVerificationState(t, PaymentVerificationRefunded, err)

	mismatch := *valid
	mismatch.Money = "9.98"
	_, err = validateEpayProviderOrder(topUp, 1001, &mismatch)
	requireVerificationState(t, PaymentVerificationMismatch, err)
}

func TestValidateWaffoPancakeProviderPaymentsRequiresUniqueUnrefundedMatch(t *testing.T) {
	previousStoreID := setting.WaffoPancakeStoreID
	t.Cleanup(func() { setting.WaffoPancakeStoreID = previousStoreID })
	setting.WaffoPancakeStoreID = "store-1"
	topUp := manualVerificationTopUp(model.PaymentProviderWaffoPancake)

	payment := waffoPancakeReconciliationPayment{
		ID:                      "payment-1",
		OrderMerchantExternalID: topUp.TradeNo,
		Status:                  "succeeded",
	}
	payment.SnapshotAmountDetails.Currency = "USD"
	payment.SnapshotAmountDetails.Subtotal = "9.99"
	payment.OnetimeOrder.Status = "completed"
	payment.OnetimeOrder.Store.ID = setting.WaffoPancakeStoreID

	verified, err := validateWaffoPancakeProviderPayments(topUp, []waffoPancakeReconciliationPayment{payment})
	require.NoError(t, err)
	assert.Equal(t, payment.ID, verified.ProviderTradeNo)

	_, err = validateWaffoPancakeProviderPayments(topUp, []waffoPancakeReconciliationPayment{payment, payment})
	requireVerificationState(t, PaymentVerificationAmbiguous, err)

	refunded := payment
	refunded.RefundStatus = "succeeded"
	_, err = validateWaffoPancakeProviderPayments(topUp, []waffoPancakeReconciliationPayment{refunded})
	requireVerificationState(t, PaymentVerificationRefunded, err)

	mismatch := payment
	mismatch.SnapshotAmountDetails.Subtotal = "10.00"
	_, err = validateWaffoPancakeProviderPayments(topUp, []waffoPancakeReconciliationPayment{mismatch})
	requireVerificationState(t, PaymentVerificationMismatch, err)
}

func TestVerifyTopUpPaymentRejectsUnsupportedProvider(t *testing.T) {
	_, err := VerifyTopUpPayment(context.Background(), manualVerificationTopUp(model.PaymentProviderStripe))
	requireVerificationState(t, PaymentVerificationUnsupported, err)
}
