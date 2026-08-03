package controller

import (
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/shopspring/decimal"

	"github.com/gin-gonic/gin"
)

// Invoice feature option keys. Values are stored in the options table and
// served to users only through GetInvoiceOptions.
const (
	optionInvoiceEnabled   = "InvoiceEnabled"
	optionInvoiceNotice    = "InvoiceNotice"
	optionInvoiceMinAmount = "InvoiceMinAmount"
)

const (
	maxInvoicePDFSize     = 10 << 20 // 10 MiB file
	maxInvoiceRequestSize = 12 << 20 // 12 MiB total multipart body (http.MaxBytesReader)
	maxInvoiceNoteBytes   = 512      // admin note column width
	maxInvoiceRemarkBytes = 512      // user remark column width
)

// emailRegexp is used only to scrub note summaries for audit logs. Delivery
// emails are validated with net/mail at the API boundary.
var emailRegexp = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

var (
	errInvoicePDFRequired       = errors.New("invoice pdf required")
	errInvoicePDFInvalid        = errors.New("invoice pdf invalid")
	errInvoicePDFDuplicate      = errors.New("multiple invoice pdf files")
	errInvoiceNoteTooLong       = errors.New("invoice note too long")
	errInvoiceEmailDeliveryFail = errors.New("invoice email delivery failed")
)

type InvoiceOrderRef struct {
	OrderType string `json:"order_type"`
	OrderId   int    `json:"order_id"`
}

type InvoiceCreateRequest struct {
	Orders      []InvoiceOrderRef `json:"orders"`
	InvoiceType string            `json:"invoice_type"`
	Title       string            `json:"title"`
	TaxId       string            `json:"tax_id"`
	Phone       string            `json:"phone"`
	Address     string            `json:"address"`
	BankName    string            `json:"bank_name"`
	BankAccount string            `json:"bank_account"`
	Email       string            `json:"email"`
	Reason      string            `json:"reason"`
	Remark      string            `json:"remark"`
}

type InvoiceProfileRequest struct {
	InvoiceType string `json:"invoice_type"`
	Title       string `json:"title"`
	TaxId       string `json:"tax_id"`
	Phone       string `json:"phone"`
	Address     string `json:"address"`
	BankName    string `json:"bank_name"`
	BankAccount string `json:"bank_account"`
	Email       string `json:"email"`
}

type InvoiceNoteRequest struct {
	Note string `json:"note"`
}

type InvoiceDetail struct {
	*model.Invoice
	Items []*model.InvoiceItem `json:"items"`
}

type InvoiceOptionOrder struct {
	OrderType     string  `json:"order_type"`
	OrderId       int     `json:"order_id"`
	TradeNo       string  `json:"trade_no"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency"`
	PaymentMethod string  `json:"payment_method"`
	CreateTime    int64   `json:"create_time"`
}

type InvoiceOptionsResponse struct {
	Enabled   bool                 `json:"enabled"`
	Notice    string               `json:"notice"`
	MinAmount float64              `json:"min_amount"`
	Orders    []InvoiceOptionOrder `json:"orders"`
}

// optionValue returns the stored option value under the OptionMap read lock so
// concurrent option updates cannot race with these reads.
func optionValue(key string) string {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return common.OptionMap[key]
}

func invoiceEnabled() bool {
	return optionValue(optionInvoiceEnabled) == "true"
}

func invoiceNotice() string {
	return optionValue(optionInvoiceNotice)
}

// invoiceMinAmount returns the configured minimum as a decimal. Invalid values
// (NaN, +Inf, -Inf, negative) fail closed to zero, disabling the threshold.
func invoiceMinAmount() decimal.Decimal {
	raw := optionValue(optionInvoiceMinAmount)
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || !model.ValidInvoiceMinAmount(value) {
		return decimal.Zero
	}
	return model.InvoiceMinAmountFromFloat(value)
}

// mapInvoiceError converts a model-layer invoice business error into a
// localized API message.
func mapInvoiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrInvoiceInvalid),
		errors.Is(err, model.ErrInvoiceTypeInvalid):
		common.ApiErrorI18n(c, i18n.MsgInvoiceTypeRequired)
	case errors.Is(err, model.ErrInvoiceReasonRequired):
		common.ApiErrorI18n(c, i18n.MsgInvoiceReasonRequired)
	case errors.Is(err, model.ErrInvoiceNoOrders):
		common.ApiErrorI18n(c, i18n.MsgInvoiceSelectOrders)
	case errors.Is(err, model.ErrInvoiceOrderNotFound),
		errors.Is(err, model.ErrInvoiceOrderNotOwned):
		common.ApiErrorI18n(c, i18n.MsgInvoiceOrderNotFound)
	case errors.Is(err, model.ErrInvoiceOrderNotPaid):
		common.ApiErrorI18n(c, i18n.MsgInvoiceOrderNotPaid)
	case errors.Is(err, model.ErrInvoiceBalanceOrder):
		common.ApiErrorI18n(c, i18n.MsgInvoiceOrderBalance)
	case errors.Is(err, model.ErrInvoiceMissingProvider):
		common.ApiErrorI18n(c, i18n.MsgInvoiceOrderMissingProvider)
	case errors.Is(err, model.ErrInvoiceMissingCurrency):
		common.ApiErrorI18n(c, i18n.MsgInvoiceOrderMissingCurrency)
	case errors.Is(err, model.ErrInvoiceInvalidAmount):
		common.ApiErrorI18n(c, i18n.MsgInvoiceOrderInvalidAmount)
	case errors.Is(err, model.ErrInvoiceOrderClaimed):
		common.ApiErrorI18n(c, i18n.MsgInvoiceOrderClaimed)
	case errors.Is(err, model.ErrInvoiceMixedCurrency):
		common.ApiErrorI18n(c, i18n.MsgInvoiceMixedCurrency)
	case errors.Is(err, model.ErrInvoiceBelowMinimum):
		common.ApiErrorI18n(c, i18n.MsgInvoiceBelowMinimum)
	case errors.Is(err, model.ErrInvoiceNotFound):
		common.ApiErrorI18n(c, i18n.MsgInvoiceNotFound)
	case errors.Is(err, model.ErrInvoiceNotOwner):
		common.ApiErrorI18n(c, i18n.MsgInvoiceNoPermission)
	case errors.Is(err, model.ErrInvoiceOnlyPendingCancel):
		common.ApiErrorI18n(c, i18n.MsgInvoiceOnlyPendingCancel)
	case errors.Is(err, model.ErrInvoiceInvalidTransition):
		common.ApiErrorI18n(c, i18n.MsgInvoiceInvalidTransition)
	case errors.Is(err, model.ErrInvoiceNotIssuing):
		common.ApiErrorI18n(c, i18n.MsgInvoiceNotIssuing)
	default:
		common.ApiError(c, err)
	}
}

// GetInvoiceOptions returns the invoice feature switch, the admin-editable
// notice, the minimum amount, and the user's currently invoiceable orders.
func GetInvoiceOptions(c *gin.Context) {
	userId := c.GetInt("id")
	topups, err := model.GetInvoiceableTopUps(userId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	orders := make([]InvoiceOptionOrder, 0, len(topups))
	for _, t := range topups {
		orders = append(orders, InvoiceOptionOrder{
			OrderType:     model.InvoiceOrderTypeTopUp,
			OrderId:       t.Id,
			TradeNo:       t.TradeNo,
			Amount:        t.Money,
			Currency:      t.PaymentCurrency,
			PaymentMethod: t.PaymentMethod,
			CreateTime:    t.CreateTime,
		})
	}
	common.ApiSuccess(c, InvoiceOptionsResponse{
		Enabled:   invoiceEnabled(),
		Notice:    invoiceNotice(),
		MinAmount: invoiceMinAmount().InexactFloat64(),
		Orders:    orders,
	})
}

// GetInvoiceProfile returns the authenticated user's reusable billing
// information. The model layer also backfills it from the latest application
// for users who used invoicing before profiles were introduced.
func GetInvoiceProfile(c *gin.Context) {
	profile, err := model.GetInvoiceProfile(c.GetInt("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, profile)
}

func validateInvoiceProfileFields(c *gin.Context, req *InvoiceProfileRequest) (*model.InvoiceProfile, bool) {
	req.InvoiceType = strings.ToLower(strings.TrimSpace(req.InvoiceType))
	req.Title = strings.TrimSpace(req.Title)
	req.TaxId = strings.TrimSpace(req.TaxId)
	req.Phone = strings.TrimSpace(req.Phone)
	req.Address = strings.TrimSpace(req.Address)
	req.BankName = strings.TrimSpace(req.BankName)
	req.BankAccount = strings.TrimSpace(req.BankAccount)
	req.Email = strings.TrimSpace(req.Email)
	if !validateInvoiceFieldLengths(req.Title, req.TaxId, req.Phone, req.Address, req.BankName, req.BankAccount, req.Email, "", "") {
		common.ApiErrorI18n(c, i18n.MsgInvalidInput)
		return nil, false
	}
	if req.InvoiceType != model.InvoiceTypeIndividual && req.InvoiceType != model.InvoiceTypeCompany {
		common.ApiErrorI18n(c, i18n.MsgInvoiceTypeRequired)
		return nil, false
	}
	if req.Title == "" || req.TaxId == "" || req.Email == "" {
		common.ApiErrorI18n(c, i18n.MsgInvoiceTitleTaxRequired)
		return nil, false
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvoiceEmailInvalid)
		return nil, false
	}
	return &model.InvoiceProfile{
		UserId:      c.GetInt("id"),
		InvoiceType: req.InvoiceType,
		Title:       req.Title,
		TaxId:       req.TaxId,
		Phone:       req.Phone,
		Address:     req.Address,
		BankName:    req.BankName,
		BankAccount: req.BankAccount,
		Email:       req.Email,
	}, true
}

// validateInvoiceFieldLengths mirrors the model column widths so oversized
// input is rejected before it reaches the database. remark is optional but
// bounded by the same varchar(512) column as the admin note and reason.
func validateInvoiceFieldLengths(title, taxId, phone, address, bankName, bankAccount, email, reason, remark string) bool {
	return len(title) <= 255 && len(taxId) <= 64 && len(phone) <= 32 && len(address) <= 255 &&
		len(bankName) <= 128 && len(bankAccount) <= 64 && len(email) <= 255 && len(reason) <= 512 && len(remark) <= 512
}

// SaveInvoiceProfile stores the authenticated user's reusable billing
// information. Invoice reasons and remarks are intentionally excluded.
func SaveInvoiceProfile(c *gin.Context) {
	var req InvoiceProfileRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	profile, ok := validateInvoiceProfileFields(c, &req)
	if !ok {
		return
	}
	if err := model.SaveInvoiceProfile(profile); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, profile)
}

// CreateInvoice validates the request and creates a pending invoice
// application. Server-side eligibility re-check happens inside the model
// transaction so concurrent applications cannot double-attach an order.
func CreateInvoice(c *gin.Context) {
	if !invoiceEnabled() {
		common.ApiErrorI18n(c, i18n.MsgInvoiceDisabled)
		return
	}
	var req InvoiceCreateRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	if len(req.Orders) == 0 {
		common.ApiErrorI18n(c, i18n.MsgInvoiceSelectOrders)
		return
	}
	req.InvoiceType = strings.ToLower(strings.TrimSpace(req.InvoiceType))
	req.Title = strings.TrimSpace(req.Title)
	req.TaxId = strings.TrimSpace(req.TaxId)
	req.Email = strings.TrimSpace(req.Email)
	req.Reason = strings.TrimSpace(req.Reason)
	req.Phone = strings.TrimSpace(req.Phone)
	req.Address = strings.TrimSpace(req.Address)
	req.BankName = strings.TrimSpace(req.BankName)
	req.BankAccount = strings.TrimSpace(req.BankAccount)
	req.Remark = strings.TrimSpace(req.Remark)
	if !validateInvoiceFieldLengths(req.Title, req.TaxId, req.Phone, req.Address, req.BankName, req.BankAccount, req.Email, req.Reason, req.Remark) {
		common.ApiErrorI18n(c, i18n.MsgInvalidInput)
		return
	}
	if req.InvoiceType != model.InvoiceTypeIndividual && req.InvoiceType != model.InvoiceTypeCompany {
		common.ApiErrorI18n(c, i18n.MsgInvoiceTypeRequired)
		return
	}
	if req.Title == "" || req.TaxId == "" {
		common.ApiErrorI18n(c, i18n.MsgInvoiceTitleTaxRequired)
		return
	}
	if req.InvoiceType == model.InvoiceTypeIndividual && req.Reason == "" {
		common.ApiErrorI18n(c, i18n.MsgInvoiceReasonRequired)
		return
	}
	if req.Email == "" {
		common.ApiErrorI18n(c, i18n.MsgInvoiceEmailRequired)
		return
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvoiceEmailInvalid)
		return
	}

	userId := c.GetInt("id")
	user, err := model.GetUserById(userId, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if strings.TrimSpace(user.Email) == "" {
		common.ApiErrorI18n(c, i18n.MsgInvoiceEmailBindRequired)
		return
	}

	var orders []*model.TopUp
	currency := ""
	total := decimal.Zero
	for _, ref := range req.Orders {
		if ref.OrderType != model.InvoiceOrderTypeTopUp {
			common.ApiErrorI18n(c, i18n.MsgInvoiceUnsupportedOrderType)
			return
		}
		topup := model.GetTopUpById(ref.OrderId)
		if topup == nil || topup.UserId != userId {
			common.ApiErrorI18n(c, i18n.MsgInvoiceOrderNotFound)
			return
		}
		if topup.Status != common.TopUpStatusSuccess {
			common.ApiErrorI18n(c, i18n.MsgInvoiceOrderNotPaid)
			return
		}
		if topup.PaymentProvider == model.PaymentProviderBalance {
			common.ApiErrorI18n(c, i18n.MsgInvoiceOrderBalance)
			return
		}
		if topup.PaymentCurrency == "" {
			common.ApiErrorI18n(c, i18n.MsgInvoiceOrderMissingCurrency)
			return
		}
		orderMoney, err := model.MoneyFromFloat(topup.Money)
		if err != nil || !orderMoney.IsPositive() {
			common.ApiErrorI18n(c, i18n.MsgInvoiceOrderInvalidAmount)
			return
		}
		orderCurrency := topup.PaymentCurrency
		if currency == "" {
			currency = orderCurrency
		} else if currency != orderCurrency {
			common.ApiErrorI18n(c, i18n.MsgInvoiceMixedCurrency)
			return
		}
		total = total.Add(orderMoney)
		orders = append(orders, topup)
	}

	// Pre-check the minimum with decimal arithmetic; the transaction re-checks
	// it against the freshly locked rows.
	minAmount := invoiceMinAmount()
	if minAmount.IsPositive() && total.LessThan(minAmount) {
		common.ApiErrorI18n(c, i18n.MsgInvoiceBelowMinimum)
		return
	}

	inv := &model.Invoice{
		InvoiceType: req.InvoiceType,
		Title:       req.Title,
		TaxId:       req.TaxId,
		Phone:       req.Phone,
		Address:     req.Address,
		BankName:    req.BankName,
		BankAccount: req.BankAccount,
		Email:       req.Email,
		Reason:      req.Reason,
		Remark:      req.Remark,
	}
	if err := model.CreateInvoiceApplication(userId, inv, orders, minAmount); err != nil {
		mapInvoiceError(c, err)
		return
	}
	common.ApiSuccess(c, inv)
}

func GetUserInvoices(c *gin.Context) {
	userId := c.GetInt("id")
	pageInfo := common.GetPageQuery(c)
	invoices, total, err := model.GetUserInvoices(userId, pageInfo)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(invoices)
	common.ApiSuccess(c, pageInfo)
}

func loadInvoiceDetail(invoiceId int) (*InvoiceDetail, error) {
	inv := model.GetInvoiceById(invoiceId)
	if inv == nil {
		return nil, model.ErrInvoiceNotFound
	}
	items, err := model.GetInvoiceItems(invoiceId)
	if err != nil {
		return nil, err
	}
	return &InvoiceDetail{Invoice: inv, Items: items}, nil
}

func GetInvoiceDetail(c *gin.Context) {
	userId := c.GetInt("id")
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	detail, err := loadInvoiceDetail(id)
	if err != nil {
		mapInvoiceError(c, err)
		return
	}
	if detail.UserId != userId {
		common.ApiErrorI18n(c, i18n.MsgInvoiceNoPermission)
		return
	}
	common.ApiSuccess(c, detail)
}

// auditNoteSummary truncates and scrubs a note for audit logging so email
// addresses cannot leak through a remark into the audit trail.
func auditNoteSummary(note string) string {
	summary := strings.TrimSpace(note)
	if summary == "" {
		return ""
	}
	if len(summary) > 100 {
		summary = summary[:100]
	}
	return emailRegexp.ReplaceAllString(summary, "[email]")
}

func CancelInvoice(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	userId := c.GetInt("id")
	before := model.GetInvoiceById(id)
	if before == nil {
		common.ApiErrorI18n(c, i18n.MsgInvoiceNotFound)
		return
	}
	changed, err := model.CancelInvoice(id, userId)
	if err != nil {
		mapInvoiceError(c, err)
		return
	}
	if !changed {
		// Idempotent repeat of an already-cancelled application: no audit, no email.
		common.ApiSuccess(c, nil)
		return
	}
	after := model.GetInvoiceById(id)
	if after != nil {
		recordUserSecurityAudit(c, userId, "invoice.cancel", map[string]interface{}{
			"invoice_id":  id,
			"from_status": model.InvoiceStatusPending,
			"to_status":   model.InvoiceStatusCancelled,
		})
		_ = sendInvoiceStatusEmailFn(after, i18n.MsgInvoiceStatusCancelled, "", nil)
	}
	common.ApiSuccess(c, nil)
}

// Admin endpoints

func GetAllInvoices(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	keyword := c.Query("keyword")
	status := c.Query("status")
	invoices, total, err := model.GetAllInvoices(pageInfo, keyword, status)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(invoices)
	common.ApiSuccess(c, pageInfo)
}

func GetInvoiceDetailAdmin(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	detail, err := loadInvoiceDetail(id)
	if err != nil {
		mapInvoiceError(c, err)
		return
	}
	common.ApiSuccess(c, detail)
}

func invoiceNoteFromRequest(c *gin.Context) string {
	var req InvoiceNoteRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		return ""
	}
	return strings.TrimSpace(req.Note)
}

func isFinalInvoiceStatus(status string) bool {
	return status == model.InvoiceStatusIssued ||
		status == model.InvoiceStatusRejected ||
		status == model.InvoiceStatusCancelled
}

// invoiceStatusLabelKey maps a status to its localized label key used in final
// status emails.
func invoiceStatusLabelKey(status string) string {
	switch status {
	case model.InvoiceStatusIssued:
		return i18n.MsgInvoiceStatusIssued
	case model.InvoiceStatusRejected:
		return i18n.MsgInvoiceStatusRejected
	case model.InvoiceStatusCancelled:
		return i18n.MsgInvoiceStatusCancelled
	default:
		return i18n.MsgInvoiceStatusPending
	}
}

// sendInvoiceStatusEmail sends a final-status email in the recipient's saved
// language. Attachments are request-local and are never read from or written to
// server storage.
func sendInvoiceStatusEmail(inv *model.Invoice, statusLabelKey string, note string, attachments []common.EmailAttachment) error {
	lang := model.GetUserLanguage(inv.UserId)
	if lang == "" {
		lang = i18n.DefaultLang
	}
	siteURL := strings.TrimRight(strings.TrimSpace(system_setting.ServerAddress), "/")
	subject := i18n.Translate(lang, i18n.MsgInvoiceEmailStatusSubject, map[string]any{
		"SystemName": common.SystemName,
		"Id":         inv.Id,
	})
	statusLabel := i18n.Translate(lang, statusLabelKey)
	body := i18n.Translate(lang, i18n.MsgInvoiceEmailStatusBody, map[string]any{
		"SystemName": html.EscapeString(common.SystemName),
		"SiteURL":    html.EscapeString(siteURL),
		"Id":         inv.Id,
		"Amount":     inv.TotalAmount,
		"Currency":   html.EscapeString(inv.Currency),
		"Status":     html.EscapeString(statusLabel),
		"Note":       strings.ReplaceAll(html.EscapeString(note), "\n", "<br>"),
	})
	var err error
	if len(attachments) > 0 {
		err = common.SendEmailWithAttachments(subject, inv.Email, body, attachments)
	} else {
		err = common.SendEmail(subject, inv.Email, body)
	}
	if err != nil {
		common.SysLog(fmt.Sprintf("invoice final status email failed invoice_id=%d error=%q", inv.Id, err.Error()))
	}
	return err
}

var sendInvoiceStatusEmailFn = sendInvoiceStatusEmail

// recordInvoiceAudit records one state transition audit. It intentionally
// carries only the invoice ID, operator, previous/next status, timestamp, and a
// scrubbed note summary — never tax IDs, bank accounts, emails, full request
// bodies, or complete invoice material.
func recordInvoiceAudit(c *gin.Context, targetUserId int, action string, invoiceId int, fromStatus string, toStatus string, note string) {
	recordManageAuditFor(c, targetUserId, action, map[string]interface{}{
		"invoice_id":  invoiceId,
		"from_status": fromStatus,
		"to_status":   toStatus,
		"note":        auditNoteSummary(note),
	})
}

// finishInvoiceTransition executes one admin state transition, records the
// audit entry, and sends a final-status email. requireNote makes the admin
// note mandatory (used by reject). The transition is idempotent for repeated
// identical actions: a repeat that does not change the state returns success
// without a second audit entry or a duplicate final-status email. Invalid
// transitions return a localized business error.
func finishInvoiceTransition(c *gin.Context, invoiceId int, transition func(note string) (bool, error), action string, fromStatus string, toStatus string, statusLabelKey string, requireNote bool) {
	note := invoiceNoteFromRequest(c)
	if requireNote && note == "" {
		common.ApiErrorI18n(c, i18n.MsgInvoiceRejectReasonRequired)
		return
	}
	if len(note) > 512 {
		common.ApiErrorI18n(c, i18n.MsgInvalidInput)
		return
	}
	changed, err := transition(note)
	if err != nil {
		mapInvoiceError(c, err)
		return
	}
	inv := model.GetInvoiceById(invoiceId)
	if inv == nil {
		common.ApiErrorI18n(c, i18n.MsgInvoiceNotFound)
		return
	}
	if !changed {
		// Idempotent repeat of an already-applied transition: no audit, no email.
		common.ApiSuccess(c, nil)
		return
	}
	recordInvoiceAudit(c, inv.UserId, action, invoiceId, fromStatus, toStatus, note)
	if isFinalInvoiceStatus(inv.Status) {
		_ = sendInvoiceStatusEmailFn(inv, statusLabelKey, note, nil)
	}
	common.ApiSuccess(c, nil)
}

func ApproveInvoice(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	finishInvoiceTransition(c, id,
		func(n string) (bool, error) { return model.ApproveInvoice(id, n) },
		"invoice.approve", model.InvoiceStatusPending, model.InvoiceStatusApproved, i18n.MsgInvoiceStatusApproved, false)
}

func RejectInvoice(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	finishInvoiceTransition(c, id,
		func(n string) (bool, error) { return model.RejectInvoice(id, n) },
		"invoice.reject", model.InvoiceStatusPending, model.InvoiceStatusRejected, i18n.MsgInvoiceStatusRejected, true)
}

func StartIssueInvoice(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	finishInvoiceTransition(c, id,
		func(n string) (bool, error) { return model.StartIssueInvoice(id, n) },
		"invoice.start_issue", model.InvoiceStatusApproved, model.InvoiceStatusIssuing, i18n.MsgInvoiceStatusIssuing, false)
}

// CompleteIssueInvoice validates an uploaded PDF and immediately sends it as
// the issued notification attachment. The bytes and filename are request-local:
// they are never persisted on the server or recorded on the invoice row.
func CompleteIssueInvoice(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxInvoiceRequestSize)
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	inv := model.GetInvoiceById(id)
	if inv == nil {
		common.ApiErrorI18n(c, i18n.MsgInvoiceNotFound)
		return
	}
	if inv.Status == model.InvoiceStatusIssued {
		common.ApiSuccess(c, nil)
		return
	}
	if inv.Status != model.InvoiceStatusIssuing {
		common.ApiErrorI18n(c, i18n.MsgInvoiceNotIssuing)
		return
	}
	note, pdfFilename, pdfBytes, err := parseCompleteIssueRequest(c)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxBytesErr), errors.Is(err, errInvoiceUploadTooLarge):
			common.ApiErrorI18n(c, i18n.MsgInvoicePdfTooLarge)
		case errors.Is(err, errInvoicePDFInvalid), errors.Is(err, errInvoicePDFDuplicate):
			common.ApiErrorI18n(c, i18n.MsgInvoicePdfInvalid)
		case errors.Is(err, errInvoicePDFRequired):
			common.ApiErrorI18n(c, i18n.MsgInvoicePdfRequired)
		case errors.Is(err, errInvoiceNoteTooLong):
			common.ApiErrorI18n(c, i18n.MsgInvalidInput)
		default:
			common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		}
		return
	}
	changed, err := model.CompleteIssueInvoice(id, note, func(issuing *model.Invoice) error {
		if err := sendInvoiceStatusEmailFn(issuing, i18n.MsgInvoiceStatusIssued, note, []common.EmailAttachment{{
			Filename:    pdfFilename,
			ContentType: "application/pdf",
			Data:        pdfBytes,
		}}); err != nil {
			return fmt.Errorf("%w: %v", errInvoiceEmailDeliveryFail, err)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, model.ErrInvoiceInvalid) || errors.Is(err, model.ErrInvoiceNotIssuing) {
			mapInvoiceError(c, err)
		} else if errors.Is(err, errInvoiceEmailDeliveryFail) {
			common.ApiErrorI18n(c, i18n.MsgInvoiceEmailDeliveryFailed)
		} else {
			common.SysError(fmt.Sprintf("invoice complete-issue failed invoice_id=%d error=%q", id, err.Error()))
			common.ApiErrorI18n(c, i18n.MsgDatabaseError)
		}
		return
	}
	if !changed {
		// Idempotent repeat of an already-issued application: no audit or email.
		common.ApiSuccess(c, nil)
		return
	}
	inv = model.GetInvoiceById(id)
	if inv == nil {
		common.ApiErrorI18n(c, i18n.MsgInvoiceNotFound)
		return
	}
	recordInvoiceAudit(c, inv.UserId, "invoice.complete_issue", id, model.InvoiceStatusIssuing, model.InvoiceStatusIssued, note)
	common.ApiSuccess(c, nil)
}

// parseCompleteIssueRequest reads the request-local PDF attachment. The caller
// sends it immediately and discards it after the SMTP call returns.
func parseCompleteIssueRequest(c *gin.Context) (string, string, []byte, error) {
	reader, err := c.Request.MultipartReader()
	if err != nil {
		return "", "", nil, err
	}
	var note, filename string
	var pdfBytes []byte
	fileFound := false
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", "", nil, err
		}
		switch part.FormName() {
		case "note":
			noteBytes, err := readUploadedFile(part, maxInvoiceNoteBytes)
			if errors.Is(err, errInvoiceUploadTooLarge) {
				return "", "", nil, errInvoiceNoteTooLong
			}
			if err != nil {
				return "", "", nil, err
			}
			note = strings.TrimSpace(string(noteBytes))
		case "file":
			if fileFound {
				return "", "", nil, errInvoicePDFDuplicate
			}
			fileFound = true
			pdfBytes, err = readUploadedFile(part, maxInvoicePDFSize)
			if err != nil {
				return "", "", nil, err
			}
			filename = sanitizePDFFilename(part.FileName())
		}
	}
	if !fileFound {
		return "", "", nil, errInvoicePDFRequired
	}
	if !looksLikePDF(pdfBytes) {
		return "", "", nil, errInvoicePDFInvalid
	}
	return note, filename, pdfBytes, nil
}
