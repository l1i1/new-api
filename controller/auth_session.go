package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func RefreshAuth(c *gin.Context) {
	setAuthNoStore(c)
	rawRefreshToken, err := c.Cookie(service.RefreshCookieName)
	if err != nil || rawRefreshToken == "" {
		if rawRefreshToken == "" {
			bundle, user, found, migrationErr := migrateLegacyDashboardSession(c)
			if found {
				if migrationErr != nil {
					service.ClearRefreshCookie(c)
					writeAuthSessionError(c, migrationErr)
					return
				}
				writeAuthBundle(c, bundle, user)
				return
			}
		}
		service.ClearRefreshCookie(c)
		writeAuthSessionError(c, service.ErrRefreshTokenInvalid)
		return
	}
	bundle, user, err := service.RefreshLoginSession(rawRefreshToken, c.GetHeader("X-Auth-Session"), c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		if errors.Is(err, service.ErrRefreshTokenInvalid) || errors.Is(err, service.ErrLoginSessionRevoked) {
			service.ClearRefreshCookie(c)
		}
		writeAuthSessionError(c, err)
		return
	}
	writeAuthBundle(c, bundle, user)
}

func writeAuthBundle(c *gin.Context, bundle *service.AuthBundle, user *model.User) {
	service.WriteRefreshCookie(c, bundle.RefreshToken)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"access_token":      bundle.AccessToken,
			"token_type":        bundle.TokenType,
			"access_expires_at": bundle.AccessExpiresAt,
			"user":              buildSelfUserData(user),
			"session":           bundle.Session,
		},
	})
}

// migrateLegacyDashboardSession bridges the signed cookie used before the
// stateless dashboard auth release. It runs only when the new refresh cookie
// is absent, and it validates the user against the database before issuing a
// new server-side session.
func migrateLegacyDashboardSession(c *gin.Context) (*service.AuthBundle, *model.User, bool, error) {
	value, present := middleware.LegacyDashboardSessionValue(c, "id")
	if !present {
		return nil, nil, false, nil
	}

	userID, ok := legacyDashboardUserID(value)
	if !ok || userID <= 0 {
		middleware.ClearLegacyDashboardSession(c)
		return nil, nil, true, service.ErrLoginSessionRevoked
	}
	user, err := model.GetUserById(userID, false)
	if err != nil {
		return nil, nil, true, err
	}
	if user.Status != common.UserStatusEnabled {
		middleware.ClearLegacyDashboardSession(c)
		return nil, nil, true, service.ErrLoginSessionRevoked
	}

	bundle, err := service.CreateLoginSession(
		user.Id,
		"legacy-cookie",
		c.ClientIP(),
		c.Request.UserAgent(),
	)
	if err != nil {
		return nil, nil, true, err
	}
	return bundle, user, true, nil
}

func legacyDashboardUserID(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		if typed > int64(^uint(0)>>1) || typed < -int64(^uint(0)>>1)-1 {
			return 0, false
		}
		return int(typed), true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func AuthLogout(c *gin.Context) {
	setAuthNoStore(c)
	expectedSID := strings.TrimSpace(c.GetHeader("X-Auth-Session"))
	rawRefreshToken, cookieErr := c.Cookie(service.RefreshCookieName)
	cookieSID, hasCookieSID := service.RefreshTokenSID(rawRefreshToken)
	if expectedSID != "" && cookieErr == nil && hasCookieSID && cookieSID != expectedSID {
		writeAuthSessionError(c, service.ErrLoginSessionMismatch)
		return
	}

	if rawAccessToken, ok := dashboardBearer(c.GetHeader("Authorization")); ok {
		if identity, err := service.ParseAccessToken(rawAccessToken); err == nil {
			if expectedSID != "" && expectedSID != identity.SessionID {
				writeAuthSessionError(c, service.ErrLoginSessionMismatch)
				return
			}
			if _, err := model.RevokeUserSession(identity.UserID, identity.SessionID, "logout"); err != nil {
				writeAuthSessionError(c, err)
				return
			}
			cookieCleared := false
			if cookieErr == nil && hasCookieSID && cookieSID == identity.SessionID {
				if err := service.RevokeByRefreshToken(rawRefreshToken, identity.SessionID, "logout"); err != nil {
					writeAuthSessionError(c, err)
					return
				}
				service.ClearRefreshCookie(c)
				cookieCleared = true
			}
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"message": "",
				"data":    gin.H{"revoked_sid": identity.SessionID, "cookie_cleared": cookieCleared},
			})
			middleware.ClearLegacyDashboardSession(c)
			return
		}
	}
	if cookieErr != nil || rawRefreshToken == "" {
		service.ClearRefreshCookie(c)
		middleware.ClearLegacyDashboardSession(c)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
		return
	}
	if err := service.RevokeByRefreshToken(rawRefreshToken, expectedSID, "logout"); err != nil {
		writeAuthSessionError(c, err)
		return
	}
	service.ClearRefreshCookie(c)
	middleware.ClearLegacyDashboardSession(c)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func GetLoginSessions(c *gin.Context) {
	identity, ok := requireBrowserSession(c)
	if !ok {
		return
	}
	sessions, err := service.ListLoginSessions(identity.UserID, identity.SessionID)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": sessions})
}

func DeleteLoginSession(c *gin.Context) {
	identity, ok := requireBrowserSession(c)
	if !ok {
		return
	}
	sid := strings.TrimSpace(c.Param("sid"))
	if sid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "code": "AUTH_SESSION_ID_REQUIRED", "message": "session id is required"})
		return
	}
	revoked, err := model.RevokeUserSession(identity.UserID, sid, "user_revoked")
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	if !revoked {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "code": "AUTH_SESSION_NOT_FOUND", "message": "session not found"})
		return
	}
	if rawRefreshToken, cookieErr := c.Cookie(service.RefreshCookieName); cookieErr == nil {
		cookieSID, ok := service.RefreshTokenSID(rawRefreshToken)
		if ok && cookieSID == sid {
			service.ClearRefreshCookie(c)
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"revoked_sid": sid, "current": sid == identity.SessionID}})
}

func RevokeOtherLoginSessions(c *gin.Context) {
	identity, ok := requireBrowserSession(c)
	if !ok {
		return
	}
	count, err := model.RevokeOtherUserSessions(identity.UserID, identity.SessionID, "user_revoked_others")
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"revoked_count": count}})
}

func requireBrowserSession(c *gin.Context) (service.AuthIdentity, bool) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"code":    "AUTH_SESSION_REQUIRED",
			"message": "a dashboard login session is required",
		})
		return service.AuthIdentity{}, false
	}
	return identity, true
}

func writeAuthSessionError(c *gin.Context, err error) {
	status, code := service.AuthSessionErrorCode(err)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		status, code = http.StatusUnauthorized, "AUTH_UNAUTHORIZED"
	}
	if status == http.StatusInternalServerError {
		// The response body only carries the generic AUTH_INTERNAL_ERROR
		// code; without this log the underlying Redis/database/session
		// failure is indistinguishable from the client side.
		logger.LogError(c.Request.Context(), fmt.Sprintf("auth session internal error (%s %s): %v", c.Request.Method, c.Request.URL.Path, err))
	}
	c.JSON(status, gin.H{"success": false, "code": code, "message": http.StatusText(status)})
}

func setAuthNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
}

func authRotationData(bundle *service.AuthBundle) gin.H {
	return gin.H{
		"access_token":      bundle.AccessToken,
		"token_type":        bundle.TokenType,
		"access_expires_at": bundle.AccessExpiresAt,
		"session":           bundle.Session,
	}
}

func dashboardBearer(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}
