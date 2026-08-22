package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/QuantumNous/new-api/relaykit/dto"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"

	"github.com/go-redis/redis/v8"
	"github.com/samber/hot"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

const (
	ContentModerationOptionKey                 = model.ContentModerationOptionKey
	ContentModerationProtocolOpenAIChat        = "openai_chat"
	ContentModerationProtocolOpenAIResponses   = "openai_responses"
	ContentModerationProtocolAnthropic         = "anthropic_messages"
	ContentModerationProtocolGemini            = "gemini"
	contentModerationDefaultBaseURL            = "https://api.openai.com"
	contentModerationDefaultModel              = "omni-moderation-latest"
	contentModerationDefaultTimeout            = 1500
	contentModerationDefaultRetries            = 1
	contentModerationDefaultBlockCode          = http.StatusForbidden
	contentModerationDefaultMaxInFlight        = 1
	contentModerationDefaultQueueWaitMS        = 200
	contentModerationDefaultOverloadStatus     = http.StatusServiceUnavailable
	contentModerationDefaultKeyCooldownMS      = 5000
	contentModerationMaxInputRunes             = 16000
	contentModerationMaxInputImages            = 1
	contentModerationMaxCandidateImages        = 16
	contentModerationMaxImageBytes             = 20 << 20
	contentModerationMaxImageURLBytes          = 8 << 10
	contentModerationMaxKeys                   = 64
	contentModerationAffinityCacheNamespace    = "new-api:content_moderation_affinity:v2"
	contentModerationAffinityLeaseNamespace    = "new-api:content_moderation_affinity_lease:v1"
	contentModerationAllowCacheNamespace       = "new-api:content_moderation_allow:v1"
	contentModerationAllowLeaseNamespace       = "new-api:content_moderation_allow_lease:v1"
	contentModerationProviderSlotNamespace     = "new-api:content_moderation_provider_slot:v1"
	contentModerationProviderCooldownNamespace = "new-api:content_moderation_provider_cooldown:v1"
	contentModerationAffinityCacheCapacity     = 100_000
	contentModerationAllowCacheCapacity        = 100_000
	contentModerationAllowDedupMaxRunes        = 256
	contentModerationAllowDedupTTL             = 30 * time.Second
	contentModerationAffinityPollInterval      = 25 * time.Millisecond
	contentModerationRedisOperationTimeout     = 100 * time.Millisecond
	contentModerationMaxViolationWindowHours   = 24 * 365
	contentModerationEmailSendTimeout          = 15 * time.Second
	contentModerationConfigRefreshInterval     = time.Second
)

var contentModerationHTTPClient = &http.Client{}

var (
	ErrContentModerationConfigPersistence = errors.New("content moderation configuration persistence failed")
	ErrContentModerationCapacity          = errors.New("content moderation capacity exhausted")
)

type contentModerationEmailSendFunc func(context.Context, string, dto.Notify, int) error

var contentModerationEmailSender contentModerationEmailSendFunc = func(ctx context.Context, email string, notification dto.Notify, userID int) error {
	return sendEmailNotifyContext(ctx, email, notification, userID)
}

var (
	contentModerationAffinityCacheOnce sync.Once
	contentModerationAffinityCache     *cachex.HybridCache[contentModerationAffinityCacheEntry]
	contentModerationAffinityFlight    singleflight.Group
	contentModerationAllowCacheOnce    sync.Once
	contentModerationAllowCache        *cachex.HybridCache[bool]
	contentModerationAllowFlight       singleflight.Group
	contentModerationConfigCacheMu     sync.Mutex
	contentModerationConfigCacheRaw    string
	contentModerationConfigCacheValue  ContentModerationConfig
	contentModerationConfigCacheAt     time.Time
	contentModerationConfigRefresh     singleflight.Group
	contentModerationCapacityState     = contentModerationLocalCapacityState{
		inFlight:      make(map[string]int),
		cooldown:      make(map[string]time.Time),
		degradedUntil: time.Time{},
	}
)

var defaultContentModerationThresholds = map[string]float64{
	"harassment":             0.98,
	"harassment/threatening": 0.90,
	"hate":                   0.65,
	"hate/threatening":       0.65,
	"illicit":                0.95,
	"illicit/violent":        0.95,
	"self-harm":              0.65,
	"self-harm/intent":       0.85,
	"self-harm/instructions": 0.65,
	"sexual":                 0.65,
	"sexual/minors":          0.65,
	"violence":               0.95,
	"violence/graphic":       0.95,
}

// ContentModerationConfig is stored as one JSON value in the Option table.
// APIKey is accepted as a newline-separated list for compatibility with the
// admin UI; APIKeys is an input convenience and is normalized away before
// persistence.
type ContentModerationConfig struct {
	Enabled              bool               `json:"enabled"`
	Mode                 string             `json:"mode"`
	BaseURL              string             `json:"base_url"`
	Model                string             `json:"model"`
	APIKey               string             `json:"api_key,omitempty"`
	APIKeys              []string           `json:"api_keys,omitempty"`
	ClearAPIKeys         bool               `json:"clear_api_keys,omitempty"`
	Thresholds           map[string]float64 `json:"thresholds"`
	AllGroups            bool               `json:"all_groups"`
	GroupIDs             []string           `json:"group_ids,omitempty"`
	AllModels            bool               `json:"all_models"`
	Models               []string           `json:"models,omitempty"`
	ModelFilters         []string           `json:"model_filters,omitempty"`
	SampleRate           float64            `json:"sample_rate"`
	TimeoutMS            int                `json:"timeout_ms"`
	RetryCount           int                `json:"retry_count"`
	MaxInFlightPerKey    int                `json:"max_in_flight_per_key"`
	QueueWaitMS          int                `json:"queue_wait_ms"`
	// OverloadStatus is retained for configuration compatibility. Capacity
	// exhaustion is fail-open and never becomes a client-facing status.
	OverloadStatus       int                `json:"overload_status"`
	KeyCooldownMS        int                `json:"key_cooldown_ms"`
	RecordNonHits        bool               `json:"record_non_hits"`
	RecordLogs           bool               `json:"record_logs"`
	BlockStatus          int                `json:"block_status"`
	BlockMessage         string             `json:"block_message,omitempty"`
	EmailOnHit           bool               `json:"email_on_hit"`
	AutoBanEnabled       bool               `json:"auto_ban_enabled"`
	BanThreshold         int                `json:"ban_threshold"`
	ViolationWindowHours int                `json:"violation_window_hours"`
}

type ContentModerationConfigView struct {
	Enabled              bool               `json:"enabled"`
	Mode                 string             `json:"mode"`
	BaseURL              string             `json:"base_url"`
	Model                string             `json:"model"`
	APIKeyCount          int                `json:"api_key_count"`
	APIKeySuffixes       []string           `json:"api_key_suffixes,omitempty"`
	Thresholds           map[string]float64 `json:"thresholds"`
	AllGroups            bool               `json:"all_groups"`
	GroupIDs             []string           `json:"group_ids,omitempty"`
	AllModels            bool               `json:"all_models"`
	Models               []string           `json:"models,omitempty"`
	ModelFilters         []string           `json:"model_filters,omitempty"`
	SampleRate           float64            `json:"sample_rate"`
	TimeoutMS            int                `json:"timeout_ms"`
	RetryCount           int                `json:"retry_count"`
	MaxInFlightPerKey    int                `json:"max_in_flight_per_key"`
	QueueWaitMS          int                `json:"queue_wait_ms"`
	OverloadStatus       int                `json:"overload_status"`
	KeyCooldownMS        int                `json:"key_cooldown_ms"`
	RecordNonHits        bool               `json:"record_non_hits"`
	RecordLogs           bool               `json:"record_logs"`
	BlockStatus          int                `json:"block_status"`
	BlockMessage         string             `json:"block_message,omitempty"`
	EmailOnHit           bool               `json:"email_on_hit"`
	AutoBanEnabled       bool               `json:"auto_ban_enabled"`
	BanThreshold         int                `json:"ban_threshold"`
	ViolationWindowHours int                `json:"violation_window_hours"`
}

type ContentModerationRequest struct {
	UserID                 int
	Group                  string
	Model                  string
	Protocol               string
	RequestPath            string
	RequestID              string
	Body                   []byte
	Text                   string
	Images                 []string
	ContentValidationError *ContentModerationValidationError
	ContentValidated       bool
	Meta                   *relaytypes.TokenCountMeta
	AffinityRuleName       string
	AffinityKeyFingerprint string
	AffinityCacheIdentity  string
	AffinityTTLSeconds     int
	AffinityChannelID      int
}

type ContentModerationInput struct {
	Text            string
	Images          []string
	ValidationError *ContentModerationValidationError
	validated       bool
}

type ContentModerationValidationError struct {
	StatusCode int
	Message    string
}

func (input *ContentModerationInput) normalize() {
	if input == nil {
		return
	}
	if input.validated {
		return
	}
	input.Text = normalizeContentModerationText(input.Text)
	if input.ValidationError == nil {
		input.Images, input.ValidationError = normalizeContentModerationImages(input.Images)
	}
	input.validated = true
}

func (input ContentModerationInput) IsValidated() bool {
	return input.validated
}

func (input ContentModerationInput) isEmpty() bool {
	return input.ValidationError == nil && strings.TrimSpace(input.Text) == "" && len(input.Images) == 0
}

func (input ContentModerationInput) hash() string {
	input.normalize()
	digest := sha256.New()
	_, _ = digest.Write([]byte("text:"))
	_, _ = digest.Write([]byte(input.Text))
	for _, image := range input.Images {
		imageDigest := sha256.Sum256([]byte(image))
		_, _ = digest.Write([]byte("\nimage:"))
		_, _ = digest.Write([]byte(hex.EncodeToString(imageDigest[:])))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

type ContentModerationDecision struct {
	Checked        bool
	Cached         bool
	Flagged        bool
	Blocked        bool
	Overloaded     bool
	Category       string
	Score          float64
	CategoryScores map[string]float64
	Error          string
	Message        string
	LogID          int
	StatusCode     int
}

type contentModerationAffinityCacheEntry struct {
	Flagged     bool    `json:"flagged"`
	Category    string  `json:"category,omitempty"`
	Score       float64 `json:"score,omitempty"`
	LogID       int     `json:"log_id,omitempty"`
	SideEffects bool    `json:"side_effects"`
}

type contentModerationLease struct {
	Token string
	TTL   time.Duration
	Redis *redis.Client
}

type contentModerationLocalCapacityState struct {
	mu            sync.Mutex
	inFlight      map[string]int
	cooldown      map[string]time.Time
	degradedUntil time.Time
}

type contentModerationProviderCredential struct {
	APIKey      string
	Fingerprint string
}

type contentModerationProviderSlot struct {
	Fingerprint string
	Local       bool
	Redis       *redis.Client
	RedisKey    string
	Token       string
}

const contentModerationReleaseLeaseScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0`

const contentModerationRenewLeaseScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0`

type moderationAPIResult struct {
	Flagged        bool               `json:"flagged"`
	Categories     map[string]bool    `json:"categories"`
	CategoryScores map[string]float64 `json:"category_scores"`
}

type moderationAPIResponse struct {
	Results []moderationAPIResult `json:"results"`
}

type moderationAPIInputPart struct {
	Type     string                     `json:"type"`
	Text     string                     `json:"text,omitempty"`
	ImageURL *moderationAPIImageURLPart `json:"image_url,omitempty"`
}

type moderationAPIImageURLPart struct {
	URL string `json:"url"`
}

func defaultContentModerationConfig() ContentModerationConfig {
	return ContentModerationConfig{
		Mode:                 "observe",
		BaseURL:              contentModerationDefaultBaseURL,
		Model:                contentModerationDefaultModel,
		Thresholds:           cloneModerationThresholds(defaultContentModerationThresholds),
		AllGroups:            true,
		AllModels:            true,
		SampleRate:           1,
		TimeoutMS:            contentModerationDefaultTimeout,
		RetryCount:           contentModerationDefaultRetries,
		MaxInFlightPerKey:    contentModerationDefaultMaxInFlight,
		QueueWaitMS:          contentModerationDefaultQueueWaitMS,
		OverloadStatus:       contentModerationDefaultOverloadStatus,
		KeyCooldownMS:        contentModerationDefaultKeyCooldownMS,
		BlockStatus:          contentModerationDefaultBlockCode,
		BlockMessage:         "Request blocked by content policy",
		BanThreshold:         10,
		ViolationWindowHours: 24,
	}
}

func cloneModerationThresholds(input map[string]float64) map[string]float64 {
	if input == nil {
		return nil
	}
	output := make(map[string]float64, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneContentModerationConfig(input ContentModerationConfig) ContentModerationConfig {
	cloned := input
	cloned.APIKeys = append([]string(nil), input.APIKeys...)
	cloned.Thresholds = cloneModerationThresholds(input.Thresholds)
	cloned.GroupIDs = append([]string(nil), input.GroupIDs...)
	cloned.Models = append([]string(nil), input.Models...)
	cloned.ModelFilters = append([]string(nil), input.ModelFilters...)
	return cloned
}

func getContentModerationAffinityCache() *cachex.HybridCache[contentModerationAffinityCacheEntry] {
	contentModerationAffinityCacheOnce.Do(func() {
		contentModerationAffinityCache = cachex.NewHybridCache[contentModerationAffinityCacheEntry](cachex.HybridCacheConfig[contentModerationAffinityCacheEntry]{
			Namespace:  cachex.Namespace(contentModerationAffinityCacheNamespace),
			Redis:      common.RDB,
			RedisCodec: cachex.JSONCodec[contentModerationAffinityCacheEntry]{},
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			Memory: func() *hot.HotCache[string, contentModerationAffinityCacheEntry] {
				return hot.NewHotCache[string, contentModerationAffinityCacheEntry](hot.LRU, contentModerationAffinityCacheCapacity).
					WithTTL(time.Hour).
					WithJanitor().
					Build()
			},
		})
	})
	return contentModerationAffinityCache
}

func getContentModerationAllowCache() *cachex.HybridCache[bool] {
	contentModerationAllowCacheOnce.Do(func() {
		contentModerationAllowCache = cachex.NewHybridCache[bool](cachex.HybridCacheConfig[bool]{
			Namespace:  cachex.Namespace(contentModerationAllowCacheNamespace),
			Redis:      common.RDB,
			RedisCodec: cachex.JSONCodec[bool]{},
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			Memory: func() *hot.HotCache[string, bool] {
				return hot.NewHotCache[string, bool](hot.LRU, contentModerationAllowCacheCapacity).
					WithTTL(contentModerationAllowDedupTTL).
					WithJanitor().
					Build()
			},
		})
	})
	return contentModerationAllowCache
}

func contentModerationAffinityCacheKey(input ContentModerationRequest, configs ...ContentModerationConfig) string {
	if (input.AffinityCacheIdentity == "" && input.AffinityKeyFingerprint == "") || input.AffinityTTLSeconds <= 0 || input.AffinityChannelID <= 0 {
		return ""
	}
	// Keep moderation affinity at channel scope; multi-key credentials can rotate
	// within a channel and must not cause the same conversation to be re-audited.
	affinityIdentity := input.AffinityCacheIdentity
	if affinityIdentity == "" {
		affinityIdentity = input.AffinityKeyFingerprint
	}
	policyFingerprint := ""
	if len(configs) > 0 {
		policyFingerprint = contentModerationPolicyFingerprint(configs[0])
	}
	seed := fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s", input.UserID, input.Group, input.Model, input.Protocol, input.AffinityRuleName, input.AffinityChannelID, affinityIdentity, policyFingerprint)
	digest := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(digest[:])
}

func contentModerationAllowCacheKey(input ContentModerationRequest, config ContentModerationConfig, content ContentModerationInput) string {
	content.normalize()
	if input.UserID <= 0 || content.ValidationError != nil || len(content.Images) > 0 || content.Text == "" {
		return ""
	}
	if len([]rune(content.Text)) > contentModerationAllowDedupMaxRunes {
		return ""
	}
	seed := fmt.Sprintf(
		"%d\x00%s\x00%s\x00%s\x00%s",
		input.UserID,
		strings.TrimSpace(input.Group),
		strings.TrimSpace(input.Protocol),
		content.hash(),
		contentModerationPolicyFingerprint(config),
	)
	digest := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(digest[:])
}

func contentModerationPolicyFingerprint(config ContentModerationConfig) string {
	policy := struct {
		Enabled              bool               `json:"enabled"`
		Mode                 string             `json:"mode"`
		BaseURL              string             `json:"base_url"`
		Model                string             `json:"model"`
		Thresholds           map[string]float64 `json:"thresholds"`
		AllGroups            bool               `json:"all_groups"`
		GroupIDs             []string           `json:"group_ids"`
		AllModels            bool               `json:"all_models"`
		Models               []string           `json:"models"`
		ModelFilters         []string           `json:"model_filters"`
		SampleRate           float64            `json:"sample_rate"`
		TimeoutMS            int                `json:"timeout_ms"`
		RetryCount           int                `json:"retry_count"`
		MaxInFlightPerKey    int                `json:"max_in_flight_per_key"`
		QueueWaitMS          int                `json:"queue_wait_ms"`
		OverloadStatus       int                `json:"overload_status"`
		KeyCooldownMS        int                `json:"key_cooldown_ms"`
		RecordNonHits        bool               `json:"record_non_hits"`
		RecordLogs           bool               `json:"record_logs"`
		BlockStatus          int                `json:"block_status"`
		BlockMessage         string             `json:"block_message"`
		EmailOnHit           bool               `json:"email_on_hit"`
		AutoBanEnabled       bool               `json:"auto_ban_enabled"`
		BanThreshold         int                `json:"ban_threshold"`
		ViolationWindowHours int                `json:"violation_window_hours"`
	}{
		Enabled: config.Enabled, Mode: config.Mode, BaseURL: config.BaseURL, Model: config.Model,
		Thresholds: cloneModerationThresholds(config.Thresholds), AllGroups: config.AllGroups,
		GroupIDs: append([]string(nil), config.GroupIDs...), AllModels: config.AllModels,
		Models: append([]string(nil), config.Models...), ModelFilters: append([]string(nil), config.ModelFilters...),
		SampleRate: config.SampleRate, TimeoutMS: config.TimeoutMS, RetryCount: config.RetryCount,
		MaxInFlightPerKey: config.MaxInFlightPerKey, QueueWaitMS: config.QueueWaitMS,
		OverloadStatus: config.OverloadStatus, KeyCooldownMS: config.KeyCooldownMS,
		RecordNonHits: config.RecordNonHits, RecordLogs: config.RecordLogs, BlockStatus: config.BlockStatus,
		BlockMessage: config.BlockMessage, EmailOnHit: config.EmailOnHit, AutoBanEnabled: config.AutoBanEnabled,
		BanThreshold: config.BanThreshold, ViolationWindowHours: config.ViolationWindowHours,
	}
	raw, _ := common.Marshal(policy)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func contentModerationAffinityCacheTTL(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	// Channel-affinity settings are operator-controlled, but cap the duration
	// before converting to time.Duration so a malformed large value cannot
	// overflow into a negative Redis TTL.
	const maxSeconds = int((365 * 24 * time.Hour) / time.Second)
	if seconds > maxSeconds {
		seconds = maxSeconds
	}
	return time.Duration(seconds) * time.Second
}

func contentModerationLeaseTTL(config ContentModerationConfig) time.Duration {
	ttl := time.Duration(config.TimeoutMS)*time.Millisecond + 5*time.Second
	if ttl < 5*time.Second {
		return 5 * time.Second
	}
	if ttl > 125*time.Second {
		return 125 * time.Second
	}
	return ttl
}

func contentModerationAffinityLeaseKey(cacheKey string) string {
	return cachex.Namespace(contentModerationAffinityLeaseNamespace).FullKey(cacheKey)
}

func contentModerationAllowLeaseKey(cacheKey string) string {
	return cachex.Namespace(contentModerationAllowLeaseNamespace).FullKey(cacheKey)
}

func tryAcquireContentModerationLease(ctx context.Context, client *redis.Client, leaseKey string, config ContentModerationConfig) (contentModerationLease, bool, error) {
	lease := contentModerationLease{Token: common.NewRequestId(), TTL: contentModerationLeaseTTL(config), Redis: client}
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		return lease, false, errors.New("redis is not initialized")
	}
	acquired, err := client.SetNX(ctx, leaseKey, lease.Token, lease.TTL).Result()
	return lease, acquired, err
}

func releaseContentModerationLease(leaseKey string, lease contentModerationLease) {
	if lease.Redis == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := lease.Redis.Eval(ctx, contentModerationReleaseLeaseScript, []string{leaseKey}, lease.Token).Err(); err != nil && !errors.Is(err, redis.Nil) {
		common.SysLog("failed to release content moderation affinity lease: " + err.Error())
	}
}

func renewContentModerationLease(leaseKey string, lease contentModerationLease) (bool, error) {
	if lease.Redis == nil {
		return false, errors.New("redis is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := lease.Redis.Eval(ctx, contentModerationRenewLeaseScript, []string{leaseKey}, lease.Token, lease.TTL.Milliseconds()).Int64()
	return result == 1, err
}

func keepContentModerationLeaseAlive(leaseKey string, lease contentModerationLease) func() {
	interval := lease.TTL / 3
	if interval <= 0 {
		interval = time.Second
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				renewed, err := renewContentModerationLease(leaseKey, lease)
				if err != nil {
					common.SysLog("failed to renew content moderation affinity lease: " + err.Error())
					continue
				}
				if !renewed {
					return
				}
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func runContentModerationWithLease(
	ctx context.Context,
	config ContentModerationConfig,
	leaseKey string,
	leaseLabel string,
	getCached func() (*ContentModerationDecision, bool),
	check func() *ContentModerationDecision,
) *ContentModerationDecision {
	client := common.RDB
	if !common.RedisEnabled || client == nil {
		return check()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		lease, acquired, err := tryAcquireContentModerationLease(ctx, client, leaseKey, config)
		if err != nil {
			common.SysLog("failed to acquire content moderation " + leaseLabel + " lease; using local singleflight: " + err.Error())
			return check()
		}
		if acquired {
			defer releaseContentModerationLease(leaseKey, lease)
			stopRenewal := keepContentModerationLeaseAlive(leaseKey, lease)
			defer stopRenewal()
			if cachedDecision, found := getCached(); found {
				return cachedDecision
			}
			return check()
		}

		ticker := time.NewTicker(contentModerationAffinityPollInterval)
		deadline := time.NewTimer(contentModerationLeaseTTL(config))
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				if !deadline.Stop() {
					select {
					case <-deadline.C:
					default:
					}
				}
				return &ContentModerationDecision{Checked: true, Error: ctx.Err().Error()}
			case <-deadline.C:
				ticker.Stop()
				goto retryLease
			case <-ticker.C:
				if cachedDecision, found := getCached(); found {
					ticker.Stop()
					if !deadline.Stop() {
						select {
						case <-deadline.C:
						default:
						}
					}
					return cachedDecision
				}
				exists, existsErr := client.Exists(ctx, leaseKey).Result()
				if existsErr != nil {
					ticker.Stop()
					if !deadline.Stop() {
						select {
						case <-deadline.C:
						default:
						}
					}
					common.SysLog("failed to wait for content moderation " + leaseLabel + " lease; using local singleflight: " + existsErr.Error())
					return check()
				}
				if exists == 0 {
					ticker.Stop()
					if !deadline.Stop() {
						select {
						case <-deadline.C:
						default:
						}
					}
					goto retryLease
				}
			}
		}
	retryLease:
	}
}

func runContentModerationWithAffinityLease(ctx context.Context, input ContentModerationRequest, config ContentModerationConfig, cacheKey string, check func() *ContentModerationDecision) *ContentModerationDecision {
	return runContentModerationWithLease(
		ctx,
		config,
		contentModerationAffinityLeaseKey(cacheKey),
		"affinity",
		func() (*ContentModerationDecision, bool) {
			return getCachedContentModerationDecision(input, config)
		},
		check,
	)
}

func runContentModerationWithAllowLease(ctx context.Context, config ContentModerationConfig, cacheKey string, check func() *ContentModerationDecision) *ContentModerationDecision {
	return runContentModerationWithLease(
		ctx,
		config,
		contentModerationAllowLeaseKey(cacheKey),
		"allow de-duplication",
		func() (*ContentModerationDecision, bool) {
			return getCachedContentModerationAllowDecision(cacheKey)
		},
		check,
	)
}

func getCachedContentModerationDecision(input ContentModerationRequest, config ContentModerationConfig) (*ContentModerationDecision, bool) {
	key := contentModerationAffinityCacheKey(input, config)
	if key == "" {
		return nil, false
	}
	entry, found, err := getContentModerationAffinityCache().Get(key)
	if err != nil {
		common.SysLog("failed to read content moderation affinity cache: " + err.Error())
		return nil, false
	}
	if !found {
		return nil, false
	}
	decision := &ContentModerationDecision{
		Checked:  true,
		Cached:   true,
		Flagged:  entry.Flagged,
		Category: entry.Category,
		Score:    entry.Score,
	}
	if entry.Flagged && !entry.SideEffects && entry.LogID > 0 {
		entryLog, err := model.GetContentModerationLog(entry.LogID)
		if err == nil {
			if applyContentModerationSideEffects(input, config, entryLog) {
				entry.SideEffects = true
				if ttl := contentModerationAffinityCacheTTL(input.AffinityTTLSeconds); ttl > 0 {
					_ = getContentModerationAffinityCache().SetWithTTL(key, entry, ttl)
				}
			}
		}
	}
	if decision.Flagged && config.Mode == "pre_block" {
		decision.Blocked = true
		decision.StatusCode = config.BlockStatus
		decision.Message = config.BlockMessage
	}
	return decision, true
}

func cacheContentModerationDecision(input ContentModerationRequest, config ContentModerationConfig, decision ContentModerationDecision, sideEffects bool) {
	key := contentModerationAffinityCacheKey(input, config)
	ttl := contentModerationAffinityCacheTTL(input.AffinityTTLSeconds)
	if key == "" || ttl <= 0 {
		return
	}
	entry := contentModerationAffinityCacheEntry{
		Flagged:     decision.Flagged,
		Category:    decision.Category,
		Score:       decision.Score,
		LogID:       decision.LogID,
		SideEffects: sideEffects,
	}
	if err := getContentModerationAffinityCache().SetWithTTL(key, entry, ttl); err != nil {
		common.SysLog("failed to write content moderation affinity cache: " + err.Error())
	}
}

func getCachedContentModerationAllowDecision(cacheKey string) (*ContentModerationDecision, bool) {
	if cacheKey == "" {
		return nil, false
	}
	allowed, found, err := getContentModerationAllowCache().Get(cacheKey)
	if err != nil {
		common.SysLog("failed to read content moderation allow cache: " + err.Error())
		return nil, false
	}
	if !found || !allowed {
		return nil, false
	}
	return &ContentModerationDecision{Checked: true, Cached: true}, true
}

func cacheContentModerationAllowDecision(cacheKey string) {
	if cacheKey == "" {
		return
	}
	if err := getContentModerationAllowCache().SetWithTTL(cacheKey, true, contentModerationAllowDedupTTL); err != nil {
		common.SysLog("failed to write content moderation allow cache: " + err.Error())
	}
}

func clearContentModerationCaches() {
	if err := getContentModerationAffinityCache().Purge(); err != nil {
		common.SysLog("failed to clear content moderation affinity cache: " + err.Error())
	}
	if err := getContentModerationAllowCache().Purge(); err != nil {
		common.SysLog("failed to clear content moderation allow cache: " + err.Error())
	}
}

func normalizeModerationStringList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func moderationAPIKeys(config ContentModerationConfig) []string {
	values := make([]string, 0, len(config.APIKeys)+1)
	values = append(values, strings.Split(config.APIKey, "\n")...)
	values = append(values, config.APIKeys...)
	seen := make(map[string]struct{}, len(values))
	keys := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		keys = append(keys, value)
		if len(keys) == contentModerationMaxKeys {
			break
		}
	}
	return keys
}

func contentModerationProviderKeyFingerprint(config ContentModerationConfig, apiKey string) string {
	identity := apiKey
	if identity == "" {
		identity = "anonymous\x00" + config.BaseURL + "\x00" + config.Model
	}
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:])
}

func contentModerationProviderCredentials(config ContentModerationConfig) []contentModerationProviderCredential {
	keys := moderationAPIKeys(config)
	if len(keys) == 0 {
		return []contentModerationProviderCredential{{
			Fingerprint: contentModerationProviderKeyFingerprint(config, ""),
		}}
	}
	credentials := make([]contentModerationProviderCredential, 0, len(keys))
	for _, apiKey := range keys {
		credentials = append(credentials, contentModerationProviderCredential{
			APIKey:      apiKey,
			Fingerprint: contentModerationProviderKeyFingerprint(config, apiKey),
		})
	}
	return credentials
}

func contentModerationProviderLeaseTTL(config ContentModerationConfig) time.Duration {
	ttl := time.Duration(config.TimeoutMS+config.QueueWaitMS)*time.Millisecond + 5*time.Second
	if ttl < 5*time.Second {
		return 5 * time.Second
	}
	if ttl > 135*time.Second {
		return 135 * time.Second
	}
	return ttl
}

func contentModerationProviderSlotKey(fingerprint string, slot int) string {
	return cachex.Namespace(contentModerationProviderSlotNamespace).FullKey(fmt.Sprintf("%s:%d", fingerprint, slot))
}

func contentModerationProviderCooldownKey(fingerprint string) string {
	return cachex.Namespace(contentModerationProviderCooldownNamespace).FullKey(fingerprint)
}

func contentModerationProviderCapacityDegraded() bool {
	contentModerationCapacityState.mu.Lock()
	defer contentModerationCapacityState.mu.Unlock()
	return time.Now().Before(contentModerationCapacityState.degradedUntil)
}

func markContentModerationProviderCapacityDegraded() {
	contentModerationCapacityState.mu.Lock()
	contentModerationCapacityState.degradedUntil = time.Now().Add(time.Second)
	contentModerationCapacityState.mu.Unlock()
}

func tryAcquireLocalContentModerationProviderSlot(fingerprint string, limit int) bool {
	contentModerationCapacityState.mu.Lock()
	defer contentModerationCapacityState.mu.Unlock()
	if contentModerationCapacityState.inFlight[fingerprint] >= limit {
		return false
	}
	contentModerationCapacityState.inFlight[fingerprint]++
	return true
}

func releaseLocalContentModerationProviderSlot(fingerprint string) {
	contentModerationCapacityState.mu.Lock()
	defer contentModerationCapacityState.mu.Unlock()
	remaining := contentModerationCapacityState.inFlight[fingerprint] - 1
	if remaining <= 0 {
		delete(contentModerationCapacityState.inFlight, fingerprint)
		return
	}
	contentModerationCapacityState.inFlight[fingerprint] = remaining
}

func contentModerationProviderKeyCoolingDown(ctx context.Context, fingerprint string) (bool, bool) {
	now := time.Now()
	contentModerationCapacityState.mu.Lock()
	localUntil := contentModerationCapacityState.cooldown[fingerprint]
	if !localUntil.IsZero() && !now.Before(localUntil) {
		delete(contentModerationCapacityState.cooldown, fingerprint)
		localUntil = time.Time{}
	}
	contentModerationCapacityState.mu.Unlock()
	if !localUntil.IsZero() {
		return true, false
	}
	if !common.RedisEnabled || common.RDB == nil {
		return false, false
	}
	if contentModerationProviderCapacityDegraded() {
		return false, true
	}
	exists, err := common.RDB.Exists(ctx, contentModerationProviderCooldownKey(fingerprint)).Result()
	if err != nil {
		markContentModerationProviderCapacityDegraded()
		return false, true
	}
	return exists > 0, false
}

func markContentModerationProviderKeyCooldown(config ContentModerationConfig, fingerprint string) {
	cooldown := time.Duration(config.KeyCooldownMS) * time.Millisecond
	contentModerationCapacityState.mu.Lock()
	contentModerationCapacityState.cooldown[fingerprint] = time.Now().Add(cooldown)
	contentModerationCapacityState.mu.Unlock()
	if !common.RedisEnabled || common.RDB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), contentModerationRedisOperationTimeout)
	defer cancel()
	if err := common.RDB.Set(ctx, contentModerationProviderCooldownKey(fingerprint), "1", cooldown).Err(); err != nil {
		common.SysLog("content moderation capacity control degraded to local key cooldown")
	}
}

func tryAcquireContentModerationProviderSlot(ctx context.Context, config ContentModerationConfig, credential contentModerationProviderCredential) (contentModerationProviderSlot, bool, bool) {
	if !tryAcquireLocalContentModerationProviderSlot(credential.Fingerprint, config.MaxInFlightPerKey) {
		return contentModerationProviderSlot{}, false, false
	}
	slot := contentModerationProviderSlot{Fingerprint: credential.Fingerprint, Local: true}
	if !common.RedisEnabled || common.RDB == nil {
		return slot, true, false
	}
	if contentModerationProviderCapacityDegraded() {
		return slot, true, true
	}
	token := common.NewRequestId()
	ttl := contentModerationProviderLeaseTTL(config)
	for slotIndex := 0; slotIndex < config.MaxInFlightPerKey; slotIndex++ {
		leaseKey := contentModerationProviderSlotKey(credential.Fingerprint, slotIndex)
		acquired, err := common.RDB.SetNX(ctx, leaseKey, token, ttl).Result()
		if err != nil {
			markContentModerationProviderCapacityDegraded()
			return slot, true, true
		}
		if acquired {
			slot.Redis = common.RDB
			slot.RedisKey = leaseKey
			slot.Token = token
			return slot, true, false
		}
	}
	releaseLocalContentModerationProviderSlot(credential.Fingerprint)
	return contentModerationProviderSlot{}, false, false
}

func (slot contentModerationProviderSlot) release() {
	if slot.Redis != nil && slot.RedisKey != "" && slot.Token != "" {
		ctx, cancel := context.WithTimeout(context.Background(), contentModerationRedisOperationTimeout)
		if err := slot.Redis.Eval(ctx, contentModerationReleaseLeaseScript, []string{slot.RedisKey}, slot.Token).Err(); err != nil {
			common.SysLog("failed to release content moderation provider capacity lease")
		}
		cancel()
	}
	if slot.Local {
		releaseLocalContentModerationProviderSlot(slot.Fingerprint)
	}
}

func contentModerationProviderCredentialStart(content ContentModerationInput, count int) int {
	if count <= 1 {
		return 0
	}
	digest := sha256.Sum256([]byte(content.hash()))
	return int(binary.BigEndian.Uint64(digest[:8]) % uint64(count))
}

func acquireContentModerationProviderSlot(ctx context.Context, config ContentModerationConfig, content ContentModerationInput, attempt int) (contentModerationProviderCredential, contentModerationProviderSlot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	credentials := contentModerationProviderCredentials(config)
	start := (contentModerationProviderCredentialStart(content, len(credentials)) + attempt) % len(credentials)
	deadline := time.Now().Add(time.Duration(config.QueueWaitMS) * time.Millisecond)
	capacityContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	degradationLogged := false
	for {
		if ctx.Err() != nil {
			return contentModerationProviderCredential{}, contentModerationProviderSlot{}, ctx.Err()
		}
		for offset := 0; offset < len(credentials); offset++ {
			credential := credentials[(start+offset)%len(credentials)]
			coolingDown, degraded := contentModerationProviderKeyCoolingDown(capacityContext, credential.Fingerprint)
			if degraded && !degradationLogged {
				common.SysLog("content moderation capacity control degraded to local limiter")
				degradationLogged = true
			}
			if coolingDown {
				continue
			}
			slot, acquired, degraded := tryAcquireContentModerationProviderSlot(capacityContext, config, credential)
			if degraded && !degradationLogged {
				common.SysLog("content moderation capacity control degraded to local limiter")
				degradationLogged = true
			}
			if acquired {
				return credential, slot, nil
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return contentModerationProviderCredential{}, contentModerationProviderSlot{}, fmt.Errorf("%w after %dms", ErrContentModerationCapacity, config.QueueWaitMS)
		}
		wait := contentModerationAffinityPollInterval
		if remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-capacityContext.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return contentModerationProviderCredential{}, contentModerationProviderSlot{}, ctx.Err()
			}
			return contentModerationProviderCredential{}, contentModerationProviderSlot{}, fmt.Errorf("%w after %dms", ErrContentModerationCapacity, config.QueueWaitMS)
		case <-timer.C:
		}
	}
}

func resetContentModerationCapacityState() {
	contentModerationCapacityState.mu.Lock()
	contentModerationCapacityState.inFlight = make(map[string]int)
	contentModerationCapacityState.cooldown = make(map[string]time.Time)
	contentModerationCapacityState.degradedUntil = time.Time{}
	contentModerationCapacityState.mu.Unlock()
}

func NormalizeContentModerationConfig(input ContentModerationConfig) (ContentModerationConfig, error) {
	config := input
	config.Mode = strings.ToLower(strings.TrimSpace(config.Mode))
	if config.Mode == "" {
		config.Mode = "observe"
	}
	if config.Mode != "observe" && config.Mode != "pre_block" {
		return ContentModerationConfig{}, errors.New("mode must be observe or pre_block")
	}

	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if config.BaseURL == "" {
		config.BaseURL = contentModerationDefaultBaseURL
	}
	parsedURL, err := url.Parse(config.BaseURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return ContentModerationConfig{}, errors.New("base_url must be an HTTP or HTTPS URL")
	}
	if config.Model = strings.TrimSpace(config.Model); config.Model == "" {
		config.Model = contentModerationDefaultModel
	}

	keys := moderationAPIKeys(config)
	config.APIKey = strings.Join(keys, "\n")
	config.APIKeys = nil

	if config.Thresholds == nil {
		config.Thresholds = cloneModerationThresholds(defaultContentModerationThresholds)
	} else {
		normalizedThresholds := cloneModerationThresholds(defaultContentModerationThresholds)
		for category, threshold := range config.Thresholds {
			category = strings.TrimSpace(category)
			if category == "" || math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0 || threshold > 1 {
				return ContentModerationConfig{}, fmt.Errorf("threshold for %q must be between 0 and 1", category)
			}
			normalizedThresholds[category] = threshold
		}
		config.Thresholds = normalizedThresholds
	}

	config.GroupIDs = normalizeModerationStringList(config.GroupIDs)
	if !config.AllModels && len(config.Models) == 0 && len(config.ModelFilters) == 0 {
		// Keep the upgrade-compatible default when all_models was omitted from an
		// older configuration payload.
		config.AllModels = true
	}
	config.Models = normalizeModerationStringList(config.Models)
	config.ModelFilters = normalizeModerationStringList(config.ModelFilters)
	if !config.AllGroups && len(config.GroupIDs) == 0 {
		// An omitted all_groups field should retain the safe default. An explicit
		// empty scope is not useful and would silently disable the feature.
		config.AllGroups = true
	}

	if config.SampleRate <= 0 {
		config.SampleRate = 1
	}
	if config.SampleRate > 1 && config.SampleRate <= 100 {
		config.SampleRate /= 100
	}
	if config.SampleRate > 1 || math.IsNaN(config.SampleRate) || math.IsInf(config.SampleRate, 0) {
		return ContentModerationConfig{}, errors.New("sample_rate must be between 0 and 1")
	}
	if config.TimeoutMS <= 0 {
		config.TimeoutMS = contentModerationDefaultTimeout
	}
	if config.TimeoutMS > 120000 {
		return ContentModerationConfig{}, errors.New("timeout_ms must not exceed 120000")
	}
	if config.RetryCount < 0 {
		return ContentModerationConfig{}, errors.New("retry_count must not be negative")
	}
	if config.RetryCount == 0 {
		config.RetryCount = contentModerationDefaultRetries
	}
	if config.RetryCount > 5 {
		return ContentModerationConfig{}, errors.New("retry_count must not exceed 5")
	}
	if config.MaxInFlightPerKey <= 0 {
		config.MaxInFlightPerKey = contentModerationDefaultMaxInFlight
	}
	if config.QueueWaitMS <= 0 {
		config.QueueWaitMS = contentModerationDefaultQueueWaitMS
	}
	if config.QueueWaitMS > 10000 {
		return ContentModerationConfig{}, errors.New("queue_wait_ms must not exceed 10000")
	}
	if config.OverloadStatus == 0 {
		config.OverloadStatus = contentModerationDefaultOverloadStatus
	}
	if config.OverloadStatus != http.StatusTooManyRequests && config.OverloadStatus != http.StatusServiceUnavailable {
		return ContentModerationConfig{}, errors.New("overload_status must be 429 or 503")
	}
	if config.KeyCooldownMS == 0 {
		config.KeyCooldownMS = contentModerationDefaultKeyCooldownMS
	}
	if config.KeyCooldownMS < 100 || config.KeyCooldownMS > 300000 {
		return ContentModerationConfig{}, errors.New("key_cooldown_ms must be between 100 and 300000")
	}
	if config.BlockStatus == 0 {
		config.BlockStatus = contentModerationDefaultBlockCode
	} else if config.BlockStatus < 400 || config.BlockStatus > 599 {
		return ContentModerationConfig{}, errors.New("block_status must be between 400 and 599")
	}
	if strings.TrimSpace(config.BlockMessage) == "" {
		config.BlockMessage = "Request blocked by content policy"
	}
	if config.BanThreshold < 0 {
		return ContentModerationConfig{}, errors.New("ban_threshold must not be negative")
	}
	if config.BanThreshold == 0 {
		config.BanThreshold = 10
	}
	if config.ViolationWindowHours < 0 {
		return ContentModerationConfig{}, errors.New("violation_window_hours must not be negative")
	}
	if config.ViolationWindowHours == 0 {
		config.ViolationWindowHours = 24
	}
	if config.ViolationWindowHours > contentModerationMaxViolationWindowHours {
		return ContentModerationConfig{}, fmt.Errorf("violation_window_hours must not exceed %d", contentModerationMaxViolationWindowHours)
	}
	if config.RecordLogs {
		config.RecordNonHits = true
		config.RecordLogs = false
	}
	config.ClearAPIKeys = false
	return config, nil
}

func readContentModerationConfig() (ContentModerationConfig, error) {
	defaults := defaultContentModerationConfig()
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[ContentModerationOptionKey]
	common.OptionMapRWMutex.RUnlock()

	contentModerationConfigCacheMu.Lock()
	cachedAvailable := !contentModerationConfigCacheAt.IsZero()
	cachedRaw := contentModerationConfigCacheRaw
	cachedConfig := cloneContentModerationConfig(contentModerationConfigCacheValue)
	if raw != cachedRaw {
		contentModerationConfigCacheMu.Unlock()
		return parseAndCacheContentModerationConfig(raw, defaults)
	}
	if cachedAvailable && time.Since(contentModerationConfigCacheAt) < contentModerationConfigRefreshInterval {
		contentModerationConfigCacheMu.Unlock()
		return cachedConfig, nil
	}
	contentModerationConfigCacheMu.Unlock()

	result, err, _ := contentModerationConfigRefresh.Do("config", func() (interface{}, error) {
		refreshRaw := raw
		loadedFromDatabase := false
		if model.DB != nil {
			var option model.Option
			if err := model.DB.First(&option, "key = ?", ContentModerationOptionKey).Error; err == nil {
				refreshRaw = option.Value
				loadedFromDatabase = true
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return ContentModerationConfig{}, fmt.Errorf("load content moderation config: %w", err)
			}
		}
		config, err := parseAndCacheContentModerationConfig(refreshRaw, defaults)
		if err != nil {
			return ContentModerationConfig{}, err
		}
		if loadedFromDatabase {
			common.OptionMapRWMutex.Lock()
			common.OptionMap[ContentModerationOptionKey] = refreshRaw
			common.OptionMapRWMutex.Unlock()
		}
		return config, nil
	})
	if err != nil {
		if cachedAvailable {
			contentModerationConfigCacheMu.Lock()
			if contentModerationConfigCacheRaw == cachedRaw {
				contentModerationConfigCacheAt = time.Now()
			} else {
				cachedConfig = cloneContentModerationConfig(contentModerationConfigCacheValue)
			}
			contentModerationConfigCacheMu.Unlock()
			common.SysLog("failed to refresh content moderation config; using last known policy: " + err.Error())
			return cachedConfig, nil
		}
		return ContentModerationConfig{}, err
	}
	return cloneContentModerationConfig(result.(ContentModerationConfig)), nil
}

func parseAndCacheContentModerationConfig(raw string, defaults ContentModerationConfig) (ContentModerationConfig, error) {
	var config ContentModerationConfig
	if strings.TrimSpace(raw) == "" {
		config = defaults
	} else {
		var stored ContentModerationConfig
		if err := common.UnmarshalJsonStr(raw, &stored); err != nil {
			return ContentModerationConfig{}, fmt.Errorf("invalid content moderation config: %w", err)
		}
		normalized, err := NormalizeContentModerationConfig(stored)
		if err != nil {
			return ContentModerationConfig{}, err
		}
		config = normalized
	}
	contentModerationConfigCacheMu.Lock()
	contentModerationConfigCacheRaw = raw
	contentModerationConfigCacheValue = cloneContentModerationConfig(config)
	contentModerationConfigCacheAt = time.Now()
	contentModerationConfigCacheMu.Unlock()
	return cloneContentModerationConfig(config), nil
}

func GetContentModerationConfig() ContentModerationConfig {
	config, err := readContentModerationConfig()
	if err != nil {
		return defaultContentModerationConfig()
	}
	return config
}

// ContentModerationEnabled lets request handlers skip input extraction while
// preserving configuration errors for fail-open observability.
func ContentModerationEnabled() (bool, error) {
	config, err := readContentModerationConfig()
	if err != nil {
		return false, err
	}
	return config.Enabled, nil
}

func IsContentModerationEnabled() bool {
	enabled, _ := ContentModerationEnabled()
	return enabled
}

func GetContentModerationConfigView() ContentModerationConfigView {
	config := GetContentModerationConfig()
	keys := moderationAPIKeys(config)
	suffixes := make([]string, 0, len(keys))
	for _, key := range keys {
		if len(key) <= 4 {
			suffixes = append(suffixes, "****")
			continue
		}
		suffixes = append(suffixes, "..."+key[len(key)-4:])
	}
	return ContentModerationConfigView{
		Enabled:              config.Enabled,
		Mode:                 config.Mode,
		BaseURL:              config.BaseURL,
		Model:                config.Model,
		APIKeyCount:          len(keys),
		APIKeySuffixes:       suffixes,
		Thresholds:           cloneModerationThresholds(config.Thresholds),
		AllGroups:            config.AllGroups,
		GroupIDs:             append([]string(nil), config.GroupIDs...),
		AllModels:            config.AllModels,
		Models:               append([]string(nil), config.Models...),
		ModelFilters:         append([]string(nil), config.ModelFilters...),
		SampleRate:           config.SampleRate,
		TimeoutMS:            config.TimeoutMS,
		RetryCount:           config.RetryCount,
		MaxInFlightPerKey:    config.MaxInFlightPerKey,
		QueueWaitMS:          config.QueueWaitMS,
		OverloadStatus:       config.OverloadStatus,
		KeyCooldownMS:        config.KeyCooldownMS,
		RecordNonHits:        config.RecordNonHits,
		RecordLogs:           config.RecordLogs,
		BlockStatus:          config.BlockStatus,
		BlockMessage:         config.BlockMessage,
		EmailOnHit:           config.EmailOnHit,
		AutoBanEnabled:       config.AutoBanEnabled,
		BanThreshold:         config.BanThreshold,
		ViolationWindowHours: config.ViolationWindowHours,
	}
}

func UpdateContentModerationConfig(input ContentModerationConfig) error {
	normalized, err := NormalizeContentModerationConfig(input)
	if err != nil {
		return err
	}
	raw, err := common.Marshal(normalized)
	if err != nil {
		return err
	}
	if err := model.UpdateOption(ContentModerationOptionKey, string(raw)); err != nil {
		return fmt.Errorf("%w: %v", ErrContentModerationConfigPersistence, err)
	}
	clearContentModerationCaches()
	return nil
}

func shouldModerateContent(config ContentModerationConfig, input ContentModerationRequest, content ContentModerationInput) bool {
	if !contentModerationInScope(config, input, content) {
		return false
	}
	if config.SampleRate >= 1 {
		return true
	}
	if config.SampleRate <= 0 {
		return false
	}
	seed := input.RequestID + "\x00" + input.Model + "\x00" + input.Group + "\x00" + content.hash()
	affinityIdentity := input.AffinityCacheIdentity
	if affinityIdentity == "" {
		affinityIdentity = input.AffinityKeyFingerprint
	}
	if affinityIdentity != "" {
		seed = affinityIdentity + "\x00" + input.Model + "\x00" + input.Group
	}
	digest := sha256.Sum256([]byte(seed))
	value := float64(binary.BigEndian.Uint64(digest[:8])) / float64(^uint64(0))
	return value < config.SampleRate
}

func contentModerationInScope(config ContentModerationConfig, input ContentModerationRequest, content ContentModerationInput) bool {
	if !config.Enabled || content.isEmpty() {
		return false
	}
	if !config.AllGroups {
		groupMatched := false
		for _, group := range config.GroupIDs {
			if group == strings.TrimSpace(input.Group) {
				groupMatched = true
				break
			}
		}
		if !groupMatched {
			return false
		}
	}
	filters := append(append([]string(nil), config.Models...), config.ModelFilters...)
	if !config.AllModels {
		modelMatched := false
		for _, filter := range filters {
			matched, err := path.Match(filter, input.Model)
			if (err == nil && matched) || filter == input.Model {
				modelMatched = true
				break
			}
		}
		if !modelMatched {
			return false
		}
	}
	return true
}

func CheckContentModeration(ctx context.Context, input ContentModerationRequest) (*ContentModerationDecision, error) {
	decision := &ContentModerationDecision{}
	config, configErr := readContentModerationConfig()
	if configErr != nil {
		decision.Error = configErr.Error()
		return decision, nil
	}
	content := ContentModerationInput{
		Text:            input.Text,
		Images:          input.Images,
		ValidationError: input.ContentValidationError,
		validated:       input.ContentValidated,
	}
	content.normalize()
	if content.isEmpty() {
		content = ExtractContentModerationInput(input.Meta, input.Body, input.Protocol)
	}
	if !contentModerationInScope(config, input, content) {
		return decision, nil
	}
	if content.ValidationError != nil {
		decision := &ContentModerationDecision{
			Checked:    true,
			Message:    content.ValidationError.Message,
			StatusCode: content.ValidationError.StatusCode,
		}
		if config.Mode == "pre_block" {
			decision.Blocked = true
		} else {
			decision.Error = content.ValidationError.Message
		}
		return decision, nil
	}
	content.Images = limitContentModerationImages(content.Images)
	allowCacheKey := contentModerationAllowCacheKey(input, config, content)
	if cachedDecision, found := getCachedContentModerationDecision(input, config); found {
		return cachedDecision, nil
	}
	if cachedDecision, found := getCachedContentModerationAllowDecision(allowCacheKey); found {
		cacheContentModerationDecision(input, config, *cachedDecision, true)
		return cachedDecision, nil
	}
	if !shouldModerateContent(config, input, content) {
		return decision, nil
	}
	check := func() *ContentModerationDecision {
		if cachedDecision, found := getCachedContentModerationDecision(input, config); found {
			return cachedDecision
		}
		if cachedDecision, found := getCachedContentModerationAllowDecision(allowCacheKey); found {
			cacheContentModerationDecision(input, config, *cachedDecision, true)
			return cachedDecision
		}
		decision := &ContentModerationDecision{Checked: true}
		if ctx == nil {
			ctx = context.Background()
		}
		requestContext, cancel := context.WithTimeout(ctx, time.Duration(config.TimeoutMS)*time.Millisecond)
		defer cancel()

		started := time.Now()
		scores, apiFlagged, callErr := callModeration(requestContext, config, content)
		latency := time.Since(started).Milliseconds()
		decision.CategoryScores = scores
		capacitySkipped := errors.Is(callErr, ErrContentModerationCapacity)
		if callErr != nil {
			decision.Error = callErr.Error()
		}
		flagged, category, score := EvaluateContentModerationScores(scores, config.Thresholds)
		if !flagged && apiFlagged && len(scores) == 0 {
			flagged = true
		}
		decision.Flagged = flagged
		decision.Category = category
		decision.Score = score
		decision.Blocked = flagged && config.Mode == "pre_block"
		if decision.Blocked {
			decision.StatusCode = config.BlockStatus
			decision.Message = config.BlockMessage
		}
		if capacitySkipped {
			decision.Overloaded = true
			// Moderation is an observability and policy aid, not a dependency of
			// the model relay. Capacity exhaustion must therefore fail open in
			// every mode; the skipped_capacity audit row remains the operator
			// signal that this request was not reviewed.
			decision.Blocked = false
			decision.StatusCode = 0
			decision.Message = ""
		}

		logComplete := true
		sideEffectsComplete := true
		if capacitySkipped || flagged || config.RecordNonHits {
			categoryScores, _ := common.Marshal(scores)
			action := moderationAction(config.Mode, flagged)
			if capacitySkipped {
				action = "skipped_capacity"
			}
			entry := &model.ContentModerationLog{
				UserID:         input.UserID,
				GroupName:      strings.TrimSpace(input.Group),
				ModelName:      strings.TrimSpace(input.Model),
				Protocol:       strings.TrimSpace(input.Protocol),
				RequestPath:    strings.TrimSpace(input.RequestPath),
				RequestID:      strings.TrimSpace(input.RequestID),
				Mode:           config.Mode,
				Action:         action,
				Flagged:        flagged,
				Blocked:        decision.Blocked,
				Category:       category,
				Score:          score,
				CategoryScores: string(categoryScores),
				Excerpt:        redactedModerationExcerpt(content.Text),
				ExcerptHash:    content.hash(),
				LatencyMS:      latency,
			}
			if callErr != nil {
				entry.Error = callErr.Error()
			}
			if err := model.CreateContentModerationLog(entry); err != nil {
				logComplete = false
				if decision.Error == "" {
					decision.Error = err.Error()
				} else {
					decision.Error += "; log: " + err.Error()
				}
			} else {
				decision.LogID = entry.ID
				if flagged {
					sideEffectsComplete = applyContentModerationSideEffects(input, config, entry)
				}
			}
		}
		if callErr == nil && logComplete {
			cacheContentModerationDecision(input, config, *decision, sideEffectsComplete)
			if !decision.Flagged && !apiFlagged && len(scores) > 0 {
				cacheContentModerationAllowDecision(allowCacheKey)
			}
		}
		return decision
	}

	runAllowDedup := func() *ContentModerationDecision {
		if allowCacheKey == "" {
			return check()
		}
		result, _, _ := contentModerationAllowFlight.Do(allowCacheKey, func() (interface{}, error) {
			return runContentModerationWithAllowLease(ctx, config, allowCacheKey, check), nil
		})
		return result.(*ContentModerationDecision)
	}

	affinityCacheKey := contentModerationAffinityCacheKey(input, config)
	if affinityCacheKey == "" {
		return runAllowDedup(), nil
	}
	result, _, _ := contentModerationAffinityFlight.Do(affinityCacheKey, func() (interface{}, error) {
		decision := runContentModerationWithAffinityLease(ctx, input, config, affinityCacheKey, runAllowDedup)
		if decision.Checked && decision.Error == "" && !decision.Flagged {
			cacheContentModerationDecision(input, config, *decision, true)
		}
		return decision, nil
	})
	return result.(*ContentModerationDecision), nil
}

func applyContentModerationSideEffects(input ContentModerationRequest, config ContentModerationConfig, entry *model.ContentModerationLog) bool {
	if input.UserID <= 0 || entry == nil {
		return false
	}
	count, countErr := model.CountFlaggedContentModerationByUserSince(input.UserID, time.Now().Add(-time.Duration(config.ViolationWindowHours)*time.Hour))
	if countErr != nil {
		common.SysLog("failed to count content moderation violations: " + countErr.Error())
		return false
	}
	if config.AutoBanEnabled && countErr == nil && count >= int64(config.BanThreshold) {
		if _, err := model.DisableUserWithAuthVersion(input.UserID, "content_moderation_auto_ban"); err != nil {
			common.SysLog(fmt.Sprintf("failed to auto-ban content moderation user %d: %v", input.UserID, err))
			return false
		}
		if err := model.InvalidateUserTokensCache(input.UserID); err != nil {
			common.SysLog(fmt.Sprintf("failed to invalidate content moderation user %d token cache after auto-ban: %v", input.UserID, err))
		}
	}
	if !config.EmailOnHit {
		return true
	}
	if entry.EmailSent {
		return true
	}
	user, err := model.GetUserById(input.UserID, false)
	if err != nil {
		return false
	}
	userSetting := user.GetSetting()
	email := strings.TrimSpace(userSetting.NotificationEmail)
	if email == "" {
		email = strings.TrimSpace(user.Email)
	}
	if email == "" {
		return true
	}
	lang := i18n.ResolveUserLang(user.Id)
	data := map[string]any{
		"SystemName": common.SystemName,
		"Category":   entry.Category,
		"Score":      fmt.Sprintf("%.3f", entry.Score),
		"Count":      count,
		"Threshold":  config.BanThreshold,
	}
	notification := dto.NewNotifyWithData(
		"content_moderation",
		i18n.TranslateTemplate(lang, i18n.MsgNotifyContentModerationSubject),
		i18n.TranslateTemplate(lang, i18n.MsgNotifyContentModerationBody),
		data,
	)
	if email == "" {
		return true
	}

	claimToken := common.NewRequestId()
	claimed, claimErr := model.ClaimContentModerationEmail(entry.ID, claimToken)
	if claimErr != nil {
		common.SysLog("failed to claim content moderation email: " + claimErr.Error())
		return false
	}
	if !claimed {
		latest, err := model.GetContentModerationLog(entry.ID)
		if err == nil && latest.EmailSent {
			entry.EmailSent = true
			return true
		}
		return false
	}

	sender := contentModerationEmailSender
	go sendClaimedContentModerationEmail(sender, email, notification, user.Id, entry.ID, claimToken, input, config)
	return false
}

func sendClaimedContentModerationEmail(sender contentModerationEmailSendFunc, email string, notification dto.Notify, userID int, logID int, claimToken string, input ContentModerationRequest, config ContentModerationConfig) {
	defer func() {
		if recovered := recover(); recovered != nil {
			// A panic can happen after SMTP accepted the message. Keep the durable
			// claim so another node cannot issue an unsafe duplicate delivery.
			common.SysLog(fmt.Sprintf("content moderation email worker panicked: %v", recovered))
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), contentModerationEmailSendTimeout)
	defer cancel()
	if err := sender(ctx, email, notification, userID); err != nil {
		if common.IsEmailDeliveryRetrySafe(err) {
			if releaseErr := model.ReleaseContentModerationEmailClaim(logID, claimToken); releaseErr != nil {
				common.SysLog("failed to release content moderation email claim: " + releaseErr.Error())
			}
		}
		common.SysLog("failed to send content moderation email: " + err.Error())
		return
	}
	if err := model.MarkContentModerationEmailSent(logID, claimToken); err != nil {
		// Keep the claim for operator review if SMTP succeeded but the completion
		// write failed; retrying automatically could duplicate mail.
		common.SysLog("content moderation email sent but completion write failed: " + err.Error())
		return
	}
	key := contentModerationAffinityCacheKey(input, config)
	if key == "" {
		return
	}
	entry, found, err := getContentModerationAffinityCache().Get(key)
	if err != nil || !found || entry.LogID != logID {
		return
	}
	entry.SideEffects = true
	if ttl := contentModerationAffinityCacheTTL(input.AffinityTTLSeconds); ttl > 0 {
		if err := getContentModerationAffinityCache().SetWithTTL(key, entry, ttl); err != nil {
			common.SysLog("failed to update content moderation affinity side effects: " + err.Error())
		}
	}
}

func moderationAction(mode string, flagged bool) string {
	if !flagged {
		return "allow"
	}
	if mode == "pre_block" {
		return "block"
	}
	return "observe"
}

func redactedModerationExcerpt(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return "[redacted]"
}

func EvaluateContentModerationScores(scores, thresholds map[string]float64) (bool, string, float64) {
	if len(scores) == 0 {
		return false, "", 0
	}
	categories := make([]string, 0, len(scores))
	for category := range scores {
		categories = append(categories, category)
	}
	sort.Strings(categories)
	flagged := false
	bestCategory := ""
	bestScore := 0.0
	for _, category := range categories {
		score := scores[category]
		if math.IsNaN(score) || math.IsInf(score, 0) {
			continue
		}
		threshold := 0.65
		if configured, ok := thresholds[category]; ok {
			threshold = configured
		}
		if score < threshold {
			continue
		}
		if !flagged || score > bestScore {
			bestCategory = category
			bestScore = score
		}
		flagged = true
	}
	return flagged, bestCategory, bestScore
}

func callModeration(ctx context.Context, config ContentModerationConfig, content ContentModerationInput) (map[string]float64, bool, error) {
	payloadInput, expectedResults, err := buildContentModerationAPIInput(content)
	if err != nil {
		return nil, false, err
	}
	payload, err := common.Marshal(map[string]any{
		"model": config.Model,
		"input": payloadInput,
	})
	if err != nil {
		return nil, false, err
	}
	endpoint := strings.TrimRight(config.BaseURL, "/") + "/v1/moderations"
	attempts := config.RetryCount + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		credential, slot, err := acquireContentModerationProviderSlot(ctx, config, content, attempt)
		if err != nil {
			return nil, false, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
		if err != nil {
			slot.release()
			return nil, false, err
		}
		request.Header.Set("Content-Type", "application/json")
		if credential.APIKey != "" {
			request.Header.Set("Authorization", "Bearer "+credential.APIKey)
		}
		response, err := contentModerationHTTPClient.Do(request)
		if err != nil {
			slot.release()
			markContentModerationProviderKeyCooldown(config, credential.Fingerprint)
			var requestErr *url.Error
			if errors.As(err, &requestErr) {
				// url.Error includes the complete request URL. Only retain the
				// transport cause so credentials accidentally placed in BaseURL
				// cannot be copied into logs or persisted moderation errors.
				lastErr = fmt.Errorf("moderation request failed: %w", requestErr.Err)
			} else {
				lastErr = errors.New("moderation request failed")
			}
			if ctx.Err() != nil {
				return nil, false, lastErr
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		response.Body.Close()
		slot.release()
		if readErr != nil {
			markContentModerationProviderKeyCooldown(config, credential.Fingerprint)
			lastErr = fmt.Errorf("read moderation response failed: %w", readErr)
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			lastErr = fmt.Errorf("moderation API returned status %d", response.StatusCode)
			if response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests {
				return nil, false, lastErr
			}
			markContentModerationProviderKeyCooldown(config, credential.Fingerprint)
			continue
		}
		var parsed moderationAPIResponse
		if err := common.Unmarshal(body, &parsed); err != nil {
			return nil, false, fmt.Errorf("decode moderation response failed: %w", err)
		}
		if len(parsed.Results) == 0 {
			return nil, false, errors.New("moderation API response has no results")
		}
		if len(parsed.Results) != expectedResults {
			return nil, false, fmt.Errorf("moderation API response result count %d does not match input count %d", len(parsed.Results), expectedResults)
		}
		scores := make(map[string]float64)
		flagged := false
		for _, result := range parsed.Results {
			flagged = flagged || result.Flagged
			for category, score := range result.CategoryScores {
				if current, exists := scores[category]; !exists || score > current {
					scores[category] = score
				}
			}
			for category, categoryFlagged := range result.Categories {
				if categoryFlagged {
					if _, exists := scores[category]; !exists {
						scores[category] = 1
					}
				}
			}
		}
		return scores, flagged, nil
	}
	if lastErr == nil {
		lastErr = errors.New("moderation API request failed")
	}
	return nil, false, lastErr
}

func buildContentModerationAPIInput(content ContentModerationInput) (any, int, error) {
	content.normalize()
	textInputs := splitContentModerationInputs(content.Text)
	images := limitContentModerationImages(content.Images)
	if len(images) == 0 {
		if len(textInputs) == 0 {
			return nil, 0, errors.New("moderation input is empty")
		}
		if len(textInputs) == 1 {
			return textInputs[0], 1, nil
		}
		return textInputs, len(textInputs), nil
	}
	parts := make([]moderationAPIInputPart, 0, len(textInputs)+len(images))
	for _, text := range textInputs {
		parts = append(parts, moderationAPIInputPart{Type: "text", Text: text})
	}
	for _, image := range images {
		parts = append(parts, moderationAPIInputPart{
			Type:     "image_url",
			ImageURL: &moderationAPIImageURLPart{URL: image},
		})
	}
	return parts, 1, nil
}

func limitContentModerationImages(images []string) []string {
	if len(images) <= contentModerationMaxInputImages {
		return images
	}
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(images))))
	if err != nil {
		return images[:contentModerationMaxInputImages]
	}
	return []string{images[index.Int64()]}
}

// ExtractContentModerationInput extracts only the latest user turn. The typed
// token metadata remains a fallback for request formats that do not expose a
// standard JSON message array or when the body is unavailable.
func ExtractContentModerationInput(meta *relaytypes.TokenCountMeta, body []byte, protocol string) ContentModerationInput {
	if len(body) > 0 {
		if input, recognized := extractLatestUserInput(body, protocol); recognized {
			input.normalize()
			return input
		}
	}
	if meta == nil {
		return ContentModerationInput{}
	}
	input := ContentModerationInput{Text: meta.CombineText}
	for _, file := range meta.Files {
		if file == nil || file.FileType != relaytypes.FileTypeImage || file.Source == nil {
			continue
		}
		if file.Source.IsURL() {
			input.Images = append(input.Images, file.Source.GetRawData())
			continue
		}
		if source, ok := file.Source.(*relaytypes.Base64Source); ok {
			if strings.HasPrefix(strings.ToLower(source.Base64Data), "data:image/") {
				input.Images = append(input.Images, source.Base64Data)
			} else if strings.HasPrefix(strings.ToLower(source.MimeType), "image/") {
				input.Images = append(input.Images, "data:"+source.MimeType+";base64,"+source.Base64Data)
			}
		}
	}
	input.normalize()
	return input
}

// ExtractContentModerationContent reads the already decoded relay request. Relay
// handlers call this after validation, so moderation does not materialize a
// disk-backed body again and remains aligned with protocol DTOs.
func ExtractContentModerationContent(request dto.Request, protocol string) ContentModerationInput {
	if request == nil {
		return ContentModerationInput{}
	}
	var input ContentModerationInput
	switch value := request.(type) {
	case *dto.GeneralOpenAIRequest:
		if len(value.Messages) > 0 && strings.EqualFold(value.Messages[len(value.Messages)-1].Role, "user") {
			input = moderationInputFromTypedContent(value.Messages[len(value.Messages)-1].Content)
		}
	case *dto.ClaudeRequest:
		if len(value.Messages) > 0 && strings.EqualFold(value.Messages[len(value.Messages)-1].Role, "user") {
			input = moderationInputFromTypedContent(value.Messages[len(value.Messages)-1].Content)
		}
	case *dto.GeminiChatRequest:
		if len(value.Contents) > 0 {
			latest := value.Contents[len(value.Contents)-1]
			role := strings.ToLower(strings.TrimSpace(latest.Role))
			if role != "" && role != "user" {
				break
			}
			for _, part := range latest.Parts {
				if strings.TrimSpace(part.Text) != "" && !strings.HasPrefix(strings.TrimSpace(part.Text), "<system-reminder>") {
					input.Text += "\n" + part.Text
				}
				if part.InlineData != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(part.InlineData.MimeType)), "image/") && strings.TrimSpace(part.InlineData.Data) != "" {
					input.Images = append(input.Images, "data:"+strings.TrimSpace(part.InlineData.MimeType)+";base64,"+strings.TrimSpace(part.InlineData.Data))
				}
				if part.FileData != nil {
					addContentModerationFileImage(&input.Images, part.FileData.MimeType, part.FileData.FileUri)
				}
			}
		}
	case *dto.OpenAIResponsesRequest:
		input = extractLatestResponsesInput(value.Input)
	case *dto.OpenAIResponsesCompactionRequest:
		input = extractLatestResponsesInput(value.Input)
	}
	input.normalize()
	return input
}

func ExtractContentModerationText(request dto.Request, protocol string) string {
	return ExtractContentModerationContent(request, protocol).Text
}

func moderationInputFromTypedContent(content any) ContentModerationInput {
	if content == nil {
		return ContentModerationInput{}
	}
	if text, ok := content.(string); ok {
		return ContentModerationInput{Text: text}
	}
	raw, err := common.Marshal(content)
	if err != nil || !gjson.ValidBytes(raw) {
		return ContentModerationInput{}
	}
	return moderationInputFromValue(gjson.ParseBytes(raw))
}

func extractLatestResponsesInput(raw json.RawMessage) ContentModerationInput {
	if len(raw) == 0 {
		return ContentModerationInput{}
	}
	root := gjson.ParseBytes(raw)
	if root.Type == gjson.String {
		return ContentModerationInput{Text: root.String()}
	}
	if !gjson.ValidBytes(raw) || !root.IsArray() {
		return ContentModerationInput{}
	}
	return latestResponsesArrayInput(root)
}

func normalizeContentModerationText(text string) string {
	text = strings.TrimSpace(text)
	return strings.Join(strings.Fields(text), " ")
}

func splitContentModerationInputs(text string) []string {
	runes := []rune(normalizeContentModerationText(text))
	if len(runes) == 0 {
		return nil
	}
	inputs := make([]string, 0, (len(runes)+contentModerationMaxInputRunes-1)/contentModerationMaxInputRunes)
	for start := 0; start < len(runes); start += contentModerationMaxInputRunes {
		end := start + contentModerationMaxInputRunes
		if end > len(runes) {
			end = len(runes)
		}
		inputs = append(inputs, string(runes[start:end]))
	}
	return inputs
}

func extractLatestUserInput(body []byte, protocol string) (ContentModerationInput, bool) {
	if !gjson.ValidBytes(body) {
		return ContentModerationInput{}, false
	}
	protocol = strings.ToLower(protocol)
	if strings.Contains(protocol, "gemini") {
		// Gemini omits role for user contents in some compatible clients; an
		// empty role is treated as user while model turns remain excluded.
		contents := gjson.GetBytes(body, "contents")
		if contents.Exists() {
			return latestUserArrayInput(contents, false), true
		}
	}
	messages := gjson.GetBytes(body, "messages")
	if messages.Exists() {
		return latestUserArrayInput(messages, true), true
	}
	if strings.Contains(protocol, "responses") || protocol == "" {
		input := gjson.GetBytes(body, "input")
		if input.Type == gjson.String {
			return ContentModerationInput{Text: input.String()}, true
		}
		if input.Exists() {
			return latestResponsesArrayInput(input), true
		}
	}
	if prompt := gjson.GetBytes(body, "prompt"); prompt.Type == gjson.String {
		return ContentModerationInput{Text: prompt.String()}, true
	}
	return ContentModerationInput{}, false
}

func latestUserArrayInput(value gjson.Result, requireUserRole bool) ContentModerationInput {
	if value.Type != gjson.JSON {
		return ContentModerationInput{}
	}
	items := value.Array()
	if len(items) == 0 {
		return ContentModerationInput{}
	}
	item := items[len(items)-1]
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	if requireUserRole && role != "user" {
		return ContentModerationInput{}
	}
	if !requireUserRole && role != "" && role != "user" {
		return ContentModerationInput{}
	}
	return moderationInputFromValue(item)
}

func latestResponsesArrayInput(value gjson.Result) ContentModerationInput {
	if value.Type != gjson.JSON || !value.IsArray() {
		return ContentModerationInput{}
	}
	items := value.Array()
	if len(items) == 0 {
		return ContentModerationInput{}
	}
	last := items[len(items)-1]
	role := strings.ToLower(strings.TrimSpace(last.Get("role").String()))
	typeName := strings.ToLower(strings.TrimSpace(last.Get("type").String()))
	if role != "" {
		if role != "user" || isContentModerationToolType(typeName) {
			return ContentModerationInput{}
		}
		return moderationInputFromValue(last)
	}
	if isContentModerationToolType(typeName) {
		return ContentModerationInput{}
	}
	if typeName == "message" || last.Get("content").Exists() {
		return ContentModerationInput{}
	}

	var input ContentModerationInput
	for _, item := range items {
		if !isDirectResponsesInputPart(strings.ToLower(strings.TrimSpace(item.Get("type").String()))) {
			return ContentModerationInput{}
		}
		input.append(moderationInputFromValue(item))
	}
	return input
}

func isDirectResponsesInputPart(typeName string) bool {
	switch typeName {
	case "input_text", "input_image", "input_file":
		return true
	default:
		return false
	}
}

func (input *ContentModerationInput) append(other ContentModerationInput) {
	if input == nil {
		return
	}
	if strings.TrimSpace(other.Text) != "" {
		if strings.TrimSpace(input.Text) != "" {
			input.Text += "\n"
		}
		input.Text += other.Text
	}
	input.Images = append(input.Images, other.Images...)
}

func moderationInputFromValue(value gjson.Result) ContentModerationInput {
	var input ContentModerationInput
	if !value.Exists() {
		return input
	}
	if value.IsArray() {
		for _, item := range value.Array() {
			input.append(moderationInputFromValue(item))
		}
		return input
	}
	switch value.Type {
	case gjson.String:
		text := strings.TrimSpace(value.String())
		if strings.HasPrefix(text, "<system-reminder>") || text == "" {
			return input
		}
		input.Text = text
	case gjson.JSON:
		typeName := strings.ToLower(strings.TrimSpace(value.Get("type").String()))
		if isContentModerationToolType(typeName) {
			return input
		}
		switch typeName {
		case "text", "input_text":
			input.append(moderationInputFromValue(value.Get("text")))
		case "image", "image_url", "input_image":
			input.Images = append(input.Images, moderationImagesFromValue(value, true)...)
		case "", "message":
			input.Images = append(input.Images, moderationImagesFromValue(value, false)...)
			input.append(moderationInputFromValue(value.Get("text")))
			input.append(moderationInputFromValue(value.Get("content")))
			input.append(moderationInputFromValue(value.Get("parts")))
		}
	}
	return input
}

func isContentModerationToolType(typeName string) bool {
	switch typeName {
	case "tool", "tool_call", "tool_result", "function", "function_call", "function_call_output", "function_response", "functionresponse", "computer_call", "computer_call_output":
		return true
	default:
		return false
	}
}

func moderationImagesFromValue(value gjson.Result, allowDirectURL bool) []string {
	if !value.IsObject() {
		return nil
	}
	images := make([]string, 0, 4)
	addContentModerationImage(&images, value.Get("image_url.url").String())
	addContentModerationImageResult(&images, value.Get("image_url"))
	if allowDirectURL {
		addContentModerationImage(&images, value.Get("url").String())
	}
	addContentModerationImage(&images, value.Get("source.url").String())
	addContentModerationImageData(&images, value.Get("source.media_type").String(), value.Get("source.data").String())
	addContentModerationImageData(&images, value.Get("source.mediaType").String(), value.Get("source.data").String())
	addContentModerationImageData(&images, value.Get("media_type").String(), value.Get("data").String())
	addContentModerationImageData(&images, value.Get("mime_type").String(), value.Get("data").String())
	addContentModerationImageData(&images, value.Get("mimeType").String(), value.Get("data").String())
	addContentModerationImageData(&images, value.Get("inline_data.mime_type").String(), value.Get("inline_data.data").String())
	addContentModerationImageData(&images, value.Get("inlineData.mimeType").String(), value.Get("inlineData.data").String())
	addContentModerationFileImage(&images, value.Get("file_data.mime_type").String(), value.Get("file_data.file_uri").String())
	addContentModerationFileImage(&images, value.Get("fileData.mimeType").String(), value.Get("fileData.fileUri").String())
	return images
}

func addContentModerationImageResult(images *[]string, value gjson.Result) {
	if value.Type == gjson.String {
		addContentModerationImage(images, value.String())
	}
}

func addContentModerationImageData(images *[]string, mimeType string, data string) {
	mimeType = strings.TrimSpace(mimeType)
	data = strings.TrimSpace(data)
	if strings.HasPrefix(strings.ToLower(data), "data:image/") {
		addContentModerationImage(images, data)
		return
	}
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") || data == "" {
		return
	}
	addContentModerationImage(images, "data:"+mimeType+";base64,"+data)
}

func addContentModerationFileImage(images *[]string, mimeType string, image string) {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if mimeType != "" && !strings.HasPrefix(mimeType, "image/") {
		return
	}
	addContentModerationImage(images, image)
}

func addContentModerationImage(images *[]string, image string) {
	image = strings.TrimSpace(image)
	if image == "" {
		return
	}
	*images = append(*images, image)
}

func normalizeContentModerationImages(images []string) ([]string, *ContentModerationValidationError) {
	normalized := make([]string, 0, len(images))
	seen := make(map[[sha256.Size]byte]struct{}, len(images))
	for _, image := range images {
		image = strings.TrimSpace(image)
		if image == "" {
			continue
		}
		if validationErr := validateContentModerationImage(image); validationErr != nil {
			return nil, validationErr
		}
		fingerprint := sha256.Sum256([]byte(image))
		if _, ok := seen[fingerprint]; ok {
			continue
		}
		if len(normalized) >= contentModerationMaxCandidateImages {
			return nil, &ContentModerationValidationError{StatusCode: http.StatusRequestEntityTooLarge, Message: "Too many images for content moderation"}
		}
		seen[fingerprint] = struct{}{}
		normalized = append(normalized, image)
	}
	return normalized, nil
}

func validateContentModerationImage(image string) *ContentModerationValidationError {
	lower := strings.ToLower(image)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		if len(image) > contentModerationMaxImageURLBytes {
			return &ContentModerationValidationError{StatusCode: http.StatusRequestEntityTooLarge, Message: "Image URL exceeds the content moderation limit"}
		}
		parsed, err := url.Parse(image)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return &ContentModerationValidationError{StatusCode: http.StatusBadRequest, Message: "Invalid image URL for content moderation"}
		}
		return nil
	}
	if !strings.HasPrefix(lower, "data:image/") {
		return &ContentModerationValidationError{StatusCode: http.StatusBadRequest, Message: "Unsupported image source for content moderation"}
	}

	header, encoded, found := strings.Cut(image, ",")
	if !found || encoded == "" {
		return &ContentModerationValidationError{StatusCode: http.StatusBadRequest, Message: "Invalid image data for content moderation"}
	}
	headerParts := strings.Split(strings.ToLower(header[len("data:"):]), ";")
	if len(headerParts) < 2 || !supportedContentModerationImageMIME(headerParts[0]) {
		return &ContentModerationValidationError{StatusCode: http.StatusBadRequest, Message: "Unsupported image format for content moderation"}
	}
	base64Encoded := false
	for _, parameter := range headerParts[1:] {
		if parameter == "base64" {
			base64Encoded = true
			break
		}
	}
	if !base64Encoded || strings.ContainsAny(encoded, " \t\r\n") {
		return &ContentModerationValidationError{StatusCode: http.StatusBadRequest, Message: "Invalid image data for content moderation"}
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(contentModerationMaxImageBytes) {
		return &ContentModerationValidationError{StatusCode: http.StatusRequestEntityTooLarge, Message: "Image exceeds the 20 MB content moderation limit"}
	}
	decoded, err := io.Copy(io.Discard, io.LimitReader(base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded)), contentModerationMaxImageBytes+1))
	if err != nil {
		return &ContentModerationValidationError{StatusCode: http.StatusBadRequest, Message: "Invalid image data for content moderation"}
	}
	if decoded > contentModerationMaxImageBytes {
		return &ContentModerationValidationError{StatusCode: http.StatusRequestEntityTooLarge, Message: "Image exceeds the 20 MB content moderation limit"}
	}
	return nil
}

func supportedContentModerationImageMIME(mimeType string) bool {
	switch mimeType {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}
