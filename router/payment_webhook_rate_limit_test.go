package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func performAPIRouterRequest(router http.Handler, method string, path string, remoteAddr string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, nil)
	request.RemoteAddr = remoteAddr
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestPaymentWebhooksDoNotConsumeGlobalAPIRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousRedisEnabled := common.RedisEnabled
	previousGlobalEnabled := common.GlobalApiRateLimitEnable
	previousGlobalLimit := common.GlobalApiRateLimitNum
	previousGlobalDuration := common.GlobalApiRateLimitDuration
	previousStripeAPISecret := setting.StripeApiSecret
	previousStripeWebhookSecret := setting.StripeWebhookSecret
	previousStripePriceID := setting.StripePriceId
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		common.GlobalApiRateLimitEnable = previousGlobalEnabled
		common.GlobalApiRateLimitNum = previousGlobalLimit
		common.GlobalApiRateLimitDuration = previousGlobalDuration
		setting.StripeApiSecret = previousStripeAPISecret
		setting.StripeWebhookSecret = previousStripeWebhookSecret
		setting.StripePriceId = previousStripePriceID
	})

	common.RedisEnabled = false
	common.GlobalApiRateLimitEnable = true
	common.GlobalApiRateLimitNum = 1
	common.GlobalApiRateLimitDuration = 60
	setting.StripeApiSecret = ""
	setting.StripeWebhookSecret = ""
	setting.StripePriceId = ""

	engine := gin.New()
	require.NoError(t, engine.SetTrustedProxies(nil))
	SetApiRouter(engine)

	webhookIP := "192.0.2.201:12345"
	firstWebhook := performAPIRouterRequest(engine, http.MethodPost, "/api/stripe/webhook", webhookIP)
	secondWebhook := performAPIRouterRequest(engine, http.MethodPost, "/api/stripe/webhook", webhookIP)
	assert.Equal(t, http.StatusForbidden, firstWebhook.Code)
	assert.Equal(t, http.StatusForbidden, secondWebhook.Code)

	apiIP := "192.0.2.202:12345"
	firstAPI := performAPIRouterRequest(engine, http.MethodGet, "/api/status", apiIP)
	secondAPI := performAPIRouterRequest(engine, http.MethodGet, "/api/status", apiIP)
	assert.Equal(t, http.StatusOK, firstAPI.Code)
	assert.Equal(t, http.StatusTooManyRequests, secondAPI.Code)
}
