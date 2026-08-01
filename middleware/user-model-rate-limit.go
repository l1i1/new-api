package middleware

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"

	"github.com/gin-gonic/gin"
)

var userModelRateLimiter common.InMemoryRateLimiter

func UserModelRateLimit() gin.HandlerFunc {
	userModelRateLimiter.Init(30 * 24 * time.Hour)
	return func(c *gin.Context) {
		userId := common.GetContextKeyInt(c, constant.ContextKeyUserId)
		modelName := common.GetContextKeyString(c, constant.ContextKeyOriginalModel)
		if userId <= 0 || modelName == "" || !shouldCountUserModelRequest(c) {
			c.Next()
			return
		}

		rules, err := model.GetCachedUserModelRateLimits(userId)
		if err != nil {
			logger.LogError(c, fmt.Sprintf("user model rate limit lookup failed: %v", err))
			abortWithOpenAiMessage(c, http.StatusInternalServerError, "rate_limit_check_failed")
			return
		}

		modelHash := fmt.Sprintf("%x", sha256.Sum256([]byte(modelName)))
		for _, rule := range rules {
			if !rule.Enabled || rule.ModelName != modelName {
				continue
			}

			key := fmt.Sprintf("%s:user:%d:model:%s:window:%d", redisRateLimitNamespace, userId, modelHash, rule.WindowSeconds)
			allowed := false
			retryAfter := int64(rule.WindowSeconds)
			if common.RedisEnabled {
				allowed, _, retryAfter, err = redisFixedWindowTake(
					c.Request.Context(),
					key,
					rule.MaxRequests,
					int64(rule.WindowSeconds),
				)
				if err != nil {
					logger.LogError(c, fmt.Sprintf("user model rate limit check failed: %v", err))
					abortWithOpenAiMessage(c, http.StatusInternalServerError, "rate_limit_check_failed")
					return
				}
			} else {
				allowed = userModelRateLimiter.Request(key, rule.MaxRequests, int64(rule.WindowSeconds))
			}

			if !allowed {
				if retryAfter > 0 {
					c.Header("Retry-After", strconv.FormatInt(retryAfter, 10))
				}
				abortWithOpenAiMessage(c, http.StatusTooManyRequests, fmt.Sprintf(
					"Model %s rate limit exceeded: at most %d requests every %d seconds",
					modelName,
					rule.MaxRequests,
					rule.WindowSeconds,
				))
				return
			}
		}

		c.Next()
	}
}

func shouldCountUserModelRequest(c *gin.Context) bool {
	mode := relayconstant.Path2RelayMode(c.Request.URL.Path)
	if mode == relayconstant.RelayModeUnknown {
		mode = c.GetInt("relay_mode")
	}
	switch mode {
	case relayconstant.RelayModeMidjourneyTaskFetch,
		relayconstant.RelayModeMidjourneyTaskImageSeed,
		relayconstant.RelayModeMidjourneyTaskFetchByCondition,
		relayconstant.RelayModeSunoFetch,
		relayconstant.RelayModeSunoFetchByID,
		relayconstant.RelayModeVideoFetchByID:
		return false
	default:
		return true
	}
}
