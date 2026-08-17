package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelObservabilityRoutesExposePaginatedAndLegacyPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	registerChannelRoutes(engine.Group("/api"))

	routes := make(map[string]bool)
	for _, route := range engine.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	require.True(t, routes[http.MethodGet+" /api/observability/channel-model"])
	require.True(t, routes[http.MethodGet+" /api/observability/channel-availability"])
	assert.True(t, routes[http.MethodGet+" /api/channel/observability"])
}
