package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/waffo-com/waffo-go/config"
	"github.com/waffo-com/waffo-go/core"
)

func TestSendWaffoWebhookResponseStatus(t *testing.T) {
	tests := []struct {
		name       string
		success    bool
		wantStatus int
		wantBody   string
	}{
		{name: "success", success: true, wantStatus: http.StatusOK, wantBody: `{"message":"success"}`},
		{name: "failure", success: false, wantStatus: http.StatusBadRequest, wantBody: `{"message":"failed"}`},
	}

	wh := core.NewWebhookHandler(&config.WaffoConfig{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)

			sendWaffoWebhookResponse(ctx, wh, tt.success, "test error")

			require.Equal(t, tt.wantStatus, recorder.Code)
			require.JSONEq(t, tt.wantBody, recorder.Body.String())
		})
	}
}

func TestWaffoPaymentAmountsSeparateLocalCNYAndProviderUSD(t *testing.T) {
	originalUnitPrice := setting.WaffoUnitPrice
	originalPrice := operation_setting.Price
	originalExchangeRate := operation_setting.USDExchangeRate
	originalQuotaDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	originalDiscounts := operation_setting.GetPaymentSetting().AmountDiscount
	originalTopupGroupRatio := common.TopupGroupRatio2JSONString()

	t.Cleanup(func() {
		setting.WaffoUnitPrice = originalUnitPrice
		operation_setting.Price = originalPrice
		operation_setting.USDExchangeRate = originalExchangeRate
		operation_setting.GetGeneralSetting().QuotaDisplayType = originalQuotaDisplayType
		operation_setting.GetPaymentSetting().AmountDiscount = originalDiscounts
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(originalTopupGroupRatio))
	})

	setting.WaffoUnitPrice = 1
	operation_setting.Price = 7
	operation_setting.USDExchangeRate = 7
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	operation_setting.GetPaymentSetting().AmountDiscount = map[int]float64{}
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1}`))

	localCNY := getWaffoLocalPayMoney(71, "default")
	providerUSD := getWaffoPayMoney(71, "default")
	require.InDelta(t, 497, localCNY, 0.000001)
	require.InDelta(t, 71, providerUSD, 0.000001)
}
