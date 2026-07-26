package middleware

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSetupContextForSelectedChannelUsesTokenAffinity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, constant.ContextKeyTokenId, 5)
	channel := &model.Channel{
		Id:     93,
		Type:   constant.ChannelTypeOpenAI,
		Key:    "key-0\nkey-1\nkey-2",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeyMode: constant.MultiKeyModeAffinity,
		},
	}
	expectedKey, expectedIndex, expectedError := channel.GetNextEnabledKey(5)
	require.Nil(t, expectedError)

	err := SetupContextForSelectedChannel(ctx, channel, "deepseek-v4-flash")
	require.Nil(t, err)
	require.Equal(t, expectedKey, common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
	require.Equal(t, expectedIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
}
