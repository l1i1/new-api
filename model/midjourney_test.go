package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveMidjourneyChannelAccessPinsCredentialAndProxy(t *testing.T) {
	credential, err := NewChannelCredential(11, 1, "key-b")
	require.NoError(t, err)
	credential.Id = 42
	credential.ProxyMode, credential.ProxyURL, err = NormalizeChannelCredentialProxy(
		ChannelCredentialProxyModeCustom,
		"http://proxy-key-b.example:8080",
	)
	require.NoError(t, err)

	channel := &Channel{
		Id:          11,
		Key:         "key-a\nkey-b",
		ChannelInfo: ChannelInfo{IsMultiKey: true},
		Credentials: []ChannelCredential{*credential},
	}
	task := &Midjourney{
		ChannelId:           channel.Id,
		ChannelCredentialID: credential.Id,
		ProxySnapshot:       "http://stale-channel-proxy.example:8080",
		ProxySnapshotSet:    true,
	}

	key, proxy := ResolveMidjourneyChannelAccess(task, channel)
	assert.Equal(t, "key-b", key)
	assert.Equal(t, credential.ProxyURL, proxy)
}
