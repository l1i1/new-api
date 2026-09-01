package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCORSAllowsConfiguredOriginWithoutCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com")

	engine := gin.New()
	engine.Use(CORS())
	engine.GET("/status", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	request.Header.Set("Origin", "https://app.example.com")
	engine.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Equal(t, "https://app.example.com", recorder.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, recorder.Header().Get("Access-Control-Allow-Credentials"))
}

func TestCORSKeepsLegacyAllowAllWhenUnconfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CORS_ALLOWED_ORIGINS", "")

	engine := gin.New()
	engine.Use(CORS())
	engine.GET("/status", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	request.Header.Set("Origin", "https://app.example.com")
	engine.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Equal(t, "*", recorder.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", recorder.Header().Get("Access-Control-Allow-Credentials"))
}

func TestCORSFailsClosedOnInvalidConfiguredOrigins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, value := range []string{"*", "https://app.example.com/path", "https://*.example.com", "not-an-origin"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CORS_ALLOWED_ORIGINS", value)
			engine := gin.New()
			engine.Use(CORS())
			engine.GET("/status", func(c *gin.Context) { c.Status(http.StatusNoContent) })

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/status", nil)
			request.Header.Set("Origin", "https://app.example.com")
			engine.ServeHTTP(recorder, request)

			assert.Empty(t, recorder.Header().Get("Access-Control-Allow-Origin"))
		})
	}
}

func TestCORSPreflightUsesHeaderAllowlist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com")
	engine := gin.New()
	engine.Use(CORS())
	engine.OPTIONS("/status", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "/status", nil)
	request.Header.Set("Origin", "https://app.example.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "Authorization, X-API-Key")
	engine.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Contains(t, recorder.Header().Get("Access-Control-Allow-Headers"), "Authorization")
	assert.Contains(t, recorder.Header().Get("Access-Control-Allow-Headers"), "X-Api-Key")
	assert.NotContains(t, recorder.Header().Get("Access-Control-Allow-Headers"), "*")
}
