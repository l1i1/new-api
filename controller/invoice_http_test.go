/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertInvoiceControllerFixture creates a user, a paid order, and an invoice
// application for that order, returning the invoice.
func insertInvoiceControllerFixture(t *testing.T, userId int, username string, email string) *model.Invoice {
	t.Helper()
	user := &model.User{Id: userId, Username: username, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Email: email}
	require.NoError(t, model.DB.Create(user).Error)
	topUp := &model.TopUp{
		Id: userId * 100, UserId: userId, Money: 50, TradeNo: fmt.Sprintf("inv-http-%d", userId),
		PaymentMethod: "epay", PaymentProvider: model.PaymentProviderEpay, PaymentCurrency: "CNY", Status: common.TopUpStatusSuccess,
	}
	require.NoError(t, model.DB.Create(topUp).Error)
	inv := &model.Invoice{
		UserId: userId, InvoiceType: model.InvoiceTypeCompany, Title: "Acme", TaxId: "TAX-" + fmt.Sprint(userId),
		Phone: "13800000000", Address: "Shanghai", BankName: "Test Bank", BankAccount: "6222" + fmt.Sprint(userId),
		Email: "billing@" + username + ".example", Reason: "r", Remark: "user remark", Status: model.InvoiceStatusPending,
		Currency: "CNY", TotalAmount: 50,
	}
	require.NoError(t, model.CreateInvoiceApplication(userId, inv, []*model.TopUp{{Id: topUp.Id}}, decimal.Zero))
	return inv
}

func jsonResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	return payload
}

func TestGetUserInvoicesListIsPiiFree(t *testing.T) {
	setupInvoiceControllerTest(t)
	insertInvoiceControllerFixture(t, 801, "user-a", "a@example.com")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", 801)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/invoice?p=1&page_size=10", nil)
	GetUserInvoices(c)

	body := recorder.Body.String()
	assert.NotContains(t, body, "TAX-801", "list must not echo tax id")
	assert.NotContains(t, body, "6222801", "list must not echo bank account")
	assert.NotContains(t, body, "13800000000", "list must not echo phone")
	assert.NotContains(t, body, "billing@user-a.example", "list must not echo delivery email")
	assert.NotContains(t, body, "user remark", "list must not echo remark")
	assert.NotContains(t, body, "Shanghai", "list must not echo address")
	assert.NotContains(t, body, `"title"`, "list must not include invoice material")
	assert.NotContains(t, body, `"tax_id"`, "list must not include invoice material")
}

func TestGetAllInvoicesAdminListIsPiiFree(t *testing.T) {
	setupInvoiceControllerTest(t)
	insertInvoiceControllerFixture(t, 802, "user-b", "b@example.com")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", 999)
	c.Set("role", common.RoleAdminUser)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/invoice/admin?p=1&page_size=10", nil)
	GetAllInvoices(c)

	body := recorder.Body.String()
	assert.NotContains(t, body, "TAX-802", "admin list must not echo tax id")
	assert.NotContains(t, body, "6222802", "admin list must not echo bank account")
	assert.NotContains(t, body, "13800000000", "admin list must not echo phone")
	assert.NotContains(t, body, "b@example.com", "admin list must not echo delivery email")
	assert.NotContains(t, body, "user remark", "admin list must not echo remark")
	assert.NotContains(t, body, "Shanghai", "admin list must not echo address")
	assert.NotContains(t, body, `"tax_id"`, "admin list must not include invoice material")
}

func TestGetInvoiceDetailEnforcesOwnership(t *testing.T) {
	setupInvoiceControllerTest(t)
	inv := insertInvoiceControllerFixture(t, 803, "owner", "owner@example.com")

	// Owner can read the detail with full material.
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", 803)
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(inv.Id)}}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/invoice/"+fmt.Sprint(inv.Id), nil)
	GetInvoiceDetail(c)
	payload := jsonResponse(t, recorder)
	assert.Equal(t, true, payload["success"])

	// Another user is rejected.
	recorder = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	c.Set("id", 804)
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(inv.Id)}}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/invoice/"+fmt.Sprint(inv.Id), nil)
	GetInvoiceDetail(c)
	payload = jsonResponse(t, recorder)
	assert.Equal(t, false, payload["success"])
}

func TestGetInvoiceDetailAdminReturnsMaterial(t *testing.T) {
	setupInvoiceControllerTest(t)
	inv := insertInvoiceControllerFixture(t, 805, "user-c", "c@example.com")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", 999)
	c.Set("role", common.RoleAdminUser)
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(inv.Id)}}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/invoice/admin/"+fmt.Sprint(inv.Id), nil)
	GetInvoiceDetailAdmin(c)

	body := recorder.Body.String()
	assert.Contains(t, body, "TAX-805", "admin detail must include tax id")
	assert.Contains(t, body, "6222805", "admin detail must include bank account")
	assert.Contains(t, body, "billing@user-c.example", "admin detail must include delivery email")
}

func TestCreateInvoiceRequiresBoundAccountEmail(t *testing.T) {
	setupInvoiceControllerTest(t)
	// User without an account email must be rejected.
	user := &model.User{Id: 806, Username: "no-email", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Email: ""}
	require.NoError(t, model.DB.Create(user).Error)
	topUp := &model.TopUp{
		Id: 80600, UserId: 806, Money: 50, TradeNo: "inv-no-email", PaymentMethod: "epay",
		PaymentProvider: model.PaymentProviderEpay, PaymentCurrency: "CNY", Status: common.TopUpStatusSuccess,
	}
	require.NoError(t, model.DB.Create(topUp).Error)

	body := fmt.Sprintf(`{"orders":[{"order_type":"topup","order_id":%d}],"invoice_type":"company","title":"Acme","tax_id":"T","email":"deliver@example.com","reason":"r","remark":""}`, topUp.Id)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", 806)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/invoice", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	CreateInvoice(c)

	payload := jsonResponse(t, recorder)
	assert.Equal(t, false, payload["success"])
	msg, _ := payload["message"].(string)
	assert.NotEmpty(t, msg)
	assert.NotEqual(t, i18n.MsgInvoiceEmailBindRequired, msg, "message must be translated, not the raw key")
}

func TestCreateInvoiceTranslatesBusinessErrors(t *testing.T) {
	setupInvoiceControllerTest(t)
	insertInvoiceControllerFixture(t, 807, "user-d", "d@example.com")

	// Mixed currency is rejected with a translated message.
	topUpUSD := &model.TopUp{
		Id: 80701, UserId: 807, Money: 10, TradeNo: "inv-mixed-usd", PaymentMethod: "stripe",
		PaymentProvider: model.PaymentProviderStripe, PaymentCurrency: "USD", Status: common.TopUpStatusSuccess,
	}
	require.NoError(t, model.DB.Create(topUpUSD).Error)

	body := fmt.Sprintf(`{"orders":[{"order_type":"topup","order_id":%d},{"order_type":"topup","order_id":%d}],"invoice_type":"company","title":"Acme","tax_id":"T","email":"deliver@example.com","reason":"r","remark":""}`, 80700, 80701)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", 807)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/invoice", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	CreateInvoice(c)

	payload := jsonResponse(t, recorder)
	assert.Equal(t, false, payload["success"])
	msg, _ := payload["message"].(string)
	assert.NotEmpty(t, msg)
	assert.NotEqual(t, i18n.MsgInvoiceMixedCurrency, msg, "message must be translated, not the raw key")
}

func TestCancelInvoiceOnlyByOwner(t *testing.T) {
	setupInvoiceControllerTest(t)
	inv := insertInvoiceControllerFixture(t, 808, "owner2", "owner2@example.com")

	// Non-owner cannot cancel.
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", 999)
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(inv.Id)}}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/invoice/"+fmt.Sprint(inv.Id)+"/cancel", nil)
	CancelInvoice(c)
	payload := jsonResponse(t, recorder)
	assert.Equal(t, false, payload["success"])
	assert.Equal(t, model.InvoiceStatusPending, model.GetInvoiceById(inv.Id).Status)

	// Owner cancels successfully and the claim is released.
	recorder = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	c.Set("id", 808)
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(inv.Id)}}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/invoice/"+fmt.Sprint(inv.Id)+"/cancel", nil)
	CancelInvoice(c)
	payload = jsonResponse(t, recorder)
	assert.Equal(t, true, payload["success"])
	assert.Equal(t, model.InvoiceStatusCancelled, model.GetInvoiceById(inv.Id).Status)

	var claims int64
	require.NoError(t, model.DB.Model(&model.InvoiceOrderClaim{}).Where("invoice_id = ?", inv.Id).Count(&claims).Error)
	assert.Zero(t, claims)
}

func TestConcurrentCreateInvoiceOnlyOneSucceeds(t *testing.T) {
	setupInvoiceControllerTest(t)
	user := &model.User{Id: 809, Username: "conc", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Email: "conc@example.com"}
	require.NoError(t, model.DB.Create(user).Error)
	topUp := &model.TopUp{
		Id: 80900, UserId: 809, Money: 50, TradeNo: "inv-conc-http", PaymentMethod: "epay",
		PaymentProvider: model.PaymentProviderEpay, PaymentCurrency: "CNY", Status: common.TopUpStatusSuccess,
	}
	require.NoError(t, model.DB.Create(topUp).Error)

	body := fmt.Sprintf(`{"orders":[{"order_type":"topup","order_id":%d}],"invoice_type":"company","title":"Acme","tax_id":"T","email":"deliver@example.com","reason":"r","remark":""}`, topUp.Id)
	const attempts = 8
	var wg sync.WaitGroup
	results := make(chan bool, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Set("id", 809)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/invoice", bytes.NewReader([]byte(body)))
			c.Request.Header.Set("Content-Type", "application/json")
			CreateInvoice(c)
			payload := jsonResponse(t, recorder)
			success, _ := payload["success"].(bool)
			results <- success
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result {
			successes++
		}
	}
	assert.Equal(t, 1, successes)

	var total int64
	require.NoError(t, model.DB.Model(&model.Invoice{}).Count(&total).Error)
	assert.EqualValues(t, 1, total)
}

// TestInvoiceI18nNoCrossLanguage asserts the three backend locales do not leak
// English fallbacks or cross-language text for every invoice message key.
func TestInvoiceI18nNoCrossLanguage(t *testing.T) {
	require.NoError(t, i18n.Init())
	keys := []string{
		i18n.MsgInvoiceDisabled,
		i18n.MsgInvoiceSelectOrders,
		i18n.MsgInvoiceTypeRequired,
		i18n.MsgInvoiceTitleTaxRequired,
		i18n.MsgInvoiceReasonRequired,
		i18n.MsgInvoiceEmailRequired,
		i18n.MsgInvoiceEmailInvalid,
		i18n.MsgInvoiceEmailBindRequired,
		i18n.MsgInvoiceUnsupportedOrderType,
		i18n.MsgInvoiceOrderNotFound,
		i18n.MsgInvoiceOrderNotPaid,
		i18n.MsgInvoiceOrderBalance,
		i18n.MsgInvoiceOrderMissingProvider,
		i18n.MsgInvoiceOrderMissingCurrency,
		i18n.MsgInvoiceOrderInvalidAmount,
		i18n.MsgInvoiceOrderClaimed,
		i18n.MsgInvoiceMixedCurrency,
		i18n.MsgInvoiceBelowMinimum,
		i18n.MsgInvoiceNotFound,
		i18n.MsgInvoiceNoPermission,
		i18n.MsgInvoiceRejectReasonRequired,
		i18n.MsgInvoiceOnlyPendingCancel,
		i18n.MsgInvoicePdfInvalid,
		i18n.MsgInvoicePdfTooLarge,
		i18n.MsgInvoicePdfRequired,
		i18n.MsgInvoiceNotIssuing,
		i18n.MsgInvoiceEmailDeliveryFailed,
		i18n.MsgInvoiceEmailStatusSubject,
		i18n.MsgInvoiceEmailStatusBody,
		i18n.MsgInvoiceStatusPending,
		i18n.MsgInvoiceStatusApproved,
		i18n.MsgInvoiceStatusIssuing,
		i18n.MsgInvoiceStatusIssued,
		i18n.MsgInvoiceStatusRejected,
		i18n.MsgInvoiceStatusCancelled,
	}
	for _, key := range keys {
		en := i18n.Translate(i18n.LangEn, key)
		zh := i18n.Translate(i18n.LangZhCN, key)
		tw := i18n.Translate(i18n.LangZhTW, key)
		assert.NotEqual(t, key, en, "en translation missing for %s", key)
		assert.NotEmpty(t, en, "en translation empty for %s", key)
		// Chinese translations must not equal the English value (fallback leak).
		assert.NotEqual(t, en, zh, "zh-CN falls back to English for %s", key)
		assert.NotEqual(t, en, tw, "zh-TW falls back to English for %s", key)
		// Chinese translations must be real Chinese, never another language:
		// contain CJK and no French accent characters.
		assert.True(t, containsCJK(zh), "zh-CN has no CJK for %s: %q", key, zh)
		assert.True(t, containsCJK(tw), "zh-TW has no CJK for %s: %q", key, tw)
		assert.False(t, containsAccent(zh), "zh-CN contains French/accented text for %s: %q", key, zh)
		assert.False(t, containsAccent(tw), "zh-TW contains French/accented text for %s: %q", key, tw)
	}
}

func containsCJK(value string) bool {
	for _, r := range value {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// containsAccent detects Latin-script letters with diacritics typical of French
// (é, à, è, ç, ô, û, etc.) that would indicate a wrong-language string.
func containsAccent(value string) bool {
	for _, r := range value {
		if (r >= 'À' && r <= 'ÿ') && r != '×' && r != '÷' {
			return true
		}
	}
	return false
}

func TestInvoiceNoticeOptionsServeEnabledAndMinAmount(t *testing.T) {
	setupInvoiceControllerTest(t)
	user := &model.User{Id: 810, Username: "opts", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Email: "opts@example.com"}
	require.NoError(t, model.DB.Create(user).Error)
	topUp := &model.TopUp{
		Id: 81000, UserId: 810, Money: 50, TradeNo: "inv-opts", PaymentMethod: "epay",
		PaymentProvider: model.PaymentProviderEpay, PaymentCurrency: "CNY", Status: common.TopUpStatusSuccess,
	}
	require.NoError(t, model.DB.Create(topUp).Error)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", 810)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/invoice/options", nil)
	GetInvoiceOptions(c)

	body := recorder.Body.String()
	assert.Contains(t, body, `"enabled":true`)
	assert.Contains(t, body, `"min_amount":0`)
	assert.Contains(t, body, `"order_id":81000`)
	// The trade number is displayed to the owner in the order selection table.
	assert.Contains(t, body, `"trade_no":"inv-opts"`)
}

func TestInvoiceProfileValidationAndAtomicUpsert(t *testing.T) {
	setupInvoiceControllerTest(t)
	user := &model.User{Id: 811, Username: "profile", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Email: "p@example.com"}
	require.NoError(t, model.DB.Create(user).Error)

	body := `{"invoice_type":"company","title":"Acme","tax_id":"TAX","phone":"","address":"","bank_name":"","bank_account":"","email":"b@example.com"}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", 811)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/invoice/profile", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	SaveInvoiceProfile(c)
	payload := jsonResponse(t, recorder)
	assert.Equal(t, true, payload["success"])

	// Invalid email is rejected.
	recorder = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	c.Set("id", 811)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/invoice/profile", bytes.NewReader([]byte(`{"invoice_type":"company","title":"Acme","tax_id":"TAX","email":"not-an-email"}`)))
	c.Request.Header.Set("Content-Type", "application/json")
	SaveInvoiceProfile(c)
	payload = jsonResponse(t, recorder)
	assert.Equal(t, false, payload["success"])

	// Only one profile row exists after repeated saves.
	var count int64
	require.NoError(t, model.DB.Model(&model.InvoiceProfile{}).Where("user_id = ?", 811).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestInvoiceProfileRejectsOversizedFields(t *testing.T) {
	setupInvoiceControllerTest(t)
	user := &model.User{Id: 812, Username: "oversize", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Email: "o@example.com"}
	require.NoError(t, model.DB.Create(user).Error)

	body := fmt.Sprintf(`{"invoice_type":"company","title":"%s","tax_id":"TAX","email":"b@example.com"}`, strings.Repeat("x", 300))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", 812)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/invoice/profile", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	SaveInvoiceProfile(c)
	payload := jsonResponse(t, recorder)
	assert.Equal(t, false, payload["success"])
}

func TestCompleteIssueInvoiceRejectsOversizedBody(t *testing.T) {
	setupInvoiceControllerTest(t)
	inv := insertInvoiceControllerFixture(t, 821, "oversize", "ov@example.com")
	_, err := model.ApproveInvoice(inv.Id, "")
	require.NoError(t, err)
	_, err = model.StartIssueInvoice(inv.Id, "")
	require.NoError(t, err)

	// A valid multipart form whose total size exceeds the request cap must be
	// rejected by http.MaxBytesReader before any PDF can be delivered.
	big := append([]byte("%PDF-1.4"), bytes.Repeat([]byte("x"), maxInvoiceRequestSize)...)
	body, contentType := buildCompleteIssueMultipart(t, big, "")
	c, recorder := invoiceControllerContext(t, 821, http.MethodPost, fmt.Sprintf("/api/invoice/%d/complete-issue", inv.Id), body, contentType)
	CompleteIssueInvoice(c)

	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &resp))
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "PDF")
	assert.Equal(t, model.InvoiceStatusIssuing, model.GetInvoiceById(inv.Id).Status)
}

func TestIdempotentRepeatDoesNotDuplicateAudit(t *testing.T) {
	setupInvoiceControllerTest(t)
	inv := insertInvoiceControllerFixture(t, 822, "idem-audit", "ia@example.com")

	adminCtx := func() *gin.Context {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set("id", 999)
		c.Set("username", "admin")
		c.Set("role", common.RoleAdminUser)
		c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(inv.Id)}}
		c.Request = httptest.NewRequest(http.MethodPost, "/api/invoice/admin/"+fmt.Sprint(inv.Id)+"/approve", strings.NewReader(`{"note":"first"}`))
		c.Request.Header.Set("Content-Type", "application/json")
		return c
	}

	// First approve: transitions and records one audit.
	c := adminCtx()
	ApproveInvoice(c)
	require.Equal(t, model.InvoiceStatusApproved, model.GetInvoiceById(inv.Id).Status)

	// Idempotent repeat: succeeds without a second audit entry.
	c = adminCtx()
	ApproveInvoice(c)
	require.Equal(t, model.InvoiceStatusApproved, model.GetInvoiceById(inv.Id).Status)

	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", model.LogTypeManage).Find(&logs).Error)
	approveCount := 0
	for _, log := range logs {
		var other struct {
			Op struct {
				Action string `json:"action"`
			} `json:"op"`
		}
		if err := common.UnmarshalJsonStr(log.Other, &other); err == nil && other.Op.Action == "invoice.approve" {
			approveCount++
		}
	}
	assert.Equal(t, 1, approveCount, "idempotent repeat must not duplicate the audit entry")
}

func TestIdempotentCancelDoesNotDuplicateAudit(t *testing.T) {
	setupInvoiceControllerTest(t)
	inv := insertInvoiceControllerFixture(t, 823, "idem-cancel", "ic@example.com")

	cancelCtx := func() *gin.Context {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set("id", 823)
		c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(inv.Id)}}
		c.Request = httptest.NewRequest(http.MethodPost, "/api/invoice/"+fmt.Sprint(inv.Id)+"/cancel", nil)
		return c
	}

	CancelInvoice(cancelCtx())
	require.Equal(t, model.InvoiceStatusCancelled, model.GetInvoiceById(inv.Id).Status)

	// A repeat cancel is idempotent and records no second audit entry.
	CancelInvoice(cancelCtx())

	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", model.LogTypeManage).Find(&logs).Error)
	cancelCount := 0
	for _, log := range logs {
		var other struct {
			Op struct {
				Action string `json:"action"`
			} `json:"op"`
		}
		if err := common.UnmarshalJsonStr(log.Other, &other); err == nil && other.Op.Action == "invoice.cancel" {
			cancelCount++
		}
	}
	assert.Equal(t, 1, cancelCount, "idempotent cancel must not duplicate the audit entry")
}
