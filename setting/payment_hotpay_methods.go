package setting

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// HotPayMethodTypePrefix marks payment entry types that route to the HotPay
// gateway; the suffix is the canonical HotPay method.
const HotPayMethodTypePrefix = "hotpay:"

// DefaultHotPayPayMethods is the default registry used when the option is unset.
// Each entry is a "hotpay:<method>" payment type the buyer list may reference.
var DefaultHotPayPayMethods = []string{
	"hotpay:alipay",
	"hotpay:wechat_pay",
}

// HotPayMethodFromType parses a PayMethods entry type into the canonical
// HotPay method. Returns "" for non-HotPay types.
func HotPayMethodFromType(paymentType string) string {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(paymentType)), HotPayMethodTypePrefix) {
		return ""
	}
	return strings.TrimSpace(paymentType[len(HotPayMethodTypePrefix):])
}

// GetHotPayPayMethods reads the registered "hotpay:<method>" channel ids from
// the OptionMap, falling back to the defaults when unset or unparsable. It is
// a whitelist: a buyer-visible PayMethods entry is only routed through the
// HotPay gateway when its type is listed here.
func GetHotPayPayMethods() []string {
	common.OptionMapRWMutex.RLock()
	jsonStr := common.OptionMap["HotPayPayMethods"]
	common.OptionMapRWMutex.RUnlock()

	if jsonStr == "" {
		return copyDefaultHotPayPayMethods()
	}
	var methods []string
	if err := common.UnmarshalJsonStr(jsonStr, &methods); err != nil {
		return copyDefaultHotPayPayMethods()
	}
	return stripInvalidHotPayTypes(methods)
}

// SetHotPayPayMethods serializes the registry into the OptionMap.
func SetHotPayPayMethods(methods []string) error {
	jsonBytes, err := common.Marshal(stripInvalidHotPayTypes(methods))
	if err != nil {
		return err
	}
	common.OptionMapRWMutex.Lock()
	common.OptionMap["HotPayPayMethods"] = string(jsonBytes)
	common.OptionMapRWMutex.Unlock()
	return nil
}

// HotPayPayMethods2JsonString serializes the defaults for InitOptionMap.
func HotPayPayMethods2JsonString() string {
	jsonBytes, err := common.Marshal(DefaultHotPayPayMethods)
	if err != nil {
		return "[]"
	}
	return string(jsonBytes)
}

func copyDefaultHotPayPayMethods() []string {
	cp := make([]string, len(DefaultHotPayPayMethods))
	copy(cp, DefaultHotPayPayMethods)
	return cp
}

// stripInvalidHotPayTypes drops empty, duplicate, or non-"hotpay:<method>"
// entries so the registry only holds validated channel ids.
func stripInvalidHotPayTypes(methods []string) []string {
	seen := make(map[string]struct{}, len(methods))
	cleaned := make([]string, 0, len(methods))
	for _, m := range methods {
		t := strings.ToLower(strings.TrimSpace(m))
		if HotPayMethodFromType(t) == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		cleaned = append(cleaned, t)
	}
	return cleaned
}

// HotPayMethodProvidersOptionKey is the OptionMap key holding the
// method→provider routing map as JSON.
const HotPayMethodProvidersOptionKey = "HotPayMethodProviders"

// hotPayRoutableProviders is the set of HotPay provider types this service can
// route a buyer-visible method to. A routing entry naming anything else is
// ignored so a typo cannot silently disable a channel.
var hotPayRoutableProviders = map[string]struct{}{
	"gopay_alipay":  {},
	"waffo_pancake": {},
	"wechat_v3":     {},
}

// DefaultHotPayMethodProviders is the method→provider routing used when the
// option is unset. alipay is served by the CNY-only gopay_alipay provider;
// wechat_pay is served by the native WeChat Pay v3 provider, whose adaptive
// chain picks native/jsapi/h5 per device. The remaining wallet methods are
// USD-only and ride Waffo Pancake, the multi-currency hosted checkout. Any
// method absent from the map also falls back to Waffo Pancake so a new method
// can never resolve to an empty provider.
var DefaultHotPayMethodProviders = map[string]string{
	"alipay":     "gopay_alipay",
	"wechat_pay": "wechat_v3",
	"card":       "waffo_pancake",
	"apple_pay":  "waffo_pancake",
	"google_pay": "waffo_pancake",
}

// GetHotPayMethodProviders reads the method→provider routing map from the
// OptionMap, falling back to the defaults when unset or unparsable. Unknown
// providers and malformed entries fall back to the default for that method.
func GetHotPayMethodProviders() map[string]string {
	common.OptionMapRWMutex.RLock()
	jsonStr := common.OptionMap[HotPayMethodProvidersOptionKey]
	common.OptionMapRWMutex.RUnlock()

	routing := copyDefaultHotPayMethodProviders()
	if jsonStr == "" {
		return routing
	}
	var parsed map[string]string
	if err := common.UnmarshalJsonStr(jsonStr, &parsed); err != nil {
		return routing
	}
	for method, provider := range stripInvalidHotPayProviders(parsed) {
		routing[method] = provider
	}
	return routing
}

// HotPayMethodProviders2JsonString serializes the defaults for InitOptionMap.
func HotPayMethodProviders2JsonString() string {
	jsonBytes, err := common.Marshal(DefaultHotPayMethodProviders)
	if err != nil {
		return "{}"
	}
	return string(jsonBytes)
}

func copyDefaultHotPayMethodProviders() map[string]string {
	cp := make(map[string]string, len(DefaultHotPayMethodProviders))
	for method, provider := range DefaultHotPayMethodProviders {
		cp[method] = provider
	}
	return cp
}

func stripInvalidHotPayProviders(routing map[string]string) map[string]string {
	cleaned := make(map[string]string, len(routing))
	for method, provider := range routing {
		method = strings.ToLower(strings.TrimSpace(method))
		provider = strings.ToLower(strings.TrimSpace(provider))
		if method == "" || provider == "" {
			continue
		}
		if _, ok := hotPayRoutableProviders[provider]; !ok {
			continue
		}
		cleaned[method] = provider
	}
	return cleaned
}
