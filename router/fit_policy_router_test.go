package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

// TestFitPolicyRoutesRejectALoggedInNonAdministrator is the half the anonymous
// test cannot make: a route registered with UserAuth and no AdminAuth still
// answers 401 to an anonymous caller, so a missing administrator gate looks
// exactly like a present one. This walks the real chain with a real credential
// belonging to an enabled ordinary user.
//
// The expected refusal is the role gate's, not the permission gate's: the body
// carries AUTH_INSUFFICIENT_PRIVILEGE, which only authHelper emits. Accepting
// any 403 would stay green after AdminAuth was dropped, because
// RequirePermission(channel.read) also answers 403 — which is the failure this
// assertion exists to catch.
func TestFitPolicyRoutesRejectALoggedInNonAdministrator(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis := common.RedisEnabled
	previousMaster := common.IsMasterNode
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.AuditLog{}, &model.CasbinRule{}, &model.AuthzRole{}))
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.IsMasterNode = true
	require.NoError(t, authz.Init(db))
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled = previousRedis
		common.IsMasterNode = previousMaster
	})

	token := "common-user-pat"
	require.NoError(t, db.Create(&model.User{
		Username: "fit-policy-common-user", Password: "placeholder",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		Group: "default", AccessToken: &token, AuthVersion: 1,
		AffCode: "fit-policy-common-user",
	}).Error)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	registerChannelRoutes(engine.Group("/api"))

	for _, request := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/fit-capability/all"},
		{http.MethodGet, "/api/fit-capability"},
		{http.MethodGet, "/api/fit-policy"},
		{http.MethodPost, "/api/fit-policy/validate"},
	} {
		recorder := httptest.NewRecorder()
		httpRequest := httptest.NewRequest(request.method, request.path, strings.NewReader(`{}`))
		httpRequest.Header.Set("Authorization", "Bearer "+token)
		httpRequest.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(recorder, httpRequest)
		assert.Equal(t, http.StatusForbidden, recorder.Code,
			"%s %s must refuse an ordinary logged-in user", request.method, request.path)
		assert.Contains(t, recorder.Body.String(), "AUTH_INSUFFICIENT_PRIVILEGE",
			"%s %s must be refused by the administrator role gate, not by the permission layer",
			request.method, request.path)
	}
}
