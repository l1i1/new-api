package controller

import (
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// Invoice feature option keys. Values are stored in the options table and
// served to users only through GetInvoiceOptions.
const (
	optionInvoiceEnabled   = "InvoiceEnabled"
	optionInvoiceNotice    = "InvoiceNotice"
	optionInvoiceMinAmount = "InvoiceMinAmount"
)

type InvoiceOrderRef struct {
	OrderType string `json:"order_type"`
	OrderId   int    `json:"order_id"`
}

type InvoiceCreateRequest struct {
	Orders      []InvoiceOrderRef `json:"orders"`
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
	Enabled   bool                `json:"enabled"`
	Notice    string              `json:"notice"`
	MinAmount float64             `json:"min_amount"`
	Orders    []InvoiceOptionOrder `json:"orders"`
}

func invoiceEnabled() bool {
	return common.OptionMap["InvoiceEnabled"] == "true"
}

func invoiceNotice() string {
	return common.OptionMap["InvoiceNotice"]
}

func invoiceMinAmount() float64 {
	raw := common.OptionMap["InvoiceMinAmount"]
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
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
		currency := t.PaymentCurrency
		if currency == "" {
			currency = "CNY"
		}
		orders = append(orders, InvoiceOptionOrder{
			OrderType:     model.InvoiceOrderTypeTopUp,
			OrderId:       t.Id,
			TradeNo:       t.TradeNo,
			Amount:        t.Money,
			Currency:      currency,
			PaymentMethod: t.PaymentMethod,
			CreateTime:    t.CreateTime,
		})
	}
	common.ApiSuccess(c, InvoiceOptionsResponse{
		Enabled:   invoiceEnabled(),
		Notice:    invoiceNotice(),
		MinAmount: invoiceMinAmount(),
		Orders:    orders,
	})
}

// CreateInvoice validates the request and creates a pending invoice
// application. Server-side eligibility re-check happens inside the model
// transaction so concurrent applications cannot double-attach an order.
func CreateInvoice(c *gin.Context) {
	if !invoiceEnabled() {
		common.ApiErrorMsg(c, "发票功能未开启")
		return
	}
	var req InvoiceCreateRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "无效的参数")
		return
	}
	if len(req.Orders) == 0 {
		common.ApiErrorMsg(c, "请选择要开票的订单")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.TaxId = strings.TrimSpace(req.TaxId)
	req.Email = strings.TrimSpace(req.Email)
	req.Reason = strings.TrimSpace(req.Reason)
	req.Phone = strings.TrimSpace(req.Phone)
	req.Address = strings.TrimSpace(req.Address)
	req.BankName = strings.TrimSpace(req.BankName)
	req.BankAccount = strings.TrimSpace(req.BankAccount)
	req.Remark = strings.TrimSpace(req.Remark)
	if req.Title == "" || req.TaxId == "" || req.Reason == "" {
		common.ApiErrorMsg(c, "发票抬头、税号和开票理由为必填项")
		return
	}
	if req.Email == "" {
		common.ApiErrorMsg(c, "收票邮箱为必填项")
		return
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		common.ApiErrorMsg(c, "收票邮箱格式不正确")
		return
	}

	userId := c.GetInt("id")
	var orders []*model.TopUp
	currency := ""
	total := 0.0
	for _, ref := range req.Orders {
		if ref.OrderType != model.InvoiceOrderTypeTopUp {
			common.ApiErrorMsg(c, "不支持的订单类型")
			return
		}
		topup := model.GetTopUpById(ref.OrderId)
		if topup == nil || topup.UserId != userId {
			common.ApiErrorMsg(c, "订单不存在或不属于当前用户")
			return
		}
		if topup.Status != common.TopUpStatusSuccess {
			common.ApiErrorMsg(c, "只有已支付成功的订单可以开票")
			return
		}
		if topup.PaymentProvider == model.PaymentProviderBalance {
			common.ApiErrorMsg(c, "余额赠送订单不能开票")
			return
		}
		orderCurrency := topup.PaymentCurrency
		if orderCurrency == "" {
			orderCurrency = "CNY"
		}
		if currency == "" {
			currency = orderCurrency
		} else if currency != orderCurrency {
			common.ApiErrorMsg(c, "所选订单币种不一致，请按币种分开申请")
			return
		}
		total += topup.Money
		orders = append(orders, topup)
	}

	minAmount := invoiceMinAmount()
	if minAmount > 0 && total < minAmount {
		common.ApiErrorMsg(c, fmt.Sprintf("开票金额不能低于 %.2f %s", minAmount, currency))
		return
	}

	inv := &model.Invoice{
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
	if err := model.CreateInvoiceApplication(userId, inv, orders); err != nil {
		common.ApiError(c, err)
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
		return nil, errors.New("发票申请不存在")
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
		common.ApiErrorMsg(c, "无效的参数")
		return
	}
	detail, err := loadInvoiceDetail(id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if detail.UserId != userId {
		common.ApiErrorMsg(c, "无权查看该发票申请")
		return
	}
	common.ApiSuccess(c, detail)
}

func CancelInvoice(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "无效的参数")
		return
	}
	userId := c.GetInt("id")
	if err := model.CancelInvoice(id, userId); err != nil {
		common.ApiError(c, err)
		return
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
		common.ApiErrorMsg(c, "无效的参数")
		return
	}
	detail, err := loadInvoiceDetail(id)
	if err != nil {
		common.ApiError(c, err)
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

// finishInvoiceTransition executes one admin state transition, records the
// audit entry, and sends the delivery email. Email failures are logged only so
// the admin action always succeeds. requireNote makes the admin note mandatory
// (used by reject).
func finishInvoiceTransition(c *gin.Context, invoiceId int, transition func(note string) error, action string, statusLabel string, requireNote bool) {
	note := invoiceNoteFromRequest(c)
	if requireNote && note == "" {
		common.ApiErrorMsg(c, "驳回原因不能为空")
		return
	}
	if err := transition(note); err != nil {
		common.ApiError(c, err)
		return
	}
	inv := model.GetInvoiceById(invoiceId)
	if inv == nil {
		common.ApiErrorMsg(c, "发票申请不存在")
		return
	}
	recordManageAuditFor(c, inv.UserId, action, map[string]interface{}{
		"invoice_id": invoiceId,
	})
	if err := common.SendEmail(
		fmt.Sprintf("发票申请 #%d 状态更新", invoiceId),
		inv.Email,
		fmt.Sprintf("您的发票申请 #%d（%.2f %s）状态已更新为：%s。\n%s", invoiceId, inv.TotalAmount, inv.Currency, statusLabel, note),
	); err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("发票状态变更邮件发送失败 invoice_id=%d email=%s error=%q", invoiceId, inv.Email, err.Error()))
	}
	common.ApiSuccess(c, nil)
}

func ApproveInvoice(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "无效的参数")
		return
	}
	finishInvoiceTransition(c, id, func(n string) error { return model.ApproveInvoice(id, n) }, "invoice.approve", "审核通过", false)
}

func StartIssueInvoice(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "无效的参数")
		return
	}
	finishInvoiceTransition(c, id, func(n string) error { return model.StartIssueInvoice(id, n) }, "invoice.start_issue", "开票中", false)
}

func CompleteIssueInvoice(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "无效的参数")
		return
	}
	finishInvoiceTransition(c, id, func(n string) error { return model.CompleteIssueInvoice(id, n) }, "invoice.complete_issue", "已开具", false)
}

func RejectInvoice(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "无效的参数")
		return
	}
	finishInvoiceTransition(c, id, func(n string) error {
		return model.RejectInvoice(id, n)
	}, "invoice.reject", "已驳回", true)
}
