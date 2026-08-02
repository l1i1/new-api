package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertTopUpForInvoiceTest(t *testing.T, id int, userId int, tradeNo string, money float64, currency string, provider string, status string) {
	t.Helper()
	topUp := &TopUp{
		Id:              id,
		UserId:          userId,
		Amount:          2,
		Money:           money,
		TradeNo:         tradeNo,
		PaymentMethod:   "epay",
		PaymentProvider: provider,
		PaymentCurrency: currency,
		Status:          status,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, DB.Create(topUp).Error)
}

func newInvoiceFixture(t *testing.T, userId int, orders []*TopUp) *Invoice {
	t.Helper()
	inv := &Invoice{
		Title:       "Acme Inc.",
		TaxId:       "91310000TEST",
		Phone:       "",
		Address:     "",
		BankName:    "",
		BankAccount: "",
		Email:       "billing@acme.example",
		Reason:      "reimbursement",
		Remark:      "",
	}
	require.NoError(t, CreateInvoiceApplication(userId, inv, orders))
	return inv
}

func TestGetInvoiceableTopUps_FiltersEligibility(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 601, 100)

	insertTopUpForInvoiceTest(t, 1, 601, "inv-topup-1", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 2, 601, "inv-topup-2", 20, "CNY", PaymentProviderBalance, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 3, 601, "inv-topup-3", 30, "CNY", PaymentProviderEpay, common.TopUpStatusPending)
	insertTopUpForInvoiceTest(t, 4, 601, "inv-topup-4", 40, "USD", PaymentProviderStripe, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 5, 601, "inv-topup-5", 50, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)

	// Attach order 5 to an active application so it must be excluded.
	active := newInvoiceFixture(t, 601, []*TopUp{{Id: 5}})
	require.Equal(t, InvoiceStatusPending, active.Status)

	eligible, err := GetInvoiceableTopUps(601)
	require.NoError(t, err)
	ids := make([]int, 0, len(eligible))
	for _, o := range eligible {
		ids = append(ids, o.Id)
	}
	assert.Equal(t, []int{4, 1}, ids) // newest first; 2/3/5 excluded

	// Rejecting the application frees order 5 again.
	require.NoError(t, RejectInvoice(active.Id, "not eligible"))
	eligible, err = GetInvoiceableTopUps(601)
	require.NoError(t, err)
	ids = ids[:0]
	for _, o := range eligible {
		ids = append(ids, o.Id)
	}
	assert.ElementsMatch(t, []int{1, 4, 5}, ids)
}

func TestCreateInvoiceApplication_SnapshotsActualPaidAmounts(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 602, 100)
	insertTopUpForInvoiceTest(t, 10, 602, "inv-create-1", 9.99, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 11, 602, "inv-create-2", 20.01, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)

	inv := newInvoiceFixture(t, 602, []*TopUp{{Id: 10}, {Id: 11}})
	assert.Equal(t, InvoiceStatusPending, inv.Status)
	assert.InDelta(t, 30.0, inv.TotalAmount, 0.0001)
	assert.Equal(t, "CNY", inv.Currency)
	assert.Equal(t, 602, inv.UserId)

	items, err := GetInvoiceItems(inv.Id)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "inv-create-1", items[0].TradeNo)
	assert.InDelta(t, 9.99, items[0].Amount, 0.0001)
	assert.Equal(t, "inv-create-2", items[1].TradeNo)
	assert.InDelta(t, 20.01, items[1].Amount, 0.0001)
}

func TestCreateInvoiceApplication_RejectsAttachedOrder(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 603, 100)
	insertTopUpForInvoiceTest(t, 20, 603, "inv-attach-1", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 21, 603, "inv-attach-2", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)

	first := newInvoiceFixture(t, 603, []*TopUp{{Id: 20}, {Id: 21}})

	// A second application referencing an already-attached order must fail
	// atomically, leaving no orphan application behind.
	second := &Invoice{Title: "Acme", TaxId: "T", Email: "b@acme.example", Reason: "r"}
	err := CreateInvoiceApplication(603, second, []*TopUp{{Id: 21}})
	require.Error(t, err)
	assert.Zero(t, second.Id)

	var total int64
	require.NoError(t, DB.Model(&Invoice{}).Count(&total).Error)
	assert.EqualValues(t, 1, total)
	assert.NotNil(t, GetInvoiceById(first.Id))
}

func TestCreateInvoiceApplication_RejectsMixedCurrency(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 604, 100)
	insertTopUpForInvoiceTest(t, 30, 604, "inv-cur-1", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 31, 604, "inv-cur-2", 10, "USD", PaymentProviderStripe, common.TopUpStatusSuccess)

	inv := &Invoice{Title: "Acme", TaxId: "T", Email: "b@acme.example", Reason: "r"}
	err := CreateInvoiceApplication(604, inv, []*TopUp{{Id: 30}, {Id: 31}})
	require.Error(t, err)
	assert.Zero(t, inv.Id)
}

func TestCreateInvoiceApplication_RejectsBalanceAndPendingOrders(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 605, 100)
	insertTopUpForInvoiceTest(t, 40, 605, "inv-bal-1", 10, "CNY", PaymentProviderBalance, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 41, 605, "inv-pend-1", 10, "CNY", PaymentProviderEpay, common.TopUpStatusPending)

	inv := &Invoice{Title: "Acme", TaxId: "T", Email: "b@acme.example", Reason: "r"}
	require.Error(t, CreateInvoiceApplication(605, inv, []*TopUp{{Id: 40}}))
	require.Error(t, CreateInvoiceApplication(605, inv, []*TopUp{{Id: 41}}))
	assert.Zero(t, inv.Id)
}

func TestInvoiceStateTransitions(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 606, 100)
	insertTopUpForInvoiceTest(t, 50, 606, "inv-state-1", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	inv := newInvoiceFixture(t, 606, []*TopUp{{Id: 50}})

	require.NoError(t, ApproveInvoice(inv.Id, "ok"))
	assert.Equal(t, InvoiceStatusApproved, GetInvoiceById(inv.Id).Status)

	// Invalid transition from approved back to pending must fail.
	require.Error(t, transitionInvoice(inv.Id, InvoiceStatusPending, InvoiceStatusApproved, ""))
	assert.Equal(t, InvoiceStatusApproved, GetInvoiceById(inv.Id).Status)

	require.NoError(t, StartIssueInvoice(inv.Id, "issuing now"))
	assert.Equal(t, InvoiceStatusIssuing, GetInvoiceById(inv.Id).Status)

	require.NoError(t, CompleteIssueInvoice(inv.Id, "invoice no 123"))
	issued := GetInvoiceById(inv.Id)
	assert.Equal(t, InvoiceStatusIssued, issued.Status)
	assert.Equal(t, "invoice no 123", issued.AdminNote)

	// An issued order stays excluded from invoiceable orders.
	eligible, err := GetInvoiceableTopUps(606)
	require.NoError(t, err)
	assert.Empty(t, eligible)
}

func TestInvoiceRejectAndCancel(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 607, 100)
	insertTopUpForInvoiceTest(t, 60, 607, "inv-rej-1", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	inv := newInvoiceFixture(t, 607, []*TopUp{{Id: 60}})

	require.NoError(t, RejectInvoice(inv.Id, "missing tax id"))
	rejected := GetInvoiceById(inv.Id)
	assert.Equal(t, InvoiceStatusRejected, rejected.Status)
	assert.Equal(t, "missing tax id", rejected.AdminNote)

	// Rejected application frees the order.
	eligible, err := GetInvoiceableTopUps(607)
	require.NoError(t, err)
	require.Len(t, eligible, 1)
	assert.Equal(t, 60, eligible[0].Id)

	// Only the owner can cancel and only while pending.
	insertTopUpForInvoiceTest(t, 61, 607, "inv-cancel-1", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	cancelInv := newInvoiceFixture(t, 607, []*TopUp{{Id: 61}})
	require.Error(t, CancelInvoice(cancelInv.Id, 999))
	require.NoError(t, CancelInvoice(cancelInv.Id, 607))
	assert.Equal(t, InvoiceStatusCancelled, GetInvoiceById(cancelInv.Id).Status)

	require.Error(t, ApproveInvoice(rejected.Id, ""))
}
