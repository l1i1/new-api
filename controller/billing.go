package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

// quotaLine resolves the (remaining, used) quota pair that the OpenAI-compatible
// billing endpoints describe.
//
// A token that carries its own quota line answers for itself. A token with
// unlimited_quota has no balance of its own - it draws on the account - so the
// account's line is the only truthful answer there. The fixed 100000000
// placeholder that used to stand in for it made every client that renders a
// balance show a fake number, and made the two endpoints describe different
// accounts so "remaining = hard_limit_usd - total_usage/100" could not work
// (reported by the partner 2026-10-09).
//
// Both endpoints must use this same helper: a limit from one account and usage
// from another produce a wrong remainder.
func quotaLine(c *gin.Context) (remain int, used int, token *model.Token, err error) {
	if common.DisplayTokenStatEnabled {
		tokenId := c.GetInt("token_id")
		token, err = model.GetTokenById(tokenId)
		if err != nil {
			return 0, 0, nil, err
		}
		if !token.UnlimitedQuota {
			return token.RemainQuota, token.UsedQuota, token, nil
		}
	}
	userId := c.GetInt("id")
	remain, err = model.GetUserQuota(userId, false)
	if err != nil {
		return 0, 0, token, err
	}
	used, err = model.GetUserUsedQuota(userId)
	return remain, used, token, err
}

// quotaAmount converts a quota value into the site's display unit, which is
// what the OpenAI-compatible *_USD fields carry here:
//   - USD: divide by QuotaPerUnit
//   - CNY: convert to USD first, then apply the exchange rate
//   - TOKENS: keep the raw token count
func quotaAmount(quota int) float64 {
	amount := float64(quota)
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		amount = amount / common.QuotaPerUnit * operation_setting.USDExchangeRate
	case operation_setting.QuotaDisplayTypeTokens:
		// amount 保持 tokens 数值
	default:
		amount = amount / common.QuotaPerUnit
	}
	return amount
}

func GetSubscription(c *gin.Context) {
	remainQuota, usedQuota, token, err := quotaLine(c)
	expiredTime := int64(0)
	if token != nil {
		expiredTime = token.ExpiredTime
	}
	if expiredTime <= 0 {
		expiredTime = 0
	}
	if err != nil {
		openAIError := types.OpenAIError{
			Message: err.Error(),
			Type:    "upstream_error",
		}
		c.JSON(200, gin.H{
			"error": openAIError,
		})
		return
	}
	// OpenAI clients render the remaining budget as
	// hard_limit_usd - total_usage/100, so the limit published here is the
	// account's *total* quota line (remaining + used) and GetUsage reports the
	// used half of that same line.
	amount := quotaAmount(remainQuota + usedQuota)
	subscription := OpenAISubscriptionResponse{
		Object:             "billing_subscription",
		HasPaymentMethod:   true,
		SoftLimitUSD:       amount,
		HardLimitUSD:       amount,
		SystemHardLimitUSD: amount,
		AccessUntil:        expiredTime,
	}
	c.JSON(200, subscription)
	return
}

func GetUsage(c *gin.Context) {
	_, usedQuota, _, err := quotaLine(c)
	if err != nil {
		openAIError := types.OpenAIError{
			Message: err.Error(),
			Type:    "new_api_error",
		}
		c.JSON(200, gin.H{
			"error": openAIError,
		})
		return
	}
	// OpenAI reports this figure in cents; the x100 is what keeps the subtract
	// above in the same unit as hard_limit_usd.
	usage := OpenAIUsageResponse{
		Object:     "list",
		TotalUsage: quotaAmount(usedQuota) * 100,
	}
	c.JSON(200, usage)
	return
}
