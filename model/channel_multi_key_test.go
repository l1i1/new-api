package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestChannelGetNextEnabledKeyAffinity(t *testing.T) {
	channel := &Channel{
		Key: "key-0\nkey-1\nkey-2",
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeyMode: constant.MultiKeyModeAffinity,
		},
	}

	key, index, err := channel.GetNextEnabledKey(4)
	require.Nil(t, err)
	require.Contains(t, []string{"key-0", "key-1", "key-2"}, key)

	for range 3 {
		repeatedKey, repeatedIndex, repeatedErr := channel.GetNextEnabledKey(4)
		require.Nil(t, repeatedErr)
		require.Equal(t, key, repeatedKey)
		require.Equal(t, index, repeatedIndex)
	}

	otherChannel := &Channel{Key: channel.Key, ChannelInfo: channel.ChannelInfo}
	otherKey, otherIndex, err := otherChannel.GetNextEnabledKey(4)
	require.Nil(t, err)
	require.Equal(t, key, otherKey)
	require.Equal(t, index, otherIndex)

	unrelatedIndex := (index + 1) % 3
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{
		unrelatedIndex: common.ChannelStatusManuallyDisabled,
	}
	keyAfterUnrelatedDisable, indexAfterUnrelatedDisable, err := channel.GetNextEnabledKey(4)
	require.Nil(t, err)
	require.Equal(t, key, keyAfterUnrelatedDisable)
	require.Equal(t, index, indexAfterUnrelatedDisable)

	channel.ChannelInfo.MultiKeyStatusList = map[int]int{
		index: common.ChannelStatusManuallyDisabled,
	}
	fallbackKey, fallbackIndex, err := channel.GetNextEnabledKey(4)
	require.Nil(t, err)
	require.NotEqual(t, key, fallbackKey)
	require.NotEqual(t, index, fallbackIndex)

	for range 3 {
		repeatedKey, repeatedIndex, repeatedErr := channel.GetNextEnabledKey(4)
		require.Nil(t, repeatedErr)
		require.Equal(t, fallbackKey, repeatedKey)
		require.Equal(t, fallbackIndex, repeatedIndex)
	}
}
