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
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupInvoiceControllerTest replaces the global database with an in-memory
// SQLite instance that carries the invoice tables, and initializes the option
// map and i18n bundle so controller behavior is exercised end to end.
func setupInvoiceControllerTest(t *testing.T) {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMainDBType, previousLogDBType := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedisEnabled := common.RedisEnabled
	previousEmailSender := sendInvoiceStatusEmailFn
	sendInvoiceStatusEmailFn = func(_ *model.Invoice, _ string, _ string, _ []common.EmailAttachment) error {
		return nil
	}

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.TopUp{},
		&model.Invoice{},
		&model.InvoiceProfile{},
		&model.InvoiceItem{},
		&model.InvoiceOrderClaim{},
		&model.Log{},
	))
	// A single connection keeps the in-memory database consistent across
	// goroutines and serializes concurrent transactions, mirroring the model
	// test setup.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	require.NoError(t, i18n.Init())

	common.OptionMapRWMutex.Lock()
	common.OptionMap = map[string]string{
		"InvoiceEnabled":   "true",
		"InvoiceNotice":    "",
		"InvoiceMinAmount": "0",
	}
	common.OptionMapRWMutex.Unlock()

	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainDBType, previousLogDBType)
		common.RedisEnabled = previousRedisEnabled
		sendInvoiceStatusEmailFn = previousEmailSender
	})
}

func TestInvoiceEmailPolicyOnlyNotifiesFinalStatuses(t *testing.T) {
	tests := []struct {
		status string
		final  bool
	}{
		{status: model.InvoiceStatusPending, final: false},
		{status: model.InvoiceStatusApproved, final: false},
		{status: model.InvoiceStatusIssuing, final: false},
		{status: model.InvoiceStatusIssued, final: true},
		{status: model.InvoiceStatusRejected, final: true},
		{status: model.InvoiceStatusCancelled, final: true},
	}

	for _, test := range tests {
		assert.Equal(t, test.final, isFinalInvoiceStatus(test.status), test.status)
	}
}

func TestLooksLikePDFValidatesMagicBytes(t *testing.T) {
	assert.True(t, looksLikePDF([]byte("%PDF-1.4")))
	assert.True(t, looksLikePDF([]byte("%PDF-1.7 trailer")))
	assert.False(t, looksLikePDF([]byte("not a pdf")))
	assert.False(t, looksLikePDF(nil))
	assert.False(t, looksLikePDF([]byte("PDF-1.4")))
}

func TestSanitizePDFFilename(t *testing.T) {
	assert.Equal(t, "invoice-1.pdf", sanitizePDFFilename("invoice-1.pdf"))
	assert.Equal(t, "a_b.pdf", sanitizePDFFilename("a/b.pdf"))
	// Dots are preserved but the result stays a single flat filename.
	assert.Equal(t, "...pdf", sanitizePDFFilename(".."))
	assert.Equal(t, "invoice.pdf", sanitizePDFFilename("invoice"))
	assert.Equal(t, "invoice.pdf", sanitizePDFFilename(""))
}

func TestAuditNoteSummaryScrubsEmail(t *testing.T) {
	assert.Equal(t, "please check [email] for details", auditNoteSummary("please check a@b.com for details"))
	assert.Equal(t, "", auditNoteSummary("  "))
	assert.Equal(t, "plain note", auditNoteSummary("plain note"))
	// Long notes are truncated.
	long := strings.Repeat("x", 200)
	assert.Len(t, auditNoteSummary(long), 100)
}

func TestInvoiceStatusLabelKeyMapsFinalStatuses(t *testing.T) {
	assert.Equal(t, i18n.MsgInvoiceStatusIssued, invoiceStatusLabelKey(model.InvoiceStatusIssued))
	assert.Equal(t, i18n.MsgInvoiceStatusRejected, invoiceStatusLabelKey(model.InvoiceStatusRejected))
	assert.Equal(t, i18n.MsgInvoiceStatusCancelled, invoiceStatusLabelKey(model.InvoiceStatusCancelled))
}

func TestInvoiceMinAmountFailsClosedOnInvalidValues(t *testing.T) {
	common.OptionMapRWMutex.Lock()
	common.OptionMap = map[string]string{"InvoiceMinAmount": "NaN"}
	common.OptionMapRWMutex.Unlock()
	assert.True(t, invoiceMinAmount().Equal(decimal.Zero))

	common.OptionMapRWMutex.Lock()
	common.OptionMap["InvoiceMinAmount"] = "abc"
	common.OptionMapRWMutex.Unlock()
	assert.True(t, invoiceMinAmount().Equal(decimal.Zero))

	common.OptionMapRWMutex.Lock()
	common.OptionMap["InvoiceMinAmount"] = "100.5"
	common.OptionMapRWMutex.Unlock()
	assert.True(t, invoiceMinAmount().Equal(decimal.NewFromFloat(100.5)))
}

// buildCompleteIssueMultipart creates a multipart/form-data body with the given
// PDF bytes under the "file" field and an optional note.
func buildCompleteIssueMultipart(t *testing.T, pdfData []byte, note string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if pdfData != nil {
		part, err := writer.CreateFormFile("file", "real-invoice.pdf")
		require.NoError(t, err)
		_, err = part.Write(pdfData)
		require.NoError(t, err)
	}
	if note != "" {
		require.NoError(t, writer.WriteField("note", note))
	}
	require.NoError(t, writer.Close())
	return buf.Bytes(), writer.FormDataContentType()
}

func invoiceControllerContext(t *testing.T, userId int, method string, path string, body []byte, contentType string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", userId)
	// Extract the numeric id from a path like /api/invoice/12/complete-issue.
	idPart := strings.TrimPrefix(path, "/api/invoice/")
	idPart = strings.SplitN(idPart, "/", 2)[0]
	c.Params = gin.Params{{Key: "id", Value: idPart}}
	c.Request = httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		c.Request.Header.Set("Content-Type", contentType)
	}
	return c, recorder
}

func TestCompleteIssueInvoiceSendsPdfImmediatelyAndMarksIssued(t *testing.T) {
	setupInvoiceControllerTest(t)

	user := &model.User{Id: 701, Username: "invoice-user", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Email: "user@example.com"}
	require.NoError(t, model.DB.Create(user).Error)
	require.NoError(t, model.DB.Create(&model.TopUp{
		Id: 7011, UserId: 701, Money: 50, TradeNo: "inv-ctrl-1", PaymentMethod: "epay",
		PaymentProvider: model.PaymentProviderEpay, PaymentCurrency: "CNY", Status: common.TopUpStatusSuccess,
	}).Error)

	inv := &model.Invoice{UserId: 701, Title: "Acme", TaxId: "T", Email: "b@acme.example", Reason: "r", Status: model.InvoiceStatusPending, Currency: "CNY", TotalAmount: 50}
	require.NoError(t, model.CreateInvoiceApplication(701, inv, []*model.TopUp{{Id: 7011}}, decimal.Zero))
	_, err := model.ApproveInvoice(inv.Id, "")
	require.NoError(t, err)
	_, err = model.StartIssueInvoice(inv.Id, "")
	require.NoError(t, err)

	common.OptionMapRWMutex.Lock()
	common.OptionMap["InvoiceEnabled"] = "true"
	common.OptionMapRWMutex.Unlock()
	var sent []common.EmailAttachment
	sendInvoiceStatusEmailFn = func(_ *model.Invoice, _ string, _ string, attachments []common.EmailAttachment) error {
		sent = append(sent, attachments...)
		return nil
	}

	body, contentType := buildCompleteIssueMultipart(t, []byte("%PDF-1.4 fake invoice"), "issued note")
	c, recorder := invoiceControllerContext(t, 701, http.MethodPost, fmt.Sprintf("/api/invoice/%d/complete-issue", inv.Id), body, contentType)
	CompleteIssueInvoice(c)

	var resp struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &resp))
	require.True(t, resp.Success, "unexpected response: %s", recorder.Body.String())

	issued := model.GetInvoiceById(inv.Id)
	require.NotNil(t, issued)
	assert.Equal(t, model.InvoiceStatusIssued, issued.Status)
	assert.Equal(t, "issued note", issued.AdminNote)
	require.Len(t, sent, 1)
	assert.Equal(t, "real-invoice.pdf", sent[0].Filename)
	assert.Equal(t, "%PDF-1.4 fake invoice", string(sent[0].Data))
}

func TestCompleteIssueInvoiceRequiresPdfForFirstIssuance(t *testing.T) {
	setupInvoiceControllerTest(t)

	user := &model.User{Id: 702, Username: "invoice-user2", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Email: "user@example.com"}
	require.NoError(t, model.DB.Create(user).Error)
	require.NoError(t, model.DB.Create(&model.TopUp{
		Id: 7012, UserId: 702, Money: 50, TradeNo: "inv-ctrl-2", PaymentMethod: "epay",
		PaymentProvider: model.PaymentProviderEpay, PaymentCurrency: "CNY", Status: common.TopUpStatusSuccess,
	}).Error)

	inv := &model.Invoice{UserId: 702, Title: "Acme", TaxId: "T", Email: "b@acme.example", Reason: "r", Status: model.InvoiceStatusPending, Currency: "CNY", TotalAmount: 50}
	require.NoError(t, model.CreateInvoiceApplication(702, inv, []*model.TopUp{{Id: 7012}}, decimal.Zero))
	_, err := model.ApproveInvoice(inv.Id, "")
	require.NoError(t, err)
	_, err = model.StartIssueInvoice(inv.Id, "")
	require.NoError(t, err)

	// Multipart form without a file: first issuance requires the PDF.
	body, contentType := buildCompleteIssueMultipart(t, nil, "note")
	c, recorder := invoiceControllerContext(t, 702, http.MethodPost, fmt.Sprintf("/api/invoice/%d/complete-issue", inv.Id), body, contentType)
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

func TestCompleteIssueInvoiceRejectsNonPdfFile(t *testing.T) {
	setupInvoiceControllerTest(t)

	user := &model.User{Id: 703, Username: "invoice-user3", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Email: "user@example.com"}
	require.NoError(t, model.DB.Create(user).Error)
	require.NoError(t, model.DB.Create(&model.TopUp{
		Id: 7013, UserId: 703, Money: 50, TradeNo: "inv-ctrl-3", PaymentMethod: "epay",
		PaymentProvider: model.PaymentProviderEpay, PaymentCurrency: "CNY", Status: common.TopUpStatusSuccess,
	}).Error)

	inv := &model.Invoice{UserId: 703, Title: "Acme", TaxId: "T", Email: "b@acme.example", Reason: "r", Status: model.InvoiceStatusPending, Currency: "CNY", TotalAmount: 50}
	require.NoError(t, model.CreateInvoiceApplication(703, inv, []*model.TopUp{{Id: 7013}}, decimal.Zero))
	_, err := model.ApproveInvoice(inv.Id, "")
	require.NoError(t, err)
	_, err = model.StartIssueInvoice(inv.Id, "")
	require.NoError(t, err)

	body, contentType := buildCompleteIssueMultipart(t, []byte("this is not a pdf"), "")
	c, recorder := invoiceControllerContext(t, 703, http.MethodPost, fmt.Sprintf("/api/invoice/%d/complete-issue", inv.Id), body, contentType)
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

func TestCompleteIssueInvoiceRejectsWrongStatusBeforePdfWrite(t *testing.T) {
	setupInvoiceControllerTest(t)

	user := &model.User{Id: 704, Username: "invoice-user4", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Email: "user@example.com"}
	require.NoError(t, model.DB.Create(user).Error)
	require.NoError(t, model.DB.Create(&model.TopUp{
		Id: 7014, UserId: 704, Money: 50, TradeNo: "inv-ctrl-4", PaymentMethod: "epay",
		PaymentProvider: model.PaymentProviderEpay, PaymentCurrency: "CNY", Status: common.TopUpStatusSuccess,
	}).Error)

	// Application is still pending: complete-issue must be rejected before the
	// PDF can be delivered.
	inv := &model.Invoice{UserId: 704, Title: "Acme", TaxId: "T", Email: "b@acme.example", Reason: "r", Status: model.InvoiceStatusPending, Currency: "CNY", TotalAmount: 50}
	require.NoError(t, model.CreateInvoiceApplication(704, inv, []*model.TopUp{{Id: 7014}}, decimal.Zero))
	require.Equal(t, model.InvoiceStatusPending, model.GetInvoiceById(inv.Id).Status)

	body, contentType := buildCompleteIssueMultipart(t, []byte("%PDF-1.4 wrong status"), "")
	c, recorder := invoiceControllerContext(t, 704, http.MethodPost, fmt.Sprintf("/api/invoice/%d/complete-issue", inv.Id), body, contentType)
	CompleteIssueInvoice(c)

	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &resp))
	assert.False(t, resp.Success)

	after := model.GetInvoiceById(inv.Id)
	require.NotNil(t, after)
	assert.Equal(t, model.InvoiceStatusPending, after.Status, "state must not change")
}

func TestCompleteIssueInvoiceIdempotentRepeatDoesNotResendPdf(t *testing.T) {
	setupInvoiceControllerTest(t)

	user := &model.User{Id: 705, Username: "invoice-user5", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Email: "user@example.com"}
	require.NoError(t, model.DB.Create(user).Error)
	require.NoError(t, model.DB.Create(&model.TopUp{
		Id: 7015, UserId: 705, Money: 50, TradeNo: "inv-ctrl-5", PaymentMethod: "epay",
		PaymentProvider: model.PaymentProviderEpay, PaymentCurrency: "CNY", Status: common.TopUpStatusSuccess,
	}).Error)

	inv := &model.Invoice{UserId: 705, Title: "Acme", TaxId: "T", Email: "b@acme.example", Reason: "r", Status: model.InvoiceStatusPending, Currency: "CNY", TotalAmount: 50}
	require.NoError(t, model.CreateInvoiceApplication(705, inv, []*model.TopUp{{Id: 7015}}, decimal.Zero))
	_, err := model.ApproveInvoice(inv.Id, "")
	require.NoError(t, err)
	_, err = model.StartIssueInvoice(inv.Id, "")
	require.NoError(t, err)
	emailCount := 0
	sendInvoiceStatusEmailFn = func(_ *model.Invoice, _ string, _ string, _ []common.EmailAttachment) error {
		emailCount++
		return nil
	}

	firstPDF := []byte("%PDF-1.4 original invoice")
	body, contentType := buildCompleteIssueMultipart(t, firstPDF, "first")
	c, recorder := invoiceControllerContext(t, 705, http.MethodPost, fmt.Sprintf("/api/invoice/%d/complete-issue", inv.Id), body, contentType)
	CompleteIssueInvoice(c)
	var okResp struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &okResp))
	require.True(t, okResp.Success, "first issuance should succeed")

	issued := model.GetInvoiceById(inv.Id)
	require.Equal(t, model.InvoiceStatusIssued, issued.Status)
	assert.Equal(t, 1, emailCount)

	// A repeat that re-uploads different bytes must be idempotent: the state
	// stays issued and no second email is sent.
	secondPDF := []byte("%PDF-1.4 replacement invoice")
	body, contentType = buildCompleteIssueMultipart(t, secondPDF, "second")
	c, recorder = invoiceControllerContext(t, 705, http.MethodPost, fmt.Sprintf("/api/invoice/%d/complete-issue", inv.Id), body, contentType)
	CompleteIssueInvoice(c)
	var repeatResp struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &repeatResp))
	require.True(t, repeatResp.Success, "idempotent repeat must succeed")

	after := model.GetInvoiceById(inv.Id)
	require.Equal(t, model.InvoiceStatusIssued, after.Status)
	assert.Equal(t, 1, emailCount)
}

func TestCompleteIssueInvoiceEmailFailureLeavesApplicationIssuing(t *testing.T) {
	setupInvoiceControllerTest(t)
	inv := insertInvoiceControllerFixture(t, 706, "delivery-failure", "delivery@example.com")
	_, err := model.ApproveInvoice(inv.Id, "")
	require.NoError(t, err)
	_, err = model.StartIssueInvoice(inv.Id, "")
	require.NoError(t, err)
	sendInvoiceStatusEmailFn = func(_ *model.Invoice, _ string, _ string, _ []common.EmailAttachment) error {
		return errors.New("smtp unavailable")
	}

	body, contentType := buildCompleteIssueMultipart(t, []byte("%PDF-1.4 retry"), "")
	c, recorder := invoiceControllerContext(t, 706, http.MethodPost, fmt.Sprintf("/api/invoice/%d/complete-issue", inv.Id), body, contentType)
	CompleteIssueInvoice(c)

	var resp struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &resp))
	assert.False(t, resp.Success)
	assert.Equal(t, model.InvoiceStatusIssuing, model.GetInvoiceById(inv.Id).Status)
}

func TestCompleteIssueInvoiceRejectsOversizedAdminNote(t *testing.T) {
	setupInvoiceControllerTest(t)
	inv := insertInvoiceControllerFixture(t, 707, "note-too-long", "notes@example.com")
	_, err := model.ApproveInvoice(inv.Id, "")
	require.NoError(t, err)
	_, err = model.StartIssueInvoice(inv.Id, "")
	require.NoError(t, err)

	body, contentType := buildCompleteIssueMultipart(t, []byte("%PDF-1.4 valid"), strings.Repeat("x", maxInvoiceNoteBytes+1))
	c, recorder := invoiceControllerContext(t, 707, http.MethodPost, fmt.Sprintf("/api/invoice/%d/complete-issue", inv.Id), body, contentType)
	CompleteIssueInvoice(c)

	var resp struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &resp))
	assert.False(t, resp.Success)
	assert.Equal(t, model.InvoiceStatusIssuing, model.GetInvoiceById(inv.Id).Status)
}

func TestInvoiceEmailRendersInRecipientLanguage(t *testing.T) {
	require.NoError(t, i18n.Init())
	subjectKey := i18n.MsgInvoiceEmailStatusSubject
	bodyKey := i18n.MsgInvoiceEmailStatusBody
	args := map[string]any{
		"SystemName": "Tokeness",
		"SiteURL":    "https://tokeness.io",
		"Id":         1,
		"Amount":     100,
		"Currency":   "CNY",
		"Status":     "Issued",
		"Note":       "",
	}

	enSubject := i18n.Translate(i18n.LangEn, subjectKey, args)
	enBody := i18n.Translate(i18n.LangEn, bodyKey, args)
	zhSubject := i18n.Translate(i18n.LangZhCN, subjectKey, args)
	zhBody := i18n.Translate(i18n.LangZhCN, bodyKey, args)

	// The recipient-language rendering must differ between languages so the
	// email is not silently sent in the operator's request language.
	assert.NotEqual(t, enSubject, zhSubject, "email subject must be localized per recipient")
	assert.NotEqual(t, enBody, zhBody, "email body must be localized per recipient")
	assert.NotContains(t, enSubject, subjectKey, "en subject must not leak the raw key")
	assert.NotContains(t, zhSubject, subjectKey, "zh subject must not leak the raw key")
	assert.Contains(t, enSubject, "1", "subject must interpolate the invoice id")
	assert.Contains(t, zhSubject, "1", "subject must interpolate the invoice id")
	assert.Contains(t, enSubject, "Tokeness", "subject must identify the sending site")
	assert.Contains(t, zhSubject, "Tokeness", "subject must identify the sending site")
	assert.Contains(t, enBody, "https://tokeness.io", "body must link back to the site")
	assert.Contains(t, zhBody, "https://tokeness.io", "body must link back to the site")
}
