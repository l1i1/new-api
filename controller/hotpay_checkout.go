package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

var errHotPayUnsupportedLegacyMethod = errors.New("payment method is not supported by the HotPay gateway")

func hotPayGatewayClient() (*service.HotPayGatewayClient, error) {
	return service.NewHotPayGatewayClientFromEnv()
}

func hotPayEnvironment() string {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("HOTPAY_GATEWAY_ENVIRONMENT")))
	if value == "test" {
		return "test"
	}
	return "prod"
}

func hotPayIdempotencyKey(c *gin.Context, namespace, merchantOrderID string) string {
	if value := strings.TrimSpace(c.GetHeader("Idempotency-Key")); value != "" {
		return value
	}
	// Older browser clients do not send an idempotency key. A unique local
	// order key keeps those requests compatible; clients that retry should send
	// Idempotency-Key explicitly to receive the same checkout attempt.
	return fmt.Sprintf("new-api:%s:%s", namespace, merchantOrderID)
}

func hotPayMerchantOrderID(namespace string, userID int, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", namespace, userID, strings.TrimSpace(idempotencyKey))))
	return fmt.Sprintf("HP_%s_%d_%s", namespace, userID, hex.EncodeToString(digest[:])[:24])
}

func hotPayMinorAmount(amount float64) (int64, error) {
	minor := decimal.NewFromFloat(amount).Mul(decimal.NewFromInt(100)).Round(0)
	if !minor.IsInteger() || minor.LessThanOrEqual(decimal.Zero) {
		return 0, errors.New("payment amount is invalid")
	}
	return minor.IntPart(), nil
}

func hotPayWalletMethod(currency, value string) (string, error) {
	method := strings.ToLower(strings.TrimSpace(value))
	switch method {
	case "wechat", "wechat_pay", "wxpay":
		method = "wechat_pay"
	case "card", "apple_pay", "applepay", "google_pay", "googlepay":
		if method == "applepay" {
			method = "apple_pay"
		}
		if method == "googlepay" {
			method = "google_pay"
		}
	default:
		return "", errHotPayUnsupportedLegacyMethod
	}
	if strings.EqualFold(currency, model.PaymentCurrencyCNY) && method != "wechat_pay" {
		return "", errHotPayUnsupportedLegacyMethod
	}
	return method, nil
}

func hotPaySubscriptionMethod(currency, value string) (string, error) {
	if strings.EqualFold(strings.TrimSpace(currency), model.PaymentCurrencyCNY) {
		return "", errHotPayUnsupportedLegacyMethod
	}
	return hotPayWalletMethod(currency, value)
}

func hotPayCheckoutResponse(result service.HotPayGatewayCreateOrderResponse) gin.H {
	return gin.H{
		"checkout_url":      result.Attempt.CheckoutURL,
		"session_id":        result.Attempt.ProviderSessionID,
		"expires_at":        result.Order.ExpiresAt,
		"order_id":          result.Order.MerchantOrderID,
		"provider_order_id": result.Order.ProviderOrderID,
	}
}

func hotPayReturnURL(path string) string {
	callback := strings.TrimRight(strings.TrimSpace(service.GetCallbackAddress()), "/")
	if callback == "" {
		return ""
	}
	parsed, err := url.Parse(callback + path)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.String()
}

func hotPayGatewayErrorMessage(err error) string {
	var gatewayErr *service.HotPayGatewayError
	if errors.As(err, &gatewayErr) && gatewayErr.Code != "" {
		switch gatewayErr.Code {
		case "unsupported_payment_method":
			return "当前支付方式暂不支持"
		case "provider_unavailable", "provider_not_configured":
			return "支付渠道暂时不可用"
		case "product_required", "product_not_found", "product_unavailable":
			return "支付商品未配置或不可用"
		case "idempotency_conflict":
			return "支付请求幂等键与已有订单冲突"
		}
	}
	if errors.Is(err, service.ErrHotPayGatewayNotConfigured) {
		return "支付网关未配置"
	}
	return "拉起支付失败"
}

func hotPayExpiresAt(seconds int) string {
	return time.Now().UTC().Add(time.Duration(seconds) * time.Second).Format(time.RFC3339)
}

func hotPayPriceSnapshot(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	return values
}

func hotPayQuotaAmount(amount int64) int64 {
	if amount <= 0 {
		return 0
	}
	quota := decimal.NewFromInt(amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	if quota.GreaterThan(decimal.NewFromInt(int64(common.MaxQuota))) {
		return 0
	}
	return quota.IntPart()
}

func hotPayStringAmount(value float64) string {
	return decimal.NewFromFloat(value).StringFixed(2)
}

func hotPayUserID(userID int) string {
	return strconv.Itoa(userID)
}

func hotPayProviderProductID() string {
	return strings.TrimSpace(setting.WaffoPancakeProductID)
}
