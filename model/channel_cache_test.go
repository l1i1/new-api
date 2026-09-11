package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
	require.Equal(t, 0, OfficialFitChannelType("qwen3.7-max"))
}

// The official-fit DeepSeek family spans the v4 and v4.1 lines: the prefix
// deliberately omits the dash so deepseek-v4.1-* maps to the official channel
// type too.
func TestOfficialFitChannelTypeCoversDeepSeekV41(t *testing.T) {
	require.Equal(t, constant.ChannelTypeDeepSeek, OfficialFitChannelType("deepseek-v4-flash"))
	require.Equal(t, constant.ChannelTypeDeepSeek, OfficialFitChannelType("deepseek-v4.1-flash"))
	require.Equal(t, constant.ChannelTypeDeepSeek, OfficialFitChannelType("DEEPSEEK-V4.1-FLASH"))
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

// A channel that declares deepseek-v4.1-flash in official_fit_models is
// official-behaving for that model even though its type is a plain aggregator.
// A pinned request must select it and must NOT fall back to a same-family
// aggregator that did not declare the model.
func TestGetRandomSatisfiedChannelPinnedHonorsOfficialFitModelsAllowlist(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	oldGroup2Model2Channels := group2model2channels
	oldChannelsIDM := channelsIDM
	oldSelection := group2model2channelSelection
	oldOfficialModels := channel2officialFitModels
	// The candidate slice is priority-descending, as InitChannelCache leaves it
	// before building the selection metadata: 8 (priority 31) then 7 (priority 0).
	group2model2channels = map[string]map[string][]int{
		"default": {"deepseek-v4.1-flash": {8, 7}},
	}
	channelsIDM = map[int]*Channel{
		// 8 is the third-party channel (serves the same-named model) and holds
		// the higher priority — this reproduces the production failure where
		// OpenRouter priority 31 outranked every official-baseline channel.
		// 7 is the reseller that maps v4.1 onto the official deepseek-flash and
		// declares that in official_fit_models.
		7: {Id: 7, Type: constant.ChannelTypeOpenAI, Priority: int64Ptr(0), Weight: uintPtr(1)},
		8: {Id: 8, Type: constant.ChannelTypeOpenAI, Priority: int64Ptr(31), Weight: uintPtr(1)},
	}
	channel2officialFitModels = map[int]map[string]struct{}{
		7: {"deepseek-v4.1-flash": {}},
	}
	group2model2channelSelection = map[string]map[string]*channelSelectionMetadata{
		"default": {
			"deepseek-v4.1-flash": buildChannelSelectionMetadata(group2model2channels["default"]["deepseek-v4.1-flash"], channelsIDM),
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
		channel2officialFitModels = oldOfficialModels
		channelSyncLock.Unlock()
	})

	for range 30 {
		selected, err := GetRandomSatisfiedChannelPinned("default", "deepseek-v4.1-flash", 0, "", nil, true)
		require.NoError(t, err)
		require.Equal(t, 7, selected.Id,
			"pinned requests must reach the channel that declared the model, even though 8 has far higher priority")
	}

	// Unpinned requests are untouched by the allowlist: selection still starts
	// at the highest priority tier, which is 8 here.
	for range 30 {
		selected, err := GetRandomSatisfiedChannelPinned("default", "deepseek-v4.1-flash", 0, "", nil, false)
		require.NoError(t, err)
		require.Equal(t, 8, selected.Id, "the allowlist must not drag normal traffic onto the verified channel")
	}
}

// A pinned request with no official-behaving candidate must fail honestly
// rather than silently degrade to an aggregator (the pin is hard).
func TestGetRandomSatisfiedChannelPinnedFailsWhenAllowlistHasNoCandidate(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	oldGroup2Model2Channels := group2model2channels
	oldChannelsIDM := channelsIDM
	oldSelection := group2model2channelSelection
	oldOfficialModels := channel2officialFitModels
	group2model2channels = map[string]map[string][]int{
		"default": {"deepseek-v4.1-flash": {8}},
	}
	channelsIDM = map[int]*Channel{
		8: {Id: 8, Type: constant.ChannelTypeOpenAI, Priority: int64Ptr(10), Weight: uintPtr(1)},
	}
	channel2officialFitModels = nil
	group2model2channelSelection = nil
	channelSyncLock.Unlock()
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		channelSyncLock.Lock()
		group2model2channels = oldGroup2Model2Channels
		channelsIDM = oldChannelsIDM
		group2model2channelSelection = oldSelection
		channel2officialFitModels = oldOfficialModels
		channelSyncLock.Unlock()
	})

	selected, err := GetRandomSatisfiedChannelPinned("default", "deepseek-v4.1-flash", 0, "", nil, true)
	require.NoError(t, err)
	require.Nil(t, selected, "a pinned request without an official candidate must not fall back to an aggregator")
}

// ChannelIsOfficialFitForModel reads the cache index built at sync time, so a
// declaring channel (aggregator type) is official for the declared model only,
// and the family's official type stays official without any declaration.
func TestChannelIsOfficialFitForModelUsesCacheIndex(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	oldChannelsIDM := channelsIDM
	oldOfficialModels := channel2officialFitModels
	channelsIDM = map[int]*Channel{
		7: {Id: 7, Type: constant.ChannelTypeOpenAI},
		5: {Id: 5, Type: constant.ChannelTypeDeepSeek},
	}
	channel2officialFitModels = map[int]map[string]struct{}{
		7: {"deepseek-v4.1-flash": {}},
	}
	channelSyncLock.Unlock()
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		channelSyncLock.Lock()
		channelsIDM = oldChannelsIDM
		channel2officialFitModels = oldOfficialModels
		channelSyncLock.Unlock()
	})

	require.True(t, ChannelIsOfficialFitForModel(7, "deepseek-v4.1-flash"))
	require.True(t, ChannelIsOfficialFitForModel(7, "DEEPSEEK-V4.1-FLASH"))
	require.False(t, ChannelIsOfficialFitForModel(7, "deepseek-v4-flash"),
		"a channel is official only for the model it declared")
	require.True(t, ChannelIsOfficialFitForModel(5, "deepseek-v4.1-flash"),
		"the family's official channel type needs no declaration")
	require.False(t, ChannelIsOfficialFitForModel(5, "gpt-4o"),
		"models outside the family are never official")
}

// The incremental cache update must keep the official-fit allowlist indexes in
// step. Before that was wired in, editing a channel's allowlist left the
// reverse index serving the stale model set, so a pinned request could still
// reach a channel whose declaration had been removed (or miss a new one).
func TestCacheUpdateChannelSyncsOfficialFitModels(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	oldChannelsIDM := channelsIDM
	oldOfficialModels := channel2officialFitModels
	channelsIDM = map[int]*Channel{}
	channel2officialFitModels = map[int]map[string]struct{}{}
	channelSyncLock.Unlock()
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		channelSyncLock.Lock()
		channelsIDM = oldChannelsIDM
		channel2officialFitModels = oldOfficialModels
		channelSyncLock.Unlock()
	})

	channel := &Channel{Id: 701, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "ch-701"}
	channel.SetOtherSettings(dto.ChannelOtherSettings{OfficialFitModels: []string{"deepseek-v4.1-flash"}})
	CacheUpdateChannel(channel)

	assert.True(t, ChannelIsOfficialFitForModel(701, "deepseek-v4.1-flash"))
	assert.False(t, ChannelIsOfficialFitForModel(701, "deepseek-v4-flash"))

	// Widening the allowlist replaces the old entry rather than accumulating it.
	channel.SetOtherSettings(dto.ChannelOtherSettings{OfficialFitModels: []string{"deepseek-v4.1-flash", "deepseek-v4-flash"}})
	CacheUpdateChannel(channel)
	assert.True(t, ChannelIsOfficialFitForModel(701, "deepseek-v4.1-flash"))
	assert.True(t, ChannelIsOfficialFitForModel(701, "deepseek-v4-flash"))

	// Clearing it removes the channel from the index.
	channel.SetOtherSettings(dto.ChannelOtherSettings{})
	CacheUpdateChannel(channel)
	assert.False(t, ChannelIsOfficialFitForModel(701, "deepseek-v4.1-flash"))
	assert.False(t, ChannelIsOfficialFitForModel(701, "deepseek-v4-flash"))
	channelSyncLock.RLock()
	require.NotContains(t, channel2officialFitModels, 701)
	channelSyncLock.RUnlock()
}

// The cache index must read settings without Channel.GetOtherSettings, whose
// malformed-JSON self-heal rewrites the row. A rebuild runs for every channel,
// so this path must leave a corrupt row untouched (save-time validation owns it).
func TestOfficialFitModelsForCacheDoesNotMutateChannel(t *testing.T) {
	channel := &Channel{Id: 702, Type: constant.ChannelTypeOpenAI, OtherSettings: "{not-json"}
	require.Nil(t, officialFitModelsForCache(channel))
	assert.Equal(t, "{not-json", channel.OtherSettings,
		"the cache read must not self-heal (and thereby write) a corrupt settings row")

	channel.OtherSettings = ""
	require.Nil(t, officialFitModelsForCache(channel))

	channel.SetOtherSettings(dto.ChannelOtherSettings{OfficialFitModels: []string{" DeepSeek-V4.1-Flash "}})
	require.Equal(t, []string{"deepseek-v4.1-flash"}, officialFitModelsForCache(channel))
}

// The DB fallback (memory cache disabled) must still classify by the allowlist.
// It reads only the classifying columns, so the channel key is never loaded.
func TestChannelIsOfficialFitForModelDBFallback(t *testing.T) {
	previousDB := DB
	previousMemoryCache := common.MemoryCacheEnabled
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&Channel{}))
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		DB = previousDB
		common.MemoryCacheEnabled = previousMemoryCache
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})

	declared := &Channel{Id: 801, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "declared", Key: "unused"}
	declared.SetOtherSettings(dto.ChannelOtherSettings{OfficialFitModels: []string{"deepseek-v4.1-flash"}})
	require.NoError(t, DB.Create(declared).Error)

	official := &Channel{Id: 802, Type: constant.ChannelTypeDeepSeek, Status: common.ChannelStatusEnabled, Name: "official", Key: "unused"}
	require.NoError(t, DB.Create(official).Error)

	plain := &Channel{Id: 803, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "plain", Key: "unused"}
	require.NoError(t, DB.Create(plain).Error)

	assert.True(t, ChannelIsOfficialFitForModel(801, "deepseek-v4.1-flash"), "allowlist declares it")
	assert.False(t, ChannelIsOfficialFitForModel(801, "deepseek-v4-flash"), "only the declared model")
	assert.True(t, ChannelIsOfficialFitForModel(802, "deepseek-v4.1-flash"), "official channel type")
	assert.False(t, ChannelIsOfficialFitForModel(803, "deepseek-v4.1-flash"), "plain aggregator")
	assert.False(t, ChannelIsOfficialFitForModel(801, "gpt-4o"), "outside the family")
	assert.False(t, ChannelIsOfficialFitForModel(999, "deepseek-v4.1-flash"), "missing channel")
}
