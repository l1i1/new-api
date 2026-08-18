package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

type CodexCredentialRefreshOptions struct {
	ResetCaches bool
	// CredentialIndex refreshes one entry in a JSON multi-key array. A nil
	// index retains the existing manual-refresh behavior and refreshes all keys.
	CredentialIndex *int
	// RefreshBefore skips credentials that expire after this duration. It is
	// used by the background task; manual and expiry-recovery refreshes force
	// the selected credential immediately.
	RefreshBefore time.Duration
}

type CodexOAuthKey struct {
	IDToken      string `json:"id_token,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`

	AccountID   string `json:"account_id,omitempty"`
	LastRefresh string `json:"last_refresh,omitempty"`
	Email       string `json:"email,omitempty"`
	Type        string `json:"type,omitempty"`
	Expired     string `json:"expired,omitempty"`
}

func parseCodexOAuthKey(raw string) (*CodexOAuthKey, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("codex channel: empty oauth key")
	}
	var key CodexOAuthKey
	if err := common.Unmarshal([]byte(raw), &key); err != nil {
		return nil, errors.New("codex channel: invalid oauth key json")
	}
	return &key, nil
}

func parseCodexOAuthKeys(raw string) ([]CodexOAuthKey, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, false, errors.New("codex channel: empty oauth key")
	}
	if !strings.HasPrefix(trimmed, "[") {
		key, err := parseCodexOAuthKey(trimmed)
		if err != nil {
			return nil, false, err
		}
		return []CodexOAuthKey{*key}, false, nil
	}
	var keys []CodexOAuthKey
	if err := common.Unmarshal([]byte(trimmed), &keys); err != nil || len(keys) == 0 {
		return nil, true, errors.New("codex channel: invalid oauth key json")
	}
	return keys, true, nil
}

func marshalCodexOAuthKeys(keys []CodexOAuthKey, multiKey bool) (string, error) {
	if len(keys) == 0 {
		return "", errors.New("codex channel: empty oauth key")
	}
	if multiKey {
		encoded, err := common.Marshal(keys)
		return string(encoded), err
	}
	encoded, err := common.Marshal(keys[0])
	return string(encoded), err
}

func marshalCodexOAuthKey(key CodexOAuthKey) (string, error) {
	encoded, err := common.Marshal(key)
	return string(encoded), err
}

func shouldRefreshCodexOAuthKey(key CodexOAuthKey, now time.Time, before time.Duration) bool {
	if strings.TrimSpace(key.RefreshToken) == "" {
		return false
	}
	if before <= 0 {
		return true
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(key.Expired))
	return err != nil || expiresAt.IsZero() || expiresAt.Sub(now) <= before
}

func selectCodexOAuthKey(channel *model.Channel) (*CodexOAuthKey, int, error) {
	if channel == nil {
		return nil, 0, errors.New("codex channel: channel is required")
	}
	keys, _, err := parseCodexOAuthKeys(channel.Key)
	if err != nil {
		return nil, 0, err
	}
	rawKeys := channel.GetKeys()
	credentialsByPosition := make(map[int]*model.ChannelCredential, len(channel.Credentials))
	for index := range channel.Credentials {
		credential := &channel.Credentials[index]
		if credential.Position < 0 || credential.Position >= len(rawKeys) ||
			credential.Fingerprint != model.ChannelCredentialFingerprint(rawKeys[credential.Position]) {
			continue
		}
		credentialsByPosition[credential.Position] = credential
	}
	for index := range keys {
		if credential, ok := credentialsByPosition[index]; ok && credential.Status != common.ChannelStatusEnabled {
			continue
		}
		if status, ok := channel.ChannelInfo.MultiKeyStatusList[index]; ok && status != common.ChannelStatusEnabled {
			continue
		}
		key := keys[index]
		return &key, index, nil
	}
	return nil, 0, errors.New("codex channel: no enabled credential")
}

// resolveCodexCredentialProxy keeps credential refresh and model discovery on
// the same per-key proxy route as regular channel requests.
func resolveCodexCredentialProxy(channel *model.Channel, credentialIndex int) (string, error) {
	if channel == nil {
		return "", errors.New("codex channel: channel is required")
	}
	proxyURL := channel.GetSetting().Proxy
	if !channel.ChannelInfo.IsMultiKey {
		return proxyURL, nil
	}
	credential := channel.CredentialForPosition(credentialIndex)
	if credential == nil {
		return proxyURL, nil
	}
	return credential.EffectiveProxyURL(proxyURL)
}

func refreshCodexOAuthKey(
	ctx context.Context,
	oauthKey CodexOAuthKey,
	proxyURL string,
) (CodexOAuthKey, error) {
	if strings.TrimSpace(oauthKey.RefreshToken) == "" {
		return CodexOAuthKey{}, fmt.Errorf("codex channel: refresh_token is required to refresh credential")
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	res, err := RefreshCodexOAuthTokenWithProxy(refreshCtx, oauthKey.RefreshToken, proxyURL)
	if err != nil {
		return CodexOAuthKey{}, err
	}

	oauthKey.AccessToken = res.AccessToken
	oauthKey.RefreshToken = res.RefreshToken
	oauthKey.LastRefresh = time.Now().Format(time.RFC3339)
	oauthKey.Expired = res.ExpiresAt.Format(time.RFC3339)
	if strings.TrimSpace(oauthKey.Type) == "" {
		oauthKey.Type = "codex"
	}

	if strings.TrimSpace(oauthKey.AccountID) == "" {
		if accountID, ok := ExtractCodexAccountIDFromJWT(oauthKey.AccessToken); ok {
			oauthKey.AccountID = accountID
		}
	}
	if strings.TrimSpace(oauthKey.Email) == "" {
		if email, ok := ExtractEmailFromJWT(oauthKey.AccessToken); ok {
			oauthKey.Email = email
		}
	}
	return oauthKey, nil
}

func persistRefreshedCodexCredentials(
	channel *model.Channel,
	rawKeys []string,
	encoded string,
	multiKey bool,
	refreshedSecrets map[int]string,
) error {
	if channel == nil || channel.Id <= 0 {
		return errors.New("codex channel: channel is required")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		// Do not overwrite an administrator's concurrent key edit. This check
		// also makes every credential-row update part of the same atomic change.
		channelUpdate := tx.Model(&model.Channel{}).
			Where("id = ? AND key = ?", channel.Id, channel.Key).
			Update("key", encoded)
		if channelUpdate.Error != nil {
			return channelUpdate.Error
		}
		if channelUpdate.RowsAffected != 1 {
			return errors.New("codex channel: credentials changed during refresh")
		}
		if !multiKey || !tx.Migrator().HasTable(&model.ChannelCredential{}) {
			return nil
		}
		for index, refreshedSecret := range refreshedSecrets {
			if index >= len(rawKeys) {
				return errors.New("codex channel: credential index is invalid")
			}
			oldFingerprint := model.ChannelCredentialFingerprint(rawKeys[index])
			credentialUpdate := tx.Model(&model.ChannelCredential{}).
				Where("channel_id = ? AND position = ? AND fingerprint = ?", channel.Id, index, oldFingerprint).
				Updates(map[string]interface{}{
					"secret":      refreshedSecret,
					"fingerprint": model.ChannelCredentialFingerprint(refreshedSecret),
				})
			if credentialUpdate.Error != nil {
				return credentialUpdate.Error
			}
			if credentialUpdate.RowsAffected != 1 {
				return errors.New("codex channel: credential changed during refresh")
			}
		}
		return nil
	})
}

func RefreshCodexChannelCredential(ctx context.Context, channelID int, opts CodexCredentialRefreshOptions) (*CodexOAuthKey, *model.Channel, error) {
	ch, err := model.GetChannelById(channelID, true)
	if err != nil {
		return nil, nil, err
	}
	if ch == nil {
		return nil, nil, fmt.Errorf("channel not found")
	}
	if ch.Type != constant.ChannelTypeCodex {
		return nil, nil, fmt.Errorf("channel type is not Codex")
	}

	rawKeys := ch.GetKeys()
	oauthKeys, multiKey, err := parseCodexOAuthKeys(ch.Key)
	if err != nil {
		return nil, nil, err
	}
	indexes := make([]int, 0, len(oauthKeys))
	if opts.CredentialIndex != nil {
		if *opts.CredentialIndex < 0 || *opts.CredentialIndex >= len(oauthKeys) {
			return nil, nil, fmt.Errorf("codex channel: credential index is invalid")
		}
		indexes = append(indexes, *opts.CredentialIndex)
	} else {
		for index := range oauthKeys {
			indexes = append(indexes, index)
		}
	}

	var refreshedKey *CodexOAuthKey
	refreshedSecrets := make(map[int]string)
	for _, index := range indexes {
		if !shouldRefreshCodexOAuthKey(oauthKeys[index], time.Now(), opts.RefreshBefore) {
			continue
		}
		proxyURL, proxyErr := resolveCodexCredentialProxy(ch, index)
		if proxyErr != nil {
			return nil, nil, proxyErr
		}
		refreshed, refreshErr := refreshCodexOAuthKey(ctx, oauthKeys[index], proxyURL)
		if refreshErr != nil {
			return nil, nil, refreshErr
		}
		oauthKeys[index] = refreshed
		refreshedSecret, marshalErr := marshalCodexOAuthKey(refreshed)
		if marshalErr != nil {
			return nil, nil, marshalErr
		}
		refreshedSecrets[index] = refreshedSecret
		if refreshedKey == nil {
			keyCopy := refreshed
			refreshedKey = &keyCopy
		}
	}
	if refreshedKey == nil {
		return nil, nil, fmt.Errorf("codex channel: no refreshable credential")
	}

	encoded, err := marshalCodexOAuthKeys(oauthKeys, multiKey)
	if err != nil {
		return nil, nil, err
	}

	if err := persistRefreshedCodexCredentials(ch, rawKeys, encoded, multiKey, refreshedSecrets); err != nil {
		return nil, nil, err
	}

	if opts.ResetCaches {
		model.InitChannelCache()
	}

	return refreshedKey, ch, nil
}
