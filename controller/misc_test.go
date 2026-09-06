package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEmailVerificationMessageUsesRequestLanguage(t *testing.T) {
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/verification?email=user@example.com", nil)
	c.Request.Header.Set("Accept-Language", "fr-FR,fr;q=0.9")

	lang := i18n.GetLangFromContext(c)
	subject, title, content := buildEmailVerificationMessage(lang, "123456")

	require.Equal(t, i18n.LangFr, lang)
	require.Contains(t, subject, "Vérification d'e-mail")
	require.Contains(t, title, "Code de vérification")
	require.Contains(t, content, "123456")
	require.NotContains(t, subject, i18n.MsgEmailVerificationSubject)
}

func executeHealthHandler(handler gin.HandlerFunc) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/health", nil)
	handler(context)
	return recorder
}

func useHealthDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = database
	t.Cleanup(func() {
		model.DB = previousDB
		sqlDB, err := database.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return database
}

func TestHealthLiveReturnsSuccessWithoutDependencies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := executeHealthHandler(HealthLive)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"success":true}`, recorder.Body.String())
}

func TestHealthReadyRequiresDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB := model.DB
	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	model.DB = nil
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
	})

	recorder := executeHealthHandler(HealthReady)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.JSONEq(t, `{"success":false}`, recorder.Body.String())
}

func TestHealthReadySkipsDisabledRedis(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := useHealthDB(t)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })

	recorder := executeHealthHandler(HealthReady)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"success":true}`, recorder.Body.String())

	sqlDB, err := database.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	closed := executeHealthHandler(HealthReady)
	assert.Equal(t, http.StatusServiceUnavailable, closed.Code)
	require.JSONEq(t, `{"success":false}`, closed.Body.String())
}

func TestHealthReadyChecksConfiguredRedis(t *testing.T) {
	gin.SetMode(gin.TestMode)
	useHealthDB(t)
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	common.RedisEnabled = true
	common.RDB = redisClient
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
		_ = redisClient.Close()
	})

	healthy := executeHealthHandler(HealthReady)
	require.Equal(t, http.StatusOK, healthy.Code)
	require.JSONEq(t, `{"success":true}`, healthy.Body.String())

	redisServer.SetError("ERR health check failed")
	unhealthy := executeHealthHandler(HealthReady)
	assert.Equal(t, http.StatusServiceUnavailable, unhealthy.Code)
	require.JSONEq(t, `{"success":false}`, unhealthy.Body.String())
}
