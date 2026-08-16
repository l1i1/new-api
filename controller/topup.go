package controller

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

func GetTopUpInfo(c *gin.Context) {
	complianceConfirmed := operation_setting.IsPaymentComplianceConfirmed()

	// 获取支付方式
	payMethods := operation_setting.PayMethods
	if !complianceConfirmed {
		payMethods = []map[string]string{}
	}

	// 如果启用了 Stripe 支付，添加到支付方法列表
	if isStripeTopUpEnabled() {
		// 检查是否已经包含 Stripe
		hasStripe := false
		for _, method := range payMethods {
			if method["type"] == "stripe" {
				hasStripe = true
				break
			}
		}

		if !hasStripe {
			stripeMethod := map[string]string{
				"name":      "Stripe",
				"type":      "stripe",
				"color":     "#635BFF",
				"min_topup": strconv.Itoa(setting.StripeMinTopUp),
			}
			payMethods = append(payMethods, stripeMethod)
		}
	}

	// After cutover the provider credentials live in HotPay. Keep the payment
	// option visible when only the gateway client is configured; otherwise the
	// New API UI silently hides the canonical checkout during credential drain.
	// The local credential check remains necessary for the legacy direct path.
	enableWaffoPancake := service.IsHotPayGatewayEnabled() || isWaffoPancakeTopUpEnabled()
	if enableWaffoPancake {
		hasWaffoPancake := false
		for _, method := range payMethods {
			if method["type"] == model.PaymentMethodWaffoPancake {
				hasWaffoPancake = true
				break
			}
		}

		if !hasWaffoPancake {
			payMethods = append(payMethods, map[string]string{
				"name":      "Waffo Pancake",
				"type":      model.PaymentMethodWaffoPancake,
				"color":     "#F97316",
				"min_topup": strconv.Itoa(setting.WaffoPancakeMinTopUp),
			})
		}
	}

	// 如果启用了 Waffo 支付，添加到支付方法列表
	enableWaffo := isWaffoTopUpEnabled()
	if enableWaffo {
		hasWaffo := false
		for _, method := range payMethods {
			if method["type"] == model.PaymentMethodWaffo {
				hasWaffo = true
				break
			}
		}

		if !hasWaffo {
			waffoMethod := map[string]string{
				"name":      "Waffo (Global Payment)",
				"type":      model.PaymentMethodWaffo,
				"color":     "#3B82F6",
				"min_topup": strconv.Itoa(setting.WaffoMinTopUp),
			}
			payMethods = append(payMethods, waffoMethod)
		}
	}

	data := gin.H{
		"enable_online_topup":              service.IsHotPayGatewayEnabled() || isEpayTopUpEnabled(),
		"enable_stripe_topup":              isStripeTopUpEnabled(),
		"enable_creem_topup":               isCreemTopUpEnabled(),
		"enable_waffo_topup":               enableWaffo,
		"enable_waffo_pancake_topup":       enableWaffoPancake,
		"enable_redemption":                complianceConfirmed,
		"payment_compliance_confirmed":     complianceConfirmed,
		"payment_compliance_terms_version": operation_setting.CurrentComplianceTermsVersion,
		"waffo_pay_methods": func() interface{} {
			if enableWaffo {
				return setting.GetWaffoPayMethods()
			}
			return nil
		}(),
		"creem_products":          setting.CreemProducts,
		"pay_methods":             payMethods,
		"min_topup":               operation_setting.MinTopUp,
		"stripe_min_topup":        setting.StripeMinTopUp,
		"waffo_min_topup":         setting.WaffoMinTopUp,
		"waffo_pancake_min_topup": setting.WaffoPancakeMinTopUp,
		"amount_options":          operation_setting.GetPaymentSetting().AmountOptions,
		"discount":                operation_setting.GetPaymentSetting().AmountDiscount,
		"topup_link":              common.TopUpLink,
	}
	common.ApiSuccess(c, data)
}

type EpayRequest struct {
	Amount        int64  `json:"amount"`
	PaymentMethod string `json:"payment_method"`
}

type AmountRequest struct {
	Amount int64 `json:"amount"`
}

func GetEpayClient() *epay.Client {
	payAddress := strings.TrimSpace(operation_setting.PayAddress)
	partnerID := strings.TrimSpace(operation_setting.EpayId)
	key := strings.TrimSpace(operation_setting.EpayKey)
	if service.IsHotPayGatewayEnabled() {
		// During cutover, callbacks are signed by HotPay. Keep this verifier
		// independent from the retired direct EPay credentials.
		if value := strings.TrimSpace(os.Getenv("HOTPAY_EPAY_PID")); value != "" {
			partnerID = value
		}
		if value := strings.TrimSpace(os.Getenv("HOTPAY_EPAY_KEY")); value != "" {
			key = value
		}
		if payAddress == "" {
			payAddress = strings.TrimRight(strings.TrimSpace(os.Getenv("HOTPAY_GATEWAY_URL")), "/")
		}
	}
	if payAddress == "" || partnerID == "" || key == "" {
		return nil
	}
	withUrl, err := epay.NewClient(&epay.Config{PartnerID: partnerID, Key: key}, payAddress)
	if err != nil {
		return nil
	}
	return withUrl
}

func getPayMoney(amount int64, group string) float64 {
	dAmount := decimal.NewFromInt(amount)
	// 充值金额以“展示类型”为准：
	// - USD/CNY: 前端传 amount 为金额单位；TOKENS: 前端传 tokens，需要换成 USD 金额
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		dQuotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
		dAmount = dAmount.Div(dQuotaPerUnit)
	}

	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}

	dTopupGroupRatio := decimal.NewFromFloat(topupGroupRatio)
	dPrice := decimal.NewFromFloat(operation_setting.Price)
	// apply optional preset discount by the original request amount (if configured), default 1.0
	discount := 1.0
	if ds, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(amount)]; ok {
		if ds > 0 {
			discount = ds
		}
	}
	dDiscount := decimal.NewFromFloat(discount)

	payMoney := dAmount.Mul(dPrice).Mul(dTopupGroupRatio).Mul(dDiscount)

	return payMoney.InexactFloat64()
}

func getMinTopup() int64 {
	minTopup := operation_setting.MinTopUp
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		dMinTopup := decimal.NewFromInt(int64(minTopup))
		dQuotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
		minTopup = int(dMinTopup.Mul(dQuotaPerUnit).IntPart())
	}
	return int64(minTopup)
}

func RequestEpay(c *gin.Context) {
	var req EpayRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if req.Amount < getMinTopup() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", getMinTopup())})
		return
	}

	id := c.GetInt("id")
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	payMoney := getPayMoney(req.Amount, group)
	if payMoney < 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}

	if !operation_setting.ContainsPayMethod(req.PaymentMethod) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付方式不存在"})
		return
	}

	if service.IsHotPayGatewayEnabled() {
		canonicalMethod, methodErr := hotPayWalletMethod(model.PaymentCurrencyCNY, req.PaymentMethod)
		if methodErr != nil {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "当前支付方式暂不支持 HotPay 网关"})
			return
		}
		amountMinor, amountErr := hotPayMinorAmount(payMoney)
		if amountErr != nil || validateHotPayAmountMinor(amountMinor) != nil {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额超出支付网关限额"})
			return
		}
		amount := req.Amount
		if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
			dAmount := decimal.NewFromInt(int64(amount))
			dQuotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
			amount = dAmount.Div(dQuotaPerUnit).IntPart()
		}
		quotaAmount, quotaErr := hotPayQuotaAmount(amount)
		if quotaErr != nil {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值额度超出系统上限"})
			return
		}
		tradeNo := fmt.Sprintf("USR%dNO%s", id, common.GetRandomString(6)+strconv.FormatInt(time.Now().Unix(), 10))
		idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if idempotencyKey != "" {
			tradeNo = hotPayMerchantOrderID("wallet", id, idempotencyKey)
		} else {
			idempotencyKey = hotPayIdempotencyKey(c, "wallet", tradeNo)
		}
		topUp := &model.TopUp{
			UserId:                   id,
			Amount:                   amount,
			Money:                    payMoney,
			TradeNo:                  tradeNo,
			PaymentMethod:            canonicalMethod,
			PaymentProvider:          model.PaymentProviderWaffoPancake,
			PaymentProviderAccountID: hotPayProviderAccountID(),
			PaymentEnvironment:       hotPayEnvironment(),
			PaymentCurrency:          model.PaymentCurrencyCNY,
			CreateTime:               time.Now().Unix(),
			Status:                   common.TopUpStatusPending,
		}
		if existing := model.GetTopUpByTradeNo(tradeNo); existing != nil {
			if existing.UserId != id || existing.PaymentProvider != model.PaymentProviderWaffoPancake || existing.PaymentCurrency != model.PaymentCurrencyCNY || existing.Amount != amount || existing.Money != payMoney || existing.PaymentMethod != canonicalMethod || existing.PaymentProviderAccountID != hotPayProviderAccountID() || existing.PaymentEnvironment != hotPayEnvironment() {
				c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付请求与已有订单不匹配"})
				return
			}
			topUp = existing
		} else if err := topUp.Insert(); err != nil {
			if existing := model.GetTopUpByTradeNo(tradeNo); existing != nil && existing.UserId == id {
				topUp = existing
			} else {
				c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
				return
			}
		}
		client, clientErr := hotPayGatewayClient()
		if clientErr != nil {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": hotPayGatewayErrorMessage(clientErr)})
			return
		}
		result, createErr := client.CreateOrder(c.Request.Context(), idempotencyKey, service.HotPayGatewayCreateOrderRequest{
			MerchantOrderID:       tradeNo,
			BusinessType:          "wallet_topup",
			UserID:                hotPayUserID(id),
			AmountMinor:           amountMinor,
			QuotaAmount:           quotaAmount,
			Currency:              model.PaymentCurrencyCNY,
			Provider:              model.PaymentProviderWaffoPancake,
			ProviderAccountID:     hotPayProviderAccountID(),
			PaymentMethod:         canonicalMethod,
			CompatibilityProtocol: "epay",
			Environment:           hotPayEnvironment(),
			MerchantNotifyURL:     hotPayReturnURL("/api/user/epay/notify"),
			ReturnURL:             hotPayReturnURL("/usage-logs"),
			PriceSnapshot: hotPayPriceSnapshot(map[string]any{
				"quota_amount":    topUp.Amount,
				"provider_amount": hotPayStringAmount(payMoney),
				"currency":        model.PaymentCurrencyCNY,
			}),
			ExpiresAt:   hotPayExpiresAt(45 * 60),
			Description: fmt.Sprintf("Wallet top-up: %d", req.Amount),
		})
		if createErr != nil {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("HotPay EPay 兼容钱包结账失败 user_id=%d trade_no=%s error=%q", id, tradeNo, createErr.Error()))
			if hotPayGatewayErrorIsPermanent(createErr) {
				topUp.Status = common.TopUpStatusFailed
				_ = topUp.Update()
			}
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": hotPayGatewayErrorMessage(createErr)})
			return
		}
		if bindErr := model.BindPaymentGatewayOrderID(model.PaymentGatewayBusinessWallet, tradeNo, result.Order.ID); bindErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("HotPay 钱包订单绑定 canonical order 失败 user_id=%d trade_no=%s error=%q", id, tradeNo, bindErr.Error()))
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付订单状态保存失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "success", "data": hotPayCheckoutResponse(result), "url": result.Attempt.CheckoutURL})
		return
	}

	callBackAddress := service.GetCallbackAddress()
	returnUrl, _ := url.Parse(paymentReturnPath("/usage-logs"))
	notifyUrl, _ := url.Parse(callBackAddress + "/api/user/epay/notify")
	tradeNo := fmt.Sprintf("%s%d", common.GetRandomString(6), time.Now().Unix())
	tradeNo = fmt.Sprintf("USR%dNO%s", id, tradeNo)
	client := GetEpayClient()
	if client == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "当前管理员未配置支付信息"})
		return
	}
	uri, params, err := client.Purchase(&epay.PurchaseArgs{
		Type:           req.PaymentMethod,
		ServiceTradeNo: tradeNo,
		Name:           fmt.Sprintf("TUC%d", req.Amount),
		Money:          strconv.FormatFloat(payMoney, 'f', 2, 64),
		Device:         epay.PC,
		NotifyUrl:      notifyUrl,
		ReturnUrl:      returnUrl,
	})
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("易支付 拉起支付失败 user_id=%d trade_no=%s payment_method=%s amount=%d error=%q", id, tradeNo, req.PaymentMethod, req.Amount, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	amount := req.Amount
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		dAmount := decimal.NewFromInt(int64(amount))
		dQuotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
		amount = dAmount.Div(dQuotaPerUnit).IntPart()
	}
	topUp := &model.TopUp{
		UserId:          id,
		Amount:          amount,
		Money:           payMoney,
		TradeNo:         tradeNo,
		PaymentMethod:   req.PaymentMethod,
		PaymentProvider: model.PaymentProviderEpay,
		PaymentCurrency: model.PaymentCurrencyCNY,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	err = topUp.Insert()
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("易支付 创建充值订单失败 user_id=%d trade_no=%s payment_method=%s amount=%d error=%q", id, tradeNo, req.PaymentMethod, req.Amount, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("易支付 充值订单创建成功 user_id=%d trade_no=%s payment_method=%s amount=%d money=%.2f uri=%q params=%q", id, tradeNo, req.PaymentMethod, req.Amount, payMoney, uri, common.GetJsonString(params)))
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": params, "url": uri})
}

// tradeNo lock
var orderLocks sync.Map
var createLock sync.Mutex

// refCountedMutex 带引用计数的互斥锁，确保最后一个使用者才从 map 中删除
type refCountedMutex struct {
	mu       sync.Mutex
	refCount int
}

// LockOrder 尝试对给定订单号加锁
func LockOrder(tradeNo string) {
	createLock.Lock()
	var rcm *refCountedMutex
	if v, ok := orderLocks.Load(tradeNo); ok {
		rcm = v.(*refCountedMutex)
	} else {
		rcm = &refCountedMutex{}
		orderLocks.Store(tradeNo, rcm)
	}
	rcm.refCount++
	createLock.Unlock()
	rcm.mu.Lock()
}

// UnlockOrder 释放给定订单号的锁
func UnlockOrder(tradeNo string) {
	v, ok := orderLocks.Load(tradeNo)
	if !ok {
		return
	}
	rcm := v.(*refCountedMutex)
	rcm.mu.Unlock()

	createLock.Lock()
	rcm.refCount--
	if rcm.refCount == 0 {
		orderLocks.Delete(tradeNo)
	}
	createLock.Unlock()
}

func EpayNotify(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	if !isEpayWebhookEnabled() && !service.IsHotPayGatewayEnabled() {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付 webhook 被拒绝 reason=webhook_disabled path=%q client_ip=%s", c.Request.RequestURI, c.ClientIP()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	var params map[string]string

	if c.Request.Method == "POST" {
		// POST 请求：从 POST body 解析参数
		if err := c.Request.ParseForm(); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("易支付 webhook POST 表单解析失败 path=%q client_ip=%s error=%q", c.Request.RequestURI, c.ClientIP(), err.Error()))
			_, _ = c.Writer.Write([]byte("fail"))
			return
		}
		params = lo.Reduce(lo.Keys(c.Request.PostForm), func(r map[string]string, t string, i int) map[string]string {
			r[t] = c.Request.PostForm.Get(t)
			return r
		}, map[string]string{})
	} else {
		// GET 请求：从 URL Query 解析参数
		params = lo.Reduce(lo.Keys(c.Request.URL.Query()), func(r map[string]string, t string, i int) map[string]string {
			r[t] = c.Request.URL.Query().Get(t)
			return r
		}, map[string]string{})
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("易支付 webhook 收到请求 path=%q client_ip=%s method=%s param_count=%d", c.Request.RequestURI, c.ClientIP(), c.Request.Method, len(params)))

	if len(params) == 0 {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付 webhook 参数为空 path=%q client_ip=%s", c.Request.RequestURI, c.ClientIP()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	client := GetEpayClient()
	if client == nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("易支付 client 未初始化 path=%q client_ip=%s", c.Request.RequestURI, c.ClientIP()))
		_, err := c.Writer.Write([]byte("fail"))
		if err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("易支付 webhook 响应写入失败 path=%q client_ip=%s error=%q", c.Request.RequestURI, c.ClientIP(), err.Error()))
		}
		return
	}
	verifyInfo, err := client.Verify(params)
	if err != nil || !verifyInfo.VerifyStatus {
		_, writeErr := c.Writer.Write([]byte("fail"))
		if writeErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("易支付 webhook 响应写入失败 path=%q client_ip=%s error=%q", c.Request.RequestURI, c.ClientIP(), writeErr.Error()))
		}
		if err != nil {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付 webhook 验签失败 path=%q client_ip=%s verify_error=%q", c.Request.RequestURI, c.ClientIP(), err.Error()))
		} else {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付 webhook 验签失败 path=%q client_ip=%s verify_status=false", c.Request.RequestURI, c.ClientIP()))
		}
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("易支付 webhook 验签成功 trade_no=%s callback_type=%s trade_status=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, verifyInfo.TradeStatus, c.ClientIP()))

	if verifyInfo.TradeStatus != epay.StatusTradeSuccess {
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("易支付 webhook 忽略事件 trade_no=%s callback_type=%s trade_status=%s client_ip=%s verify_info=%q", verifyInfo.ServiceTradeNo, verifyInfo.Type, verifyInfo.TradeStatus, c.ClientIP(), common.GetJsonString(verifyInfo)))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	if service.IsHotPayGatewayEnabled() {
		// HotPay has already committed the signed settlement into the local
		// order. The EPay-compatible callback is only an acknowledgement path
		// during migration and must never run the legacy EPay credit operation.
		if acknowledgeHotPayEpayNotification(c, verifyInfo.ServiceTradeNo, verifyInfo.TradeStatus) {
			return
		}
	}

	if err := model.CompleteEpayTopUp(verifyInfo.ServiceTradeNo, verifyInfo.Type, verifyInfo.Money, c.ClientIP()); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("易支付 充值结算失败 trade_no=%s callback_type=%s client_ip=%s error=%q", verifyInfo.ServiceTradeNo, verifyInfo.Type, c.ClientIP(), err.Error()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("易支付 充值结算成功 trade_no=%s callback_type=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, c.ClientIP()))
	if _, err := c.Writer.Write([]byte("success")); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("易支付 webhook 响应写入失败 trade_no=%s client_ip=%s error=%q", verifyInfo.ServiceTradeNo, c.ClientIP(), err.Error()))
	}
}

// acknowledgeHotPayEpayNotification is deliberately informational. HotPay
// settles the order through the signed internal settlement endpoint first;
// this compatibility callback may only acknowledge an already-committed
// gateway order and never performs a legacy credit operation.
func acknowledgeHotPayEpayNotification(c *gin.Context, tradeNo, tradeStatus string) bool {
	if !service.IsHotPayGatewayEnabled() || strings.TrimSpace(tradeStatus) != epay.StatusTradeSuccess {
		return false
	}
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return false
	}
	if topUp := model.GetTopUpByTradeNo(tradeNo); topUp != nil && topUp.PaymentProvider == model.PaymentProviderWaffoPancake && topUp.PaymentGatewayOrderID != "" && topUp.Status == common.TopUpStatusSuccess {
		_, _ = c.Writer.Write([]byte("success"))
		return true
	}
	if order := model.GetSubscriptionOrderByTradeNo(tradeNo); order != nil && order.PaymentProvider == model.PaymentProviderWaffoPancake && order.PaymentGatewayOrderID != "" && order.Status == common.TopUpStatusSuccess {
		_, _ = c.Writer.Write([]byte("success"))
		return true
	}
	return false
}

func RequestAmount(c *gin.Context) {
	var req AmountRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}

	if req.Amount < getMinTopup() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", getMinTopup())})
		return
	}
	id := c.GetInt("id")
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	payMoney := getPayMoney(req.Amount, group)
	if payMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": strconv.FormatFloat(payMoney, 'f', 2, 64)})
}

func GetUserTopUps(c *gin.Context) {
	userId := c.GetInt("id")
	pageInfo := common.GetPageQuery(c)
	keyword := c.Query("keyword")

	var (
		topups []*model.TopUp
		total  int64
		err    error
	)
	if keyword != "" {
		topups, total, err = model.SearchUserTopUps(userId, keyword, pageInfo)
	} else {
		topups, total, err = model.GetUserTopUps(userId, pageInfo)
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}

	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(topups)
	common.ApiSuccess(c, pageInfo)
}

// GetAllTopUps 管理员获取全平台充值记录
func GetAllTopUps(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	keyword := c.Query("keyword")

	var (
		topups []*model.TopUp
		total  int64
		err    error
	)
	if keyword != "" {
		topups, total, err = model.SearchAllTopUps(keyword, pageInfo)
	} else {
		topups, total, err = model.GetAllTopUps(pageInfo)
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}

	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(topups)
	common.ApiSuccess(c, pageInfo)
}

type AdminCompleteTopupRequest struct {
	TradeNo string `json:"trade_no"`
}

// AdminCompleteTopUp 管理员补单接口
func AdminCompleteTopUp(c *gin.Context) {
	var req AdminCompleteTopupRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.TradeNo == "" {
		common.ApiErrorMsg(c, "参数错误")
		return
	}

	topUp := model.GetTopUpByTradeNo(req.TradeNo)
	if topUp == nil {
		common.ApiErrorMsg(c, "充值订单不存在")
		return
	}
	if strings.TrimSpace(topUp.PaymentGatewayOrderID) != "" {
		common.ApiErrorMsg(c, "该订单由 HotPay 管理，请通过网关对账")
		return
	}
	if topUp.Status == common.TopUpStatusSuccess {
		common.ApiSuccess(c, nil)
		return
	}
	if topUp.Status != common.TopUpStatusPending {
		common.ApiErrorMsg(c, "订单状态不是待支付，无法补单")
		return
	}

	verifiedPayment, err := service.VerifyTopUpPayment(c.Request.Context(), topUp)
	if err != nil {
		state := service.PaymentVerificationStateOf(err)
		logger.LogWarn(c.Request.Context(), fmt.Sprintf(
			"管理员补单供应商核验失败 admin_id=%d trade_no=%s provider=%s state=%s client_ip=%s",
			c.GetInt("id"), topUp.TradeNo, topUp.PaymentProvider, state, c.ClientIP(),
		))
		common.ApiErrorMsg(c, paymentVerificationMessage(state))
		return
	}

	switch topUp.PaymentProvider {
	case model.PaymentProviderEpay:
		err = model.CompleteEpayTopUp(topUp.TradeNo, verifiedPayment.PaymentMethod, verifiedPayment.PaidAmount, c.ClientIP())
	case model.PaymentProviderWaffoPancake:
		err = model.RechargeWaffoPancake(topUp.TradeNo)
	default:
		err = fmt.Errorf("unsupported verified payment provider")
	}
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"管理员补单结算失败 admin_id=%d trade_no=%s provider=%s provider_trade_no=%s client_ip=%s error=%q",
			c.GetInt("id"), topUp.TradeNo, topUp.PaymentProvider, verifiedPayment.ProviderTradeNo, c.ClientIP(), err.Error(),
		))
		common.ApiErrorMsg(c, "供应商已确认付款，但本地结算失败，请稍后重试")
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf(
		"管理员补单核验并结算成功 admin_id=%d trade_no=%s provider=%s provider_trade_no=%s client_ip=%s",
		c.GetInt("id"), topUp.TradeNo, topUp.PaymentProvider, verifiedPayment.ProviderTradeNo, c.ClientIP(),
	))
	common.ApiSuccess(c, nil)
}

func paymentVerificationMessage(state service.PaymentVerificationState) string {
	switch state {
	case service.PaymentVerificationNotPaid:
		return "供应商订单尚未支付，不能补单"
	case service.PaymentVerificationNotFound:
		return "供应商未找到该订单，不能补单"
	case service.PaymentVerificationRefunded:
		return "供应商订单已退款，不能补单"
	case service.PaymentVerificationMismatch:
		return "供应商订单信息与本地订单不一致，不能补单"
	case service.PaymentVerificationAmbiguous:
		return "供应商返回多个成功支付记录，需人工对账"
	case service.PaymentVerificationUnsupported:
		return "该支付渠道暂不支持安全补单"
	default:
		return "暂时无法向供应商核验订单，请稍后重试"
	}
}
