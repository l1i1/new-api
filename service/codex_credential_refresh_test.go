package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupCodexCredentialRefreshTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.ChannelCredential{}))
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		sqlDB, closeErr := db.DB()
		if closeErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestParseCodexOAuthKeysPreservesJSONMultiKeyOrder(t *testing.T) {
	keys, multiKey, err := parseCodexOAuthKeys(`[
		{"access_token":"first","refresh_token":"refresh-first","account_id":"account-first"},
		{"access_token":"second","refresh_token":"refresh-second","account_id":"account-second"}
	]`)

	require.NoError(t, err)
	assert.True(t, multiKey)
	require.Len(t, keys, 2)
	assert.Equal(t, "first", keys[0].AccessToken)
	assert.Equal(t, "second", keys[1].AccessToken)

	encoded, err := marshalCodexOAuthKeys(keys, multiKey)
	require.NoError(t, err)
	assert.JSONEq(t, `[
		{"access_token":"first","refresh_token":"refresh-first","account_id":"account-first"},
		{"access_token":"second","refresh_token":"refresh-second","account_id":"account-second"}
	]`, encoded)
}

func TestShouldRefreshCodexOAuthKeyHonorsExpiryThreshold(t *testing.T) {
	now := time.Now()
	assert.True(t, shouldRefreshCodexOAuthKey(CodexOAuthKey{
		RefreshToken: "refresh",
		Expired:      now.Add(30 * time.Minute).Format(time.RFC3339),
	}, now, time.Hour))
	assert.False(t, shouldRefreshCodexOAuthKey(CodexOAuthKey{
		RefreshToken: "refresh",
		Expired:      now.Add(2 * time.Hour).Format(time.RFC3339),
	}, now, time.Hour))
	assert.False(t, shouldRefreshCodexOAuthKey(CodexOAuthKey{}, now, time.Hour))
}

func TestSelectCodexOAuthKeySkipsDisabledJSONCredential(t *testing.T) {
	channel := &model.Channel{
		Key: `[
			{"access_token":"disabled","refresh_token":"refresh-disabled","account_id":"account-disabled"},
			{"access_token":"enabled","refresh_token":"refresh-enabled","account_id":"account-enabled"}
		]`,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
			MultiKeyStatusList: map[int]int{
				0: common.ChannelStatusManuallyDisabled,
				1: common.ChannelStatusEnabled,
			},
		},
	}

	selected, index, err := selectCodexOAuthKey(channel)

	require.NoError(t, err)
	assert.Equal(t, 1, index)
	assert.Equal(t, "enabled", selected.AccessToken)
}

func TestResolveCodexCredentialProxyUsesPerKeyProxyMode(t *testing.T) {
	channelProxy := `{"proxy":"http://channel-proxy.example:8080"}`
	channel := &model.Channel{
		Setting: &channelProxy,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
		},
		Credentials: []model.ChannelCredential{
			{Position: 0, ProxyMode: model.ChannelCredentialProxyModeCustom, ProxyURL: "http://key-proxy.example:8080"},
			{Position: 1, ProxyMode: model.ChannelCredentialProxyModeDirect},
			{Position: 2, ProxyMode: model.ChannelCredentialProxyModeInherit},
		},
	}

	proxyURL, err := resolveCodexCredentialProxy(channel, 0)
	require.NoError(t, err)
	assert.Equal(t, "http://key-proxy.example:8080", proxyURL)

	proxyURL, err = resolveCodexCredentialProxy(channel, 1)
	require.NoError(t, err)
	assert.Empty(t, proxyURL)

	proxyURL, err = resolveCodexCredentialProxy(channel, 2)
	require.NoError(t, err)
	assert.Equal(t, "http://channel-proxy.example:8080", proxyURL)
}

func TestPersistRefreshedCodexCredentialsPreservesCredentialMetadata(t *testing.T) {
	db := setupCodexCredentialRefreshTestDB(t)
	oldKey := `[{"access_token":"first","refresh_token":"refresh-first","account_id":"account-first"},{"access_token":"second","refresh_token":"refresh-second","account_id":"account-second"}]`
	channel := &model.Channel{
		Type: constant.ChannelTypeCodex,
		Key:  oldKey,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
		},
	}
	require.NoError(t, db.Create(channel).Error)
	rawKeys := channel.GetKeys()
	firstCredential, err := model.NewChannelCredential(channel.Id, 0, rawKeys[0])
	require.NoError(t, err)
	firstCredential.ProxyMode = model.ChannelCredentialProxyModeCustom
	firstCredential.ProxyURL = "http://first-proxy.example:8080"
	firstCredential.LastTestStatus = "success"
	firstCredential.LastTestHTTPStatus = 200
	secondCredential, err := model.NewChannelCredential(channel.Id, 1, rawKeys[1])
	require.NoError(t, err)
	secondCredential.Status = common.ChannelStatusManuallyDisabled
	secondCredential.DisabledReason = "manual"
	require.NoError(t, db.Create(firstCredential).Error)
	require.NoError(t, db.Create(secondCredential).Error)

	loaded, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	newFirst := `{"access_token":"first-new","refresh_token":"refresh-first-new","account_id":"account-first"}`
	newSecond := `{"access_token":"second-new","refresh_token":"refresh-second-new","account_id":"account-second"}`
	encoded := "[" + newFirst + "," + newSecond + "]"
	require.NoError(t, persistRefreshedCodexCredentials(loaded, rawKeys, encoded, true, map[int]string{
		0: newFirst,
		1: newSecond,
	}))

	var persistedChannel model.Channel
	require.NoError(t, db.First(&persistedChannel, channel.Id).Error)
	assert.Equal(t, encoded, persistedChannel.Key)
	var persistedFirst model.ChannelCredential
	require.NoError(t, db.First(&persistedFirst, firstCredential.Id).Error)
	assert.Equal(t, newFirst, persistedFirst.Secret)
	assert.Equal(t, model.ChannelCredentialFingerprint(newFirst), persistedFirst.Fingerprint)
	assert.Equal(t, model.ChannelCredentialProxyModeCustom, persistedFirst.ProxyMode)
	assert.Equal(t, "http://first-proxy.example:8080", persistedFirst.ProxyURL)
	assert.Equal(t, "success", persistedFirst.LastTestStatus)
	assert.Equal(t, 200, persistedFirst.LastTestHTTPStatus)
	var persistedSecond model.ChannelCredential
	require.NoError(t, db.First(&persistedSecond, secondCredential.Id).Error)
	assert.Equal(t, newSecond, persistedSecond.Secret)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, persistedSecond.Status)
	assert.Equal(t, "manual", persistedSecond.DisabledReason)
}

func TestPersistRefreshedCodexCredentialsRollsBackWhenCredentialChanged(t *testing.T) {
	db := setupCodexCredentialRefreshTestDB(t)
	oldKey := `[{"access_token":"first","refresh_token":"refresh-first","account_id":"account-first"},{"access_token":"second","refresh_token":"refresh-second","account_id":"account-second"}]`
	channel := &model.Channel{
		Type: constant.ChannelTypeCodex,
		Key:  oldKey,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
		},
	}
	require.NoError(t, db.Create(channel).Error)
	rawKeys := channel.GetKeys()
	firstCredential, err := model.NewChannelCredential(channel.Id, 0, rawKeys[0])
	require.NoError(t, err)
	require.NoError(t, db.Create(firstCredential).Error)

	newFirst := `{"access_token":"first-new","refresh_token":"refresh-first-new","account_id":"account-first"}`
	newSecond := `{"access_token":"second-new","refresh_token":"refresh-second-new","account_id":"account-second"}`
	encoded := "[" + newFirst + "," + newSecond + "]"
	err = persistRefreshedCodexCredentials(channel, rawKeys, encoded, true, map[int]string{
		0: newFirst,
		1: newSecond,
	})
	require.Error(t, err)

	var persistedChannel model.Channel
	require.NoError(t, db.First(&persistedChannel, channel.Id).Error)
	assert.Equal(t, oldKey, persistedChannel.Key)
	var persistedFirst model.ChannelCredential
	require.NoError(t, db.First(&persistedFirst, firstCredential.Id).Error)
	assert.Equal(t, rawKeys[0], persistedFirst.Secret)
}
