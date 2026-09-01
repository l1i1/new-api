package service

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

func FetchCodexChannelModels(channel *model.Channel) ([]string, error) {
	if channel == nil || channel.Type != constant.ChannelTypeCodex {
		return nil, fmt.Errorf("channel type is not Codex")
	}
	oauthKey, credentialIndex, err := selectCodexOAuthKey(channel)
	if err != nil {
		return nil, err
	}
	proxyURL, err := resolveCodexCredentialProxy(channel, credentialIndex)
	if err != nil {
		return nil, err
	}
	client, err := NewProxyHttpClient(proxyURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	clientVersion, err := GetLatestCodexClientVersion(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("failed to get Codex client version: %w", err)
	}

	baseURL := channel.GetBaseURL()
	if baseURL == "" {
		baseURL = constant.GetChannelBaseURL(constant.ChannelTypeCodex)
	}
	return fetchCodexChannelModels(ctx, channel, oauthKey, credentialIndex, baseURL, client, clientVersion)
}

func fetchCodexChannelModels(
	ctx context.Context,
	channel *model.Channel,
	oauthKey *CodexOAuthKey,
	credentialIndex int,
	baseURL string,
	client *http.Client,
	clientVersion string,
) ([]string, error) {
	statusCode, models, err := FetchCodexModels(ctx, client, baseURL, oauthKey, clientVersion)
	if err != nil {
		return nil, err
	}
	if statusCode == http.StatusUnauthorized {
		if channel.Id <= 0 {
			return nil, fmt.Errorf("codex channel credential expired; save the channel before retrying model fetch")
		}
		refreshedKey, _, refreshErr := RefreshCodexChannelCredential(
			ctx,
			channel.Id,
			CodexCredentialRefreshOptions{
				ResetCaches:     true,
				CredentialIndex: &credentialIndex,
			},
		)
		if refreshErr != nil {
			return nil, fmt.Errorf("failed to refresh Codex channel credential: %w", refreshErr)
		}
		statusCode, models, err = FetchCodexModels(ctx, client, baseURL, &CodexOAuthKey{
			AccessToken: refreshedKey.AccessToken,
			AccountID:   refreshedKey.AccountID,
		}, clientVersion)
		if err != nil {
			return nil, err
		}
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("upstream status: %d", statusCode)
	}
	return models, nil
}
