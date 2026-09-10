package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/require"
)

func TestHotPayWalletMethodMatrix(t *testing.T) {
	method, err := hotPayWalletMethod(model.PaymentCurrencyCNY, "wxpay")
	require.NoError(t, err)
	require.Equal(t, "wechat_pay", method)

	method, err = hotPayWalletMethod(model.PaymentCurrencyCNY, "alipay")
	require.NoError(t, err)
	require.Equal(t, "alipay", method)

	method, err = hotPayWalletMethod(model.PaymentCurrencyUSD, "applepay")
	require.NoError(t, err)
	require.Equal(t, "apple_pay", method)

	_, err = hotPayWalletMethod(model.PaymentCurrencyCNY, "card")
	require.Error(t, err)
	_, err = hotPayWalletMethod(model.PaymentCurrencyUSD, "alipay")
	require.Error(t, err)

	// CNY subscriptions ride gopay_alipay or the native WeChat provider; Waffo
	// Pancake has no CNY subscription products.
	method, err = hotPaySubscriptionMethod(model.PaymentCurrencyCNY, "wechat_pay")
	require.NoError(t, err)
	require.Equal(t, "wechat_pay", method)

	method, err = hotPaySubscriptionMethod(model.PaymentCurrencyCNY, "alipay")
	require.NoError(t, err)
	require.Equal(t, "alipay", method)

	method, err = hotPaySubscriptionMethod(model.PaymentCurrencyUSD, "wechat_pay")
	require.NoError(t, err)
	require.Equal(t, "wechat_pay", method)
}

func TestHotPayProviderSelectionFollowsMethod(t *testing.T) {
	require.Equal(t, model.PaymentProviderGoPayAlipay, hotPayProviderForMethod("alipay", model.PaymentCurrencyCNY))
	require.Equal(t, model.PaymentProviderWechatV3, hotPayProviderForMethod("wechat_pay", model.PaymentCurrencyCNY))
	// The native WeChat provider is CNY-only, so a non-CNY WeChat request falls
	// back to Waffo Pancake, the only multi-currency WeChat channel.
	require.Equal(t, model.PaymentProviderWaffoPancake, hotPayProviderForMethod("wechat_pay", model.PaymentCurrencyUSD))

	// USD-only wallet methods ride the multi-currency provider, and a method
	// absent from the routing map must never resolve to an empty provider.
	require.Equal(t, model.PaymentProviderWaffoPancake, hotPayProviderForMethod("card", model.PaymentCurrencyUSD))
	require.Equal(t, model.PaymentProviderWaffoPancake, hotPayProviderForMethod("apple_pay", model.PaymentCurrencyUSD))
	require.Equal(t, model.PaymentProviderWaffoPancake, hotPayProviderForMethod("google_pay", model.PaymentCurrencyUSD))
	require.Equal(t, model.PaymentProviderWaffoPancake, hotPayProviderForMethod("some_future_method", model.PaymentCurrencyUSD))

	// Alipay and the native WeChat provider are routed entirely by HotPay: no
	// account pin is forwarded, HotPay picks the channel by its own priority,
	// and the routed account is backfilled onto the local order at bind time.
	require.Equal(t, "", hotPayProviderAccountIDForMethod("alipay", model.PaymentCurrencyCNY))
	require.Equal(t, "", hotPayProviderAccountIDForMethod("wechat_pay", model.PaymentCurrencyCNY))
	require.Equal(t, hotPayProviderAccountID(), hotPayProviderAccountIDForMethod("wechat_pay", model.PaymentCurrencyUSD))
}

func TestHotPayGatewayMethodResolvesToProviderVocabulary(t *testing.T) {
	// The native WeChat provider registers its own method names; this merchant
	// only has the native QR product authorized.
	require.Equal(t, "wechat_v3_native", hotPayGatewayMethodFor(model.PaymentProviderWechatV3, "wechat_pay"))
	require.Equal(t, "wechat_v3_native", hotPayGatewayMethodFor(model.PaymentProviderWechatV3, "wxpay"))
	// Other providers keep the canonical method name unchanged.
	require.Equal(t, "alipay", hotPayGatewayMethodFor(model.PaymentProviderGoPayAlipay, "alipay"))
	require.Equal(t, "wechat_pay", hotPayGatewayMethodFor(model.PaymentProviderWaffoPancake, "wechat_pay"))
}

// TestHotPayMethodRoutingIsConfigurable pins the operator-facing routing map:
// a configured entry overrides the built-in default, unknown providers are
// ignored rather than silently breaking a channel, and an unset map restores
// the defaults.
func TestHotPayMethodRoutingIsConfigurable(t *testing.T) {
	common.OptionMapRWMutex.Lock()
	original, hadOriginal := common.OptionMap[setting.HotPayMethodProvidersOptionKey]
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		if hadOriginal {
			common.OptionMap[setting.HotPayMethodProvidersOptionKey] = original
		} else {
			delete(common.OptionMap, setting.HotPayMethodProvidersOptionKey)
		}
		common.OptionMapRWMutex.Unlock()
	})

	setRouting := func(value string) {
		common.OptionMapRWMutex.Lock()
		if common.OptionMap == nil {
			common.OptionMap = map[string]string{}
		}
		common.OptionMap[setting.HotPayMethodProvidersOptionKey] = value
		common.OptionMapRWMutex.Unlock()
	}

	// A configured entry wins over the built-in default for that method.
	setRouting(`{"wechat_pay":"waffo_pancake"}`)
	require.Equal(t, model.PaymentProviderWaffoPancake, hotPayProviderForMethod("wechat_pay", model.PaymentCurrencyCNY))

	// An unknown provider is ignored, so that method falls back to the built-in
	// default instead of persisting a broken route.
	setRouting(`{"wechat_pay":"not_a_provider"}`)
	require.Equal(t, model.PaymentProviderWechatV3, hotPayProviderForMethod("wechat_pay", model.PaymentCurrencyCNY))

	// An unset map restores the built-in defaults.
	setRouting("")
	require.Equal(t, model.PaymentProviderWechatV3, hotPayProviderForMethod("wechat_pay", model.PaymentCurrencyCNY))
	require.Equal(t, model.PaymentProviderGoPayAlipay, hotPayProviderForMethod("alipay", model.PaymentCurrencyCNY))
}

func TestHotPayMerchantOrderIDIsStablePerIdempotencyKey(t *testing.T) {
	first := hotPayMerchantOrderID("wallet", 42, "retry-key")
	second := hotPayMerchantOrderID("wallet", 42, "retry-key")
	require.Equal(t, first, second)
	require.NotEqual(t, first, hotPayMerchantOrderID("wallet", 42, "other-key"))
	require.NotEqual(t, first, hotPayMerchantOrderID("subscription", 42, "retry-key"))
}

func TestHotPayQuotaAmountFailsClosedOnOverflow(t *testing.T) {
	quota, err := hotPayQuotaAmount(1)
	require.NoError(t, err)
	require.Equal(t, int64(common.QuotaPerUnit), quota)

	overflowAmount := int64(common.MaxQuota)/int64(common.QuotaPerUnit) + 1
	_, err = hotPayQuotaAmount(overflowAmount)
	require.ErrorIs(t, err, errHotPayQuotaOverflow)
}

func TestHotPayGatewayPermanentErrorClassification(t *testing.T) {
	require.True(t, hotPayGatewayErrorIsPermanent(&service.HotPayGatewayError{Code: "invalid_amount"}))
	require.True(t, hotPayGatewayErrorIsPermanent(&service.HotPayGatewayError{Code: "product_not_found"}))
	require.False(t, hotPayGatewayErrorIsPermanent(&service.HotPayGatewayError{Code: "provider_unavailable", StatusCode: 503}))
	require.False(t, hotPayGatewayErrorIsPermanent(&service.HotPayGatewayError{Code: "idempotency_conflict", StatusCode: 409}))
}

func TestHotPayWaffoWalletPriceSnapshotPreservesSettlementCurrency(t *testing.T) {
	snapshot := hotPayWaffoWalletPriceSnapshot(100, "7.00", model.PaymentCurrencyCNY, model.PaymentCurrencyUSD)

	require.Equal(t, int64(100), snapshot["quota_amount"])
	require.Equal(t, "7.00", snapshot["provider_amount"])
	require.Equal(t, model.PaymentCurrencyUSD, snapshot["pricing_currency"])
	require.Equal(t, model.PaymentCurrencyCNY, snapshot["display_currency"])
	require.Equal(t, model.PaymentCurrencyUSD, snapshot["provider_currency"])
}
