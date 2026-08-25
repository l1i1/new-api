package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestGetRandomSatisfiedChannelPreservesCachedPrioritySelection(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	oldGroup2Model2Channels := group2model2channels
	oldChannelsIDM := channelsIDM
	oldSelection := group2model2channelSelection
	group2model2channels = map[string]map[string][]int{
		"default": {"test-model": {1, 2, 3}},
	}
	channelsIDM = map[int]*Channel{
		1: {Id: 1, Priority: int64Ptr(10), Weight: uintPtr(2)},
		2: {Id: 2, Priority: int64Ptr(10), Weight: uintPtr(3)},
		3: {Id: 3, Priority: int64Ptr(5), Weight: uintPtr(100)},
	}
	group2model2channelSelection = map[string]map[string]*channelSelectionMetadata{
		"default": {
			"test-model": buildChannelSelectionMetadata(group2model2channels["default"]["test-model"], channelsIDM),
		},
	}
	channelSyncLock.Unlock()
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		channelSyncLock.Lock()
		group2model2channels = oldGroup2Model2Channels
		channelsIDM = oldChannelsIDM
		group2model2channelSelection = oldSelection
		channelSyncLock.Unlock()
	})

	selected, err := GetRandomSatisfiedChannel("default", "test-model", 0, "")
	require.NoError(t, err)
	require.Contains(t, []int{1, 2}, selected.Id)

	selected, err = GetRandomSatisfiedChannel("default", "test-model", 1, "")
	require.NoError(t, err)
	require.Equal(t, 3, selected.Id)

	selected, err = GetRandomSatisfiedChannel("default", "test-model", 99, "")
	require.NoError(t, err)
	require.Equal(t, 3, selected.Id)
}

func TestCacheUpdateChannelStatusDropsDisabledChannelFromSelection(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	oldGroup2Model2Channels := group2model2channels
	oldChannelsIDM := channelsIDM
	oldSelection := group2model2channelSelection
	group2model2channels = map[string]map[string][]int{
		"default": {"test-model": {1, 2}},
	}
	channelsIDM = map[int]*Channel{
		1: {Id: 1, Priority: int64Ptr(10), Weight: uintPtr(0), Status: common.ChannelStatusEnabled},
		2: {Id: 2, Priority: int64Ptr(10), Weight: uintPtr(0), Status: common.ChannelStatusEnabled},
	}
	group2model2channelSelection = map[string]map[string]*channelSelectionMetadata{
		"default": {
			"test-model": buildChannelSelectionMetadata(group2model2channels["default"]["test-model"], channelsIDM),
		},
	}
	channelSyncLock.Unlock()
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		channelSyncLock.Lock()
		group2model2channels = oldGroup2Model2Channels
		channelsIDM = oldChannelsIDM
		group2model2channelSelection = oldSelection
		channelSyncLock.Unlock()
	})

	CacheUpdateChannelStatus(1, common.ChannelStatusAutoDisabled)

	// The selection metadata is discarded, and the slow path must respect the
	// removal from group2model2channels: only channel 2 remains selectable.
	channelSyncLock.RLock()
	remaining := group2model2channels["default"]["test-model"]
	channelSyncLock.RUnlock()
	require.Equal(t, []int{2}, remaining)

	for range 20 {
		selected, err := GetRandomSatisfiedChannel("default", "test-model", 0, "")
		require.NoError(t, err)
		require.Equal(t, 2, selected.Id)
	}
}

func TestGetRandomSatisfiedChannelReportsUninitializedCache(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	oldGroup2Model2Channels := group2model2channels
	oldChannelsIDM := channelsIDM
	group2model2channels = nil
	channelsIDM = nil
	channelSyncLock.Unlock()
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		channelSyncLock.Lock()
		group2model2channels = oldGroup2Model2Channels
		channelsIDM = oldChannelsIDM
		channelSyncLock.Unlock()
	})

	selected, err := GetRandomSatisfiedChannel("default", "test-model", 0, "")
	require.Nil(t, selected)
	require.EqualError(t, err, "channel cache is not initialized")
}

func int64Ptr(value int64) *int64 {
	return &value
}

func uintPtr(value uint) *uint {
	return &value
}
