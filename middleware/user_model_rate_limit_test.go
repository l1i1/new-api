package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupUserModelRateLimitMiddlewareTest(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousRedisEnabled := common.RedisEnabled
	previousRedis := common.RDB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.UserModelRateLimit{}))
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	require.NoError(t, redisClient.Ping(context.Background()).Err())
	model.DB = db
	common.RedisEnabled = true
	common.RDB = redisClient
	t.Cleanup(func() {
		_ = redisClient.Close()
		model.DB = previousDB
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRedis
	})
}

func TestUserModelRateLimitIsolatesUsersAndModels(t *testing.T) {
	setupUserModelRateLimitMiddlewareTest(t)
	for _, userId := range []int{1, 2} {
		rules, err := model.ReplaceUserModelRateLimits(userId, []model.UserModelRateLimit{{
			ModelName: "model-a", WindowSeconds: 60, MaxRequests: 1, Enabled: true,
		}})
		require.NoError(t, err)
		require.Len(t, rules, 1)
	}

	router := gin.New()
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Set("id", map[string]int{"user-a": 1, "user-b": 2}[c.GetHeader("x-user")])
		c.Set(string(constant.ContextKeyOriginalModel), c.GetHeader("x-model"))
		UserModelRateLimit()(c)
	})
	// The route above intentionally executes the limiter as a nested handler so
	// the test controls the same context values populated by TokenAuth/Distribute.
	// The nested limiter calls c.Next and therefore reaches this endpoint.

	request := func(user, modelName string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("x-user", user)
		req.Header.Set("x-model", modelName)
		router.ServeHTTP(recorder, req)
		return recorder
	}

	assert.Equal(t, http.StatusOK, request("user-a", "model-a").Code)
	limited := request("user-a", "model-a")
	assert.Equal(t, http.StatusTooManyRequests, limited.Code)
	assert.NotEmpty(t, limited.Header().Get("Retry-After"))
	assert.Equal(t, http.StatusOK, request("user-a", "model-b").Code)
	assert.Equal(t, http.StatusOK, request("user-b", "model-a").Code)
}

func TestUserModelRateLimitEnforcesMultipleWindows(t *testing.T) {
	setupUserModelRateLimitMiddlewareTest(t)
	_, err := model.ReplaceUserModelRateLimits(3, []model.UserModelRateLimit{
		{ModelName: "model-a", WindowSeconds: 60, MaxRequests: 2, Enabled: true},
		{ModelName: "model-a", WindowSeconds: 3600, MaxRequests: 3, Enabled: true},
	})
	require.NoError(t, err)

	router := gin.New()
	router.POST("/", func(c *gin.Context) {
		c.Set("id", 3)
		c.Set(string(constant.ContextKeyOriginalModel), "model-a")
		UserModelRateLimit()(c)
	})
	request := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))
		return recorder
	}
	assert.Equal(t, http.StatusOK, request().Code)
	assert.Equal(t, http.StatusOK, request().Code)
	assert.Equal(t, http.StatusTooManyRequests, request().Code)
}

func TestUserModelRateLimitFailsClosedWhenRedisIsUnavailable(t *testing.T) {
	setupUserModelRateLimitMiddlewareTest(t)
	_, err := model.ReplaceUserModelRateLimits(4, []model.UserModelRateLimit{{
		ModelName: "model-a", WindowSeconds: 60, MaxRequests: 10, Enabled: true,
	}})
	require.NoError(t, err)
	require.NoError(t, common.RDB.Close())

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", 4)
		c.Set(string(constant.ContextKeyOriginalModel), "model-a")
	})
	router.Use(UserModelRateLimit())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
}
