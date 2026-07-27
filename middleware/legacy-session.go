package middleware

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
)

const legacyDashboardSessionName = "session"

// LegacyDashboardSession installs the signed cookie store used by releases
// before stateless dashboard authentication. The store is read by the refresh
// endpoint only; all authorization decisions continue to use the new session
// and access-token flow.
func LegacyDashboardSession() gin.HandlerFunc {
	store := cookie.NewStore([]byte(common.SessionSecret))
	store.Options(sessions.Options{
		Path:     "/",
		MaxAge:   2592000,
		HttpOnly: true,
		Secure:   common.SessionCookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
	return sessions.Sessions(legacyDashboardSessionName, store)
}

// LegacyDashboardSessionValue returns a value from the pre-stateless cookie.
// A missing middleware context is treated as an absent legacy session so
// controller unit tests and non-dashboard routes stay safe by default.
func LegacyDashboardSessionValue(c *gin.Context, key string) (any, bool) {
	session := sessions.Default(c)
	if session == nil {
		return nil, false
	}
	value := session.Get(key)
	return value, value != nil
}

// ClearLegacyDashboardSession expires the old cookie after an explicit logout
// or after a failed migration. It is intentionally best-effort because the
// new refresh cookie remains the authoritative credential.
func ClearLegacyDashboardSession(c *gin.Context) {
	session := sessions.Default(c)
	if session == nil {
		return
	}
	session.Clear()
	_ = session.Save()
}
