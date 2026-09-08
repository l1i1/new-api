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
