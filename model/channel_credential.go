package model

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	CredentialProxyModeInherit = "inherit"
	CredentialProxyModeDirect  = "direct"
	CredentialProxyModeCustom  = "custom"

	// Keep the longer names for callers that already adopted the proposal.
	ChannelCredentialProxyModeInherit = CredentialProxyModeInherit
	ChannelCredentialProxyModeDirect  = CredentialProxyModeDirect
	ChannelCredentialProxyModeCustom  = CredentialProxyModeCustom

	ChannelCredentialDisabledReasonLegacyRemoved = "legacy_key_removed"
)

var (
	ErrChannelCredentialInvalid          = errors.New("channel credential is invalid")
	ErrChannelCredentialProxyInvalid     = errors.New("channel credential proxy is invalid")
	ErrChannelCredentialRevisionInput    = errors.New("channel credential revision input is invalid")
	ErrChannelCredentialRevisionConflict = errors.New("channel credential revision conflict")
	ErrChannelCredentialNotFound         = errors.New("channel credential not found")
	ErrChannelCredentialSelectionEmpty   = errors.New("channel credential selection is empty")
)

// ChannelCredential is the durable identity of one upstream credential.
//
// Secret and ProxyURL are deliberately write-only at the JSON boundary. Callers
// that need to return credentials to an administrator must use PublicView,
// which contains only a fingerprint and a redacted proxy summary.
type ChannelCredential struct {
	Id                  int    `json:"credential_id" gorm:"primaryKey"`
	ChannelID           int    `json:"channel_id" gorm:"not null;index:idx_channel_credential_channel_position,priority:1;index:idx_channel_credential_channel_status,priority:1;index:idx_channel_credential_channel_fingerprint,priority:1"`
	Position            int    `json:"position" gorm:"not null;index:idx_channel_credential_channel_position,priority:2"`
	Secret              string `json:"-" gorm:"column:secret;type:text;not null"`
	Fingerprint         string `json:"fingerprint" gorm:"type:char(64);not null;index:idx_channel_credential_channel_fingerprint,priority:2"`
	Status              int    `json:"status" gorm:"not null;default:1;index:idx_channel_credential_channel_status,priority:2"`
	DisabledReason      string `json:"disabled_reason,omitempty" gorm:"type:varchar(255);not null;default:''"`
	DisabledAt          int64  `json:"disabled_at,omitempty" gorm:"bigint;not null;default:0"`
	ProxyMode           string `json:"proxy_mode" gorm:"type:varchar(16);not null;default:'inherit'"`
	ProxyURL            string `json:"-" gorm:"column:proxy_url;type:text;not null"`
	LastTestAt          int64  `json:"last_test_at,omitempty" gorm:"bigint;not null;default:0"`
	LastTestStatus      string `json:"last_test_status,omitempty" gorm:"type:varchar(32);not null;default:''"`
	LastTestLatencyMs   int64  `json:"last_test_latency_ms,omitempty" gorm:"bigint;not null;default:0"`
	LastTestErrorCode   string `json:"last_test_error_code,omitempty" gorm:"type:varchar(128);not null;default:''"`
	LastTestErrorClass  string `json:"last_test_error_class,omitempty" gorm:"type:varchar(64);not null;default:''"`
	ConsecutiveFailures int    `json:"consecutive_failures" gorm:"not null;default:0"`
	CreatedAt           int64  `json:"created_at" gorm:"bigint;not null;autoCreateTime"`
	UpdatedAt           int64  `json:"updated_at" gorm:"bigint;not null;autoUpdateTime"`
}

func (ChannelCredential) TableName() string {
	return "channel_credentials"
}

// ChannelCredentialRevision stores the optimistic-concurrency revision for a
// channel's credential set. It is separate from Channel so legacy channel JSON
// remains readable during the migration window.
type ChannelCredentialRevision struct {
	ChannelID    int   `json:"channel_id" gorm:"primaryKey"`
	KeysRevision int64 `json:"keys_revision" gorm:"not null;default:0"`
	UpdatedAt    int64 `json:"updated_at" gorm:"bigint;not null;autoUpdateTime"`
}

func (ChannelCredentialRevision) TableName() string {
	return "channel_credential_revisions"
}

// ChannelCredentialPublic is the safe administrator-facing representation.
// It intentionally has no Secret or full ProxyURL field.
type ChannelCredentialPublic struct {
	Id                  int    `json:"credential_id"`
	ChannelID           int    `json:"channel_id"`
	Position            int    `json:"position"`
	Fingerprint         string `json:"fingerprint"`
	Status              int    `json:"status"`
	DisabledReason      string `json:"disabled_reason,omitempty"`
	DisabledAt          int64  `json:"disabled_at,omitempty"`
	ProxyMode           string `json:"proxy_mode"`
	ProxySummary        string `json:"proxy_summary,omitempty"`
	LastTestAt          int64  `json:"last_test_at,omitempty"`
	LastTestStatus      string `json:"last_test_status,omitempty"`
	LastTestLatencyMs   int64  `json:"last_test_latency_ms,omitempty"`
	LastTestErrorCode   string `json:"last_test_error_code,omitempty"`
	LastTestErrorClass  string `json:"last_test_error_class,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
}

// NewChannelCredential creates a normalized credential without exposing the
// raw secret in any returned value.
func NewChannelCredential(channelID, position int, secret string) (*ChannelCredential, error) {
	credential := &ChannelCredential{
		ChannelID: channelID,
		Position:  position,
		Secret:    strings.TrimSpace(secret),
		Status:    common.ChannelStatusEnabled,
		ProxyMode: ChannelCredentialProxyModeInherit,
	}
	if err := credential.NormalizeForPersistence(); err != nil {
		return nil, err
	}
	return credential, nil
}

// ChannelCredentialFingerprint returns a stable one-way identifier for a
// secret. The domain prefix prevents accidental reuse as a generic hash.
func ChannelCredentialFingerprint(secret string) string {
	secret = strings.TrimSpace(secret)
	digest := common.Sha256Raw([]byte("new-api/channel-credential/v1:" + secret))
	return fmt.Sprintf("%x", digest)
}

// NormalizeForPersistence validates and normalizes fields before a credential
// is inserted or explicitly updated by a controller/service.
func (credential *ChannelCredential) NormalizeForPersistence() error {
	if credential == nil || credential.ChannelID <= 0 || credential.Position < 0 {
		return ErrChannelCredentialInvalid
	}
	credential.Secret = strings.TrimSpace(credential.Secret)
	if credential.Secret == "" {
		return ErrChannelCredentialInvalid
	}
	credential.Fingerprint = ChannelCredentialFingerprint(credential.Secret)
	if credential.Status == common.ChannelStatusUnknown {
		credential.Status = common.ChannelStatusEnabled
	}
	if credential.Status != common.ChannelStatusEnabled && credential.DisabledAt == 0 {
		credential.DisabledAt = common.GetTimestamp()
	}
	if credential.Status == common.ChannelStatusEnabled {
		credential.DisabledAt = 0
		credential.DisabledReason = ""
	}
	proxyMode, proxyURL, err := NormalizeChannelCredentialProxy(credential.ProxyMode, credential.ProxyURL)
	if err != nil {
		return err
	}
	credential.ProxyMode = proxyMode
	credential.ProxyURL = proxyURL
	return nil
}

// BeforeCreate protects direct GORM creates from accidentally persisting a
// credential with a mismatched fingerprint or invalid proxy mode.
func (credential *ChannelCredential) BeforeCreate(_ *gorm.DB) error {
	return credential.NormalizeForPersistence()
}

// NormalizeCredentialProxyMode provides a safe read boundary for relay code.
// Invalid persisted data falls back to inherit so legacy channel proxy behavior
// remains intact instead of unexpectedly forcing direct traffic.
func NormalizeCredentialProxyMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case CredentialProxyModeInherit, CredentialProxyModeDirect, CredentialProxyModeCustom:
		return mode
	default:
		return CredentialProxyModeInherit
	}
}

// NormalizeChannelCredentialProxy validates a proxy mode and returns the
// canonical write-only URL. Inherit and direct never persist a per-key URL.
func NormalizeChannelCredentialProxy(mode, rawProxyURL string) (string, string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = ChannelCredentialProxyModeInherit
	}
	rawProxyURL = strings.TrimSpace(rawProxyURL)
	switch mode {
	case ChannelCredentialProxyModeInherit:
		if rawProxyURL != "" {
			return "", "", ErrChannelCredentialProxyInvalid
		}
		return mode, "", nil
	case ChannelCredentialProxyModeDirect:
		if rawProxyURL != "" {
			return "", "", ErrChannelCredentialProxyInvalid
		}
		return mode, "", nil
	case ChannelCredentialProxyModeCustom:
		parsedURL, err := common.ParseProxyURLStrict(rawProxyURL)
		if err != nil || parsedURL == nil {
			return "", "", ErrChannelCredentialProxyInvalid
		}
		return mode, parsedURL.String(), nil
	default:
		return "", "", ErrChannelCredentialProxyInvalid
	}
}

// EffectiveProxyURL resolves the per-credential override against the legacy
// channel proxy. Environment proxy resolution remains the caller's concern.
func (credential ChannelCredential) EffectiveProxyURL(channelProxy string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(credential.ProxyMode)) {
	case ChannelCredentialProxyModeDirect:
		return "", nil
	case ChannelCredentialProxyModeCustom:
		_, proxyURL, err := NormalizeChannelCredentialProxy(ChannelCredentialProxyModeCustom, credential.ProxyURL)
		return proxyURL, err
	case "", ChannelCredentialProxyModeInherit:
		parsedURL, _, err := common.ParseProxyURLRuntime(channelProxy)
		if err != nil {
			return "", ErrChannelCredentialProxyInvalid
		}
		if parsedURL == nil {
			return "", nil
		}
		return parsedURL.String(), nil
	default:
		return "", ErrChannelCredentialProxyInvalid
	}
}

// ProxySummary strips userinfo and returns only a safe scheme/host/port view.
func (credential ChannelCredential) ProxySummary() string {
	if strings.ToLower(strings.TrimSpace(credential.ProxyMode)) != ChannelCredentialProxyModeCustom || strings.TrimSpace(credential.ProxyURL) == "" {
		return ""
	}
	parsedURL, err := common.ParseProxyURLStrict(credential.ProxyURL)
	if err != nil || parsedURL == nil {
		return ""
	}
	parsedURL.User = nil
	parsedURL.Path = ""
	parsedURL.RawPath = ""
	parsedURL.RawQuery = ""
	parsedURL.Fragment = ""
	return parsedURL.String()
}

func (credential ChannelCredential) PublicView() ChannelCredentialPublic {
	return ChannelCredentialPublic{
		Id:                  credential.Id,
		ChannelID:           credential.ChannelID,
		Position:            credential.Position,
		Fingerprint:         credential.Fingerprint,
		Status:              credential.Status,
		DisabledReason:      credential.DisabledReason,
		DisabledAt:          credential.DisabledAt,
		ProxyMode:           credential.ProxyMode,
		ProxySummary:        credential.ProxySummary(),
		LastTestAt:          credential.LastTestAt,
		LastTestStatus:      credential.LastTestStatus,
		LastTestLatencyMs:   credential.LastTestLatencyMs,
		LastTestErrorCode:   credential.LastTestErrorCode,
		LastTestErrorClass:  credential.LastTestErrorClass,
		ConsecutiveFailures: credential.ConsecutiveFailures,
	}
}

// CredentialForPosition resolves the stable credential record selected by the
// legacy key picker. During the compatibility window selection still returns a
// key position, while all new state is attached to this durable row.
func (channel *Channel) CredentialForPosition(position int) *ChannelCredential {
	if channel == nil || position < 0 {
		return nil
	}
	for index := range channel.Credentials {
		credential := &channel.Credentials[index]
		if credential.Position == position {
			return credential
		}
	}
	return nil
}

// CredentialForID resolves a loaded durable credential. Administrative probes
// use this identity instead of a mutable legacy key position.
func (channel *Channel) CredentialForID(credentialID int) *ChannelCredential {
	if channel == nil || credentialID <= 0 {
		return nil
	}
	for index := range channel.Credentials {
		credential := &channel.Credentials[index]
		if credential.Id == credentialID {
			return credential
		}
	}
	return nil
}

// ResolveSelectedChannelAccess chooses one currently enabled credential for a
// management or auxiliary request and resolves its effective proxy. These
// requests do not pass through the normal distributor context, so they must
// use the same credential/proxy pairing explicitly.
func ResolveSelectedChannelAccess(channel *Channel) (string, string, int, error) {
	if channel == nil {
		return "", "", 0, ErrChannelCredentialInvalid
	}
	key, position, apiErr := channel.GetNextEnabledKey()
	if apiErr != nil {
		return "", "", 0, apiErr
	}
	proxy := channel.GetSetting().Proxy
	credentialID := 0
	if channel.ChannelInfo.IsMultiKey {
		credential := channel.CredentialForPosition(position)
		if credential == nil {
			return key, proxy, 0, nil
		}
		effectiveProxy, err := credential.EffectiveProxyURL(proxy)
		if err != nil {
			return "", "", 0, err
		}
		proxy = effectiveProxy
		credentialID = credential.Id
	}
	return key, proxy, credentialID, nil
}

// ChannelCredentialStatusUpdate is the transactional input for batch enable
// and disable operations. The caller must use All explicitly; an empty list
// never means "all".
type ChannelCredentialStatusUpdate struct {
	ChannelID     int
	CredentialIDs []int
	Positions     []int
	All           bool
	Status        int
	Reason        string
	ExpectedRev   int64
}

// UpdateChannelCredentialStatuses changes selected credential states and the
// legacy channel aggregate in one transaction. The aggregate is kept in sync
// because the request selector still reads ChannelInfo during the migration.
func UpdateChannelCredentialStatuses(db *gorm.DB, input ChannelCredentialStatusUpdate) (int64, error) {
	if db == nil || input.ChannelID <= 0 {
		return 0, ErrChannelCredentialRevisionInput
	}
	if input.Status != common.ChannelStatusEnabled && input.Status != common.ChannelStatusManuallyDisabled {
		return 0, ErrChannelCredentialInvalid
	}
	if !input.All && len(input.CredentialIDs) == 0 && len(input.Positions) == 0 {
		return 0, ErrChannelCredentialSelectionEmpty
	}
	var nextRevision int64
	err := db.Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, "id = ?", input.ChannelID).Error; err != nil {
			return err
		}
		currentRevision, err := getOrCreateCredentialRevisionForUpdate(tx, input.ChannelID)
		if err != nil {
			return err
		}
		if input.ExpectedRev > 0 && currentRevision != input.ExpectedRev {
			return ErrChannelCredentialRevisionConflict
		}

		var credentials []ChannelCredential
		if err := lockForUpdate(tx).Where("channel_id = ?", input.ChannelID).Order("position ASC, id ASC").Find(&credentials).Error; err != nil {
			return err
		}
		selected := make(map[int]bool)
		idSet := make(map[int]bool, len(input.CredentialIDs))
		for _, id := range input.CredentialIDs {
			if id > 0 {
				idSet[id] = true
			}
		}
		positionSet := make(map[int]bool, len(input.Positions))
		for _, position := range input.Positions {
			if position >= 0 {
				positionSet[position] = true
			}
		}
		for index := range credentials {
			credential := &credentials[index]
			if input.All || idSet[credential.Id] || positionSet[credential.Position] {
				selected[credential.Id] = true
			}
		}
		if len(selected) == 0 {
			return ErrChannelCredentialNotFound
		}

		now := common.GetTimestamp()
		reason := strings.TrimSpace(input.Reason)
		if input.Status == common.ChannelStatusManuallyDisabled && reason == "" {
			reason = "manual"
		}
		for index := range credentials {
			credential := &credentials[index]
			if !selected[credential.Id] {
				continue
			}
			updates := map[string]interface{}{"status": input.Status}
			if input.Status == common.ChannelStatusEnabled {
				updates["disabled_reason"] = ""
				updates["disabled_at"] = int64(0)
			} else {
				updates["disabled_reason"] = reason
				updates["disabled_at"] = now
			}
			if err := tx.Model(credential).Updates(updates).Error; err != nil {
				return err
			}
			credential.Status = input.Status
			credential.DisabledReason = updates["disabled_reason"].(string)
			credential.DisabledAt = updates["disabled_at"].(int64)
		}

		if channel.ChannelInfo.MultiKeyStatusList == nil {
			channel.ChannelInfo.MultiKeyStatusList = make(map[int]int)
		}
		if channel.ChannelInfo.MultiKeyDisabledReason == nil {
			channel.ChannelInfo.MultiKeyDisabledReason = make(map[int]string)
		}
		if channel.ChannelInfo.MultiKeyDisabledTime == nil {
			channel.ChannelInfo.MultiKeyDisabledTime = make(map[int]int64)
		}
		for index := range credentials {
			credential := &credentials[index]
			if !selected[credential.Id] {
				continue
			}
			if input.Status == common.ChannelStatusEnabled {
				delete(channel.ChannelInfo.MultiKeyStatusList, credential.Position)
				delete(channel.ChannelInfo.MultiKeyDisabledReason, credential.Position)
				delete(channel.ChannelInfo.MultiKeyDisabledTime, credential.Position)
			} else {
				channel.ChannelInfo.MultiKeyStatusList[credential.Position] = input.Status
				channel.ChannelInfo.MultiKeyDisabledReason[credential.Position] = reason
				channel.ChannelInfo.MultiKeyDisabledTime[credential.Position] = now
			}
		}

		keys := channel.GetKeys()
		channel.ChannelInfo.MultiKeySize = len(keys)
		hasEnabled := false
		for position := range keys {
			status := channel.ChannelInfo.MultiKeyStatusList[position]
			if status == common.ChannelStatusEnabled || status == common.ChannelStatusUnknown {
				hasEnabled = true
				break
			}
		}
		other := channel.GetOtherInfo()
		if hasEnabled {
			if channel.Status == common.ChannelStatusAutoDisabled && other["status_reason"] == "All keys are disabled" {
				channel.Status = common.ChannelStatusEnabled
				delete(other, "status_reason")
				delete(other, "status_time")
				channel.SetOtherInfo(other)
			}
		} else {
			channel.Status = common.ChannelStatusAutoDisabled
			other["status_reason"] = "All keys are disabled"
			other["status_time"] = now
			channel.SetOtherInfo(other)
		}
		if err := tx.Model(&channel).Select("channel_info", "status", "other_info").Updates(&channel).Error; err != nil {
			return err
		}
		if tx.Migrator().HasTable(&Ability{}) {
			if err := channel.UpdateAbilities(tx); err != nil {
				return err
			}
		}
		nextRevision = currentRevision + 1
		return tx.Model(&ChannelCredentialRevision{}).Where("channel_id = ?", input.ChannelID).Updates(map[string]interface{}{
			"keys_revision": nextRevision,
			"updated_at":    now,
		}).Error
	})
	if err != nil {
		return 0, err
	}
	return nextRevision, nil
}

func getOrCreateCredentialRevisionForUpdate(tx *gorm.DB, channelID int) (int64, error) {
	var revision ChannelCredentialRevision
	err := lockForUpdate(tx).Where("channel_id = ?", channelID).First(&revision).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		revision = ChannelCredentialRevision{ChannelID: channelID, KeysRevision: 0, UpdatedAt: common.GetTimestamp()}
		if err := tx.Create(&revision).Error; err != nil {
			return 0, err
		}
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return revision.KeysRevision, nil
}

// UpdateChannelCredentialProxy updates one credential's proxy mode and bumps
// the credential revision so a stale admin form cannot overwrite a new key set.
func UpdateChannelCredentialProxy(db *gorm.DB, channelID, credentialID int, mode, proxyURL string, expectedRevision int64) (string, string, int64, error) {
	if db == nil || channelID <= 0 || credentialID <= 0 {
		return "", "", 0, ErrChannelCredentialRevisionInput
	}
	canonicalMode, canonicalProxy, err := NormalizeChannelCredentialProxy(mode, proxyURL)
	if err != nil {
		return "", "", 0, err
	}
	var oldProxy string
	var nextRevision int64
	err = db.Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, "id = ?", channelID).Error; err != nil {
			return err
		}
		currentRevision, err := getOrCreateCredentialRevisionForUpdate(tx, channelID)
		if err != nil {
			return err
		}
		if expectedRevision > 0 && currentRevision != expectedRevision {
			return ErrChannelCredentialRevisionConflict
		}
		var credential ChannelCredential
		if err := lockForUpdate(tx).Where("id = ? AND channel_id = ?", credentialID, channelID).First(&credential).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrChannelCredentialNotFound
			}
			return err
		}
		oldProxy = credential.ProxyURL
		if err := tx.Model(&credential).Updates(map[string]interface{}{"proxy_mode": canonicalMode, "proxy_url": canonicalProxy}).Error; err != nil {
			return err
		}
		nextRevision = currentRevision + 1
		return tx.Model(&ChannelCredentialRevision{}).Where("channel_id = ?", channelID).Updates(map[string]interface{}{
			"keys_revision": nextRevision,
			"updated_at":    common.GetTimestamp(),
		}).Error
	})
	return oldProxy, canonicalProxy, nextRevision, err
}

type ChannelCredentialProxyUpdate struct {
	ChannelID     int
	CredentialIDs []int
	All           bool
	Mode          string
	ProxyURL      string
	ExpectedRev   int64
}

// UpdateChannelCredentialProxies applies one proxy policy to a stable set of
// credentials under the channel row lock. It returns only old/new canonical
// values for connection-pool invalidation; callers must never serialize them.
func UpdateChannelCredentialProxies(db *gorm.DB, input ChannelCredentialProxyUpdate) (map[int]string, string, int64, error) {
	if db == nil || input.ChannelID <= 0 {
		return nil, "", 0, ErrChannelCredentialRevisionInput
	}
	mode, proxyURL, err := NormalizeChannelCredentialProxy(input.Mode, input.ProxyURL)
	if err != nil {
		return nil, "", 0, err
	}
	if !input.All && len(input.CredentialIDs) == 0 {
		return nil, "", 0, ErrChannelCredentialSelectionEmpty
	}
	old := map[int]string{}
	var next int64
	err = db.Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, "id = ?", input.ChannelID).Error; err != nil {
			return err
		}
		current, err := getOrCreateCredentialRevisionForUpdate(tx, input.ChannelID)
		if err != nil {
			return err
		}
		if input.ExpectedRev > 0 && input.ExpectedRev != current {
			return ErrChannelCredentialRevisionConflict
		}
		var credentials []ChannelCredential
		if err := lockForUpdate(tx).Where("channel_id = ?", input.ChannelID).Order("position ASC, id ASC").Find(&credentials).Error; err != nil {
			return err
		}
		selected := map[int]bool{}
		for _, id := range input.CredentialIDs {
			selected[id] = true
		}
		changed := 0
		for i := range credentials {
			credential := &credentials[i]
			if !input.All && !selected[credential.Id] {
				continue
			}
			old[credential.Id] = credential.ProxyURL
			if err := tx.Model(credential).Updates(map[string]any{"proxy_mode": mode, "proxy_url": proxyURL}).Error; err != nil {
				return err
			}
			changed++
		}
		if changed == 0 {
			return ErrChannelCredentialNotFound
		}
		next = current + 1
		return tx.Model(&ChannelCredentialRevision{}).Where("channel_id = ?", input.ChannelID).Updates(map[string]any{"keys_revision": next, "updated_at": common.GetTimestamp()}).Error
	})
	return old, proxyURL, next, err
}

// RecordChannelCredentialTest updates health metadata without changing the
// credential state. Test metadata is intentionally independent from key-set
// revisions so concurrent tests do not invalidate an admin status form.
func RecordChannelCredentialTest(db *gorm.DB, channelID, credentialID int, status string, latencyMs int64, errorClass string) error {
	if db == nil || channelID <= 0 || credentialID <= 0 {
		return ErrChannelCredentialRevisionInput
	}
	if status != "success" && status != "failed" && status != "skipped" {
		return ErrChannelCredentialInvalid
	}
	if latencyMs < 0 {
		latencyMs = 0
	}
	return db.Model(&ChannelCredential{}).Where("id = ? AND channel_id = ?", credentialID, channelID).Updates(map[string]interface{}{
		"last_test_at":          common.GetTimestamp(),
		"last_test_status":      status,
		"last_test_latency_ms":  latencyMs,
		"last_test_error_code":  strings.TrimSpace(errorClass),
		"last_test_error_class": strings.TrimSpace(errorClass),
		"consecutive_failures":  gorm.Expr("CASE WHEN ? = 'success' THEN 0 ELSE consecutive_failures + 1 END", status),
	}).Error
}

// MigrateChannelCredentialStore creates the new tables and imports legacy
// Channel.Key values. It is idempotent and safe to call on every startup.
func MigrateChannelCredentialStore(db *gorm.DB) error {
	if db == nil {
		return ErrChannelCredentialRevisionInput
	}
	if err := db.AutoMigrate(&ChannelCredential{}, &ChannelCredentialRevision{}); err != nil {
		return err
	}
	return MigrateLegacyChannelCredentialsWithDB(db)
}

// MigrateChannelCredentials is the startup convenience wrapper used by the
// main database migration after DB has been initialized.
func MigrateChannelCredentials() error {
	return MigrateChannelCredentialStore(DB)
}

// MigrateLegacyChannelCredentials imports legacy channel keys using the
// process-wide main database. It is the startup entry point used by model/main.go.
func MigrateLegacyChannelCredentials() error {
	return MigrateChannelCredentialStore(DB)
}

// MigrateLegacyChannelCredentialsWithDB imports legacy channel keys while
// preserving per-key identity across reorder operations. Removed legacy keys
// are retained as disabled rows so historical metrics can continue to resolve
// their ID.
func MigrateLegacyChannelCredentialsWithDB(db *gorm.DB) error {
	if db == nil {
		return ErrChannelCredentialRevisionInput
	}
	if !db.Migrator().HasTable(&Channel{}) || !db.Migrator().HasTable(&ChannelCredential{}) {
		return nil
	}
	if !db.Migrator().HasTable(&ChannelCredentialRevision{}) {
		if err := db.AutoMigrate(&ChannelCredentialRevision{}); err != nil {
			return err
		}
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var channels []Channel
		if err := tx.Find(&channels).Error; err != nil {
			return err
		}
		for index := range channels {
			if err := migrateLegacyChannelCredentialsForChannel(tx, &channels[index]); err != nil {
				return err
			}
		}
		return nil
	})
}

func migrateLegacyChannelCredentialsForChannel(tx *gorm.DB, channel *Channel) error {
	if channel == nil || channel.Id <= 0 || (channel.Key == "" && !channel.ChannelInfo.IsMultiKey) {
		return nil
	}
	keys := channel.GetKeys()
	var existing []ChannelCredential
	if err := tx.Where("channel_id = ?", channel.Id).Order("position ASC, id ASC").Find(&existing).Error; err != nil {
		return err
	}
	var revision ChannelCredentialRevision
	revisionErr := tx.Where("channel_id = ?", channel.Id).First(&revision).Error
	if revisionErr != nil && !errors.Is(revisionErr, gorm.ErrRecordNotFound) {
		return revisionErr
	}
	legacyStatusAuthoritative := errors.Is(revisionErr, gorm.ErrRecordNotFound)

	byFingerprint := make(map[string][]*ChannelCredential)
	for index := range existing {
		credential := &existing[index]
		byFingerprint[credential.Fingerprint] = append(byFingerprint[credential.Fingerprint], credential)
	}
	used := make(map[int]bool, len(existing))
	changed := false
	activeCount := 0
	for position, rawSecret := range keys {
		secret := strings.TrimSpace(rawSecret)
		if secret == "" {
			continue
		}
		activeCount++
		fingerprint := ChannelCredentialFingerprint(secret)
		var credential *ChannelCredential
		for _, candidate := range byFingerprint[fingerprint] {
			if !used[candidate.Id] {
				credential = candidate
				break
			}
		}
		status, reason, disabledAt := legacyCredentialStatus(channel, position)
		if credential == nil {
			credential = &ChannelCredential{
				ChannelID:      channel.Id,
				Position:       position,
				Secret:         secret,
				Fingerprint:    fingerprint,
				Status:         status,
				DisabledReason: reason,
				DisabledAt:     disabledAt,
				ProxyMode:      ChannelCredentialProxyModeInherit,
			}
			if err := tx.Create(credential).Error; err != nil {
				return err
			}
			changed = true
		} else {
			used[credential.Id] = true
			updates := map[string]interface{}{}
			if credential.Position != position {
				updates["position"] = position
				credential.Position = position
				changed = true
			}
			if credential.ProxyMode == "" {
				updates["proxy_mode"] = ChannelCredentialProxyModeInherit
				credential.ProxyMode = ChannelCredentialProxyModeInherit
				changed = true
			}
			if legacyStatusAuthoritative && applyLegacyCredentialStatus(credential, status, reason, disabledAt, updates) {
				changed = true
			}
			if len(updates) > 0 {
				if err := tx.Model(credential).Updates(updates).Error; err != nil {
					return err
				}
			}
		}
		used[credential.Id] = true
	}

	for index := range existing {
		credential := &existing[index]
		if used[credential.Id] {
			continue
		}
		updates := map[string]interface{}{
			"status":          common.ChannelStatusManuallyDisabled,
			"disabled_reason": ChannelCredentialDisabledReasonLegacyRemoved,
			"disabled_at":     common.GetTimestamp(),
		}
		// Removed credentials remain addressable by ID for historical metrics,
		// but must not collide with an active key that shifted into its position.
		if credential.Position >= 0 {
			updates["position"] = -credential.Id
		}
		if err := tx.Model(credential).Updates(updates).Error; err != nil {
			return err
		}
		changed = true
	}

	if revisionErr == nil {
		if changed {
			revision.KeysRevision++
			revision.UpdatedAt = common.GetTimestamp()
			return tx.Model(&revision).Updates(map[string]interface{}{
				"keys_revision": revision.KeysRevision,
				"updated_at":    revision.UpdatedAt,
			}).Error
		}
		return nil
	}
	initialRevision := int64(0)
	if activeCount > 0 || len(existing) > 0 {
		initialRevision = 1
	}
	revision = ChannelCredentialRevision{ChannelID: channel.Id, KeysRevision: initialRevision, UpdatedAt: common.GetTimestamp()}
	return tx.Create(&revision).Error
}

// SyncChannelCredentialsForChannel reconciles the stable credential store with
// a legacy channel key edit. It is used after delete/reorder/replace actions so
// the runtime selector and the per-key management UI observe the same identity,
// position, status, and proxy metadata without waiting for a restart.
func SyncChannelCredentialsForChannel(db *gorm.DB, channelID int) error {
	if db == nil || channelID <= 0 {
		return ErrChannelCredentialRevisionInput
	}
	if !db.Migrator().HasTable(&Channel{}) || !db.Migrator().HasTable(&ChannelCredential{}) {
		return nil
	}
	if err := MigrateLegacyChannelCredentialsWithDB(db); err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := tx.First(&channel, "id = ?", channelID).Error; err != nil {
			return err
		}
		if !channel.ChannelInfo.IsMultiKey {
			return nil
		}
		var credentials []ChannelCredential
		if err := tx.Where("channel_id = ?", channelID).Order("position ASC, id ASC").Find(&credentials).Error; err != nil {
			return err
		}
		changed := false
		keys := channel.GetKeys()
		for index := range credentials {
			credential := &credentials[index]
			if credential.Position < 0 || credential.Position >= len(keys) {
				continue
			}
			status, reason, disabledAt := legacyCredentialStatus(&channel, credential.Position)
			updates := map[string]interface{}{}
			if credential.Status != status {
				updates["status"] = status
				changed = true
			}
			if status == common.ChannelStatusEnabled {
				if credential.DisabledReason != "" || credential.DisabledAt != 0 {
					updates["disabled_reason"] = ""
					updates["disabled_at"] = int64(0)
					changed = true
				}
			} else if credential.DisabledReason != reason || credential.DisabledAt != disabledAt {
				updates["disabled_reason"] = reason
				updates["disabled_at"] = disabledAt
				changed = true
			}
			if len(updates) > 0 {
				if err := tx.Model(credential).Updates(updates).Error; err != nil {
					return err
				}
			}
		}
		if !changed {
			return nil
		}
		var revision ChannelCredentialRevision
		if err := lockForUpdate(tx).Where("channel_id = ?", channelID).First(&revision).Error; err != nil {
			return err
		}
		revision.KeysRevision++
		revision.UpdatedAt = common.GetTimestamp()
		return tx.Model(&revision).Updates(map[string]interface{}{
			"keys_revision": revision.KeysRevision,
			"updated_at":    revision.UpdatedAt,
		}).Error
	})
}

// SyncChannelCredentialStatusForLegacyKey keeps automatic channel-error
// handling compatible with the durable credential store. The legacy key is
// only used inside this model boundary to find its one-way fingerprint; it is
// never returned or logged.
func SyncChannelCredentialStatusForLegacyKey(db *gorm.DB, channelID int, secret string, status int, reason string) error {
	if db == nil || channelID <= 0 || strings.TrimSpace(secret) == "" {
		return ErrChannelCredentialRevisionInput
	}
	if !db.Migrator().HasTable(&ChannelCredential{}) {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var credential ChannelCredential
		if err := lockForUpdate(tx).Where("channel_id = ? AND fingerprint = ?", channelID, ChannelCredentialFingerprint(secret)).First(&credential).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		updates := map[string]interface{}{"status": status}
		if status == common.ChannelStatusEnabled {
			updates["disabled_reason"] = ""
			updates["disabled_at"] = int64(0)
		} else {
			updates["disabled_reason"] = strings.TrimSpace(reason)
			updates["disabled_at"] = common.GetTimestamp()
		}
		if err := tx.Model(&credential).Updates(updates).Error; err != nil {
			return err
		}
		var revision ChannelCredentialRevision
		if err := lockForUpdate(tx).Where("channel_id = ?", channelID).First(&revision).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				revision = ChannelCredentialRevision{ChannelID: channelID, KeysRevision: 1, UpdatedAt: common.GetTimestamp()}
				return tx.Create(&revision).Error
			}
			return err
		}
		revision.KeysRevision++
		revision.UpdatedAt = common.GetTimestamp()
		return tx.Model(&revision).Updates(map[string]interface{}{
			"keys_revision": revision.KeysRevision,
			"updated_at":    revision.UpdatedAt,
		}).Error
	})
}

func legacyCredentialStatus(channel *Channel, position int) (int, string, int64) {
	status := common.ChannelStatusEnabled
	if channel != nil && channel.ChannelInfo.MultiKeyStatusList != nil {
		if value, ok := channel.ChannelInfo.MultiKeyStatusList[position]; ok && value != common.ChannelStatusUnknown {
			status = value
		}
	}
	if status == common.ChannelStatusEnabled {
		return status, "", 0
	}
	var reason string
	var disabledAt int64
	if channel != nil {
		reason = channel.ChannelInfo.MultiKeyDisabledReason[position]
		disabledAt = channel.ChannelInfo.MultiKeyDisabledTime[position]
	}
	if disabledAt == 0 {
		disabledAt = common.GetTimestamp()
	}
	return status, reason, disabledAt
}

func applyLegacyCredentialStatus(credential *ChannelCredential, status int, reason string, disabledAt int64, updates map[string]interface{}) bool {
	if credential == nil {
		return false
	}
	if status == common.ChannelStatusUnknown {
		status = common.ChannelStatusEnabled
	}
	changed := false
	if credential.Status != status {
		credential.Status = status
		updates["status"] = status
		changed = true
	}
	if status == common.ChannelStatusEnabled {
		if credential.DisabledReason != "" || credential.DisabledAt != 0 {
			updates["disabled_reason"] = ""
			updates["disabled_at"] = int64(0)
			changed = true
		}
	} else if credential.DisabledReason != reason || credential.DisabledAt != disabledAt {
		updates["disabled_reason"] = reason
		updates["disabled_at"] = disabledAt
		changed = true
	}
	return changed
}

// GetChannelCredentialRevision returns zero for a channel that has not been
// migrated yet. The caller can use the value as an optimistic-concurrency token.
func GetChannelCredentialRevision(db *gorm.DB, channelID int) (int64, error) {
	if db == nil || channelID <= 0 {
		return 0, ErrChannelCredentialRevisionInput
	}
	var revision ChannelCredentialRevision
	if err := db.Where("channel_id = ?", channelID).First(&revision).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return revision.KeysRevision, nil
}

// BumpChannelCredentialRevision increments a channel revision atomically.
// Callers should execute credential writes and this helper in the same
// transaction when they need a strict compare-and-swap contract.
func BumpChannelCredentialRevision(db *gorm.DB, channelID int) (int64, error) {
	if db == nil || channelID <= 0 {
		return 0, ErrChannelCredentialRevisionInput
	}
	var next int64
	err := db.Transaction(func(tx *gorm.DB) error {
		var revision ChannelCredentialRevision
		err := tx.Where("channel_id = ?", channelID).First(&revision).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			candidate := &ChannelCredentialRevision{ChannelID: channelID, KeysRevision: 0, UpdatedAt: common.GetTimestamp()}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(candidate).Error; err != nil {
				return err
			}
			err = lockForUpdate(tx).Where("channel_id = ?", channelID).First(&revision).Error
		}
		if err != nil {
			return err
		}
		revision.KeysRevision++
		revision.UpdatedAt = common.GetTimestamp()
		next = revision.KeysRevision
		return tx.Model(&revision).Updates(map[string]interface{}{
			"keys_revision": revision.KeysRevision,
			"updated_at":    revision.UpdatedAt,
		}).Error
	})
	return next, err
}

// ListChannelCredentials returns stable position ordering for controller use.
func ListChannelCredentials(db *gorm.DB, channelID int) ([]ChannelCredential, error) {
	if db == nil || channelID <= 0 {
		return nil, ErrChannelCredentialRevisionInput
	}
	var credentials []ChannelCredential
	err := db.Where("channel_id = ?", channelID).Order("position ASC, id ASC").Find(&credentials).Error
	return credentials, err
}

func GetChannelCredential(db *gorm.DB, channelID, credentialID int) (*ChannelCredential, error) {
	if db == nil || channelID <= 0 || credentialID <= 0 {
		return nil, ErrChannelCredentialRevisionInput
	}
	var credential ChannelCredential
	if err := db.Where("id = ? AND channel_id = ?", credentialID, channelID).First(&credential).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrChannelCredentialNotFound
		}
		return nil, err
	}
	return &credential, nil
}

// GetChannelCredentials is the process-wide convenience wrapper used by
// channel loading. It returns credentials in stable position order.
func GetChannelCredentials(channelID int) ([]ChannelCredential, error) {
	return ListChannelCredentials(DB, channelID)
}

// ValidateStoredProxyURL is a small boundary helper for update handlers that
// receive a write-only URL but should never echo it in an error response.
func ValidateStoredProxyURL(mode, proxyURL string) error {
	_, _, err := NormalizeChannelCredentialProxy(mode, proxyURL)
	return err
}

// RedactProxyURL returns only scheme/host/port and never includes userinfo.
func RedactProxyURL(rawProxyURL string) string {
	parsedURL, err := common.ParseProxyURLStrict(rawProxyURL)
	if err != nil || parsedURL == nil {
		return ""
	}
	parsedURL.User = nil
	return (&url.URL{Scheme: parsedURL.Scheme, Host: parsedURL.Host}).String()
}
