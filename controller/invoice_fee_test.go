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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setInvoiceFeeRateForTest overrides the fee rate for the current test only.
func setInvoiceFeeRateForTest(t *testing.T, rate string) {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	common.OptionMap["InvoiceFeeRate"] = rate
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		delete(common.OptionMap, "InvoiceFeeRate")
		common.OptionMapRWMutex.Unlock()
	})
}

func insertOperatorFeeUserWithTopUp(t *testing.T, userId int, username string, email string, quota int, topUpId int, money float64) {
	t.Helper()
	user := &model.User{Id: userId, Username: username, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Email: email, Quota: quota}
	require.NoError(t, model.DB.Create(user).Error)
	topUp := &model.TopUp{
		Id: topUpId, UserId: userId, Money: money, TradeNo: fmt.Sprintf("inv-fee-http-%d", userId),
		PaymentMethod: "epay", PaymentProvider: model.PaymentProviderEpay, PaymentCurrency: "CNY", Status: common.TopUpStatusSuccess,
	}
	require.NoError(t, model.DB.Create(topUp).Error)
}

func readUserQuota(t *testing.T, userId int) int {
	t.Helper()
	var user model.User
	require.NoError(t, model.DB.Select("quota").Where("id = ?", userId).First(&user).Error)
	return user.Quota
}

func TestGetInvoiceOptionsReportsFeeRate(t *testing.T) {
	setupInvoiceControllerTest(t)
	setInvoiceFeeRateForTest(t, "0.06")
	insertOperatorFeeUserWithTopUp(t, 901, "fee-options", "opts@fee.example", 10_000_000, 90100, 50)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", 901)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/invoice/options", nil)
	GetInvoiceOptions(c)

	payload := jsonResponse(t, recorder)
	data, _ := payload["data"].(map[string]any)
	require.NotNil(t, data)
	assert.Equal(t, 0.06, data["fee_rate"])
	assert.Equal(t, true, data["enabled"])
}

func TestCreateInvoiceChargesFeeFromBalance(t *testing.T) {
	setupInvoiceControllerTest(t)
	setInvoiceFeeRateForTest(t, "0.06")
	insertOperatorFeeUserWithTopUp(t, 902, "fee-create", "create@fee.example", 2_000_000, 90200, 50)

	body := fmt.Sprintf(`{"orders":[{"order_type":"topup","order_id":%d}],"invoice_type":"organization","title":"Acme","tax_id":"T","email":"deliver@example.com","reason":"r","remark":""}`, 90200)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", 902)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/invoice", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	CreateInvoice(c)

	payload := jsonResponse(t, recorder)
	require.Equal(t, true, payload["success"], "payload=%v", payload)
	data, _ := payload["data"].(map[string]any)
	require.NotNil(t, data)

	expectedFeeQuota, _ := common.QuotaFromDecimalChecked(
		decimal.NewFromFloat(50).Mul(decimal.NewFromFloat(common.QuotaPerUnit)).Mul(decimal.NewFromFloat(0.06)),
	)
	assert.Equal(t, 0.06, data["fee_rate"])
	assert.Equal(t, 3.0, data["fee_amount"]) // 50 * 0.06 = 3.00
	assert.Equal(t, float64(expectedFeeQuota), data["fee_quota"])
	assert.Equal(t, 2_000_000-expectedFeeQuota, readUserQuota(t, 902), "quota must be debited by the fee")
}

func TestCreateInvoiceInsufficientBalanceReturnsError(t *testing.T) {
	setupInvoiceControllerTest(t)
	setInvoiceFeeRateForTest(t, "0.06")
	insertOperatorFeeUserWithTopUp(t, 903, "fee-poor", "poor@fee.example", 0, 90300, 50)

	body := fmt.Sprintf(`{"orders":[{"order_type":"topup","order_id":%d}],"invoice_type":"organization","title":"Acme","tax_id":"T","email":"deliver@example.com","reason":"r","remark":""}`, 90300)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", 903)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/invoice", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	CreateInvoice(c)

	payload := jsonResponse(t, recorder)
	assert.Equal(t, false, payload["success"])
	msg, _ := payload["message"].(string)
	assert.NotEmpty(t, msg)
	assert.NotEqual(t, i18n.MsgInvoiceInsufficientBalance, msg, "message must be translated, not the raw key")
	assert.Equal(t, 0, readUserQuota(t, 903), "quota must stay untouched")

	var invoiceCount int64
	require.NoError(t, model.DB.Model(&model.Invoice{}).Where("user_id = ?", 903).Count(&invoiceCount).Error)
	assert.Zero(t, invoiceCount, "no invoice application must be created")
}
