package model

import (
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Invoice application statuses. The fine-grained state machine is:
//
//	pending --approve--> approved --start-issue--> issuing --complete-issue--> issued
//	pending --reject--> rejected
//	pending --cancel (user)--> cancelled
//
// rejected and cancelled free the attached orders so they become invoiceable again.
const (
	InvoiceStatusPending   = "pending"
	InvoiceStatusApproved  = "approved"
	InvoiceStatusIssuing   = "issuing"
	InvoiceStatusIssued    = "issued"
	InvoiceStatusRejected  = "rejected"
	InvoiceStatusCancelled = "cancelled"
)

const InvoiceOrderTypeTopUp = "topup"

const (
	InvoiceTypeIndividual = "individual"
	InvoiceTypeCompany    = "company"
)

// Invoice eligibility and transition errors. Controllers map these to
// localized user-facing messages; the errors themselves stay language-neutral.
var (
	ErrInvoiceInvalid           = errors.New("invalid invoice application")
	ErrInvoiceTypeInvalid       = errors.New("invalid invoice type")
	ErrInvoiceReasonRequired    = errors.New("individual invoices require a reason")
	ErrInvoiceNoOrders          = errors.New("no orders selected")
	ErrInvoiceOrderNotFound     = errors.New("some orders do not exist")
	ErrInvoiceOrderNotOwned     = errors.New("order does not belong to the user")
	ErrInvoiceOrderNotPaid      = errors.New("order is not paid")
	ErrInvoiceBalanceOrder      = errors.New("balance credit orders cannot be invoiced")
	ErrInvoiceMissingProvider   = errors.New("order payment provider is missing")
	ErrInvoiceMissingCurrency   = errors.New("order payment currency is missing")
	ErrInvoiceInvalidAmount     = errors.New("order amount is not a positive finite number")
	ErrInvoiceOrderClaimed      = errors.New("order is already attached to an invoice application")
	ErrInvoiceMixedCurrency     = errors.New("all selected orders must use the same currency")
	ErrInvoiceBelowMinimum      = errors.New("invoice amount is below the minimum")
	ErrInvoiceNotFound          = errors.New("invoice application not found")
	ErrInvoiceNotOwner          = errors.New("not the invoice owner")
	ErrInvoiceOnlyPendingCancel = errors.New("only pending invoice applications can be cancelled")
	ErrInvoiceInvalidTransition = errors.New("invalid invoice transition")
	ErrInvoiceNotIssuing        = errors.New("invoice is not in the issuing status")
)

// Invoice is an invoice application created by a user. Material fields are PII
// and must only be exposed to the owner and administrators (detail endpoints,
// never list endpoints).
type Invoice struct {
	Id          int     `json:"id"`
	UserId      int     `json:"user_id" gorm:"index"`
	InvoiceType string  `json:"invoice_type" gorm:"type:varchar(16)"`
	Title       string  `json:"title" gorm:"type:varchar(255)"`
	TaxId       string  `json:"tax_id" gorm:"type:varchar(64)"`
	Phone       string  `json:"phone" gorm:"type:varchar(32)"`
	Address     string  `json:"address" gorm:"type:varchar(255)"`
	BankName    string  `json:"bank_name" gorm:"type:varchar(128)"`
	BankAccount string  `json:"bank_account" gorm:"type:varchar(64)"`
	Email       string  `json:"email" gorm:"type:varchar(255)"`
	Reason      string  `json:"reason" gorm:"type:varchar(512)"`
	Remark      string  `json:"remark" gorm:"type:varchar(512)"`
	Status      string  `json:"status" gorm:"type:varchar(16);index"`
	AdminNote   string  `json:"admin_note" gorm:"type:varchar(512)"`
	TotalAmount float64 `json:"total_amount"`
	Currency    string  `json:"currency" gorm:"type:varchar(8)"`
	CreateTime  int64   `json:"create_time"`
	UpdateTime  int64   `json:"update_time"`
}

// InvoiceProfile stores the user's reusable billing information separately
// from individual invoice applications. Reason and remark remain application-
// specific and are intentionally not persisted here.
type InvoiceProfile struct {
	Id          int    `json:"id"`
	UserId      int    `json:"user_id" gorm:"uniqueIndex"`
	InvoiceType string `json:"invoice_type" gorm:"type:varchar(16)"`
	Title       string `json:"title" gorm:"type:varchar(255)"`
	TaxId       string `json:"tax_id" gorm:"type:varchar(64)"`
	Phone       string `json:"phone" gorm:"type:varchar(32)"`
	Address     string `json:"address" gorm:"type:varchar(255)"`
	BankName    string `json:"bank_name" gorm:"type:varchar(128)"`
	BankAccount string `json:"bank_account" gorm:"type:varchar(64)"`
	Email       string `json:"email" gorm:"type:varchar(255)"`
	CreateTime  int64  `json:"create_time"`
	UpdateTime  int64  `json:"update_time"`
}

// InvoiceItem snapshots one paid order attached to an invoice application.
// Historical items survive rejection and cancellation; they are not used for
// duplicate detection (see InvoiceOrderClaim).
type InvoiceItem struct {
	Id            int     `json:"id"`
	InvoiceId     int     `json:"invoice_id" gorm:"index"`
	OrderType     string  `json:"order_type" gorm:"type:varchar(16)"`
	OrderId       int     `json:"order_id"`
	TradeNo       string  `json:"trade_no" gorm:"type:varchar(255)"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency" gorm:"type:varchar(8)"`
	PaymentMethod string  `json:"payment_method" gorm:"type:varchar(50)"`
}

// InvoiceOrderClaim is the current active claim of a paid order by an invoice
// application. The unique (order_type, order_id) index is the database-level
// protection against two concurrent applications attaching the same order.
// Claims are inserted when an application is created and deleted when it
// becomes rejected or cancelled.
type InvoiceOrderClaim struct {
	Id         int    `json:"id"`
	OrderType  string `json:"order_type" gorm:"type:varchar(16);uniqueIndex:idx_invoice_order_claim"`
	OrderId    int    `json:"order_id" gorm:"uniqueIndex:idx_invoice_order_claim"`
	InvoiceId  int    `json:"invoice_id" gorm:"index"`
	CreateTime int64  `json:"create_time"`
}

// InvoiceListItem is the user-facing list row: it carries only the columns the
// user list needs and never exposes invoice material.
type InvoiceListItem struct {
	Id          int     `json:"id"`
	InvoiceType string  `json:"invoice_type"`
	Status      string  `json:"status"`
	TotalAmount float64 `json:"total_amount"`
	Currency    string  `json:"currency"`
	CreateTime  int64   `json:"create_time"`
	UpdateTime  int64   `json:"update_time"`
}

// AdminInvoiceListItem is the administrator list row. It must not carry tax id,
// bank account, address, phone, email, reason, remark, or admin note.
type AdminInvoiceListItem struct {
	Id          int     `json:"id"`
	UserId      int     `json:"user_id"`
	InvoiceType string  `json:"invoice_type"`
	Title       string  `json:"title"`
	Status      string  `json:"status"`
	TotalAmount float64 `json:"total_amount"`
	Currency    string  `json:"currency"`
	CreateTime  int64   `json:"create_time"`
	UpdateTime  int64   `json:"update_time"`
}

// Money is the canonical decimal-based money value for invoice arithmetic.
// Summing and comparing through decimal avoids float64 drift and makes exact
// minimum-boundary checks deterministic.
type Money = decimal.Decimal

// MoneyFromFloat converts a stored float64 money snapshot into a decimal value,
// rejecting NaN and infinite values that could corrupt sums or comparisons.
func MoneyFromFloat(value float64) (Money, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return decimal.Decimal{}, ErrInvoiceInvalidAmount
	}
	return decimal.NewFromFloat(value), nil
}

// ValidInvoiceMinAmount reports whether the configured minimum is a finite
// non-negative decimal value. NaN, +Inf, -Inf and negatives are invalid.
func ValidInvoiceMinAmount(value float64) bool {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return false
	}
	return true
}

// InvoiceMinAmountFromFloat returns the configured minimum as a decimal, or 0
// when the stored value is invalid (fail-closed: an invalid minimum disables
// the threshold rather than blocking invoicing with a bogus cap).
func InvoiceMinAmountFromFloat(value float64) Money {
	if !ValidInvoiceMinAmount(value) {
		return decimal.Zero
	}
	return decimal.NewFromFloat(value)
}

func isDuplicateKeyError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate") || strings.Contains(msg, "unique")
}

func validInvoiceType(invoiceType string) bool {
	return invoiceType == InvoiceTypeIndividual || invoiceType == InvoiceTypeCompany
}

// validateInvoiceTopUp re-checks one locked order against the invoice
// eligibility rules: belongs to the user, successfully paid, non-balance
// provider with a snapshot, non-empty currency, and a positive finite money.
func validateInvoiceTopUp(order *TopUp) error {
	if order.Status != common.TopUpStatusSuccess {
		return ErrInvoiceOrderNotPaid
	}
	if order.PaymentProvider == "" {
		return ErrInvoiceMissingProvider
	}
	if order.PaymentProvider == PaymentProviderBalance {
		return ErrInvoiceBalanceOrder
	}
	if order.PaymentCurrency == "" {
		return ErrInvoiceMissingCurrency
	}
	money, err := MoneyFromFloat(order.Money)
	if err != nil {
		return err
	}
	if !money.IsPositive() {
		return ErrInvoiceInvalidAmount
	}
	return nil
}

// GetInvoiceableTopUps returns the user's paid orders that are still available
// for an invoice application: successfully paid with a complete payment
// snapshot, not a manual balance credit, and not currently claimed by any
// active invoice application (claims table, not historical items).
func GetInvoiceableTopUps(userId int) ([]*TopUp, error) {
	var topups []*TopUp
	err := DB.
		Where("user_id = ? AND status = ? AND payment_provider <> ? AND payment_provider <> '' AND payment_currency <> ''", userId, common.TopUpStatusSuccess, PaymentProviderBalance).
		Where("id NOT IN (?)", DB.Model(&InvoiceOrderClaim{}).Select("order_id").Where("order_type = ?", InvoiceOrderTypeTopUp)).
		Order("id desc").
		Find(&topups).Error
	if err != nil {
		return nil, err
	}
	// The SQL predicates cannot reliably exclude non-finite money values;
	// filter them here so the options list mirrors the creation rules.
	eligible := make([]*TopUp, 0, len(topups))
	for _, topup := range topups {
		if money, err := MoneyFromFloat(topup.Money); err == nil && money.IsPositive() {
			eligible = append(eligible, topup)
		}
	}
	return eligible, nil
}

// CreateInvoiceApplication atomically validates the selected orders and creates
// an invoice application with its item snapshots and order claims. The orders
// are row-locked (in sorted id order) so concurrent applications cannot
// double-attach the same paid order; the unique claim index is the final
// backstop. minAmount is re-checked inside the transaction using decimal
// arithmetic.
func CreateInvoiceApplication(userId int, inv *Invoice, itemOrders []*TopUp, minAmount Money) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if inv == nil {
			return ErrInvoiceInvalid
		}
		if inv.InvoiceType == "" {
			inv.InvoiceType = InvoiceTypeCompany
		}
		if !validInvoiceType(inv.InvoiceType) {
			return ErrInvoiceTypeInvalid
		}
		if inv.InvoiceType == InvoiceTypeIndividual && strings.TrimSpace(inv.Reason) == "" {
			return ErrInvoiceReasonRequired
		}
		if len(itemOrders) == 0 {
			return ErrInvoiceNoOrders
		}

		orderIds := make([]int, 0, len(itemOrders))
		for _, o := range itemOrders {
			orderIds = append(orderIds, o.Id)
		}
		// Lock in sorted id order so concurrent multi-order applications
		// acquire locks in the same sequence and cannot deadlock each other.
		sort.Ints(orderIds)

		var locked []TopUp
		if err := lockForUpdate(tx).Where("id IN ?", orderIds).Find(&locked).Error; err != nil {
			return err
		}
		if len(locked) != len(orderIds) {
			return ErrInvoiceOrderNotFound
		}

		currency := ""
		total := decimal.Zero
		for _, o := range locked {
			if o.UserId != userId {
				return ErrInvoiceOrderNotOwned
			}
			if err := validateInvoiceTopUp(&o); err != nil {
				return err
			}
			orderCurrency := o.PaymentCurrency
			if currency == "" {
				currency = orderCurrency
			} else if currency != orderCurrency {
				return ErrInvoiceMixedCurrency
			}
			orderMoney, err := MoneyFromFloat(o.Money)
			if err != nil {
				return err
			}
			total = total.Add(orderMoney)
		}

		// The minimum is enforced again here, inside the transaction, so a
		// stale or tampered pre-check cannot bypass the business rule.
		if minAmount.IsPositive() && total.LessThan(minAmount) {
			return ErrInvoiceBelowMinimum
		}

		now := common.GetTimestamp()
		inv.UserId = userId
		inv.TotalAmount = total.InexactFloat64()
		inv.Currency = currency
		inv.Status = InvoiceStatusPending
		inv.CreateTime = now
		inv.UpdateTime = now
		if err := tx.Create(inv).Error; err != nil {
			return err
		}

		for _, o := range locked {
			claim := &InvoiceOrderClaim{
				OrderType:  InvoiceOrderTypeTopUp,
				OrderId:    o.Id,
				InvoiceId:  inv.Id,
				CreateTime: now,
			}
			if err := tx.Create(claim).Error; err != nil {
				if isDuplicateKeyError(err) {
					return ErrInvoiceOrderClaimed
				}
				return err
			}
		}

		for _, o := range locked {
			item := &InvoiceItem{
				InvoiceId:     inv.Id,
				OrderType:     InvoiceOrderTypeTopUp,
				OrderId:       o.Id,
				TradeNo:       o.TradeNo,
				Amount:        o.Money,
				Currency:      inv.Currency,
				PaymentMethod: o.PaymentMethod,
			}
			if err := tx.Create(item).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func GetInvoiceById(id int) *Invoice {
	var inv Invoice
	err := DB.Where("id = ?", id).First(&inv).Error
	if err != nil {
		return nil
	}
	return &inv
}

// GetInvoiceProfile returns the saved profile. Existing users without a
// profile are backfilled from their latest application without creating a
// second record during a read.
func GetInvoiceProfile(userId int) (*InvoiceProfile, error) {
	var profile InvoiceProfile
	err := DB.Where("user_id = ?", userId).First(&profile).Error
	if err == nil {
		return &profile, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	var latest Invoice
	err = DB.Where("user_id = ?", userId).Order("id desc").First(&latest).Error
	if err == nil {
		return &InvoiceProfile{
			UserId:      userId,
			InvoiceType: invoiceTypeOrCompany(latest.InvoiceType),
			Title:       latest.Title,
			TaxId:       latest.TaxId,
			Phone:       latest.Phone,
			Address:     latest.Address,
			BankName:    latest.BankName,
			BankAccount: latest.BankAccount,
			Email:       latest.Email,
		}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	return &InvoiceProfile{UserId: userId}, nil
}

// SaveInvoiceProfile atomically upserts the user's reusable billing
// information. The (user_id) unique index makes this an insert-or-update in a
// single statement, so concurrent saves cannot race through a read-then-write
// gap.
func SaveInvoiceProfile(profile *InvoiceProfile) error {
	if profile == nil || profile.UserId == 0 {
		return errors.New("invalid invoice profile")
	}
	if profile.InvoiceType == "" {
		profile.InvoiceType = InvoiceTypeCompany
	}
	if !validInvoiceType(profile.InvoiceType) {
		return errors.New("invalid invoice type")
	}

	now := common.GetTimestamp()
	if profile.CreateTime == 0 {
		profile.CreateTime = now
	}
	profile.UpdateTime = now
	return DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"invoice_type", "title", "tax_id", "phone", "address",
			"bank_name", "bank_account", "email", "update_time",
		}),
	}).Create(profile).Error
}

func invoiceTypeOrCompany(invoiceType string) string {
	if invoiceType == InvoiceTypeIndividual {
		return InvoiceTypeIndividual
	}
	return InvoiceTypeCompany
}

func GetInvoiceItems(invoiceId int) ([]*InvoiceItem, error) {
	var items []*InvoiceItem
	err := DB.Where("invoice_id = ?", invoiceId).Order("id asc").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

// GetUserInvoices returns the owner's invoice applications as PII-free list
// rows. The user list page never receives material fields.
func GetUserInvoices(userId int, pageInfo *common.PageInfo) ([]*InvoiceListItem, int64, error) {
	var items []*InvoiceListItem
	var total int64
	if err := DB.Model(&Invoice{}).Where("user_id = ?", userId).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := DB.Model(&Invoice{}).
		Select("id, invoice_type, status, total_amount, currency, create_time, update_time").
		Where("user_id = ?", userId).
		Order("id desc").
		Limit(pageInfo.GetPageSize()).
		Offset(pageInfo.GetStartIdx()).
		Scan(&items).Error
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// GetAllInvoices returns administrator list rows. The explicit DTO excludes
// tax id, bank account, address, phone, email, reason, remark, and admin note.
// keyword searches server-side without echoing sensitive columns in list rows.
func GetAllInvoices(pageInfo *common.PageInfo, keyword string, status string) ([]*AdminInvoiceListItem, int64, error) {
	query := DB.Model(&Invoice{})
	if keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("title LIKE ? OR tax_id LIKE ? OR email LIKE ? OR reason LIKE ?", like, like, like, like)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*AdminInvoiceListItem
	err := query.Select("id, user_id, invoice_type, title, status, total_amount, currency, create_time, update_time").
		Order("id desc").
		Limit(pageInfo.GetPageSize()).
		Offset(pageInfo.GetStartIdx()).
		Scan(&items).Error
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// validInvoiceTransitions is the allowed state machine. A transition is only
// legal when the exact (from, to) pair appears here.
var validInvoiceTransitions = map[[2]string]bool{
	{InvoiceStatusPending, InvoiceStatusApproved}:  true,
	{InvoiceStatusApproved, InvoiceStatusIssuing}:  true,
	{InvoiceStatusIssuing, InvoiceStatusIssued}:    true,
	{InvoiceStatusPending, InvoiceStatusRejected}:  true,
	{InvoiceStatusPending, InvoiceStatusCancelled}: true,
}

// transitionInvoice atomically moves an invoice from fromStatus to toStatus. It
// row-locks the invoice so concurrent admin actions cannot race. A repeat of the
// already-applied transition returns (false, nil) so callers can skip audit and
// notification side effects; any other status or any transition outside the
// allowed state machine is a business error. A non-empty admin note is kept for
// the last state change and an empty note never clears the previous one.
func transitionInvoice(invoiceId int, fromStatus string, toStatus string, adminNote string) (bool, error) {
	if !validInvoiceTransitions[[2]string{fromStatus, toStatus}] {
		return false, ErrInvoiceInvalidTransition
	}
	changed := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var inv Invoice
		if err := lockForUpdate(tx).Where("id = ?", invoiceId).First(&inv).Error; err != nil {
			return err
		}
		if inv.Status == toStatus {
			return nil // idempotent repeat of the same transition
		}
		if inv.Status != fromStatus {
			return ErrInvoiceInvalidTransition
		}
		inv.Status = toStatus
		if strings.TrimSpace(adminNote) != "" {
			inv.AdminNote = adminNote
		}
		inv.UpdateTime = common.GetTimestamp()
		if err := tx.Save(&inv).Error; err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

// releaseInvoiceClaims deletes the active order claims of an invoice inside the
// current transaction. It is called by reject and cancel so the attached orders
// become invoiceable again.
func releaseInvoiceClaims(tx *gorm.DB, invoiceId int) error {
	return tx.Where("invoice_id = ?", invoiceId).Delete(&InvoiceOrderClaim{}).Error
}

func ApproveInvoice(invoiceId int, note string) (bool, error) {
	return transitionInvoice(invoiceId, InvoiceStatusPending, InvoiceStatusApproved, note)
}

func StartIssueInvoice(invoiceId int, note string) (bool, error) {
	return transitionInvoice(invoiceId, InvoiceStatusApproved, InvoiceStatusIssuing, note)
}

// CompleteIssueInvoice serializes delivery for one application. The delivery
// callback runs while the row is locked so concurrent complete-issue requests
// cannot send the same attachment twice. A delivery failure rolls the
// transaction back and leaves the application in issuing for a fresh upload.
func CompleteIssueInvoice(invoiceId int, note string, deliver func(*Invoice) error) (bool, error) {
	changed := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var inv Invoice
		if err := lockForUpdate(tx).Where("id = ?", invoiceId).First(&inv).Error; err != nil {
			return err
		}
		if inv.Status == InvoiceStatusIssued {
			return nil // idempotent repeat; never replaces an issued PDF
		}
		if inv.Status != InvoiceStatusIssuing {
			return ErrInvoiceNotIssuing
		}
		if deliver == nil {
			return ErrInvoiceInvalid
		}
		if err := deliver(&inv); err != nil {
			return err
		}
		inv.Status = InvoiceStatusIssued
		if strings.TrimSpace(note) != "" {
			inv.AdminNote = note
		}
		inv.UpdateTime = common.GetTimestamp()
		if err := tx.Save(&inv).Error; err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

func RejectInvoice(invoiceId int, note string) (bool, error) {
	changed := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var inv Invoice
		if err := lockForUpdate(tx).Where("id = ?", invoiceId).First(&inv).Error; err != nil {
			return err
		}
		if inv.Status == InvoiceStatusRejected {
			return nil // idempotent repeat
		}
		if inv.Status != InvoiceStatusPending {
			return ErrInvoiceInvalidTransition
		}
		inv.Status = InvoiceStatusRejected
		if strings.TrimSpace(note) != "" {
			inv.AdminNote = note
		}
		inv.UpdateTime = common.GetTimestamp()
		if err := tx.Save(&inv).Error; err != nil {
			return err
		}
		if err := releaseInvoiceClaims(tx, invoiceId); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

// CancelInvoice lets the owner cancel their own pending application and frees
// the attached orders in the same transaction. It returns (false, nil) for an
// idempotent repeat of an already-cancelled application.
func CancelInvoice(invoiceId int, userId int) (bool, error) {
	changed := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var inv Invoice
		if err := lockForUpdate(tx).Where("id = ?", invoiceId).First(&inv).Error; err != nil {
			return err
		}
		if inv.UserId != userId {
			return ErrInvoiceNotOwner
		}
		if inv.Status == InvoiceStatusCancelled {
			return nil // idempotent repeat
		}
		if inv.Status != InvoiceStatusPending {
			return ErrInvoiceOnlyPendingCancel
		}
		inv.Status = InvoiceStatusCancelled
		inv.UpdateTime = common.GetTimestamp()
		if err := tx.Save(&inv).Error; err != nil {
			return err
		}
		if err := releaseInvoiceClaims(tx, invoiceId); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}
