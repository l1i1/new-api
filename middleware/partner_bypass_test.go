package middleware

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func signPartnerTestRequest(t *testing.T, secret, keyID string, body []byte) *http.Request {
	t.Helper()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	signature := common.HmacSha256(string(body), secret)
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/partner/health", bytes.NewReader(body))
	request.Header.Set("X-Partner-Key-ID", keyID)
	request.Header.Set("X-Partner-Timestamp", timestamp)
	request.Header.Set("X-Partner-Signature", signature)
	return request
}

func TestSignedPartnerBypassSkipsLimiter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PARTNER_CONSOLE_SECRETS", "pc-1:test-secret,pc-2:second-secret")
	useRateLimitMiniRedis(t)

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	// The limiter only allows 1 request: an unsigned second request must 429
	// while any number of signed requests pass through.
	router.Use(SignedPartnerBypass())
	router.Use(PartnerLimiter(rateLimitFactory(1, 60, "TEST-PARTNER-BYPASS")))
	router.POST("/internal/v1/partner/health", func(c *gin.Context) {
		c.Header("X-Test-Bypass", strconv.FormatBool(PartnerBypassActive(c)))
		c.Status(http.StatusNoContent)
	})

	signed := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, signPartnerTestRequest(t, "test-secret", "pc-1", []byte(`{}`)))
		return recorder
	}
	for range 3 {
		recorder := signed()
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		assert.Equal(t, "true", recorder.Header().Get("X-Test-Bypass"))
	}

	unsigned := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/v1/partner/health", bytes.NewReader([]byte(`{}`))))
		return recorder
	}
	// Signed requests never touch the limiter bucket, so the first unsigned
	// request still has the full allowance; the second one is limited.
	first := unsigned()
	assert.Equal(t, http.StatusNoContent, first.Code)
	assert.Equal(t, "false", first.Header().Get("X-Test-Bypass"))
	assert.Equal(t, http.StatusTooManyRequests, unsigned().Code)
}

func TestSignedPartnerBypassRejectsBadSignature(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PARTNER_CONSOLE_SECRETS", "pc-1:test-secret")
	useRateLimitMiniRedis(t)

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.Use(SignedPartnerBypass())
	router.Use(rateLimitFactory(1000, 60, "TEST-PARTNER-REJECT"))
	router.POST("/internal/v1/partner/health", func(c *gin.Context) {
		assert.False(t, PartnerBypassActive(c))
		c.Status(http.StatusNoContent)
	})

	cases := map[string]func(*http.Request){
		"wrong secret": func(r *http.Request) {
			r.Header.Set("X-Partner-Signature", common.HmacSha256(`{}`, "wrong"))
		},
		"tampered body": func(r *http.Request) {
			r.Body = http.NoBody
		},
		"expired timestamp": func(r *http.Request) {
			r.Header.Set("X-Partner-Timestamp", strconv.FormatInt(time.Now().Unix()-3600, 10))
		},
		"unknown key": func(r *http.Request) {
			r.Header.Set("X-Partner-Key-ID", "pc-9")
		},
	}
	for name, mutate := range cases {
		request := signPartnerTestRequest(t, "test-secret", "pc-1", []byte(`{}`))
		mutate(request)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusNoContent, recorder.Code, fmt.Sprintf("case %s must fall through, not bypass", name))
	}
}

func TestSignedPartnerBypassWithoutSecretsConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PARTNER_CONSOLE_SECRETS", "")

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.Use(SignedPartnerBypass())
	router.POST("/internal/v1/partner/health", func(c *gin.Context) {
		assert.False(t, PartnerBypassActive(c))
		c.Status(http.StatusNoContent)
	})
	request := signPartnerTestRequest(t, "test-secret", "pc-1", []byte(`{}`))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusNoContent, recorder.Code)
}
