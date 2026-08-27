package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
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

func TestGetRandomSatisfiedChannelPrefersOfficialDeepSeekForV4Models(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	oldGroup2Model2Channels := group2model2channels
	oldChannelsIDM := channelsIDM
	oldSelection := group2model2channelSelection
	group2model2channels = map[string]map[string][]int{
		"default": {"deepseek-v4-flash": {1, 2}},
	}
	channelsIDM = map[int]*Channel{
		1: {Id: 1, Type: constant.ChannelTypeDeepSeek, Priority: int64Ptr(10), Weight: uintPtr(1)},
		2: {Id: 2, Type: constant.ChannelTypeOpenAI, Priority: int64Ptr(10), Weight: uintPtr(1)},
	}
	group2model2channelSelection = map[string]map[string]*channelSelectionMetadata{
		"default": {
			"deepseek-v4-flash": buildChannelSelectionMetadata(group2model2channels["default"]["deepseek-v4-flash"], channelsIDM),
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

	// Unpinned requests keep weighted selection across both channel types;
	// pinned requests narrow to the official channel.
	unpinned := map[int]bool{}
	for range 30 {
		selected, err := GetRandomSatisfiedChannelPinned("default", "deepseek-v4-flash", 0, "", nil, false)
		require.NoError(t, err)
		unpinned[selected.Id] = true
	}
	require.True(t, unpinned[1] && unpinned[2], "unpinned V4 requests keep weighted selection across channel types")

	for range 30 {
		selected, err := GetRandomSatisfiedChannelPinned("default", "deepseek-v4-flash", 0, "", nil, true)
		require.NoError(t, err)
		require.Equal(t, 1, selected.Id, "pinned V4 requests always select the official channel")
	}
}

func TestGetRandomSatisfiedChannelKeepsAggregatorsWhenNoOfficialDeepSeek(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	oldGroup2Model2Channels := group2model2channels
	oldChannelsIDM := channelsIDM
	oldSelection := group2model2channelSelection
	group2model2channels = map[string]map[string][]int{
		"default": {"deepseek-v4-flash": {3, 4}},
	}
	channelsIDM = map[int]*Channel{
		3: {Id: 3, Type: constant.ChannelTypeOpenAI, Priority: int64Ptr(10), Weight: uintPtr(1)},
		4: {Id: 4, Type: constant.ChannelTypeOpenAI, Priority: int64Ptr(10), Weight: uintPtr(1)},
	}
	group2model2channelSelection = map[string]map[string]*channelSelectionMetadata{
		"default": {
			"deepseek-v4-flash": buildChannelSelectionMetadata(group2model2channels["default"]["deepseek-v4-flash"], channelsIDM),
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

	for range 30 {
		selected, err := GetRandomSatisfiedChannel("default", "deepseek-v4-flash", 0, "")
		require.NoError(t, err)
		require.Contains(t, []int{3, 4}, selected.Id)
	}
}

func TestGetRandomSatisfiedChannelPinnedKimiK3PrefersMoonshot(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	oldGroup2Model2Channels := group2model2channels
	oldChannelsIDM := channelsIDM
	oldSelection := group2model2channelSelection
	group2model2channels = map[string]map[string][]int{
		"default": {"kimi-k3": {1, 2}},
	}
	channelsIDM = map[int]*Channel{
		1: {Id: 1, Type: constant.ChannelTypeMoonshot, Priority: int64Ptr(10), Weight: uintPtr(1)},
		2: {Id: 2, Type: constant.ChannelTypeOpenAI, Priority: int64Ptr(10), Weight: uintPtr(1)},
	}
	group2model2channelSelection = map[string]map[string]*channelSelectionMetadata{
		"default": {
			"kimi-k3": buildChannelSelectionMetadata(group2model2channels["default"]["kimi-k3"], channelsIDM),
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

	// Unpinned kimi-k3 keeps weighted selection across aggregators and the
	// official Moonshot channel; the Route pin narrows to the official one.
	unpinned := map[int]bool{}
	for range 30 {
		selected, err := GetRandomSatisfiedChannelPinned("default", "kimi-k3", 0, "", nil, false)
		require.NoError(t, err)
		unpinned[selected.Id] = true
	}
	require.True(t, unpinned[1] && unpinned[2], "unpinned kimi-k3 keeps weighted selection across channel types")

	for range 30 {
		selected, err := GetRandomSatisfiedChannelPinned("default", "kimi-k3", 0, "", nil, true)
		require.NoError(t, err)
		require.Equal(t, 1, selected.Id, "pinned kimi-k3 requests always select the Moonshot official channel")
	}

	// Non-fit families are unaffected by the pin.
	require.Equal(t, 0, officialFitChannelType("qwen3.7-max"))
}

func TestGetRandomSatisfiedChannelUnaffectedForNonV4Models(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	oldGroup2Model2Channels := group2model2channels
	oldChannelsIDM := channelsIDM
	oldSelection := group2model2channelSelection
	group2model2channels = map[string]map[string][]int{
		"default": {"gpt-test": {1, 2}},
	}
	channelsIDM = map[int]*Channel{
		1: {Id: 1, Type: constant.ChannelTypeDeepSeek, Priority: int64Ptr(10), Weight: uintPtr(1)},
		2: {Id: 2, Type: constant.ChannelTypeOpenAI, Priority: int64Ptr(10), Weight: uintPtr(1)},
	}
	group2model2channelSelection = map[string]map[string]*channelSelectionMetadata{
		"default": {
			"gpt-test": buildChannelSelectionMetadata(group2model2channels["default"]["gpt-test"], channelsIDM),
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

	seen := map[int]bool{}
	for range 30 {
		selected, err := GetRandomSatisfiedChannel("default", "gpt-test", 0, "")
		require.NoError(t, err)
		seen[selected.Id] = true
	}
	require.True(t, seen[1] && seen[2], "non-V4 models must keep weighted selection across channel types")
}

func TestGetRandomSatisfiedChannelPrefersOfficialDeepSeekWithMultipleOfficial(t *testing.T) {
	// Two official channels at the same priority: the metadata fast path must
	// be bypassed so both remain selectable but no aggregator leaks in.
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	oldGroup2Model2Channels := group2model2channels
	oldChannelsIDM := channelsIDM
	oldSelection := group2model2channelSelection
	group2model2channels = map[string]map[string][]int{
		"default": {"deepseek-v4-flash": {1, 5, 2}},
	}
	channelsIDM = map[int]*Channel{
		1: {Id: 1, Type: constant.ChannelTypeDeepSeek, Priority: int64Ptr(10), Weight: uintPtr(1)},
		5: {Id: 5, Type: constant.ChannelTypeDeepSeek, Priority: int64Ptr(10), Weight: uintPtr(1)},
		2: {Id: 2, Type: constant.ChannelTypeOpenAI, Priority: int64Ptr(10), Weight: uintPtr(1)},
	}
	group2model2channelSelection = map[string]map[string]*channelSelectionMetadata{
		"default": {
			"deepseek-v4-flash": buildChannelSelectionMetadata(group2model2channels["default"]["deepseek-v4-flash"], channelsIDM),
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

	for range 30 {
		selected, err := GetRandomSatisfiedChannelPinned("default", "deepseek-v4-flash", 0, "", nil, true)
		require.NoError(t, err)
		require.Contains(t, []int{1, 5}, selected.Id, "pinned V4 requests must never select the aggregator when officials exist")
	}
}
