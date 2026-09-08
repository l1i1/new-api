package model

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// invoiceFeeQuotaForTest computes the expected fee quota the same way the model
// does: total (money) * QuotaPerUnit * rate. This keeps the test assertions
// robust against a change in the conversion constant.
func invoiceFeeQuotaForTest(total float64, rate float64) int {
	value := decimal.NewFromFloat(total).
		Mul(decimal.NewFromFloat(common.QuotaPerUnit)).
		Mul(decimal.NewFromFloat(rate))
	quota, _ := common.QuotaFromDecimalChecked(value)
	return quota
}

func newFeeInvoiceFixture(title string) *Invoice {
	return &Invoice{
		InvoiceType: InvoiceTypeOrganization,
		Title:       title,
		TaxId:       "91310000FEE",
		Email:       "billing@fee.example",
		Reason:      "reimbursement",
	}
}

func TestCreateInvoiceApplicationWithFee_DebitsQuota(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 801, 3_000_000)
	insertTopUpForInvoiceTest(t, 8011, 801, "inv-fee-1", 100, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)

	inv := newFeeInvoiceFixture("Acme Fee")
	err := CreateInvoiceApplicationWithPaymentMethods(801, inv, []*TopUp{{Id: 8011}}, decimal.Zero, nil, decimal.NewFromFloat(0.06))
	require.NoError(t, err)

	require.Greater(t, inv.Id, 0)
	assert.Equal(t, 0.06, inv.FeeRate)
	assert.Equal(t, 6.0, inv.FeeAmount) // 100 * 0.06
	assert.Equal(t, invoiceFeeQuotaForTest(100, 0.06), inv.FeeQuota)
	assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, 801))
}

func TestCreateInvoiceApplicationWithFee_InsufficientBalanceAborts(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 802, 100) // far below the fee
	insertTopUpForInvoiceTest(t, 8021, 802, "inv-fee-2", 100, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)

	inv := newFeeInvoiceFixture("Acme Poor")
	err := CreateInvoiceApplicationWithPaymentMethods(802, inv, []*TopUp{{Id: 8021}}, decimal.Zero, nil, decimal.NewFromFloat(0.06))
	require.ErrorIs(t, err, ErrInvoiceInsufficientBalance)

	// GORM assigns inv.Id in memory during tx.Create even though the enclosing
	// transaction rolls back, so assert on persistence, not on the struct id.
	assert.Nil(t, GetInvoiceById(inv.Id), "no invoice must be persisted on insufficient balance")
	assert.Equal(t, 100, getUserQuotaForPaymentGuardTest(t, 802), "quota must be untouched")

	// The order must remain invoiceable (no claim left behind).
	eligible, verifyErr := GetInvoiceableTopUpsWithPaymentMethods(802, nil)
	require.NoError(t, verifyErr)
	assert.Len(t, eligible, 1)
	assert.Equal(t, 8021, eligible[0].Id)
}

func TestRejectInvoiceWithFee_RefundsQuota(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 803, 3_000_000)
	insertTopUpForInvoiceTest(t, 8031, 803, "inv-fee-3", 100, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)

	inv := newFeeInvoiceFixture("Acme Reject")
	require.NoError(t, CreateInvoiceApplicationWithPaymentMethods(803, inv, []*TopUp{{Id: 8031}}, decimal.Zero, nil, decimal.NewFromFloat(0.06)))
	assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, 803))

	changed, err := RejectInvoice(inv.Id, "not eligible")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, 3_000_000, getUserQuotaForPaymentGuardTest(t, 803), "reject must refund the fee")

	after := GetInvoiceById(inv.Id)
	require.NotNil(t, after)
	assert.Equal(t, InvoiceStatusRejected, after.Status)
}

func TestCancelInvoiceWithFee_RefundsQuota(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 804, 3_000_000)
	insertTopUpForInvoiceTest(t, 8041, 804, "inv-fee-4", 100, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)

	inv := newFeeInvoiceFixture("Acme Cancel")
	require.NoError(t, CreateInvoiceApplicationWithPaymentMethods(804, inv, []*TopUp{{Id: 8041}}, decimal.Zero, nil, decimal.NewFromFloat(0.06)))
	assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, 804))

	changed, err := CancelInvoice(inv.Id, 804)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, 3_000_000, getUserQuotaForPaymentGuardTest(t, 804), "cancel must refund the fee")

	after := GetInvoiceById(inv.Id)
	require.NotNil(t, after)
	assert.Equal(t, InvoiceStatusCancelled, after.Status)
}

func TestRejectInvoiceWithFee_IdempotentRepeatDoesNotDoubleRefund(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 805, 3_000_000)
	insertTopUpForInvoiceTest(t, 8051, 805, "inv-fee-5", 100, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)

	inv := newFeeInvoiceFixture("Acme Idempotent")
	require.NoError(t, CreateInvoiceApplicationWithPaymentMethods(805, inv, []*TopUp{{Id: 8051}}, decimal.Zero, nil, decimal.NewFromFloat(0.06)))

	changed, err := RejectInvoice(inv.Id, "reject")
	require.NoError(t, err)
	assert.True(t, changed)

	// Repeating the same reject must report no change and must not credit again.
	changed, err = RejectInvoice(inv.Id, "reject again")
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, 3_000_000, getUserQuotaForPaymentGuardTest(t, 805), "idempotent reject must not double-refund")
}

func TestCreateInvoiceApplication_ZeroFeeRateDoesNotTouchQuota(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 806, 12345)
	insertTopUpForInvoiceTest(t, 8061, 806, "inv-fee-6", 100, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)

	inv := newFeeInvoiceFixture("Acme Free")
	err := CreateInvoiceApplicationWithPaymentMethods(806, inv, []*TopUp{{Id: 8061}}, decimal.Zero, nil, decimal.Zero)
	require.NoError(t, err)

	assert.Zero(t, inv.FeeQuota)
	assert.Zero(t, inv.FeeAmount)
	assert.Equal(t, 12345, getUserQuotaForPaymentGuardTest(t, 806), "zero fee must not touch quota")
}

func TestValidInvoiceFeeRateRejectsOutOfRange(t *testing.T) {
	assert.True(t, ValidInvoiceFeeRate(0))
	assert.True(t, ValidInvoiceFeeRate(0.06))
	assert.True(t, ValidInvoiceFeeRate(1))
	assert.False(t, ValidInvoiceFeeRate(-0.01), "negative rates are invalid")
	assert.False(t, ValidInvoiceFeeRate(1.01), "rates above 100% are invalid")
	assert.False(t, ValidInvoiceFeeRate(math.NaN()))
	assert.False(t, ValidInvoiceFeeRate(math.Inf(1)))
	assert.False(t, ValidInvoiceFeeRate(math.Inf(-1)))
}
