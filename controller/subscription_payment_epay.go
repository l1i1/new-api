package controller

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

type SubscriptionEpayPayRequest struct {
	PlanId        int    `json:"plan_id"`
	PaymentMethod string `json:"payment_method"`
}

func SubscriptionRequestEpay(c *gin.Context) {
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
	if !operation_setting.ContainsPayMethod(req.PaymentMethod) {
		common.ApiErrorMsg(c, "支付方式不存在")
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

	if service.IsHotPayGatewayEnabled() {
		planCurrency := strings.ToUpper(strings.TrimSpace(plan.Currency))
		if planCurrency == "" {
			planCurrency = model.PaymentCurrencyUSD
		}
		canonicalMethod, methodErr := hotPaySubscriptionMethod(planCurrency, req.PaymentMethod)
		if methodErr != nil {
			common.ApiErrorMsg(c, "当前支付方式或套餐币种暂不支持 HotPay 网关")
			return
		}
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
			PaymentProvider:          model.PaymentProviderWaffoPancake,
			PaymentProviderAccountID: hotPayProviderAccountID(),
			PaymentEnvironment:       hotPayEnvironment(),
			PaymentCurrency:          planCurrency,
			CreateTime:               time.Now().Unix(),
			Status:                   common.TopUpStatusPending,
		}
		if existing := model.GetSubscriptionOrderByTradeNo(tradeNo); existing != nil {
			if existing.UserId != userId || existing.PlanId != plan.Id || existing.Money != plan.PriceAmount || existing.PaymentProvider != model.PaymentProviderWaffoPancake || existing.PaymentCurrency != planCurrency || existing.PaymentMethod != canonicalMethod || existing.PaymentProviderAccountID != hotPayProviderAccountID() || existing.PaymentEnvironment != hotPayEnvironment() {
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
			Provider:              model.PaymentProviderWaffoPancake,
			ProviderAccountID:     hotPayProviderAccountID(),
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
			if hotPayGatewayErrorIsPermanent(createErr) {
				order.Status = common.TopUpStatusFailed
				_ = order.Update()
			}
			common.ApiErrorMsg(c, hotPayGatewayErrorMessage(createErr))
			return
		}
		if bindErr := model.BindPaymentGatewayOrderID(model.PaymentGatewayBusinessSubscription, tradeNo, result.Order.ID); bindErr != nil {
			common.ApiErrorMsg(c, "支付订单状态保存失败")
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "success", "data": hotPayCheckoutResponse(result), "url": result.Attempt.CheckoutURL})
		return
	}

	callBackAddress := service.GetCallbackAddress()
	returnUrl, err := url.Parse(callBackAddress + "/api/subscription/epay/return")
	if err != nil {
		common.ApiErrorMsg(c, "回调地址配置错误")
		return
	}
	notifyUrl, err := url.Parse(callBackAddress + "/api/subscription/epay/notify")
	if err != nil {
		common.ApiErrorMsg(c, "回调地址配置错误")
		return
	}

	tradeNo := fmt.Sprintf("%s%d", common.GetRandomString(6), time.Now().Unix())
	tradeNo = fmt.Sprintf("SUBUSR%dNO%s", userId, tradeNo)

	client := GetEpayClient()
	if client == nil {
		common.ApiErrorMsg(c, "当前管理员未配置支付信息")
		return
	}

	order := &model.SubscriptionOrder{
		UserId:          userId,
		PlanId:          plan.Id,
		Money:           plan.PriceAmount,
		TradeNo:         tradeNo,
		PaymentMethod:   req.PaymentMethod,
		PaymentProvider: model.PaymentProviderEpay,
		PaymentCurrency: plan.Currency,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	if err := order.Insert(); err != nil {
		common.ApiErrorMsg(c, "创建订单失败")
		return
	}
	uri, params, err := client.Purchase(&epay.PurchaseArgs{
		Type:           req.PaymentMethod,
		ServiceTradeNo: tradeNo,
		Name:           fmt.Sprintf("SUB:%s", plan.Title),
		Money:          strconv.FormatFloat(plan.PriceAmount, 'f', 2, 64),
		Device:         epay.PC,
		NotifyUrl:      notifyUrl,
		ReturnUrl:      returnUrl,
	})
	if err != nil {
		_ = model.ExpireSubscriptionOrder(tradeNo, model.PaymentProviderEpay)
		common.ApiErrorMsg(c, "拉起支付失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": params, "url": uri})
}

func SubscriptionEpayNotify(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var params map[string]string

	if c.Request.Method == "POST" {
		// POST 请求：从 POST body 解析参数
		if err := c.Request.ParseForm(); err != nil {
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

	if len(params) == 0 {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	client := GetEpayClient()
	if client == nil {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	verifyInfo, err := client.Verify(params)
	if err != nil || !verifyInfo.VerifyStatus {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	if verifyInfo.TradeStatus != epay.StatusTradeSuccess {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	if service.IsHotPayGatewayEnabled() {
		if acknowledgeHotPayEpayNotification(c, verifyInfo.ServiceTradeNo, verifyInfo.TradeStatus) {
			return
		}
	}

	if err := model.CompleteEpaySubscriptionOrder(verifyInfo.ServiceTradeNo, common.GetJsonString(verifyInfo), verifyInfo.Type, verifyInfo.Money); err != nil {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	_, _ = c.Writer.Write([]byte("success"))
}

// SubscriptionEpayReturn handles browser return after payment.
// It verifies the payload and completes the order, then redirects to console.
func SubscriptionEpayReturn(c *gin.Context) {
	var params map[string]string

	if c.Request.Method == "POST" {
		// POST 请求：从 POST body 解析参数
		if err := c.Request.ParseForm(); err != nil {
			c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
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

	if len(params) == 0 {
		c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
		return
	}
	if service.IsHotPayGatewayEnabled() {
		tradeNo := strings.TrimSpace(params["out_trade_no"])
		if order := model.GetSubscriptionOrderByTradeNo(tradeNo); order != nil {
			if order.Status == common.TopUpStatusSuccess {
				c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=success"))
			} else {
				c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=pending"))
			}
			return
		}
		c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=pending"))
		return
	}

	client := GetEpayClient()
	if client == nil {
		c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
		return
	}
	verifyInfo, err := client.Verify(params)
	if err != nil || !verifyInfo.VerifyStatus {
		c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
		return
	}
	if verifyInfo.TradeStatus == epay.StatusTradeSuccess {
		if err := model.CompleteEpaySubscriptionOrder(verifyInfo.ServiceTradeNo, common.GetJsonString(verifyInfo), verifyInfo.Type, verifyInfo.Money); err != nil {
			c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
			return
		}
		c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=success"))
		return
	}
	c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=pending"))
}
