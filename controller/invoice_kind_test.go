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

	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createInvoiceReq(t *testing.T, userId int, userIdStr string, body string) map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", userId)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/invoice", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	CreateInvoice(c)
	return jsonResponse(t, recorder)
}

func TestCreateInvoiceSpecial_RejectsIndividual(t *testing.T) {
	setupInvoiceControllerTest(t)
	insertOperatorFeeUserWithTopUp(t, 920, "kind-ind", "kindind@example.com", 10_000_000, 92000, 50)
	body := fmt.Sprintf(`{"orders":[{"order_type":"topup","order_id":%d}],"invoice_type":"individual","invoice_kind":"special","title":"Acme","tax_id":"T","email":"deliver@example.com","address":"SH","phone":"13800000000","bank_name":"Bank","bank_account":"6222","reason":"r","remark":""}`, 92000)

	payload := createInvoiceReq(t, 920, "", body)
	assert.Equal(t, false, payload["success"])
	msg, _ := payload["message"].(string)
	assert.NotEmpty(t, msg)
	assert.NotEqual(t, i18n.MsgInvoiceSpecialRequiresOrg, msg, "message must be translated, not the raw key")
}

func TestCreateInvoiceSpecial_MissingFullInfoRejected(t *testing.T) {
	setupInvoiceControllerTest(t)
	insertOperatorFeeUserWithTopUp(t, 921, "kind-org", "kindorg@example.com", 10_000_000, 92100, 50)
	// Organization + special, but no bank account: must be rejected.
	body := fmt.Sprintf(`{"orders":[{"order_type":"topup","order_id":%d}],"invoice_type":"organization","invoice_kind":"special","title":"Acme","tax_id":"T","email":"deliver@example.com","address":"SH","phone":"13800000000","bank_name":"Bank","bank_account":"","reason":"r","remark":""}`, 92100)

	payload := createInvoiceReq(t, 921, "", body)
	assert.Equal(t, false, payload["success"])
	msg, _ := payload["message"].(string)
	assert.NotEmpty(t, msg)
	assert.NotEqual(t, i18n.MsgInvoiceSpecialRequiresFullInfo, msg, "message must be translated, not the raw key")
}

func TestCreateInvoiceSpecial_Succeeds(t *testing.T) {
	setupInvoiceControllerTest(t)
	insertOperatorFeeUserWithTopUp(t, 922, "kind-full", "kindfull@example.com", 10_000_000, 92200, 50)
	body := fmt.Sprintf(`{"orders":[{"order_type":"topup","order_id":%d}],"invoice_type":"organization","invoice_kind":"special","title":"Acme","tax_id":"T","email":"deliver@example.com","address":"SH","phone":"13800000000","bank_name":"Bank","bank_account":"6222","reason":"r","remark":""}`, 92200)

	payload := createInvoiceReq(t, 922, "", body)
	require.Equal(t, true, payload["success"], "payload=%v", payload)
	data, _ := payload["data"].(map[string]any)
	require.NotNil(t, data)
	assert.Equal(t, "special", data["invoice_kind"])
	assert.Equal(t, "organization", data["invoice_type"])

	var invoiceCount int64
	require.NoError(t, model.DB.Model(&model.Invoice{}).Where("user_id = ?", 922).Count(&invoiceCount).Error)
	assert.Equal(t, int64(1), invoiceCount)
}

func TestCreateInvoiceGeneral_OmittingKindDefaultsToGeneral(t *testing.T) {
	setupInvoiceControllerTest(t)
	insertOperatorFeeUserWithTopUp(t, 923, "kind-default", "kinddefault@example.com", 10_000_000, 92300, 50)
	// No invoice_kind field: must default to general.
	body := fmt.Sprintf(`{"orders":[{"order_type":"topup","order_id":%d}],"invoice_type":"organization","title":"Acme","tax_id":"T","email":"deliver@example.com","reason":"r","remark":""}`, 92300)

	payload := createInvoiceReq(t, 923, "", body)
	require.Equal(t, true, payload["success"], "payload=%v", payload)
	data, _ := payload["data"].(map[string]any)
	require.NotNil(t, data)
	assert.Equal(t, "general", data["invoice_kind"])
}
