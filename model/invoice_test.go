package model

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
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
	require.NoError(t, CreateInvoiceApplication(userId, inv, orders, decimal.Zero))
	return inv
}

func TestGetInvoiceableTopUps_FiltersEligibility(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 601, 100)

	insertTopUpForInvoiceTest(t, 1, 601, "inv-topup-1", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 2, 601, "inv-topup-2", 20, "CNY", PaymentProviderBalance, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 3, 601, "inv-topup-3", 30, "CNY", PaymentProviderEpay, common.TopUpStatusPending)
	insertTopUpForInvoiceTest(t, 4, 601, "inv-topup-4", 40, "USD", PaymentProviderStripe, common.TopUpStatusSuccess)
	// Orders without a payment snapshot must never be invoiceable.
	insertTopUpForInvoiceTest(t, 6, 601, "inv-topup-6", 60, "", PaymentProviderEpay, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 7, 601, "inv-topup-7", 70, "CNY", "", common.TopUpStatusSuccess)
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
	assert.Equal(t, []int{4, 1}, ids) // newest first; 2/3/5/6/7 excluded

	// Rejecting the application frees order 5 again.
	_, err = RejectInvoice(active.Id, "not eligible")
	require.NoError(t, err)
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

	// A claim row must exist for each attached order.
	var claims []InvoiceOrderClaim
	require.NoError(t, DB.Where("invoice_id = ?", inv.Id).Find(&claims).Error)
	require.Len(t, claims, 2)
}

func TestInvoiceProfileRoundTrip(t *testing.T) {
	userId := 608
	_ = DB.Where("user_id = ?", userId).Delete(&InvoiceProfile{}).Error

	saved := &InvoiceProfile{
		UserId:      userId,
		Title:       "Acme Inc.",
		TaxId:       "91310000TEST",
		Phone:       "13800000000",
		Address:     "Shanghai",
		BankName:    "Test Bank",
		BankAccount: "6222000000000000",
		Email:       "billing@acme.example",
	}
	require.NoError(t, SaveInvoiceProfile(saved))

	loaded, err := GetInvoiceProfile(userId)
	require.NoError(t, err)
	assert.Equal(t, saved.Title, loaded.Title)
	assert.Equal(t, saved.TaxId, loaded.TaxId)
	assert.Equal(t, saved.Email, loaded.Email)

	saved.Title = "Acme Updated"
	saved.Phone = "13900000000"
	require.NoError(t, SaveInvoiceProfile(saved))
	loaded, err = GetInvoiceProfile(userId)
	require.NoError(t, err)
	assert.Equal(t, "Acme Updated", loaded.Title)
	assert.Equal(t, "13900000000", loaded.Phone)
}

func TestSaveInvoiceProfile_ConcurrentSavesAreAtomic(t *testing.T) {
	userId := 6081
	_ = DB.Where("user_id = ?", userId).Delete(&InvoiceProfile{}).Error

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			p := &InvoiceProfile{
				UserId:      userId,
				Title:       "Concurrent " + string(rune('A'+n)),
				TaxId:       "TAX",
				Email:       "c@example.com",
				InvoiceType: InvoiceTypeCompany,
			}
			errs <- SaveInvoiceProfile(p)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	var count int64
	require.NoError(t, DB.Model(&InvoiceProfile{}).Where("user_id = ?", userId).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestCreateInvoiceApplication_InvoiceTypeReasonRules(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 609, 100)
	insertTopUpForInvoiceTest(t, 70, 609, "inv-individual-reason", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 71, 609, "inv-company-no-reason", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)

	individual := &Invoice{
		InvoiceType: InvoiceTypeIndividual,
		Title:       "Alex",
		TaxId:       "ID-609",
		Email:       "alex@example.com",
	}
	require.ErrorIs(t, CreateInvoiceApplication(609, individual, []*TopUp{{Id: 70}}, decimal.Zero), ErrInvoiceReasonRequired)
	assert.Zero(t, individual.Id)

	company := &Invoice{
		InvoiceType: InvoiceTypeCompany,
		Title:       "Acme Inc.",
		TaxId:       "91310000TEST",
		Email:       "billing@acme.example",
	}
	require.NoError(t, CreateInvoiceApplication(609, company, []*TopUp{{Id: 71}}, decimal.Zero))
	assert.Equal(t, InvoiceStatusPending, company.Status)
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
	err := CreateInvoiceApplication(603, second, []*TopUp{{Id: 21}}, decimal.Zero)
	require.ErrorIs(t, err, ErrInvoiceOrderClaimed)
	assert.Nil(t, GetInvoiceById(second.Id)) // rolled back, not persisted

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
	err := CreateInvoiceApplication(604, inv, []*TopUp{{Id: 30}, {Id: 31}}, decimal.Zero)
	require.ErrorIs(t, err, ErrInvoiceMixedCurrency)
	assert.Zero(t, inv.Id)
}

func TestCreateInvoiceApplication_RejectsBalanceAndPendingOrders(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 605, 100)
	insertTopUpForInvoiceTest(t, 40, 605, "inv-bal-1", 10, "CNY", PaymentProviderBalance, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 41, 605, "inv-pend-1", 10, "CNY", PaymentProviderEpay, common.TopUpStatusPending)

	inv := &Invoice{Title: "Acme", TaxId: "T", Email: "b@acme.example", Reason: "r"}
	require.ErrorIs(t, CreateInvoiceApplication(605, inv, []*TopUp{{Id: 40}}, decimal.Zero), ErrInvoiceBalanceOrder)
	require.ErrorIs(t, CreateInvoiceApplication(605, inv, []*TopUp{{Id: 41}}, decimal.Zero), ErrInvoiceOrderNotPaid)
	assert.Zero(t, inv.Id)
}

func TestCreateInvoiceApplication_RejectsMissingSnapshotAndInvalidMoney(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 610, 100)
	insertTopUpForInvoiceTest(t, 80, 610, "inv-snap-provider", 10, "CNY", "", common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 81, 610, "inv-snap-currency", 10, "", PaymentProviderEpay, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 82, 610, "inv-money-zero", 0, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 83, 610, "inv-money-neg", -5, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)

	inv := &Invoice{Title: "Acme", TaxId: "T", Email: "b@acme.example", Reason: "r"}
	require.ErrorIs(t, CreateInvoiceApplication(610, inv, []*TopUp{{Id: 80}}, decimal.Zero), ErrInvoiceMissingProvider)
	require.ErrorIs(t, CreateInvoiceApplication(610, inv, []*TopUp{{Id: 81}}, decimal.Zero), ErrInvoiceMissingCurrency)
	require.ErrorIs(t, CreateInvoiceApplication(610, inv, []*TopUp{{Id: 82}}, decimal.Zero), ErrInvoiceInvalidAmount)
	require.ErrorIs(t, CreateInvoiceApplication(610, inv, []*TopUp{{Id: 83}}, decimal.Zero), ErrInvoiceInvalidAmount)
	assert.Zero(t, inv.Id)
}

func TestCreateInvoiceApplication_MinimumAmountBoundary(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 611, 100)
	// Exact minimum (100.00) must pass; 99.99 must fail.
	insertTopUpForInvoiceTest(t, 90, 611, "inv-min-exact", 100.00, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 91, 611, "inv-min-low", 99.99, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)

	min := decimal.NewFromFloat(100.00)
	exact := &Invoice{Title: "Acme", TaxId: "T", Email: "b@acme.example", Reason: "r"}
	require.NoError(t, CreateInvoiceApplication(611, exact, []*TopUp{{Id: 90}}, min))
	assert.InDelta(t, 100.00, exact.TotalAmount, 0.0001)

	low := &Invoice{Title: "Acme", TaxId: "T", Email: "b@acme.example", Reason: "r"}
	require.ErrorIs(t, CreateInvoiceApplication(611, low, []*TopUp{{Id: 91}}, min), ErrInvoiceBelowMinimum)
	assert.Zero(t, low.Id)
}

func TestCreateInvoiceApplication_FloatBoundaryAmountsSumExact(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 612, 100)
	// 0.1 + 0.2 would be 0.30000000000000004 in bare float64; decimal keeps 0.3.
	insertTopUpForInvoiceTest(t, 100, 612, "inv-float-1", 0.1, "USD", PaymentProviderStripe, common.TopUpStatusSuccess)
	insertTopUpForInvoiceTest(t, 101, 612, "inv-float-2", 0.2, "USD", PaymentProviderStripe, common.TopUpStatusSuccess)

	inv := &Invoice{Title: "Acme", TaxId: "T", Email: "b@acme.example", Reason: "r"}
	require.NoError(t, CreateInvoiceApplication(612, inv, []*TopUp{{Id: 100}, {Id: 101}}, decimal.Zero))
	// Exact boundary check: the decimal sum is exactly 0.3, so a minimum of
	// 0.3 passes even though float64(0.1)+float64(0.2) > 0.3.
	_, err := RejectInvoice(inv.Id, "")
	require.NoError(t, err)

	inv2 := &Invoice{Title: "Acme", TaxId: "T", Email: "b@acme.example", Reason: "r"}
	require.NoError(t, CreateInvoiceApplication(612, inv2, []*TopUp{{Id: 100}, {Id: 101}}, decimal.NewFromFloat(0.3)))
}

func TestValidInvoiceMinAmount_RejectsInvalidValues(t *testing.T) {
	for _, invalid := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, -0.01} {
		assert.False(t, ValidInvoiceMinAmount(invalid), "%v", invalid)
	}
	for _, valid := range []float64{0, 0.01, 1, 100.5, 1e6} {
		assert.True(t, ValidInvoiceMinAmount(valid), "%v", valid)
	}
}

func TestInvoiceStateTransitions(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 606, 100)
	insertTopUpForInvoiceTest(t, 50, 606, "inv-state-1", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	inv := newInvoiceFixture(t, 606, []*TopUp{{Id: 50}})

	_, err := ApproveInvoice(inv.Id, "ok")
	require.NoError(t, err)
	assert.Equal(t, InvoiceStatusApproved, GetInvoiceById(inv.Id).Status)

	// Repeating the same transition is idempotent.
	_, err = ApproveInvoice(inv.Id, "")
	require.NoError(t, err)

	// A genuinely invalid transition (approved -> pending) must fail.
	_, err = transitionInvoice(inv.Id, InvoiceStatusApproved, InvoiceStatusPending, "")
	require.ErrorIs(t, err, ErrInvoiceInvalidTransition)
	assert.Equal(t, InvoiceStatusApproved, GetInvoiceById(inv.Id).Status)

	_, err = StartIssueInvoice(inv.Id, "issuing now")
	require.NoError(t, err)
	assert.Equal(t, InvoiceStatusIssuing, GetInvoiceById(inv.Id).Status)

	// Repeating the same approved->issuing transition is idempotent.
	_, err = StartIssueInvoice(inv.Id, "")
	require.NoError(t, err)

	_, err = CompleteIssueInvoice(inv.Id, "invoice no 123", func(*Invoice) error { return nil })
	require.NoError(t, err)
	issued := GetInvoiceById(inv.Id)
	assert.Equal(t, InvoiceStatusIssued, issued.Status)
	assert.Equal(t, "invoice no 123", issued.AdminNote)

	// An idempotent repeat reports no change and does not invoke delivery again.
	_, err = CompleteIssueInvoice(inv.Id, "", func(*Invoice) error { return nil })
	require.NoError(t, err)

	// An issued order stays excluded from invoiceable orders.
	eligible, err := GetInvoiceableTopUps(606)
	require.NoError(t, err)
	assert.Empty(t, eligible)
}

func TestTransitionKeepsLastAdminNote(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 613, 100)
	insertTopUpForInvoiceTest(t, 110, 613, "inv-note-1", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	inv := newInvoiceFixture(t, 613, []*TopUp{{Id: 110}})

	_, err := ApproveInvoice(inv.Id, "approved with note")
	require.NoError(t, err)
	assert.Equal(t, "approved with note", GetInvoiceById(inv.Id).AdminNote)

	// An empty note on the next transition must not clear the previous one.
	_, err = StartIssueInvoice(inv.Id, "")
	require.NoError(t, err)
	assert.Equal(t, "approved with note", GetInvoiceById(inv.Id).AdminNote)
}

func TestInvoiceRejectAndCancel(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 607, 100)
	insertTopUpForInvoiceTest(t, 60, 607, "inv-rej-1", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	inv := newInvoiceFixture(t, 607, []*TopUp{{Id: 60}})

	_, err := RejectInvoice(inv.Id, "missing tax id")
	require.NoError(t, err)
	rejected := GetInvoiceById(inv.Id)
	assert.Equal(t, InvoiceStatusRejected, rejected.Status)
	assert.Equal(t, "missing tax id", rejected.AdminNote)

	// Rejected application frees the order (claim released).
	eligible, err := GetInvoiceableTopUps(607)
	require.NoError(t, err)
	require.Len(t, eligible, 1)
	assert.Equal(t, 60, eligible[0].Id)

	var claims int64
	require.NoError(t, DB.Model(&InvoiceOrderClaim{}).Where("invoice_id = ?", inv.Id).Count(&claims).Error)
	assert.Zero(t, claims)

	// Only the owner can cancel and only while pending.
	insertTopUpForInvoiceTest(t, 61, 607, "inv-cancel-1", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	cancelInv := newInvoiceFixture(t, 607, []*TopUp{{Id: 61}})
	_, err = CancelInvoice(cancelInv.Id, 999)
	require.ErrorIs(t, err, ErrInvoiceNotOwner)
	_, err = CancelInvoice(cancelInv.Id, 607)
	require.NoError(t, err)
	assert.Equal(t, InvoiceStatusCancelled, GetInvoiceById(cancelInv.Id).Status)

	// Repeating the cancel is idempotent.
	_, err = CancelInvoice(cancelInv.Id, 607)
	require.NoError(t, err)

	_, err = ApproveInvoice(rejected.Id, "")
	require.Error(t, err)
}

func TestConcurrentInvoiceApplicationsDoNotDoubleAttachOrder(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 614, 100)
	insertTopUpForInvoiceTest(t, 120, 614, "inv-conc-1", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)

	const attempts = 8
	var wg sync.WaitGroup
	errs := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inv := &Invoice{Title: "Acme", TaxId: "T", Email: "b@acme.example", Reason: "r"}
			errs <- CreateInvoiceApplication(614, inv, []*TopUp{{Id: 120}}, decimal.Zero)
		}()
	}
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		} else {
			assert.ErrorIs(t, err, ErrInvoiceOrderClaimed)
		}
	}
	assert.Equal(t, 1, successes)

	var total int64
	require.NoError(t, DB.Model(&Invoice{}).Count(&total).Error)
	assert.EqualValues(t, 1, total)
}

func TestConcurrentCompleteIssueDeliversOnlyOnce(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 615, 100)
	insertTopUpForInvoiceTest(t, 121, 615, "inv-complete-concurrent", 10, "CNY", PaymentProviderEpay, common.TopUpStatusSuccess)
	inv := newInvoiceFixture(t, 615, []*TopUp{{Id: 121}})
	_, err := ApproveInvoice(inv.Id, "")
	require.NoError(t, err)
	_, err = StartIssueInvoice(inv.Id, "")
	require.NoError(t, err)

	const attempts = 4
	start := make(chan struct{})
	results := make(chan struct {
		changed bool
		err     error
	}, attempts)
	var deliveries atomic.Int32
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			changed, err := CompleteIssueInvoice(inv.Id, "", func(*Invoice) error {
				deliveries.Add(1)
				return nil
			})
			results <- struct {
				changed bool
				err     error
			}{changed: changed, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	changes := 0
	for result := range results {
		require.NoError(t, result.err)
		if result.changed {
			changes++
		}
	}
	assert.Equal(t, 1, changes)
	assert.Equal(t, int32(1), deliveries.Load())
	assert.Equal(t, InvoiceStatusIssued, GetInvoiceById(inv.Id).Status)
}

// Test helpers referenced by boundary tests.
