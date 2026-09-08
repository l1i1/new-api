package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertInvKindFixture builds a base invoice application for the kind tests.
func insertInvKindUserAndOrder(t *testing.T, userId int, topUpId int) {
	t.Helper()
	insertUserForPaymentGuardTest(t, userId, 10_000_000)
	insertTopUpForInvoiceTest(t, topUpId, userId, "inv-kind-"+string(rune(userId)), 100, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
}

func kindInvoice(otype string, kind string, full bool) *Invoice {
	inv := &Invoice{
		InvoiceType: otype,
		InvoiceKind: kind,
		Title:       "Acme Kind",
		TaxId:       "91310000KIND",
		Email:       "billing@kind.example",
		Reason:      "reimbursement",
	}
	if full {
		inv.Address = "Shanghai"
		inv.Phone = "13800000000"
		inv.BankName = "Test Bank"
		inv.BankAccount = "62228888"
	}
	return inv
}

func TestCreateInvoiceSpecial_RequiresOrganization(t *testing.T) {
	truncateTables(t)
	insertInvKindUserAndOrder(t, 910, 9100)

	inv := kindInvoice(InvoiceTypeIndividual, InvoiceKindSpecial, true)
	err := CreateInvoiceApplicationWithPaymentMethods(910, inv, []*TopUp{{Id: 9100}}, decimal.Zero, nil, decimal.Zero)
	require.ErrorIs(t, err, ErrInvoiceSpecialRequiresOrg)
	assert.Nil(t, GetInvoiceById(inv.Id))
}

func TestCreateInvoiceSpecial_RequiresFullInfo(t *testing.T) {
	truncateTables(t)
	insertInvKindUserAndOrder(t, 911, 9110)

	// Organization but missing bank details: must be rejected.
	inv := kindInvoice(InvoiceTypeOrganization, InvoiceKindSpecial, false)
	err := CreateInvoiceApplicationWithPaymentMethods(911, inv, []*TopUp{{Id: 9110}}, decimal.Zero, nil, decimal.Zero)
	require.ErrorIs(t, err, ErrInvoiceSpecialRequiresFullInfo)
	assert.Nil(t, GetInvoiceById(inv.Id))
}

func TestCreateInvoiceSpecial_WithCompleteInfoSucceeds(t *testing.T) {
	truncateTables(t)
	insertInvKindUserAndOrder(t, 912, 9120)

	inv := kindInvoice(InvoiceTypeOrganization, InvoiceKindSpecial, true)
	err := CreateInvoiceApplicationWithPaymentMethods(912, inv, []*TopUp{{Id: 9120}}, decimal.Zero, nil, decimal.Zero)
	require.NoError(t, err)
	require.Greater(t, inv.Id, 0)

	stored := GetInvoiceById(inv.Id)
	require.NotNil(t, stored)
	assert.Equal(t, InvoiceKindSpecial, stored.InvoiceKind)
	assert.Equal(t, InvoiceTypeOrganization, stored.InvoiceType)
}

func TestCreateInvoiceGeneral_KeepsExistingRules(t *testing.T) {
	truncateTables(t)
	insertInvKindUserAndOrder(t, 913, 9130)

	// Organization + general kind, no bank/address: current rules unchanged.
	inv := kindInvoice(InvoiceTypeOrganization, InvoiceKindGeneral, false)
	err := CreateInvoiceApplicationWithPaymentMethods(913, inv, []*TopUp{{Id: 9130}}, decimal.Zero, nil, decimal.Zero)
	require.NoError(t, err)
	assert.Greater(t, inv.Id, 0)
	assert.Equal(t, InvoiceKindGeneral, inv.InvoiceKind)
}

func TestNormalizeInvoiceKindDefaultsEmptyToGeneral(t *testing.T) {
	assert.Equal(t, InvoiceKindGeneral, NormalizeInvoiceKind(""))
	assert.Equal(t, InvoiceKindGeneral, NormalizeInvoiceKind("  "))
	assert.Equal(t, InvoiceKindSpecial, NormalizeInvoiceKind("special"))
	assert.Equal(t, InvoiceKindSpecial, NormalizeInvoiceKind(" SPECIAL "))
}

func TestIsValidInvoiceKind(t *testing.T) {
	assert.True(t, IsValidInvoiceKind("general"))
	assert.True(t, IsValidInvoiceKind("special"))
	assert.True(t, IsValidInvoiceKind(""))
	assert.False(t, IsValidInvoiceKind("other"))
	assert.False(t, IsValidInvoiceKind("vatinv"))
}
