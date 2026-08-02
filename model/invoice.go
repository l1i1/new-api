package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
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

// Invoice is an invoice application created by a user. Material fields are PII
// and must only be exposed to the owner and administrators (detail endpoints,
// never list endpoints).
type Invoice struct {
	Id          int     `json:"id"`
	UserId      int     `json:"user_id" gorm:"index"`
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

// InvoiceItem snapshots one paid order attached to an invoice application. The
// unique (order_type, order_id) index guarantees a paid order can never be
// attached to two applications at the same time.
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

// activeInvoiceStatuses are the statuses that keep their attached orders locked
// from further invoice applications.
var activeInvoiceStatuses = []string{
	InvoiceStatusPending,
	InvoiceStatusApproved,
	InvoiceStatusIssuing,
	InvoiceStatusIssued,
}

// GetInvoiceableTopUps returns the user's paid orders that are still available
// for an invoice application: successfully paid, not a manual balance credit,
// and not already attached to an active invoice application.
func GetInvoiceableTopUps(userId int) ([]*TopUp, error) {
	var topups []*TopUp
	err := DB.
		Where("user_id = ? AND status = ? AND payment_provider <> ?", userId, common.TopUpStatusSuccess, PaymentProviderBalance).
		Where("id NOT IN (?)", DB.Model(&InvoiceItem{}).
			Select("order_id").
			Where("order_type = ?", InvoiceOrderTypeTopUp).
			Where("invoice_id IN (?)", DB.Model(&Invoice{}).Select("id").Where("status IN ?", activeInvoiceStatuses))).
		Order("id desc").
		Find(&topups).Error
	if err != nil {
		return nil, err
	}
	return topups, nil
}

// CreateInvoiceApplication atomically validates the selected orders and creates
// an invoice application with its item snapshots. The orders are row-locked so
// concurrent applications cannot double-attach the same paid order.
func CreateInvoiceApplication(userId int, inv *Invoice, itemOrders []*TopUp) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if len(itemOrders) == 0 {
			return errors.New("no orders selected")
		}

		orderIds := make([]int, 0, len(itemOrders))
		for _, o := range itemOrders {
			orderIds = append(orderIds, o.Id)
		}

		var locked []TopUp
		if err := lockForUpdate(tx).Where("id IN ?", orderIds).Find(&locked).Error; err != nil {
			return err
		}
		if len(locked) != len(orderIds) {
			return errors.New("some orders do not exist")
		}

		currency := ""
		total := 0.0
		for _, o := range locked {
			if o.UserId != userId {
				return errors.New("order does not belong to the user")
			}
			if o.Status != common.TopUpStatusSuccess {
				return errors.New("order is not paid")
			}
			if o.PaymentProvider == PaymentProviderBalance {
				return errors.New("balance credit orders cannot be invoiced")
			}
			attached, err := isOrderAttachedToActiveInvoice(tx, InvoiceOrderTypeTopUp, o.Id)
			if err != nil {
				return err
			}
			if attached {
				return errors.New("order is already attached to an invoice application")
			}
			orderCurrency := o.PaymentCurrency
			if orderCurrency == "" {
				orderCurrency = "CNY"
			}
			if currency == "" {
				currency = orderCurrency
			} else if currency != orderCurrency {
				return errors.New("all selected orders must use the same currency")
			}
			total += o.Money
		}

		inv.UserId = userId
		inv.TotalAmount = total
		inv.Currency = currency
		inv.Status = InvoiceStatusPending
		inv.CreateTime = common.GetTimestamp()
		inv.UpdateTime = inv.CreateTime
		if err := tx.Create(inv).Error; err != nil {
			return err
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

func isOrderAttachedToActiveInvoice(tx *gorm.DB, orderType string, orderId int) (bool, error) {
	var count int64
	err := tx.Model(&InvoiceItem{}).
		Where("order_type = ? AND order_id = ?", orderType, orderId).
		Where("invoice_id IN (?)", tx.Model(&Invoice{}).Select("id").Where("status IN ?", activeInvoiceStatuses)).
		Count(&count).Error
	return count > 0, err
}

func GetInvoiceById(id int) *Invoice {
	var inv Invoice
	err := DB.Where("id = ?", id).First(&inv).Error
	if err != nil {
		return nil
	}
	return &inv
}

func GetInvoiceItems(invoiceId int) ([]*InvoiceItem, error) {
	var items []*InvoiceItem
	err := DB.Where("invoice_id = ?", invoiceId).Order("id asc").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func GetUserInvoices(userId int, pageInfo *common.PageInfo) ([]*Invoice, int64, error) {
	var invoices []*Invoice
	var total int64
	if err := DB.Model(&Invoice{}).Where("user_id = ?", userId).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := DB.Where("user_id = ?", userId).
		Order("id desc").
		Limit(pageInfo.GetPageSize()).
		Offset(pageInfo.GetStartIdx()).
		Find(&invoices).Error
	if err != nil {
		return nil, 0, err
	}
	return invoices, total, nil
}

func GetAllInvoices(pageInfo *common.PageInfo, keyword string, status string) ([]*Invoice, int64, error) {
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
	var invoices []*Invoice
	err := query.Order("id desc").
		Limit(pageInfo.GetPageSize()).
		Offset(pageInfo.GetStartIdx()).
		Find(&invoices).Error
	if err != nil {
		return nil, 0, err
	}
	return invoices, total, nil
}

// transitionInvoice atomically moves an invoice from fromStatus to toStatus. It
// row-locks the invoice so concurrent admin actions cannot race, and returns
// an error when the current status does not match fromStatus.
func transitionInvoice(invoiceId int, fromStatus string, toStatus string, adminNote string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var inv Invoice
		if err := lockForUpdate(tx).Where("id = ?", invoiceId).First(&inv).Error; err != nil {
			return err
		}
		if inv.Status != fromStatus {
			return fmt.Errorf("invoice is not in %s status", fromStatus)
		}
		inv.Status = toStatus
		inv.AdminNote = adminNote
		inv.UpdateTime = common.GetTimestamp()
		return tx.Save(&inv).Error
	})
}

func ApproveInvoice(invoiceId int, note string) error {
	return transitionInvoice(invoiceId, InvoiceStatusPending, InvoiceStatusApproved, note)
}

func StartIssueInvoice(invoiceId int, note string) error {
	return transitionInvoice(invoiceId, InvoiceStatusApproved, InvoiceStatusIssuing, note)
}

func CompleteIssueInvoice(invoiceId int, note string) error {
	return transitionInvoice(invoiceId, InvoiceStatusIssuing, InvoiceStatusIssued, note)
}

func RejectInvoice(invoiceId int, note string) error {
	return transitionInvoice(invoiceId, InvoiceStatusPending, InvoiceStatusRejected, note)
}

// CancelInvoice lets the owner cancel their own pending application and frees
// the attached orders.
func CancelInvoice(invoiceId int, userId int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var inv Invoice
		if err := lockForUpdate(tx).Where("id = ?", invoiceId).First(&inv).Error; err != nil {
			return err
		}
		if inv.UserId != userId {
			return errors.New("not the invoice owner")
		}
		if inv.Status != InvoiceStatusPending {
			return errors.New("only pending invoice applications can be cancelled")
		}
		inv.Status = InvoiceStatusCancelled
		inv.UpdateTime = common.GetTimestamp()
		return tx.Save(&inv).Error
	})
}
