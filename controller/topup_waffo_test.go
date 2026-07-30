package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/require"
)

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
