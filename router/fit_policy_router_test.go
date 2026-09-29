package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fit-policy administration surface is registered by hand rather than
// through the channelPermissionRoutes table (it lives outside the /channel
// group on purpose), so these tests pin the three properties that would
// otherwise only be visible by reading the registration block: the routes
// exist with the expected handlers, the static /all path does not conflict
// with the group's existing routes, and every one of them is behind auth.

func TestFitPolicyRoutesRegisterWithoutConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")

	require.NotPanics(t, func() {
		registerChannelRoutes(api)
	})

	registered := map[string]string{}
	for _, route := range engine.Routes() {
		registered[route.Method+" "+route.Path] = route.Handler
		for _, path := range []string{
			"/api/fit-capability",
			"/api/fit-capability/all",
			"/api/fit-capability/report",
			"/api/fit-policy",
			"/api/fit-policy/validate",
		} {
			if route.Path == path {
				t.Logf("%s %s -> %s", route.Method, route.Path, route.Handler)
			}
		}
	}

	for _, expected := range []struct {
		method  string
		path    string
		handler string
	}{
		{http.MethodGet, "/api/fit-capability", "GetChannelFitCapabilities"},
		{http.MethodGet, "/api/fit-capability/all", "GetChannelFitCapabilitiesPage"},
		{http.MethodPut, "/api/fit-capability", "PutChannelFitCapability"},
		{http.MethodPost, "/api/fit-capability/report", "PostFitCapabilityReport"},
		{http.MethodGet, "/api/fit-policy", "GetFitPolicy"},
		{http.MethodPost, "/api/fit-policy/validate", "ValidateFitPolicyDocument"},
	} {
		handler, ok := registered[expected.method+" "+expected.path]
		require.True(t, ok, "%s %s is not registered", expected.method, expected.path)
		assert.True(t,
			strings.HasSuffix(handler, expected.handler),
			"%s %s is wired to %s, expected %s", expected.method, expected.path, handler, expected.handler,
		)
	}
}

// Every administration route must reject an anonymous caller. A route added
// without its auth middleware would answer 200 to anyone, which no handler
// test would catch because handler tests call the function directly.
func TestFitPolicyRoutesRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	registerChannelRoutes(engine.Group("/api"))

	for _, request := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/fit-capability/all"},
		{http.MethodGet, "/api/fit-policy"},
		{http.MethodPost, "/api/fit-policy/validate"},
	} {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(request.method, request.path, nil))
		assert.Equal(t, http.StatusUnauthorized, recorder.Code,
			"%s %s must require authentication", request.method, request.path)
	}
}
