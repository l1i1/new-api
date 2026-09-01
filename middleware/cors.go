package middleware

import (
	"log"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func corsAllowedOrigins() []string {
	raw := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS"))
	if raw == "" {
		return nil
	}

	origins := make([]string, 0)
	for _, value := range strings.Split(raw, ",") {
		origin, err := common.NormalizeOrigin(value)
		if err != nil {
			log.Printf("WARNING: invalid CORS_ALLOWED_ORIGINS entry; cross-origin access disabled: %v", err)
			return nil
		}
		origins = append(origins, origin)
	}
	return origins
}

func legacyAllowAllCORS() gin.HandlerFunc {
	config := cors.DefaultConfig()
	config.AllowAllOrigins = true
	config.AllowCredentials = true
	config.AllowMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	config.AllowHeaders = []string{"*"}
	return cors.New(config)
}

func CORS() gin.HandlerFunc {
	raw := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS"))
	if raw == "" {
		// Unset keeps the legacy permissive default; strict origin allowlisting
		// is opt-in via CORS_ALLOWED_ORIGINS.
		return legacyAllowAllCORS()
	}

	origins := corsAllowedOrigins()
	if len(origins) == 0 {
		return func(c *gin.Context) {
			c.Next()
		}
	}

	config := cors.DefaultConfig()
	config.AllowOrigins = origins
	config.AllowCredentials = false
	config.AllowMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	config.AllowHeaders = []string{
		"Authorization",
		"Content-Type",
		"Accept",
		"Cache-Control",
		"X-API-Key",
		"X-Goog-API-Key",
		"Anthropic-Version",
		"X-Requested-With",
	}
	return cors.New(config)
}

func Version() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-New-Api-Version", common.Version)
		c.Next()
	}
}
