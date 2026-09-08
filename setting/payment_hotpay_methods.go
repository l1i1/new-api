package setting

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// HotPayPayMethod defines a user-visible payment method entry backed by the
// HotPay gateway, following the same registry pattern as WaffoPayMethod.
// Entries use the standard PayMethods shape: the Type must be a
// "hotpay:<method>" entry whose method part is a canonical HotPay method
// (alipay, wechat_pay, card, apple_pay, google_pay).
type HotPayPayMethod struct {
	Name     string `json:"name"`      // Frontend display name
	Icon     string `json:"icon"`      // Frontend icon identifier (react-icons name)
	Type     string `json:"type"`      // "hotpay:<method>" entry type
	MinTopUp string `json:"min_topup"` // Optional per-method minimum top-up, empty falls back to the global value
}

// DefaultHotPayPayMethods is the default registry used when the option is unset.
var DefaultHotPayPayMethods = []HotPayPayMethod{
	{Name: "支付宝", Icon: "SiAlipay", Type: "hotpay:alipay"},
	{Name: "微信", Icon: "SiWechat", Type: "hotpay:wechat_pay"},
}

// HotPayMethodTypePrefix marks payment entry types that route to the HotPay
// gateway; the suffix is the canonical HotPay method.
const HotPayMethodTypePrefix = "hotpay:"

// HotPayMethodFromType parses a PayMethods entry type into the canonical
// HotPay method. Returns "" for non-HotPay types.
func HotPayMethodFromType(paymentType string) string {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(paymentType)), HotPayMethodTypePrefix) {
		return ""
	}
	return strings.TrimSpace(paymentType[len(HotPayMethodTypePrefix):])
}

// GetHotPayPayMethods reads the registry from the OptionMap, falling back to
// the defaults when unset or unparsable.
func GetHotPayPayMethods() []HotPayPayMethod {
	common.OptionMapRWMutex.RLock()
	jsonStr := common.OptionMap["HotPayPayMethods"]
	common.OptionMapRWMutex.RUnlock()

	if jsonStr == "" {
		return copyDefaultHotPayPayMethods()
	}
	var methods []HotPayPayMethod
	if err := common.UnmarshalJsonStr(jsonStr, &methods); err != nil {
		return copyDefaultHotPayPayMethods()
	}
	return methods
}

// SetHotPayPayMethods serializes the registry into the OptionMap.
func SetHotPayPayMethods(methods []HotPayPayMethod) error {
	jsonBytes, err := common.Marshal(methods)
	if err != nil {
		return err
	}
	common.OptionMapRWMutex.Lock()
	common.OptionMap["HotPayPayMethods"] = string(jsonBytes)
	common.OptionMapRWMutex.Unlock()
	return nil
}

func copyDefaultHotPayPayMethods() []HotPayPayMethod {
	cp := make([]HotPayPayMethod, len(DefaultHotPayPayMethods))
	copy(cp, DefaultHotPayPayMethods)
	return cp
}

// HotPayPayMethods2JsonString serializes the defaults for InitOptionMap.
func HotPayPayMethods2JsonString() string {
	jsonBytes, err := common.Marshal(DefaultHotPayPayMethods)
	if err != nil {
		return "[]"
	}
	return string(jsonBytes)
}
