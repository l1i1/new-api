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
	MinGroupModelRateLimitWindowSeconds = MinUserModelRateLimitWindowSeconds
	MaxGroupModelRateLimitWindowSeconds = MaxUserModelRateLimitWindowSeconds
	MinGroupModelRateLimitRequests      = MinUserModelRateLimitRequests
	MaxGroupModelRateLimitRequests      = MaxUserModelRateLimitRequests
	groupModelRateLimitCacheTTL         = 30 * time.Second
)

// GroupModelRateLimit is a fleet-wide per-user rate limit rule: for the given
// group+model, every user is limited to MaxRequests within WindowSeconds.
// The unique key intentionally mirrors the user rule shape so the same admin
// UX (window + max requests) applies, but the scope is global configuration.
type GroupModelRateLimit struct {
	Id            int    `json:"id"`
	GroupName     string `json:"group_name" gorm:"type:varchar(255);not null;index;uniqueIndex:idx_group_model_rate_limit"`
	ModelName     string `json:"model_name" gorm:"type:varchar(255);not null;uniqueIndex:idx_group_model_rate_limit"`
	WindowSeconds int    `json:"window_seconds" gorm:"not null;uniqueIndex:idx_group_model_rate_limit"`
	MaxRequests   int    `json:"max_requests" gorm:"not null"`
	Enabled       bool   `json:"enabled" gorm:"not null"`
	CreatedAt     int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt     int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

type groupModelRateLimitCacheEntry struct {
	rules     []GroupModelRateLimit
	expiresAt time.Time
}

var groupModelRateLimitLocalCache sync.Map

func NormalizeGroupModelRateLimits(rules []GroupModelRateLimit) ([]GroupModelRateLimit, error) {
	normalized := make([]GroupModelRateLimit, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		rule.GroupName = strings.TrimSpace(rule.GroupName)
		rule.ModelName = strings.TrimSpace(rule.ModelName)
		if rule.GroupName == "" || len(rule.GroupName) > 255 {
			return nil, errors.New("group_name must contain between 1 and 255 characters")
		}
		if rule.ModelName == "" || len(rule.ModelName) > 255 {
			return nil, errors.New("model_name must contain between 1 and 255 characters")
		}
		if rule.WindowSeconds < MinGroupModelRateLimitWindowSeconds || rule.WindowSeconds > MaxGroupModelRateLimitWindowSeconds {
			return nil, fmt.Errorf("window_seconds must be between %d and %d", MinGroupModelRateLimitWindowSeconds, MaxGroupModelRateLimitWindowSeconds)
		}
		if rule.MaxRequests < MinGroupModelRateLimitRequests || rule.MaxRequests > MaxGroupModelRateLimitRequests {
			return nil, fmt.Errorf("max_requests must be between %d and %d", MinGroupModelRateLimitRequests, MaxGroupModelRateLimitRequests)
		}

		identity := fmt.Sprintf("%s\x00%s\x00%d", rule.GroupName, rule.ModelName, rule.WindowSeconds)
		if _, exists := seen[identity]; exists {
			return nil, fmt.Errorf("duplicate rate limit for group %q, model %q and window %d", rule.GroupName, rule.ModelName, rule.WindowSeconds)
		}
		seen[identity] = struct{}{}

		rule.Id = 0
		rule.CreatedAt = 0
		rule.UpdatedAt = 0
		normalized = append(normalized, rule)
	}

	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].GroupName != normalized[j].GroupName {
			return normalized[i].GroupName < normalized[j].GroupName
		}
		if normalized[i].ModelName != normalized[j].ModelName {
			return normalized[i].ModelName < normalized[j].ModelName
		}
		return normalized[i].WindowSeconds < normalized[j].WindowSeconds
	})
	return normalized, nil
}

func GetGroupModelRateLimits() ([]GroupModelRateLimit, error) {
	var rules []GroupModelRateLimit
	err := DB.Order("group_name asc").
		Order("model_name asc").
		Order("window_seconds asc").
		Find(&rules).Error
	return rules, err
}

func ReplaceGroupModelRateLimits(rules []GroupModelRateLimit) ([]GroupModelRateLimit, error) {
	normalized, err := NormalizeGroupModelRateLimits(rules)
	if err != nil {
		return nil, err
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1 = 1").Delete(&GroupModelRateLimit{}).Error; err != nil {
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

	if err := cacheGroupModelRateLimits(normalized); err != nil {
		return nil, fmt.Errorf("rate limits were saved but cache refresh failed: %w", err)
	}
	return normalized, nil
}

// GetCachedGroupModelRateLimits returns the fleet-wide rule set, preferring the
// shared Redis cache in production and a short process-local cache otherwise.
func GetCachedGroupModelRateLimits() ([]GroupModelRateLimit, error) {
	if common.RedisEnabled && common.RDB == nil {
		return nil, errors.New("Redis client is not initialized")
	}
	if common.RedisEnabled {
		key := groupModelRateLimitCacheKey()
		encoded, err := common.RDB.Get(common.RDB.Context(), key).Result()
		if err == nil {
			var rules []GroupModelRateLimit
			if unmarshalErr := common.UnmarshalJsonStr(encoded, &rules); unmarshalErr == nil {
				return rules, nil
			}
		} else if !errors.Is(err, redis.Nil) {
			return nil, err
		}

		rules, dbErr := GetGroupModelRateLimits()
		if dbErr != nil {
			return nil, dbErr
		}
		if err := setGroupModelRateLimitRedisCache(key, rules); err != nil {
			return nil, err
		}
		return rules, nil
	}

	const globalKey = "global"
	if cached, ok := groupModelRateLimitLocalCache.Load(globalKey); ok {
		entry := cached.(groupModelRateLimitCacheEntry)
		if time.Now().Before(entry.expiresAt) {
			return entry.rules, nil
		}
		groupModelRateLimitLocalCache.Delete(globalKey)
	}
	rules, err := GetGroupModelRateLimits()
	if err != nil {
		return nil, err
	}
	groupModelRateLimitLocalCache.Store(globalKey, groupModelRateLimitCacheEntry{
		rules:     rules,
		expiresAt: time.Now().Add(groupModelRateLimitCacheTTL),
	})
	return rules, nil
}

func cacheGroupModelRateLimits(rules []GroupModelRateLimit) error {
	if common.RedisEnabled {
		if common.RDB == nil {
			return errors.New("Redis client is not initialized")
		}
		return setGroupModelRateLimitRedisCache(groupModelRateLimitCacheKey(), rules)
	}
	groupModelRateLimitLocalCache.Store("global", groupModelRateLimitCacheEntry{
		rules:     rules,
		expiresAt: time.Now().Add(groupModelRateLimitCacheTTL),
	})
	return nil
}

func groupModelRateLimitCacheKey() string {
	return "groupModelRateLimit:global"
}

func setGroupModelRateLimitRedisCache(key string, rules []GroupModelRateLimit) error {
	encoded, err := common.Marshal(rules)
	if err != nil {
		return err
	}
	return common.RDB.Set(common.RDB.Context(), key, encoded, groupModelRateLimitCacheTTL).Err()
}
