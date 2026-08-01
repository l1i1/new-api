package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

const (
	MinUserModelRateLimitWindowSeconds = 1
	MaxUserModelRateLimitWindowSeconds = 30 * 24 * 60 * 60
	MinUserModelRateLimitRequests      = 1
	MaxUserModelRateLimitRequests      = 1_000_000_000
	userModelRateLimitCacheTTL         = 30 * time.Second
)

type UserModelRateLimit struct {
	Id            int    `json:"id"`
	UserId        int    `json:"user_id" gorm:"not null;index;uniqueIndex:idx_user_model_rate_limit"`
	ModelName     string `json:"model_name" gorm:"type:varchar(255);not null;uniqueIndex:idx_user_model_rate_limit"`
	WindowSeconds int    `json:"window_seconds" gorm:"not null;uniqueIndex:idx_user_model_rate_limit"`
	MaxRequests   int    `json:"max_requests" gorm:"not null"`
	Enabled       bool   `json:"enabled" gorm:"not null"`
	CreatedAt     int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt     int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

type userModelRateLimitCacheEntry struct {
	rules     []UserModelRateLimit
	expiresAt time.Time
}

var userModelRateLimitLocalCache sync.Map

func NormalizeUserModelRateLimits(userId int, rules []UserModelRateLimit) ([]UserModelRateLimit, error) {
	if userId <= 0 {
		return nil, errors.New("user id must be positive")
	}

	normalized := make([]UserModelRateLimit, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		rule.ModelName = strings.TrimSpace(rule.ModelName)
		if rule.ModelName == "" || len(rule.ModelName) > 255 {
			return nil, errors.New("model_name must contain between 1 and 255 characters")
		}
		if rule.WindowSeconds < MinUserModelRateLimitWindowSeconds || rule.WindowSeconds > MaxUserModelRateLimitWindowSeconds {
			return nil, fmt.Errorf("window_seconds must be between %d and %d", MinUserModelRateLimitWindowSeconds, MaxUserModelRateLimitWindowSeconds)
		}
		if rule.MaxRequests < MinUserModelRateLimitRequests || rule.MaxRequests > MaxUserModelRateLimitRequests {
			return nil, fmt.Errorf("max_requests must be between %d and %d", MinUserModelRateLimitRequests, MaxUserModelRateLimitRequests)
		}

		identity := fmt.Sprintf("%s\x00%d", rule.ModelName, rule.WindowSeconds)
		if _, exists := seen[identity]; exists {
			return nil, fmt.Errorf("duplicate rate limit for model %q and window %d", rule.ModelName, rule.WindowSeconds)
		}
		seen[identity] = struct{}{}

		rule.Id = 0
		rule.UserId = userId
		rule.CreatedAt = 0
		rule.UpdatedAt = 0
		normalized = append(normalized, rule)
	}

	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].ModelName != normalized[j].ModelName {
			return normalized[i].ModelName < normalized[j].ModelName
		}
		return normalized[i].WindowSeconds < normalized[j].WindowSeconds
	})
	return normalized, nil
}

func GetUserModelRateLimits(userId int) ([]UserModelRateLimit, error) {
	var rules []UserModelRateLimit
	err := DB.Where("user_id = ?", userId).
		Order("model_name asc").
		Order("window_seconds asc").
		Find(&rules).Error
	return rules, err
}

func ReplaceUserModelRateLimits(userId int, rules []UserModelRateLimit) ([]UserModelRateLimit, error) {
	normalized, err := NormalizeUserModelRateLimits(userId, rules)
	if err != nil {
		return nil, err
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userId).Delete(&UserModelRateLimit{}).Error; err != nil {
			return err
		}
		if len(normalized) == 0 {
			return nil
		}
		return tx.Create(&normalized).Error
	})
	if err != nil {
		return nil, err
	}

	if err := cacheUserModelRateLimits(userId, normalized); err != nil {
		return nil, fmt.Errorf("rate limits were saved but cache refresh failed: %w", err)
	}
	return normalized, nil
}

func GetCachedUserModelRateLimits(userId int) ([]UserModelRateLimit, error) {
	if common.RedisEnabled && common.RDB == nil {
		return nil, errors.New("Redis client is not initialized")
	}
	if common.RedisEnabled {
		key := userModelRateLimitCacheKey(userId)
		encoded, err := common.RDB.Get(common.RDB.Context(), key).Result()
		if err == nil {
			var rules []UserModelRateLimit
			if unmarshalErr := common.UnmarshalJsonStr(encoded, &rules); unmarshalErr == nil {
				return rules, nil
			}
		} else if !errors.Is(err, redis.Nil) {
			return nil, err
		}

		rules, dbErr := GetUserModelRateLimits(userId)
		if dbErr != nil {
			return nil, dbErr
		}
		if err := setUserModelRateLimitRedisCache(key, rules); err != nil {
			return nil, err
		}
		return rules, nil
	}

	if cached, ok := userModelRateLimitLocalCache.Load(userId); ok {
		entry := cached.(userModelRateLimitCacheEntry)
		if time.Now().Before(entry.expiresAt) {
			return entry.rules, nil
		}
		userModelRateLimitLocalCache.Delete(userId)
	}
	rules, err := GetUserModelRateLimits(userId)
	if err != nil {
		return nil, err
	}
	userModelRateLimitLocalCache.Store(userId, userModelRateLimitCacheEntry{
		rules:     rules,
		expiresAt: time.Now().Add(userModelRateLimitCacheTTL),
	})
	return rules, nil
}

func cacheUserModelRateLimits(userId int, rules []UserModelRateLimit) error {
	if common.RedisEnabled {
		if common.RDB == nil {
			return errors.New("Redis client is not initialized")
		}
		return setUserModelRateLimitRedisCache(userModelRateLimitCacheKey(userId), rules)
	}
	userModelRateLimitLocalCache.Store(userId, userModelRateLimitCacheEntry{
		rules:     rules,
		expiresAt: time.Now().Add(userModelRateLimitCacheTTL),
	})
	return nil
}

func userModelRateLimitCacheKey(userId int) string {
	return fmt.Sprintf("userModelRateLimit:%d", userId)
}

func setUserModelRateLimitRedisCache(key string, rules []UserModelRateLimit) error {
	encoded, err := common.Marshal(rules)
	if err != nil {
		return err
	}
	return common.RDB.Set(common.RDB.Context(), key, encoded, userModelRateLimitCacheTTL).Err()
}
