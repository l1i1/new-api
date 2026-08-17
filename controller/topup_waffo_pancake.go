package controller

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/thanhpk/randstr"
)

type WaffoPancakePayRequest struct {
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	PaymentMethod string `json:"payment_method"`
}

func normalizeWaffoPancakeWalletCurrency(currency string) (string, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		// Keep older clients working while the frontend starts sending its
		// display-currency selection explicitly.
		return model.PaymentCurrencyCNY, nil
	}
	if currency != model.PaymentCurrencyCNY && currency != model.PaymentCurrencyUSD {
		return "", fmt.Errorf("unsupported wallet currency %q", currency)
	}
	return currency, nil
}

func waffoPancakeProviderPaymentMethod(paymentMethod string) (service.WaffoPancakePaymentMethod, error) {
	normalized := strings.ToLower(strings.TrimSpace(paymentMethod))
	normalized = strings.TrimPrefix(normalized, model.PaymentMethodWaffoPancake+":")
	switch normalized {
	case "card":
		return service.WaffoPancakePaymentMethodCard, nil
	case "apple_pay", "applepay":
		return service.WaffoPancakePaymentMethodApplePay, nil
	case "google_pay", "googlepay":
		return service.WaffoPancakePaymentMethodGooglePay, nil
	case "wechat_pay", "wechat":
		return service.WaffoPancakePaymentMethodWeChat, nil
	default:
		return "", fmt.Errorf("unsupported Waffo Pancake payment method %q", paymentMethod)
	}
}

func isWaffoPancakePaymentMethodType(paymentType string) bool {
	paymentType = strings.ToLower(strings.TrimSpace(paymentType))
	switch paymentType {
	case model.PaymentMethodWaffoPancake,
		model.PaymentMethodWaffoPancake + ":wechat",
		model.PaymentMethodWaffoPancake + ":googlepay",
		model.PaymentMethodWaffoPancake + ":applepay",
		model.PaymentMethodWaffoPancake + ":card":
		return true
	default:
		return false
	}
}

type waffoPancakeWalletRoute struct {
	ProviderCurrency      string
	ProviderMethod        service.WaffoPancakePaymentMethod
	IncludePaymentMethods []service.WaffoPancakePaymentMethod
	AllowCNYToUSDFallback bool
}

func resolveWaffoPancakeWalletRoute(displayCurrency, paymentMethod string) (waffoPancakeWalletRoute, error) {
	paymentMethod = strings.TrimSpace(paymentMethod)
	if strings.EqualFold(paymentMethod, model.PaymentMethodWaffoPancake) {
		paymentMethod = ""
	}
	if paymentMethod == "" {
		return waffoPancakeWalletRoute{ProviderCurrency: model.PaymentCurrencyUSD}, nil
	}
	providerMethod, err := waffoPancakeProviderPaymentMethod(paymentMethod)
	if err != nil {
		return waffoPancakeWalletRoute{}, err
	}
	providerCurrency := model.PaymentCurrencyUSD
	allowFallback := false
	if displayCurrency == model.PaymentCurrencyCNY && providerMethod == service.WaffoPancakePaymentMethodWeChat {
		providerCurrency = model.PaymentCurrencyCNY
		allowFallback = true
	}
	return waffoPancakeWalletRoute{
		ProviderCurrency:      providerCurrency,
		ProviderMethod:        providerMethod,
		IncludePaymentMethods: []service.WaffoPancakePaymentMethod{providerMethod},
		AllowCNYToUSDFallback: allowFallback,
	}, nil
}

func RequestWaffoPancakeAmount(c *gin.Context) {
	var req WaffoPancakePayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}

	if req.Amount < int64(setting.WaffoPancakeMinTopUp) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", setting.WaffoPancakeMinTopUp)})
		return
	}
	displayCurrency, err := normalizeWaffoPancakeWalletCurrency(req.Currency)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "钱包币种仅支持 CNY 或 USD"})
		return
	}
	route, err := resolveWaffoPancakeWalletRoute(displayCurrency, req.PaymentMethod)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 支付方式不受支持"})
		return
	}

	id := c.GetInt("id")
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}

	providerPayMoney := getWaffoPancakePaymentAmount(req.Amount, group, route.ProviderCurrency)
	if providerPayMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "success", "data": formatWaffoPancakeAmount(providerPayMoney)})
}

func getWaffoPancakeLocalPayMoney(amount int64, group string) float64 {
	return getPayMoney(amount, group)
}

func getWaffoPancakeExchangeRate() float64 {
	if setting.WaffoPancakeExchangeRate > 0 {
		return setting.WaffoPancakeExchangeRate
	}
	if operation_setting.Price > 0 {
		return operation_setting.Price
	}
	if operation_setting.USDExchangeRate > 0 {
		return operation_setting.USDExchangeRate
	}
	return 1
}

func getWaffoPancakePaymentAmount(amount int64, group, currency string) float64 {
	localCNY := decimal.NewFromFloat(getWaffoPancakeLocalPayMoney(amount, group))
	if currency == model.PaymentCurrencyCNY {
		return localCNY.InexactFloat64()
	}
	return localCNY.
		Div(decimal.NewFromFloat(getWaffoPancakeExchangeRate())).
		Mul(decimal.NewFromFloat(setting.WaffoPancakeUnitPrice)).
		InexactFloat64()
}

func getWaffoPancakeCNYAmount(amount int64, group string) float64 {
	return getWaffoPancakeLocalPayMoney(amount, group)
}

func normalizeWaffoPancakeTopUpAmount(amount int64) int64 {
	if operation_setting.GetQuotaDisplayType() != operation_setting.QuotaDisplayTypeTokens {
		return amount
	}

	normalized := decimal.NewFromInt(amount).
		Div(decimal.NewFromFloat(common.QuotaPerUnit)).
		IntPart()
	if normalized < 1 {
		return 1
	}
	return normalized
}

func formatWaffoPancakeAmount(payMoney float64) string {
	return decimal.NewFromFloat(payMoney).StringFixed(2)
}

func getWaffoPancakeBuyerEmail(user *model.User) string {
	if user != nil && strings.TrimSpace(user.Email) != "" {
		return user.Email
	}
	return ""
}

func legacyWaffoPancakeProviderAccountID() string {
	return strings.TrimSpace(setting.WaffoPancakeStoreID)
}

// The admin config endpoints below accept typed-but-not-yet-saved creds in
// the body and fall back to persisted creds when the body is blank (see
// resolveWaffoPancakeAdminCreds). Only SaveWaffoPancake writes to OptionMap.

type saveWaffoPancakeRequest struct {
	MerchantID string `json:"merchant_id"`
	PrivateKey string `json:"private_key"`
	ReturnURL  string `json:"return_url"`
	StoreID    string `json:"store_id"`
	ProductID  string `json:"product_id"`
}

type createWaffoPancakePairRequest struct {
	MerchantID string `json:"merchant_id"`
	PrivateKey string `json:"private_key"`
	ReturnURL  string `json:"return_url"`
}

// SaveWaffoPancake atomically persists all five operator-controlled fields.
// Catalog / pair endpoints are transient — only this one writes the OptionMap.
func SaveWaffoPancake(c *gin.Context) {
	var req saveWaffoPancakeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if err := service.SaveWaffoPancakeConfig(
		c.Request.Context(),
		req.MerchantID,
		req.PrivateKey,
		req.ReturnURL,
		req.StoreID,
		req.ProductID,
	); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 保存配置失败 store_id=%q product_id=%q error=%q",
			req.StoreID, req.ProductID, err.Error(),
		))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"product_id": setting.WaffoPancakeProductID,
			"store_id":   setting.WaffoPancakeStoreID,
		},
	})
}

// resolveWaffoPancakeAdminCreds prefers body creds (typed-but-not-yet-saved
// values, for verification) and falls back to persisted creds when the body
// is blank (so returning admins don't have to re-paste the private key,
// which is stripped from GET /api/option/).
func resolveWaffoPancakeAdminCreds(bodyMerchantID, bodyPrivateKey string) (string, string) {
	m := strings.TrimSpace(bodyMerchantID)
	k := strings.TrimSpace(bodyPrivateKey)
	if m == "" && k == "" {
		return setting.WaffoPancakeMerchantID, setting.WaffoPancakePrivateKey
	}
	return m, k
}

// CreateWaffoPancakePair mints a Store + OnetimeProduct pair in one round-
// trip. Surfaces an orphan-store flag when the product half fails so the
// frontend can preselect / retry without losing context.
func CreateWaffoPancakePair(c *gin.Context) {
	var req createWaffoPancakePairRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
			return
		}
	}
	merchantID, privateKey := resolveWaffoPancakeAdminCreds(req.MerchantID, req.PrivateKey)
	if merchantID == "" || privateKey == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 凭证未配置"})
		return
	}
	result, err := service.CreateWaffoPancakePrimaryPair(
		c.Request.Context(), merchantID, privateKey, req.ReturnURL,
	)
	if err != nil {
		orphan := result != nil && result.OrphanStore
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 创建店铺与产品失败 orphan_store=%t store_id=%q error=%q",
			orphan, func() string {
				if result == nil {
					return ""
				}
				return result.StoreID
			}(), err.Error(),
		))
		data := gin.H{"error": err.Error()}
		if orphan {
			data["store_id"] = result.StoreID
			data["store_name"] = result.StoreName
			data["orphan_store"] = true
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": data})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"store_id":     result.StoreID,
			"store_name":   result.StoreName,
			"product_id":   result.ProductID,
			"product_name": result.ProductName,
		},
	})
}

// ListWaffoPancakeCatalog returns the merchant's Stores + OnetimeProducts.
// Doubles as a credential probe (a successful 200 proves the resolved creds
// authenticate). See resolveWaffoPancakeAdminCreds for credential resolution.
func ListWaffoPancakeCatalog(c *gin.Context) {
	// Missing query creds mean "use persisted creds".
	merchantID, privateKey := resolveWaffoPancakeAdminCreds(
		strings.TrimSpace(c.Query("merchant_id")),
		strings.TrimSpace(c.Query("private_key")),
	)
	if merchantID == "" || privateKey == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 凭证未配置"})
		return
	}
	catalog, err := service.ListWaffoPancakeCatalog(c.Request.Context(), merchantID, privateKey)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 拉取店铺与产品目录失败 error=%q", err.Error(),
		))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉取目录失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": catalog})
}

type createWaffoPancakeSubscriptionProductRequest struct {
	Name   string `json:"name"`
	Amount string `json:"amount"`
}

// CreateWaffoPancakeSubscriptionProduct mints an OnetimeProduct (not
// SubscriptionProduct — see service.CreateWaffoPancakeProductForPlan)
// sized to a plan's `name` + `amount`, using persisted Pancake credentials
// + StoreID. Reads from the form, not the plan row, so newly-typed unsaved
// plans can mint a product too.
func CreateWaffoPancakeSubscriptionProduct(c *gin.Context) {
	var req createWaffoPancakeSubscriptionProductRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
			return
		}
	}
	if strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "套餐名称不能为空"})
		return
	}
	if strings.TrimSpace(req.Amount) == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "套餐价格不能为空"})
		return
	}
	merchantID, privateKey := resolveWaffoPancakeAdminCreds("", "")
	storeID := strings.TrimSpace(setting.WaffoPancakeStoreID)
	if merchantID == "" || privateKey == "" || storeID == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 未完成配置，请先在支付设置中完成网关绑定"})
		return
	}
	productID, err := service.CreateWaffoPancakeProductForPlan(
		c.Request.Context(),
		merchantID,
		privateKey,
		storeID,
		req.Name,
		req.Amount,
		setting.WaffoPancakeReturnURL,
	)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 创建套餐产品失败 store_id=%q name=%q amount=%q error=%q",
			storeID, req.Name, req.Amount, err.Error(),
		))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建套餐产品失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"product_id":   productID,
			"product_name": req.Name,
			"store_id":     storeID,
		},
	})
}

// ListWaffoPancakeSubscriptionProductOptions returns the OnetimeProducts
// in the saved Pancake store, for the subscription-plan dropdown. The name
// reflects new-api's plan concept; under the hood it's still OnetimeProducts.
func ListWaffoPancakeSubscriptionProductOptions(c *gin.Context) {
	merchantID, privateKey := resolveWaffoPancakeAdminCreds("", "")
	storeID := strings.TrimSpace(setting.WaffoPancakeStoreID)
	if merchantID == "" || privateKey == "" || storeID == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 未完成配置，请先在支付设置中完成网关绑定"})
		return
	}
	catalog, err := service.ListWaffoPancakeCatalog(c.Request.Context(), merchantID, privateKey)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 拉取订阅产品列表失败 store_id=%q error=%q", storeID, err.Error(),
		))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉取产品列表失败"})
		return
	}
	products := []service.WaffoPancakeCatalogProduct{}
	for _, store := range catalog.Stores {
		if store.ID == storeID {
			products = store.OnetimeProducts
			break
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"store_id": storeID,
			"products": products,
		},
	})
}

func getWaffoPancakeBuyerIdentity(user *model.User) string {
	if user == nil {
		return ""
	}
	return service.WaffoPancakeBuyerIdentityFromUserID(user.Id)
}

func RequestWaffoPancakePay(c *gin.Context) {
	if !service.IsHotPayGatewayEnabled() && !isWaffoPancakeTopUpEnabled() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 配置不完整"})
		return
	}

	var req WaffoPancakePayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if req.Amount < int64(setting.WaffoPancakeMinTopUp) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", setting.WaffoPancakeMinTopUp)})
		return
	}
	displayCurrency, err := normalizeWaffoPancakeWalletCurrency(req.Currency)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "钱包币种仅支持 CNY 或 USD"})
		return
	}
	route, err := resolveWaffoPancakeWalletRoute(displayCurrency, req.PaymentMethod)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 支付方式不受支持"})
		return
	}

	id := c.GetInt("id")
	user, err := model.GetUserById(id, false)
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "用户不存在"})
		return
	}

	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}

	providerCurrency := route.ProviderCurrency
	providerPayMoney := getWaffoPancakePaymentAmount(req.Amount, group, providerCurrency)
	if providerPayMoney < 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}

	gatewayEnabled := service.IsHotPayGatewayEnabled()
	canonicalMethod := ""
	amountMinor := int64(0)
	var gatewayFallback *service.HotPayGatewayCheckoutFallback
	if gatewayEnabled {
		if route.ProviderMethod != "" {
			canonicalMethod, err = hotPayWalletMethod(providerCurrency, string(route.ProviderMethod))
			if err != nil {
				c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 必须选择与币种匹配的支付方式"})
				return
			}
		}
		var amountErr error
		amountMinor, amountErr = hotPayMinorAmount(providerPayMoney)
		if amountErr != nil || validateHotPayAmountMinor(amountMinor) != nil {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额超出支付网关限额"})
			return
		}
		if route.AllowCNYToUSDFallback {
			fallbackPayMoney := getWaffoPancakePaymentAmount(req.Amount, group, model.PaymentCurrencyUSD)
			fallbackAmountMinor, fallbackErr := hotPayMinorAmount(fallbackPayMoney)
			if fallbackPayMoney < 0.01 || fallbackErr != nil || validateHotPayAmountMinor(fallbackAmountMinor) != nil {
				c.JSON(http.StatusOK, gin.H{"message": "error", "data": "USD 回退金额超出支付网关限额"})
				return
			}
			gatewayFallback = &service.HotPayGatewayCheckoutFallback{
				AmountMinor: fallbackAmountMinor, Currency: model.PaymentCurrencyUSD, PaymentMethod: "wechat_pay",
				PriceSnapshot: hotPayWaffoWalletPriceSnapshot(
					normalizeWaffoPancakeTopUpAmount(req.Amount), hotPayStringAmount(fallbackPayMoney), displayCurrency, model.PaymentCurrencyUSD,
				),
			}
		}
	}
	normalizedTopUpAmount := normalizeWaffoPancakeTopUpAmount(req.Amount)
	quotaAmount := int64(0)
	if gatewayEnabled {
		quotaAmount, err = hotPayQuotaAmount(normalizedTopUpAmount)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值额度超出系统上限"})
			return
		}
	}
	tradeNo := fmt.Sprintf("WAFFO_PANCAKE-%d-%d-%s", id, time.Now().UnixMilli(), randstr.String(6))
	idempotencyKey := ""
	if gatewayEnabled {
		idempotencyKey = strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if idempotencyKey != "" {
			tradeNo = hotPayMerchantOrderID("wallet", id, idempotencyKey)
		} else {
			idempotencyKey = hotPayIdempotencyKey(c, "wallet", tradeNo)
		}
	}
	paymentProvider := model.PaymentProviderWaffoPancake
	paymentMethod := model.PaymentMethodWaffoPancake
	if canonicalMethod != "" {
		paymentMethod = canonicalMethod
	} else if gatewayEnabled {
		paymentMethod = "auto"
	}
	providerAccountID := hotPayProviderAccountID()
	if !gatewayEnabled {
		providerAccountID = legacyWaffoPancakeProviderAccountID()
	}
	topUp := &model.TopUp{
		UserId:                   id,
		Amount:                   normalizedTopUpAmount,
		Money:                    providerPayMoney,
		TradeNo:                  tradeNo,
		PaymentMethod:            paymentMethod,
		PaymentProvider:          paymentProvider,
		PaymentProviderAccountID: providerAccountID,
		PaymentEnvironment:       hotPayEnvironment(),
		PaymentCurrency:          providerCurrency,
		CreateTime:               time.Now().Unix(),
		Status:                   common.TopUpStatusPending,
	}
	if existing := model.GetTopUpByTradeNo(tradeNo); existing != nil {
		primaryMatches := existing.PaymentCurrency == providerCurrency && existing.Money == providerPayMoney
		existingAmountMinor, existingAmountErr := hotPayMinorAmount(existing.Money)
		fallbackMatches := gatewayFallback != nil && existing.PaymentCurrency == gatewayFallback.Currency &&
			existingAmountErr == nil && existingAmountMinor == gatewayFallback.AmountMinor
		if existing.UserId != id || (!primaryMatches && !fallbackMatches) || existing.PaymentProvider != paymentProvider || existing.Amount != topUp.Amount || existing.PaymentMethod != paymentMethod || existing.PaymentProviderAccountID != providerAccountID || existing.PaymentEnvironment != hotPayEnvironment() {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付请求与已有订单不匹配"})
			return
		}
		topUp = existing
	} else if err := topUp.Insert(); err != nil {
		if existing := model.GetTopUpByTradeNo(tradeNo); existing != nil && existing.UserId == id {
			topUp = existing
		} else {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 创建充值订单失败 user_id=%d trade_no=%s amount=%d error=%q", id, tradeNo, req.Amount, err.Error()))
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
			return
		}
	}

	if gatewayEnabled {
		client, clientErr := hotPayGatewayClient()
		if clientErr != nil {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": hotPayGatewayErrorMessage(clientErr)})
			return
		}
		result, createErr := client.CreateOrder(c.Request.Context(), idempotencyKey, service.HotPayGatewayCreateOrderRequest{
			MerchantOrderID:   tradeNo,
			BusinessType:      "wallet_topup",
			UserID:            hotPayUserID(id),
			BuyerEmail:        getWaffoPancakeBuyerEmail(user),
			ProductID:         hotPayProviderProductID(),
			AmountMinor:       amountMinor,
			Currency:          providerCurrency,
			QuotaAmount:       quotaAmount,
			Provider:          paymentProvider,
			ProviderAccountID: hotPayProviderAccountID(),
			PaymentMethod:     canonicalMethod,
			Environment:       hotPayEnvironment(),
			ReturnURL:         hotPayReturnURL("/usage-logs"),
			PriceSnapshot: hotPayWaffoWalletPriceSnapshot(
				topUp.Amount,
				hotPayStringAmount(providerPayMoney),
				displayCurrency,
				providerCurrency,
			),
			ExpiresAt: hotPayExpiresAt(45 * 60), Description: "Wallet top-up", Fallback: gatewayFallback,
		})
		if createErr != nil {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("HotPay 钱包结账失败 user_id=%d trade_no=%s error=%q", id, tradeNo, createErr.Error()))
			if hotPayGatewayErrorIsPermanent(createErr) {
				topUp.Status = common.TopUpStatusFailed
				_ = topUp.Update()
			}
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": hotPayGatewayErrorMessage(createErr)})
			return
		}
		if bindErr := model.BindPaymentGatewayWalletOrder(tradeNo, model.PaymentGatewayWalletOrderSnapshot{
			OrderID: result.Order.ID, UserID: id, AmountMinor: result.Order.AmountMinor, Currency: result.Order.Currency,
			QuotaAmount: result.Order.QuotaAmount, Provider: result.Order.Provider, ProviderAccountID: result.Order.ProviderAccountID,
			Environment: result.Order.Environment, PaymentMethod: result.Order.PaymentMethod, PriceSnapshot: result.Order.PriceSnapshot,
		}); bindErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("HotPay 钱包订单绑定 canonical order 失败 user_id=%d trade_no=%s error=%q", id, tradeNo, bindErr.Error()))
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付订单状态保存失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "success", "data": hotPayCheckoutResponse(result), "url": result.Attempt.CheckoutURL})
		return
	}

	expiresInSeconds := 45 * 60
	checkoutParams := &service.WaffoPancakeCreateSessionParams{
		ProductID:     setting.WaffoPancakeProductID,
		Currency:      providerCurrency,
		BuyerIdentity: getWaffoPancakeBuyerIdentity(user),
		PriceSnapshot: &service.WaffoPancakePriceSnapshot{
			Amount:      formatWaffoPancakeAmount(providerPayMoney),
			TaxCategory: "saas",
		},
		BuyerEmail:              getWaffoPancakeBuyerEmail(user),
		ExpiresInSeconds:        &expiresInSeconds,
		OrderMerchantExternalID: tradeNo,
		IncludePaymentMethods:   route.IncludePaymentMethods,
	}
	session, err := service.CreateWaffoPancakeCheckoutSession(c.Request.Context(), checkoutParams)
	if err != nil && route.AllowCNYToUSDFallback && service.IsWaffoPancakeUnsupportedCurrencyError(err, model.PaymentCurrencyCNY) {
		providerCurrency = model.PaymentCurrencyUSD
		providerPayMoney = getWaffoPancakePaymentAmount(req.Amount, group, providerCurrency)
		if providerPayMoney < 0.01 {
			err = fmt.Errorf("USD fallback amount is too low")
		} else {
			topUp.Money = providerPayMoney
			topUp.PaymentCurrency = providerCurrency
			if updateErr := topUp.Update(); updateErr != nil {
				err = fmt.Errorf("persist USD fallback snapshot: %w", updateErr)
			} else {
				checkoutParams.Currency = providerCurrency
				checkoutParams.PriceSnapshot.Amount = formatWaffoPancakeAmount(providerPayMoney)
				session, err = service.CreateWaffoPancakeCheckoutSession(c.Request.Context(), checkoutParams)
				if err == nil {
					logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo Pancake CNY checkout unsupported; used USD fallback user_id=%d trade_no=%s", id, tradeNo))
				}
			}
		}
	}
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 创建结账会话失败 user_id=%d trade_no=%s error=%q", id, tradeNo, err.Error()))
		topUp.Status = common.TopUpStatusFailed
		_ = topUp.Update()
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake 充值订单创建成功 user_id=%d trade_no=%s session_id=%s amount=%d display_currency=%s provider_currency=%s provider_amount=%.2f", id, tradeNo, session.SessionID, req.Amount, displayCurrency, providerCurrency, providerPayMoney))

	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"checkout_url":     session.CheckoutURL,
			"session_id":       session.SessionID,
			"expires_at":       session.ExpiresAt,
			"order_id":         tradeNo,
			"token":            session.Token,
			"token_expires_at": session.TokenExpiresAt,
			"currency":         providerCurrency,
		},
	})
}

func WaffoPancakeWebhook(c *gin.Context) {
	legacyWebhookDrain := strings.EqualFold(strings.TrimSpace(os.Getenv("HOTPAY_GATEWAY_ALLOW_LEGACY_WEBHOOKS")), "true") || strings.TrimSpace(os.Getenv("HOTPAY_GATEWAY_ALLOW_LEGACY_WEBHOOKS")) == "1"
	if service.IsHotPayGatewayEnabled() && !legacyWebhookDrain {
		// New provider callbacks terminate at HotPay after cutover. Keeping the
		// old endpoint closed prevents a duplicate direct-SDK settlement path.
		c.String(http.StatusGone, "legacy webhook disabled")
		return
	}
	if !isWaffoPancakeWebhookEnabled() {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 被拒绝 reason=webhook_disabled path=%q client_ip=%s", c.Request.RequestURI, c.ClientIP()))
		c.String(http.StatusForbidden, "webhook disabled")
		return
	}

	// :env splits test vs prod traffic at the routing layer — operator
	// registers each URL in the matching webhook slot in Pancake's dashboard.
	// We then enforce event.mode == expectedEnv to catch mis-registrations.
	expectedEnv := strings.TrimSpace(c.Param("env"))
	if expectedEnv != "test" && expectedEnv != "prod" {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake webhook 路径环境段无效 env=%q path=%q client_ip=%s",
			expectedEnv, c.Request.RequestURI, c.ClientIP(),
		))
		c.String(http.StatusNotFound, "unknown env")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 读取请求体失败 path=%q client_ip=%s error=%q", c.Request.RequestURI, c.ClientIP(), err.Error()))
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.String(http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			c.String(http.StatusBadRequest, "bad request")
		}
		return
	}

	signature := c.GetHeader("X-Waffo-Signature")
	payloadSHA256 := fmt.Sprintf("%x", sha256.Sum256(bodyBytes))
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 收到请求 path=%q client_ip=%s payload_sha256=%s payload_bytes=%d", c.Request.RequestURI, c.ClientIP(), payloadSHA256, len(bodyBytes)))

	event, err := service.VerifyConfiguredWaffoPancakeWebhook(string(bodyBytes), signature)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 验签失败 path=%q client_ip=%s payload_sha256=%s payload_bytes=%d error=%q", c.Request.RequestURI, c.ClientIP(), payloadSHA256, len(bodyBytes), err.Error()))
		c.String(http.StatusUnauthorized, "invalid signature")
		return
	}

	if !strings.EqualFold(strings.TrimSpace(event.Mode), expectedEnv) {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake webhook 环境不匹配 expected=%q actual_mode=%q event_id=%s order_id=%s client_ip=%s",
			expectedEnv, event.Mode, event.ID, event.Data.OrderID, c.ClientIP(),
		))
		c.String(http.StatusConflict, "environment mismatch")
		return
	}
	if service.IsHotPayGatewayEnabled() && isHotPayBoundWaffoPancakeOrder(event.Data.OrderMerchantExternalID) {
		// A gateway-created order is identified by its durable local binding, not
		// by a merchant-order prefix. Orders created without an idempotency key
		// may use the legacy WAFFO_PANCAKE-* shape, so prefix checks can bypass
		// the signed gateway settlement receiver during legacy draining.
		c.String(http.StatusGone, "gateway order webhook moved")
		return
	}
	if err := validateLegacyWaffoPancakeAccount(event); err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo Pancake legacy webhook account mismatch event_id=%s order_id=%s client_ip=%s", event.ID, event.Data.OrderID, c.ClientIP()))
		c.String(http.StatusConflict, "account mismatch")
		return
	}
	merchantOrderID := strings.TrimSpace(event.Data.OrderMerchantExternalID)
	eventID := strings.TrimSpace(event.ID)
	if eventID == "" {
		eventID = strings.TrimSpace(event.EventID)
	}
	if eventID == "" {
		c.String(http.StatusBadRequest, "missing event id")
		return
	}
	duplicate, recordErr := model.RecordLegacyProviderEvent("waffo_pancake", expectedEnv, eventID, merchantOrderID, payloadSHA256, time.Now().Unix())
	if recordErr != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake legacy webhook event record failed event_id=%s order_id=%s client_ip=%s error=%q", eventID, event.Data.OrderID, c.ClientIP(), recordErr.Error()))
		c.String(http.StatusServiceUnavailable, "retry")
		return
	}
	if duplicate {
		c.String(http.StatusOK, "OK")
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 验签成功 event_type=%s event_id=%s order_id=%s client_ip=%s", event.NormalizedEventType(), event.ID, event.Data.OrderID, c.ClientIP()))
	if event.NormalizedEventType() != "order.completed" {
		_ = model.CompleteLegacyProviderEvent("waffo_pancake", expectedEnv, eventID)
		c.String(http.StatusOK, "OK")
		return
	}
	if handleWaffoPancakeCompletedEvent(c, event, bodyBytes) {
		_ = model.CompleteLegacyProviderEvent("waffo_pancake", expectedEnv, eventID)
		return
	}
	_ = model.FailLegacyProviderEvent("waffo_pancake", expectedEnv, eventID)
}

func validateLegacyWaffoPancakeAccount(event *service.WaffoPancakeWebhookEvent) error {
	if event == nil {
		return fmt.Errorf("event is nil")
	}
	expected := legacyWaffoPancakeProviderAccountID()
	actual := strings.TrimSpace(event.StoreID)
	if expected == "" || actual == "" || actual != expected {
		return fmt.Errorf("store mismatch")
	}
	tradeNo := strings.TrimSpace(event.Data.OrderMerchantExternalID)
	if topUp := model.GetTopUpByTradeNo(tradeNo); topUp != nil && !legacyWaffoPancakeOrderAccountMatches(topUp.PaymentProviderAccountID, actual) {
		return fmt.Errorf("top-up account mismatch")
	}
	if order := model.GetSubscriptionOrderByTradeNo(tradeNo); order != nil && !legacyWaffoPancakeOrderAccountMatches(order.PaymentProviderAccountID, actual) {
		return fmt.Errorf("subscription account mismatch")
	}
	return nil
}

func legacyWaffoPancakeOrderAccountMatches(snapshot, storeID string) bool {
	snapshot = strings.TrimSpace(snapshot)
	if snapshot == "" || snapshot == strings.TrimSpace(storeID) {
		return true
	}
	// A short-lived release incorrectly persisted MerchantID in this field.
	// Accept only that known historical value so paid pending orders can be
	// retried, while still requiring the signed event's StoreID above.
	return snapshot == strings.TrimSpace(setting.WaffoPancakeMerchantID)
}

func isHotPayBoundWaffoPancakeOrder(tradeNo string) bool {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return false
	}
	if topUp := model.GetTopUpByTradeNo(tradeNo); topUp != nil &&
		topUp.PaymentProvider == model.PaymentProviderWaffoPancake &&
		strings.TrimSpace(topUp.PaymentGatewayOrderID) != "" {
		return true
	}
	if order := model.GetSubscriptionOrderByTradeNo(tradeNo); order != nil &&
		order.PaymentProvider == model.PaymentProviderWaffoPancake &&
		strings.TrimSpace(order.PaymentGatewayOrderID) != "" {
		return true
	}
	return false
}

func handleWaffoPancakeCompletedEvent(c *gin.Context, event *service.WaffoPancakeWebhookEvent, bodyBytes []byte) bool {
	// Dispatch by trade_no prefix. OrderMerchantExternalID = our trade_no;
	// OrderID is Pancake's internal ORD_* (logs only).
	rawTradeNo := strings.TrimSpace(event.Data.OrderMerchantExternalID)
	isSubscription := strings.HasPrefix(rawTradeNo, "WAFFO_PANCAKE_SUB-")

	if isSubscription {
		tradeNo, err := service.ResolveWaffoPancakeSubscriptionTradeNo(event)
		if err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf(
				"Waffo Pancake webhook 订阅订单解析失败 event_id=%s order_id=%s client_ip=%s error=%q",
				event.ID, event.Data.OrderID, c.ClientIP(), err.Error(),
			))
			c.String(http.StatusServiceUnavailable, "retry")
			return false
		}
		LockOrder(tradeNo)
		defer UnlockOrder(tradeNo)
		if err := model.CompleteSubscriptionOrder(tradeNo, string(bodyBytes), model.PaymentProviderWaffoPancake, ""); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅完成失败 trade_no=%s event_id=%s order_id=%s client_ip=%s error=%q", tradeNo, event.ID, event.Data.OrderID, c.ClientIP(), err.Error()))
			c.String(http.StatusInternalServerError, "retry")
			return false
		}
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅完成 trade_no=%s event_id=%s order_id=%s client_ip=%s", tradeNo, event.ID, event.Data.OrderID, c.ClientIP()))
		c.String(http.StatusOK, "OK")
		return false
	}

	tradeNo, err := service.ResolveWaffoPancakeTradeNo(event)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake webhook 订单解析失败 event_id=%s order_id=%s client_ip=%s error=%q",
			event.ID, event.Data.OrderID, c.ClientIP(), err.Error(),
		))
		c.String(http.StatusServiceUnavailable, "retry")
		return false
	}

	LockOrder(tradeNo)
	defer UnlockOrder(tradeNo)

	if err := model.RechargeWaffoPancake(tradeNo); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 充值处理失败 trade_no=%s event_id=%s order_id=%s client_ip=%s error=%q", tradeNo, event.ID, event.Data.OrderID, c.ClientIP(), err.Error()))
		c.String(http.StatusInternalServerError, "retry")
		return false
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake 充值成功 trade_no=%s event_id=%s order_id=%s client_ip=%s", tradeNo, event.ID, event.Data.OrderID, c.ClientIP()))
	c.String(http.StatusOK, "OK")
	return true
}
