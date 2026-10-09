package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The partner-facing balance contract: /v1/dashboard/billing/subscription must
// describe the same account as /v1/dashboard/billing/usage, so that
//
//	remaining = hard_limit_usd - total_usage/100
//
// equals the real remaining quota. An unlimited token has no quota line of its
// own, so both endpoints have to answer with the account's line instead of the
// old fixed 100000000 placeholder.
func TestBillingEndpointsReportTheAccountForUnlimitedTokens(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))

	previousDisplay := operation_setting.GetGeneralSetting().QuotaDisplayType
	previousUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"general_setting.quota_display_type": operation_setting.QuotaDisplayTypeUSD,
	}))
	t.Cleanup(func() {
		common.QuotaPerUnit = previousUnit
		operation_setting.GetGeneralSetting().QuotaDisplayType = previousDisplay
	})

	// 20 USD left in the wallet, 10 USD already spent.
	user := model.User{Username: "balance-partner", Quota: 10_000_000, UsedQuota: 5_000_000, AffCode: "balance-aff"}
	require.NoError(t, db.Create(&user).Error)

	unlimited := model.Token{UserId: user.Id, Key: "balance-unlimited", UnlimitedQuota: true, RemainQuota: -1_234, UsedQuota: 7_654_321}
	limited := model.Token{UserId: user.Id, Key: "balance-limited", UnlimitedQuota: false, RemainQuota: 2_000_000, UsedQuota: 1_000_000}
	require.NoError(t, db.Create(&unlimited).Error)
	require.NoError(t, db.Create(&limited).Error)

	type subscription struct {
		SoftLimitUSD       float64 `json:"soft_limit_usd"`
		HardLimitUSD       float64 `json:"hard_limit_usd"`
		SystemHardLimitUSD float64 `json:"system_hard_limit_usd"`
		Object             string  `json:"object"`
		Currency           string  `json:"currency"`
	}
	type usage struct {
		Object     string  `json:"object"`
		TotalUsage float64 `json:"total_usage"`
		Currency   string  `json:"currency"`
	}

	call := func(handler gin.HandlerFunc, token model.Token) (subscription, usage) {
		t.Helper()
		var sub subscription
		var use usage
		for _, target := range []struct {
			handler gin.HandlerFunc
			out     any
		}{{GetSubscription, &sub}, {GetUsage, &use}} {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/dashboard/billing/subscription", nil)
			ctx.Set("id", token.UserId)
			ctx.Set("token_id", token.Id)
			target.handler(ctx)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), target.out), recorder.Body.String())
		}
		return sub, use
	}

	t.Run("unlimited token answers with the account balance", func(t *testing.T) {
		sub, use := call(GetSubscription, unlimited)
		assert.Equal(t, "billing_subscription", sub.Object)
		// (remaining 20 + used 10) USD
		assert.InDelta(t, 30.0, sub.HardLimitUSD, 0.0001)
		assert.InDelta(t, 30.0, sub.SoftLimitUSD, 0.0001)
		assert.InDelta(t, 30.0, sub.SystemHardLimitUSD, 0.0001)
		// usage is reported in cents: 10 USD -> 1000
		assert.Equal(t, "list", use.Object)
		assert.InDelta(t, 1000.0, use.TotalUsage, 0.0001)
		// what a client renders as leftover must be the real remaining quota
		remaining := sub.HardLimitUSD - use.TotalUsage/100
		assert.InDelta(t, 20.0, remaining, 0.0001, "client-visible remaining must equal the wallet")
		assert.NotEqual(t, 100000000.0, sub.HardLimitUSD, "the placeholder must be gone")
	})

	t.Run("limited token keeps answering with its own line", func(t *testing.T) {
		sub, use := call(GetSubscription, limited)
		// (remaining 4 + used 2) USD
		assert.InDelta(t, 6.0, sub.HardLimitUSD, 0.0001)
		assert.InDelta(t, 200.0, use.TotalUsage, 0.0001)
		assert.InDelta(t, 4.0, sub.HardLimitUSD-use.TotalUsage/100, 0.0001)
	})

	// The values follow the site's display currency, so the response has to say
	// which one it is: this site runs CNY, and a client that hardcodes "$" would
	// otherwise mislabel the balance.
	t.Run("currency marker names the unit that was actually computed", func(t *testing.T) {
		for displayType, want := range map[string]string{
			operation_setting.QuotaDisplayTypeCNY:    "cny",
			operation_setting.QuotaDisplayTypeUSD:    "usd",
			operation_setting.QuotaDisplayTypeTokens: "tokens",
		} {
			operation_setting.GetGeneralSetting().QuotaDisplayType = displayType
			sub, use := call(GetSubscription, unlimited)
			assert.Equal(t, want, sub.Currency, "subscription currency for %s", displayType)
			assert.Equal(t, want, use.Currency, "usage currency for %s", displayType)
		}
		operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	})
}
