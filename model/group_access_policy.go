package model

import (
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

const (
	groupAccessPolicyCacheTTL    = 30 * time.Second
	maxGroupAccessPolicyChannels = 1024
	maxGroupAccessPolicyModels   = 1024
	maxGroupAccessPolicyGroups   = 256
)

// GroupAccessPolicyIntList is stored as JSON text to keep the schema portable
// across SQLite, MySQL, and PostgreSQL while still exposing a typed API value.
type GroupAccessPolicyIntList []int

func (values GroupAccessPolicyIntList) Value() (driver.Value, error) {
	if values == nil {
		values = GroupAccessPolicyIntList{}
	}
	data, err := common.Marshal(values)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

func (values *GroupAccessPolicyIntList) Scan(value interface{}) error {
	if values == nil {
		return errors.New("cannot scan group access policy channel list into nil pointer")
	}
	switch typed := value.(type) {
	case nil:
		*values = GroupAccessPolicyIntList{}
	case []byte:
		if err := common.Unmarshal(typed, values); err != nil {
			return err
		}
	case string:
		if err := common.Unmarshal([]byte(typed), values); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported group access policy channel list value %T", value)
	}
	if *values == nil {
		*values = GroupAccessPolicyIntList{}
	}
	return nil
}

// GroupAccessPolicyStringList is the typed JSON-text representation used for
// model and target-group deny lists.
type GroupAccessPolicyStringList []string

func (values GroupAccessPolicyStringList) Value() (driver.Value, error) {
	if values == nil {
		values = GroupAccessPolicyStringList{}
	}
	data, err := common.Marshal(values)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

func (values *GroupAccessPolicyStringList) Scan(value interface{}) error {
	if values == nil {
		return errors.New("cannot scan group access policy string list into nil pointer")
	}
	switch typed := value.(type) {
	case nil:
		*values = GroupAccessPolicyStringList{}
	case []byte:
		if err := common.Unmarshal(typed, values); err != nil {
			return err
		}
	case string:
		if err := common.Unmarshal([]byte(typed), values); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported group access policy string list value %T", value)
	}
	if *values == nil {
		*values = GroupAccessPolicyStringList{}
	}
	return nil
}

// GroupAccessPolicy is both the database row and the normalized request-time
// snapshot. The fingerprint is derived and never persisted.
type GroupAccessPolicy struct {
	Id                        int                         `json:"id,omitempty"`
	GroupName                 string                      `json:"group_name" gorm:"type:varchar(255);not null;uniqueIndex"`
	BlockedChannelIDs         GroupAccessPolicyIntList    `json:"blocked_channel_ids" gorm:"type:text;not null"`
	BlockedModels             GroupAccessPolicyStringList `json:"blocked_models" gorm:"type:text;not null"`
	BlockedGroups             GroupAccessPolicyStringList `json:"blocked_groups" gorm:"type:text;not null"`
	ContentModerationDisabled bool                        `json:"content_moderation_disabled" gorm:"not null"`
	CreatedAt                 int64                       `json:"created_at,omitempty" gorm:"autoCreateTime"`
	UpdatedAt                 int64                       `json:"updated_at,omitempty" gorm:"autoUpdateTime"`
	Fingerprint               string                      `json:"fingerprint,omitempty" gorm:"-"`
}

// GroupAccessPolicySnapshot remains a named semantic alias for request-scoped
// policy values without duplicating the persisted structure.
type GroupAccessPolicySnapshot = GroupAccessPolicy

// GroupAccessPolicyInput is kept for callers that build a request payload
// separately from the persisted structure.
type GroupAccessPolicyInput struct {
	BlockedChannelIDs         []int    `json:"blocked_channel_ids"`
	BlockedModels             []string `json:"blocked_models"`
	BlockedGroups             []string `json:"blocked_groups"`
	ContentModerationDisabled bool     `json:"content_moderation_disabled"`
}

var groupAccessPolicyLocalCache sync.Map

func emptyGroupAccessPolicy(groupName string) GroupAccessPolicy {
	policy := GroupAccessPolicy{
		GroupName:         strings.TrimSpace(groupName),
		BlockedChannelIDs: GroupAccessPolicyIntList{},
		BlockedModels:     GroupAccessPolicyStringList{},
		BlockedGroups:     GroupAccessPolicyStringList{},
	}
	policy.Fingerprint = groupAccessPolicyFingerprint(policy)
	return policy
}

func NormalizeGroupAccessPolicy(policy GroupAccessPolicy) (GroupAccessPolicy, error) {
	groupName := strings.TrimSpace(policy.GroupName)
	if groupName == "" || len(groupName) > 255 {
		return GroupAccessPolicy{}, errors.New("group_name must contain between 1 and 255 characters")
	}

	channels, err := normalizeGroupAccessPolicyChannels(policy.BlockedChannelIDs)
	if err != nil {
		return GroupAccessPolicy{}, err
	}
	models, err := normalizeGroupAccessPolicyStrings(policy.BlockedModels, "blocked_models", maxGroupAccessPolicyModels, false)
	if err != nil {
		return GroupAccessPolicy{}, err
	}
	groups, err := normalizeGroupAccessPolicyStrings(policy.BlockedGroups, "blocked_groups", maxGroupAccessPolicyGroups, true)
	if err != nil {
		return GroupAccessPolicy{}, err
	}

	normalized := GroupAccessPolicy{
		GroupName:                 groupName,
		BlockedChannelIDs:         channels,
		BlockedModels:             models,
		BlockedGroups:             groups,
		ContentModerationDisabled: policy.ContentModerationDisabled,
	}
	normalized.Fingerprint = groupAccessPolicyFingerprint(normalized)
	return normalized, nil
}

func normalizeGroupAccessPolicyChannels(values []int) (GroupAccessPolicyIntList, error) {
	if len(values) > maxGroupAccessPolicyChannels {
		return nil, fmt.Errorf("blocked_channel_ids must contain at most %d entries", maxGroupAccessPolicyChannels)
	}
	seen := make(map[int]struct{}, len(values))
	result := make(GroupAccessPolicyIntList, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			return nil, errors.New("blocked_channel_ids must contain positive channel IDs")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Ints(result)
	return result, nil
}

func normalizeGroupAccessPolicyStrings(values []string, field string, limit int, rejectAuto bool) (GroupAccessPolicyStringList, error) {
	if len(values) > limit {
		return nil, fmt.Errorf("%s must contain at most %d entries", field, limit)
	}
	seen := make(map[string]struct{}, len(values))
	result := make(GroupAccessPolicyStringList, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 255 {
			return nil, fmt.Errorf("%s entries must contain between 1 and 255 characters", field)
		}
		if field == "blocked_models" && strings.ContainsAny(value, "*?[") {
			return nil, errors.New("blocked_models only supports exact model names")
		}
		if rejectAuto && value == "auto" {
			return nil, errors.New("blocked_groups cannot contain auto")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func (policy GroupAccessPolicy) BlocksChannel(channelID int) bool {
	for _, blockedID := range policy.BlockedChannelIDs {
		if blockedID == channelID {
			return true
		}
	}
	return false
}

func (policy GroupAccessPolicy) IsChannelBlocked(channelID int) bool {
	return policy.BlocksChannel(channelID)
}

func (policy GroupAccessPolicy) BlocksModel(modelName string) bool {
	requested := groupAccessPolicyModelVariants(modelName)
	if len(requested) == 0 {
		return false
	}
	for _, blockedModel := range policy.BlockedModels {
		for variant := range groupAccessPolicyModelVariants(blockedModel) {
			if _, ok := requested[variant]; ok {
				return true
			}
		}
	}
	return false
}

func groupAccessPolicyModelVariants(modelName string) map[string]struct{} {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil
	}
	variants := map[string]struct{}{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" {
			variants[name] = struct{}{}
		}
	}
	add(modelName)
	if baseModel, ok := ratio_setting.CompactBaseModelName(modelName); ok {
		add(baseModel)
	} else {
		add(ratio_setting.WithCompactModelSuffix(modelName))
	}
	return variants
}

func (policy GroupAccessPolicy) IsModelBlocked(modelName string) bool {
	return policy.BlocksModel(modelName)
}

// GroupAccessPolicyModelsMatch reports whether two model names identify the
// same routing model, including compact aliases used by the relay layer.
func GroupAccessPolicyModelsMatch(left, right string) bool {
	leftVariants := groupAccessPolicyModelVariants(left)
	if len(leftVariants) == 0 {
		return false
	}
	for variant := range groupAccessPolicyModelVariants(right) {
		if _, ok := leftVariants[variant]; ok {
			return true
		}
	}
	return false
}

func (policy GroupAccessPolicy) BlocksGroup(groupName string) bool {
	groupName = strings.TrimSpace(groupName)
	for _, blockedGroup := range policy.BlockedGroups {
		if blockedGroup == groupName {
			return true
		}
	}
	return false
}

func (policy GroupAccessPolicy) IsGroupBlocked(groupName string) bool {
	return policy.BlocksGroup(groupName)
}

func (policy GroupAccessPolicy) BlockedChannelSet() map[int]struct{} {
	result := make(map[int]struct{}, len(policy.BlockedChannelIDs))
	for _, channelID := range policy.BlockedChannelIDs {
		result[channelID] = struct{}{}
	}
	return result
}

func groupAccessPolicyFingerprint(policy GroupAccessPolicy) string {
	policy.Id = 0
	policy.CreatedAt = 0
	policy.UpdatedAt = 0
	policy.Fingerprint = ""
	encoded, err := common.Marshal(policy)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func IsConfiguredGroupAccessPolicyGroup(groupName string) bool {
	groupName = strings.TrimSpace(groupName)
	if groupName == "" || groupName == "auto" {
		return false
	}
	if _, ok := setting.GetUserUsableGroupsCopy()[groupName]; ok {
		return true
	}
	return ratio_setting.ContainsGroupRatio(groupName)
}

func GetGroupAccessPolicy(groupName string) (GroupAccessPolicy, error) {
	groupName = strings.TrimSpace(groupName)
	if groupName == "" {
		return GroupAccessPolicy{}, errors.New("group_name is required")
	}
	if DB == nil {
		return GroupAccessPolicy{}, errors.New("database is not initialized")
	}
	var policy GroupAccessPolicy
	err := DB.Where("group_name = ?", groupName).First(&policy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return emptyGroupAccessPolicy(groupName), nil
	}
	if err != nil {
		return GroupAccessPolicy{}, err
	}
	normalized, err := NormalizeGroupAccessPolicy(policy)
	if err != nil {
		return GroupAccessPolicy{}, err
	}
	normalized.Id = policy.Id
	normalized.CreatedAt = policy.CreatedAt
	normalized.UpdatedAt = policy.UpdatedAt
	return normalized, nil
}

func GetCachedGroupAccessPolicy(groupName string) (GroupAccessPolicy, error) {
	groupName = strings.TrimSpace(groupName)
	if groupName == "" {
		return GroupAccessPolicy{}, errors.New("group_name is required")
	}
	if common.RedisEnabled && common.RDB == nil {
		return GroupAccessPolicy{}, errors.New("Redis client is not initialized")
	}

	if common.RedisAvailable() {
		key := groupAccessPolicyCacheKey(groupName)
		encoded, err := common.RDB.Get(common.RDB.Context(), key).Result()
		if err == nil {
			var policy GroupAccessPolicy
			if unmarshalErr := common.UnmarshalJsonStr(encoded, &policy); unmarshalErr == nil {
				if normalized, normalizeErr := NormalizeGroupAccessPolicy(policy); normalizeErr == nil && normalized.GroupName == groupName {
					normalized.Id = policy.Id
					normalized.CreatedAt = policy.CreatedAt
					normalized.UpdatedAt = policy.UpdatedAt
					return normalized, nil
				}
			}
		} else if !errors.Is(err, redis.Nil) {
			common.SysLog(fmt.Sprintf("group access policy Redis read failed: %v", err))
		}

		policy, dbErr := GetGroupAccessPolicy(groupName)
		if dbErr != nil {
			return GroupAccessPolicy{}, dbErr
		}
		if cacheErr := setGroupAccessPolicyRedisCache(key, policy); cacheErr != nil {
			common.SysLog(fmt.Sprintf("group access policy Redis write failed: %v", cacheErr))
		}
		return policy, nil
	}

	if cached, ok := groupAccessPolicyLocalCache.Load(groupName); ok {
		entry := cached.(groupAccessPolicyLocalCacheEntry)
		if time.Now().Before(entry.expiresAt) {
			return entry.policy, nil
		}
		groupAccessPolicyLocalCache.Delete(groupName)
	}
	policy, err := GetGroupAccessPolicy(groupName)
	if err != nil {
		return GroupAccessPolicy{}, err
	}
	groupAccessPolicyLocalCache.Store(groupName, groupAccessPolicyLocalCacheEntry{
		policy:    policy,
		expiresAt: time.Now().Add(groupAccessPolicyCacheTTL),
	})
	return policy, nil
}

type groupAccessPolicyLocalCacheEntry struct {
	policy    GroupAccessPolicy
	expiresAt time.Time
}

func ReplaceGroupAccessPolicy(policy GroupAccessPolicy) (GroupAccessPolicy, error) {
	if !IsConfiguredGroupAccessPolicyGroup(policy.GroupName) {
		return GroupAccessPolicy{}, errors.New("group is not configured")
	}
	if DB == nil {
		return GroupAccessPolicy{}, errors.New("database is not initialized")
	}
	normalized, err := NormalizeGroupAccessPolicy(policy)
	if err != nil {
		return GroupAccessPolicy{}, err
	}
	for _, groupName := range normalized.BlockedGroups {
		if !IsConfiguredGroupAccessPolicyGroup(groupName) {
			return GroupAccessPolicy{}, fmt.Errorf("blocked group %q is not configured", groupName)
		}
	}

	if err := DB.Transaction(func(tx *gorm.DB) error {
		var current GroupAccessPolicy
		findErr := tx.Where("group_name = ?", normalized.GroupName).First(&current).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			return tx.Create(&normalized).Error
		}
		if findErr != nil {
			return findErr
		}
		channels, valueErr := normalized.BlockedChannelIDs.Value()
		if valueErr != nil {
			return valueErr
		}
		models, valueErr := normalized.BlockedModels.Value()
		if valueErr != nil {
			return valueErr
		}
		groups, valueErr := normalized.BlockedGroups.Value()
		if valueErr != nil {
			return valueErr
		}
		return tx.Model(&current).Updates(map[string]interface{}{
			"blocked_channel_ids":         channels,
			"blocked_models":              models,
			"blocked_groups":              groups,
			"content_moderation_disabled": normalized.ContentModerationDisabled,
		}).Error
	}); err != nil {
		return GroupAccessPolicy{}, err
	}

	if err := cacheGroupAccessPolicy(normalized); err != nil {
		return GroupAccessPolicy{}, fmt.Errorf("policy saved but cache refresh failed: %w", err)
	}
	return normalized, nil
}

func cacheGroupAccessPolicy(policy GroupAccessPolicy) error {
	if common.RedisAvailable() {
		key := groupAccessPolicyCacheKey(policy.GroupName)
		if err := setGroupAccessPolicyRedisCache(key, policy); err != nil {
			// Remove a stale value when refresh fails so the next request falls
			// back to the database instead of temporarily using the old policy.
			if deleteErr := common.RDB.Del(common.RDB.Context(), key).Err(); deleteErr != nil {
				return fmt.Errorf("refresh failed: %w; invalidate failed: %v", err, deleteErr)
			}
			return err
		}
		return nil
	}
	if common.RedisEnabled {
		return errors.New("Redis client is not initialized")
	}
	groupAccessPolicyLocalCache.Store(policy.GroupName, groupAccessPolicyLocalCacheEntry{
		policy:    policy,
		expiresAt: time.Now().Add(groupAccessPolicyCacheTTL),
	})
	return nil
}

func setGroupAccessPolicyRedisCache(key string, policy GroupAccessPolicy) error {
	encoded, err := common.Marshal(policy)
	if err != nil {
		return err
	}
	return common.RDB.Set(common.RDB.Context(), key, encoded, groupAccessPolicyCacheTTL).Err()
}

func groupAccessPolicyCacheKey(groupName string) string {
	return "groupAccessPolicy:" + groupName
}

// InvalidateGroupAccessPolicyCache removes one policy snapshot from all local
// and shared caches. A subsequent request reads the database-authoritative value.
func InvalidateGroupAccessPolicyCache(groupName string) error {
	groupName = strings.TrimSpace(groupName)
	if groupName == "" {
		return nil
	}
	groupAccessPolicyLocalCache.Delete(groupName)
	if !common.RedisAvailable() {
		if common.RedisEnabled {
			return errors.New("Redis client is not initialized")
		}
		return nil
	}
	return common.RDB.Del(common.RDB.Context(), groupAccessPolicyCacheKey(groupName)).Err()
}
