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

	"github.com/gin-gonic/gin"
)

var groupModelRateLimiter common.InMemoryRateLimiter

// effectiveRelayGroup resolves the group the request was actually routed
// through. Auto routing picks a concrete group and stores it in
// ContextKeyAutoGroup; otherwise ContextKeyUsingGroup (user group overridden by
// token group) is the effective group.
func effectiveRelayGroup(c *gin.Context) string {
	if group := common.GetContextKeyString(c, constant.ContextKeyAutoGroup); group != "" {
		return group
	}
	return common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
}

// GroupModelRateLimit enforces fleet-wide per-user model rate limits: a rule
// configured for the request's effective group and original model counts
// independently per user.
func GroupModelRateLimit() gin.HandlerFunc {
	groupModelRateLimiter.Init(30 * 24 * time.Hour)
	return func(c *gin.Context) {
		userId := common.GetContextKeyInt(c, constant.ContextKeyUserId)
		groupName := effectiveRelayGroup(c)
		modelName := common.GetContextKeyString(c, constant.ContextKeyOriginalModel)
		if userId <= 0 || groupName == "" || modelName == "" || !shouldCountUserModelRequest(c) {
			c.Next()
			return
		}

		rules, err := model.GetCachedGroupModelRateLimits()
		if err != nil {
			logger.LogError(c, fmt.Sprintf("group model rate limit lookup failed: %v", err))
			abortWithOpenAiMessage(c, http.StatusInternalServerError, "rate_limit_check_failed")
			return
		}

		groupHash := fmt.Sprintf("%x", sha256.Sum256([]byte(groupName)))
		modelHash := fmt.Sprintf("%x", sha256.Sum256([]byte(modelName)))
		for _, rule := range rules {
			if !rule.Enabled || rule.GroupName != groupName || rule.ModelName != modelName {
				continue
			}

			key := fmt.Sprintf("%s:group:%s:model:%s:user:%d:window:%d", redisRateLimitNamespace, groupHash, modelHash, userId, rule.WindowSeconds)
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
					logger.LogError(c, fmt.Sprintf("group model rate limit check failed: %v", err))
					abortWithOpenAiMessage(c, http.StatusInternalServerError, "rate_limit_check_failed")
					return
				}
			} else {
				allowed = groupModelRateLimiter.Request(key, rule.MaxRequests, int64(rule.WindowSeconds))
			}

			if !allowed {
				if retryAfter > 0 {
					c.Header("Retry-After", strconv.FormatInt(retryAfter, 10))
				}
				abortWithOpenAiMessage(c, http.StatusTooManyRequests, fmt.Sprintf(
					"Group %s model %s rate limit exceeded: at most %d requests every %d seconds",
					groupName,
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
