package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// RequestHotPayPay creates a canonical HotPay checkout for a registered
// "hotpay:<method>" payment entry. It mirrors the wallet flow of the legacy
// EPay path while settlement is delegated to the HotPay gateway.
func RequestHotPayPay(c *gin.Context) {
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
	if rejectInvalidTopUpQuota(c, id, req.Amount) {
		return
	}
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

	if !service.IsHotPayGatewayEnabled() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "HotPay 网关未启用"})
		return
	}
	hotPayMethod := hotPayMethodFromType(req.PaymentMethod)
	if hotPayMethod == "" || !isRegisteredHotPayMethodType(req.PaymentMethod) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付方式不存在"})
		return
	}
	canonicalMethod, methodErr := hotPayWalletMethod(model.PaymentCurrencyCNY, hotPayMethod)
	if methodErr != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "当前支付方式暂不支持 HotPay 网关"})
		return
	}
	paymentProvider := hotPayProviderForMethod(canonicalMethod)
	providerAccountID := hotPayProviderAccountIDForMethod(canonicalMethod)
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
		PaymentProvider:          paymentProvider,
		PaymentProviderAccountID: providerAccountID,
		PaymentEnvironment:       hotPayEnvironment(),
		PaymentCurrency:          model.PaymentCurrencyCNY,
		CreateTime:               time.Now().Unix(),
		Status:                   common.TopUpStatusPending,
	}
	if existing := model.GetTopUpByTradeNo(tradeNo); existing != nil {
		if existing.UserId != id || existing.PaymentProvider != paymentProvider || existing.PaymentCurrency != model.PaymentCurrencyCNY || existing.Amount != amount || existing.Money != payMoney || existing.PaymentMethod != canonicalMethod || (providerAccountID != "" && existing.PaymentProviderAccountID != providerAccountID) || existing.PaymentEnvironment != hotPayEnvironment() {
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
		MerchantOrderID:   tradeNo,
		BusinessType:      "wallet_topup",
		UserID:            hotPayUserID(id),
		AmountMinor:       amountMinor,
		QuotaAmount:       quotaAmount,
		Currency:          model.PaymentCurrencyCNY,
		Provider:          paymentProvider,
		ProviderAccountID: providerAccountID,
		PaymentMethod:     canonicalMethod,
		// HotPay only emits the EPay-shaped informational notify for orders
		// marked with the epay compatibility protocol; idempotent replays
		// compare this field verbatim.
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
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("HotPay 钱包结账失败 user_id=%d trade_no=%s error=%q", id, tradeNo, createErr.Error()))
		if hotPayGatewayErrorIsPermanent(createErr) {
			topUp.Status = common.TopUpStatusFailed
			_ = topUp.Update()
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": hotPayGatewayErrorMessage(createErr)})
		return
	}
	if bindErr := model.BindPaymentGatewayOrderID(model.PaymentGatewayBusinessWallet, tradeNo, result.Order.ID, result.Order.ProviderAccountID); bindErr != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("HotPay 钱包订单绑定 canonical order 失败 user_id=%d trade_no=%s error=%q", id, tradeNo, bindErr.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付订单状态保存失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": hotPayCheckoutResponse(result), "url": result.Attempt.CheckoutURL})
}

// SubscriptionRequestHotPayPay creates a canonical HotPay checkout for a
// subscription purchase through a registered "hotpay:<method>" entry.
func SubscriptionRequestHotPayPay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	var req SubscriptionEpayPayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !plan.Enabled {
		common.ApiErrorMsg(c, "套餐未启用")
		return
	}
	if plan.PriceAmount < 0.01 {
		common.ApiErrorMsg(c, "套餐金额过低")
		return
	}

	userId := c.GetInt("id")
	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			common.ApiErrorMsg(c, "已达到该套餐购买上限")
			return
		}
	}

	if !service.IsHotPayGatewayEnabled() {
		common.ApiErrorMsg(c, "HotPay 网关未启用")
		return
	}
	hotPayMethod := hotPayMethodFromType(req.PaymentMethod)
	if hotPayMethod == "" || !isRegisteredHotPayMethodType(req.PaymentMethod) {
		common.ApiErrorMsg(c, "支付方式不存在")
		return
	}
	planCurrency := strings.ToUpper(strings.TrimSpace(plan.Currency))
	if planCurrency == "" {
		planCurrency = model.PaymentCurrencyUSD
	}
	canonicalMethod, methodErr := hotPaySubscriptionMethod(planCurrency, hotPayMethod)
	if methodErr != nil {
		common.ApiErrorMsg(c, "当前支付方式或套餐币种暂不支持 HotPay 网关")
		return
	}
	paymentProvider := hotPayProviderForMethod(canonicalMethod)
	providerAccountID := hotPayProviderAccountIDForMethod(canonicalMethod)
	if strings.TrimSpace(plan.WaffoPancakeProductId) == "" {
		common.ApiErrorMsg(c, "该套餐未配置 HotPay 商品")
		return
	}
	amountMinor, amountErr := hotPayMinorAmount(plan.PriceAmount)
	if amountErr != nil || validateHotPayAmountMinor(amountMinor) != nil {
		common.ApiErrorMsg(c, "套餐金额超出支付网关限额")
		return
	}
	tradeNo := fmt.Sprintf("SUBUSR%dNO%s", userId, common.GetRandomString(6)+strconv.FormatInt(time.Now().Unix(), 10))
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotencyKey != "" {
		tradeNo = hotPayMerchantOrderID("subscription", userId, idempotencyKey)
	} else {
		idempotencyKey = hotPayIdempotencyKey(c, "subscription", tradeNo)
	}
	order := &model.SubscriptionOrder{
		UserId:                   userId,
		PlanId:                   plan.Id,
		Money:                    plan.PriceAmount,
		TradeNo:                  tradeNo,
		PaymentMethod:            canonicalMethod,
		PaymentProvider:          paymentProvider,
		PaymentProviderAccountID: providerAccountID,
		PaymentEnvironment:       hotPayEnvironment(),
		PaymentCurrency:          planCurrency,
		CreateTime:               time.Now().Unix(),
		Status:                   common.TopUpStatusPending,
	}
	if existing := model.GetSubscriptionOrderByTradeNo(tradeNo); existing != nil {
		if existing.UserId != userId || existing.PlanId != plan.Id || existing.Money != plan.PriceAmount || existing.PaymentProvider != paymentProvider || existing.PaymentCurrency != planCurrency || existing.PaymentMethod != canonicalMethod || (providerAccountID != "" && existing.PaymentProviderAccountID != providerAccountID) || existing.PaymentEnvironment != hotPayEnvironment() {
			common.ApiErrorMsg(c, "支付请求与已有订单不匹配")
			return
		}
		order = existing
	} else if err := order.Insert(); err != nil {
		if existing := model.GetSubscriptionOrderByTradeNo(tradeNo); existing != nil && existing.UserId == userId {
			order = existing
		} else {
			common.ApiErrorMsg(c, "创建订单失败")
			return
		}
	}
	client, clientErr := hotPayGatewayClient()
	if clientErr != nil {
		common.ApiErrorMsg(c, hotPayGatewayErrorMessage(clientErr))
		return
	}
	result, createErr := client.CreateOrder(c.Request.Context(), idempotencyKey, service.HotPayGatewayCreateOrderRequest{
		MerchantOrderID:       tradeNo,
		BusinessType:          "subscription",
		UserID:                hotPayUserID(userId),
		ProductID:             strings.TrimSpace(plan.WaffoPancakeProductId),
		AmountMinor:           amountMinor,
		Currency:              planCurrency,
		Provider:              paymentProvider,
		ProviderAccountID:     providerAccountID,
		PaymentMethod:         canonicalMethod,
		CompatibilityProtocol: "epay",
		Environment:           hotPayEnvironment(),
		MerchantNotifyURL:     hotPayReturnURL("/api/subscription/epay/notify"),
		ReturnURL:             hotPayReturnURL("/api/subscription/epay/return"),
		PriceSnapshot: hotPayPriceSnapshot(map[string]any{
			"plan_id":      plan.Id,
			"plan_title":   plan.Title,
			"price_amount": hotPayStringAmount(plan.PriceAmount),
			"currency":     planCurrency,
		}),
		ExpiresAt:   hotPayExpiresAt(45 * 60),
		Description: "Subscription: " + plan.Title,
	})
	if createErr != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("HotPay 订阅结账失败 user_id=%d plan_id=%d trade_no=%s error=%q", userId, plan.Id, tradeNo, createErr.Error()))
		if hotPayGatewayErrorIsPermanent(createErr) {
			order.Status = common.TopUpStatusFailed
			_ = order.Update()
		}
		common.ApiErrorMsg(c, hotPayGatewayErrorMessage(createErr))
		return
	}
	if bindErr := model.BindPaymentGatewayOrderID(model.PaymentGatewayBusinessSubscription, tradeNo, result.Order.ID, result.Order.ProviderAccountID); bindErr != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("HotPay 订阅订单绑定 canonical order 失败 user_id=%d plan_id=%d trade_no=%s error=%q", userId, plan.Id, tradeNo, bindErr.Error()))
		common.ApiErrorMsg(c, "支付订单状态保存失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": hotPayCheckoutResponse(result), "url": result.Attempt.CheckoutURL})
}
