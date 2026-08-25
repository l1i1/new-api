/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGetTopUpInfoIncludesWalletPresentationContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	paymentSetting := operation_setting.GetPaymentSetting()
	originalContact := paymentSetting.TopupContact
	originalSubtitle := paymentSetting.TopupSubtitle
	paymentSetting.TopupContact = "**support@example.com**"
	paymentSetting.TopupSubtitle = "<strong>Choose a payment method</strong>"
	t.Cleanup(func() {
		paymentSetting.TopupContact = originalContact
		paymentSetting.TopupSubtitle = originalSubtitle
	})

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	GetTopUpInfo(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			TopupContact  string `json:"topup_contact"`
			TopupSubtitle string `json:"topup_subtitle"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.Equal(t, "**support@example.com**", response.Data.TopupContact)
	require.Equal(t, "<strong>Choose a payment method</strong>", response.Data.TopupSubtitle)
}
