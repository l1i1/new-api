package controller

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/thanhpk/randstr"
)

type SubscriptionWaffoPancakePayRequest struct {
	PlanId        int    `json:"plan_id"`
	PaymentMethod string `json:"payment_method"`
}

func SubscriptionRequestWaffoPancakePay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	var req SubscriptionWaffoPancakePayRequest
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
	if strings.TrimSpace(plan.WaffoPancakeProductId) == "" {
		common.ApiErrorMsg(c, "该套餐未配置 WaffoPancakeProductId")
		return
	}
	// Plan targets its own Pancake product, so we only require credentials
	// here — not the gateway-level WaffoPancakeProductID.
	if !service.IsHotPayGatewayEnabled() && (strings.TrimSpace(setting.WaffoPancakeMerchantID) == "" ||
		strings.TrimSpace(setting.WaffoPancakePrivateKey) == "") {
		common.ApiErrorMsg(c, "Waffo Pancake 未配置或密钥无效")
		return
	}

	userId := c.GetInt("id")
	user, err := model.GetUserById(userId, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if user == nil {
		common.ApiErrorMsg(c, "用户不存在")
		return
	}

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
	planCurrency := strings.ToUpper(strings.TrimSpace(plan.Currency))
	if planCurrency == "" {
		// Legacy plans without a currency snapshot used Pancake's historical USD
		// checkout contract.
		planCurrency = model.PaymentCurrencyUSD
	}
	gatewayEnabled := service.IsHotPayGatewayEnabled()
	canonicalMethod := ""
	amountMinor := int64(0)
	if gatewayEnabled {
		canonicalMethod, err = hotPaySubscriptionMethod(planCurrency, req.PaymentMethod)
		if err != nil {
			common.ApiErrorMsg(c, "Waffo Pancake 必须选择与套餐币种匹配的支付方式")
			return
		}
		var amountErr error
		amountMinor, amountErr = hotPayMinorAmount(plan.PriceAmount)
		if amountErr != nil || validateHotPayAmountMinor(amountMinor) != nil {
			common.ApiErrorMsg(c, "套餐金额超出支付网关限额")
			return
		}
	}

	// WAFFO_PANCAKE_SUB- prefix (vs. wallet's WAFFO_PANCAKE-) drives webhook
	// dispatch in WaffoPancakeWebhook.
	tradeNo := fmt.Sprintf("WAFFO_PANCAKE_SUB-%d-%d-%s", userId, time.Now().UnixMilli(), randstr.String(6))
	idempotencyKey := ""
	if gatewayEnabled {
		idempotencyKey = strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if idempotencyKey != "" {
			tradeNo = hotPayMerchantOrderID("subscription", userId, idempotencyKey)
		} else {
			idempotencyKey = hotPayIdempotencyKey(c, "subscription", tradeNo)
		}
	}
	paymentMethod := model.PaymentMethodWaffoPancake
	if canonicalMethod != "" {
		paymentMethod = canonicalMethod
	}
	providerAccountID := hotPayProviderAccountID()
	if !gatewayEnabled {
		providerAccountID = legacyWaffoPancakeProviderAccountID()
	}

	order := &model.SubscriptionOrder{
		UserId:                   userId,
		PlanId:                   plan.Id,
		Money:                    plan.PriceAmount,
		TradeNo:                  tradeNo,
		PaymentMethod:            paymentMethod,
		PaymentProvider:          model.PaymentProviderWaffoPancake,
		PaymentProviderAccountID: providerAccountID,
		PaymentEnvironment:       hotPayEnvironment(),
		PaymentCurrency:          planCurrency,
		CreateTime:               time.Now().Unix(),
		Status:                   common.TopUpStatusPending,
	}
	if existing := model.GetSubscriptionOrderByTradeNo(tradeNo); existing != nil {
		if existing.UserId != userId || existing.PlanId != plan.Id || existing.Money != plan.PriceAmount || existing.PaymentProvider != model.PaymentProviderWaffoPancake || existing.PaymentCurrency != planCurrency || existing.PaymentMethod != paymentMethod || existing.PaymentProviderAccountID != providerAccountID || existing.PaymentEnvironment != hotPayEnvironment() {
			common.ApiErrorMsg(c, "支付请求与已有订单不匹配")
			return
		}
		order = existing
	} else if err := order.Insert(); err != nil {
		if existing := model.GetSubscriptionOrderByTradeNo(tradeNo); existing != nil && existing.UserId == userId {
			order = existing
		} else {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅订单创建失败 user_id=%d plan_id=%d trade_no=%s error=%q", userId, plan.Id, tradeNo, err.Error()))
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
			return
		}
	}

	if gatewayEnabled {
		client, clientErr := hotPayGatewayClient()
		if clientErr != nil {
			common.ApiErrorMsg(c, hotPayGatewayErrorMessage(clientErr))
			return
		}
		result, createErr := client.CreateOrder(c.Request.Context(), idempotencyKey, service.HotPayGatewayCreateOrderRequest{
			MerchantOrderID:   tradeNo,
			BusinessType:      "subscription",
			UserID:            hotPayUserID(userId),
			BuyerEmail:        getWaffoPancakeBuyerEmail(user),
			ProductID:         strings.TrimSpace(plan.WaffoPancakeProductId),
			AmountMinor:       amountMinor,
			Currency:          planCurrency,
			Provider:          model.PaymentProviderWaffoPancake,
			ProviderAccountID: hotPayProviderAccountID(),
			PaymentMethod:     canonicalMethod,
			Environment:       hotPayEnvironment(),
			ReturnURL:         hotPayReturnURL("/api/subscription/epay/return"),
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
		if bindErr := model.BindPaymentGatewayOrderID(model.PaymentGatewayBusinessSubscription, tradeNo, result.Order.ID); bindErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("HotPay 订阅订单绑定 canonical order 失败 user_id=%d plan_id=%d trade_no=%s error=%q", userId, plan.Id, tradeNo, bindErr.Error()))
			common.ApiErrorMsg(c, "支付订单状态保存失败")
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "success", "data": hotPayCheckoutResponse(result), "url": result.Attempt.CheckoutURL})
		return
	}

	expiresInSeconds := 45 * 60
	session, err := service.CreateWaffoPancakeCheckoutSession(c.Request.Context(), &service.WaffoPancakeCreateSessionParams{
		ProductID:     plan.WaffoPancakeProductId,
		Currency:      planCurrency,
		BuyerIdentity: service.WaffoPancakeBuyerIdentityFromUserID(user.Id),
		PriceSnapshot: &service.WaffoPancakePriceSnapshot{
			Amount:      decimal.NewFromFloat(plan.PriceAmount).StringFixed(2),
			TaxCategory: "saas",
		},
		BuyerEmail:              getWaffoPancakeBuyerEmail(user),
		ExpiresInSeconds:        &expiresInSeconds,
		OrderMerchantExternalID: tradeNo,
	})
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅结账会话创建失败 user_id=%d plan_id=%d trade_no=%s error=%q", userId, plan.Id, tradeNo, err.Error()))
		order.Status = common.TopUpStatusFailed
		_ = order.Update()
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅订单创建成功 user_id=%d plan_id=%d trade_no=%s session_id=%s money=%.2f", userId, plan.Id, tradeNo, session.SessionID, plan.PriceAmount))

	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"checkout_url":     session.CheckoutURL,
			"session_id":       session.SessionID,
			"expires_at":       session.ExpiresAt,
			"order_id":         tradeNo,
			"token":            session.Token,
			"token_expires_at": session.TokenExpiresAt,
		},
	})
}
