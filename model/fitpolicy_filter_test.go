package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installFitSelectionCache publishes an in-memory candidate set for
// deepseek-v4-flash: two official (DeepSeek-typed) channels and one aggregator.
func installFitSelectionCache(t *testing.T) {
	t.Helper()
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	oldGroup2Model2Channels := group2model2channels
	oldChannelsIDM := channelsIDM
	oldSelection := group2model2channelSelection
	group2model2channels = map[string]map[string][]int{
		"default": {"deepseek-v4-flash": {1, 2, 3}},
	}
	channelsIDM = map[int]*Channel{
		1: {Id: 1, Type: constant.ChannelTypeDeepSeek, Priority: int64Ptr(10), Weight: uintPtr(1)},
		2: {Id: 2, Type: constant.ChannelTypeDeepSeek, Priority: int64Ptr(10), Weight: uintPtr(1)},
		3: {Id: 3, Type: constant.ChannelTypeOpenAI, Priority: int64Ptr(10), Weight: uintPtr(1)},
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
}

// TestFitAwareSelectionNarrowsToMarkedChannels is the phase-1 gate: when a
// capability mark is available, only channels that satisfy it may be selected,
// and the aggregator is never a fallback.
func TestFitAwareSelectionNarrowsToMarkedChannels(t *testing.T) {
	installFitSelectionCache(t)

	fit := &FitChannelFilter{
		Requirement:   fitpolicy.Requirement{Marks: []string{"usage.thinking_counting"}, Model: "deepseek-v4-flash"},
		MarkSatisfied: func(channelID int) bool { return channelID == 2 },
	}
	for range 30 {
		selected, err := GetRandomSatisfiedChannelPinnedWithFit("default", "deepseek-v4-flash", 0, "", nil, true, false, fit)
		require.NoError(t, err)
		require.Equal(t, 2, selected.Id, "only the channel satisfying every required behaviour may be selected")
	}
}

// TestFitAwareSelectionWithoutMarksMatchesLegacyPin is the step-A equivalence
// gate for the selector: with no capability data, the fit-aware path must
// reproduce the legacy official pin exactly — never the aggregator.
func TestFitAwareSelectionWithoutMarksMatchesLegacyPin(t *testing.T) {
	installFitSelectionCache(t)

	fit := &FitChannelFilter{
		Requirement: fitpolicy.Requirement{Marks: []string{"usage.thinking_counting"}, Model: "deepseek-v4-flash"},
	}
	seen := map[int]bool{}
	for range 30 {
		selected, err := GetRandomSatisfiedChannelPinnedWithFit("default", "deepseek-v4-flash", 0, "", nil, true, false, fit)
		require.NoError(t, err)
		seen[selected.Id] = true
		require.NotEqual(t, 3, selected.Id, "the aggregator must never serve a pinned request")
	}
	require.True(t, seen[1] && seen[2], "both official channels stay eligible")

	// A filter with no opinion must be indistinguishable from the legacy call.
	noOpinion := &FitChannelFilter{}
	for range 30 {
		withFilter, err := GetRandomSatisfiedChannelPinnedWithFit("default", "deepseek-v4-flash", 0, "", nil, true, false, noOpinion)
		require.NoError(t, err)
		require.NotEqual(t, 3, withFilter.Id)
		legacy, err := GetRandomSatisfiedChannelPinned("default", "deepseek-v4-flash", 0, "", nil, true, false)
		require.NoError(t, err)
		require.NotEqual(t, 3, legacy.Id)
	}
}

// TestFitAwareSelectionFailsClosedWhenNoMarkedChannel confirms phase 3 keeps
// today's honest failure instead of widening to the aggregator.
func TestFitAwareSelectionFailsClosedWhenNoMarkedChannel(t *testing.T) {
	installFitSelectionCache(t)

	// Every official channel is excluded by the marks, so the marked subset is
	// empty and phase 2 (the official set) still applies.
	fit := &FitChannelFilter{
		Requirement:   fitpolicy.Requirement{Marks: []string{"usage.thinking_counting"}},
		MarkSatisfied: func(int) bool { return false },
	}
	for range 20 {
		selected, err := GetRandomSatisfiedChannelPinnedWithFit("default", "deepseek-v4-flash", 0, "", nil, true, false, fit)
		require.NoError(t, err)
		require.NotEqual(t, 3, selected.Id, "an unsatisfied mark must fall back to official channels, never to the aggregator")
	}

	// An empty marked set combined with an empty official set fails with the
	// existing nil result rather than degrading.
	emptyOfficial := &FitChannelFilter{Requirement: fitpolicy.Requirement{Marks: []string{"usage.thinking_counting"}}}
	selected, err := GetRandomSatisfiedChannelPinnedWithFit("default", "deepseek-v4-pro", 0, "", nil, true, false, emptyOfficial)
	require.NoError(t, err)
	require.Nil(t, selected, "no candidate must stay nil so the caller reports its existing error")
}

func TestFitChannelFilterForRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	if got := FitChannelFilterForRequest(nil); got != nil {
		t.Fatal("a nil context must yield no filter")
	}

	empty, _ := gin.CreateTestContext(nil)
	if got := FitChannelFilterForRequest(empty); got != nil {
		t.Fatal("a request without a requirement must yield no filter")
	}

	withOpinion, _ := gin.CreateTestContext(nil)
	common.SetContextKey(withOpinion, constant.ContextKeyFitRequirement, fitpolicy.Requirement{
		Family: "deepseek-v4", Model: "deepseek-v4-flash", Marks: []string{"m"},
	})
	filter := FitChannelFilterForRequest(withOpinion)
	require.NotNil(t, filter)
	assert.Equal(t, []string{"m"}, filter.Requirement.Marks)

	noOpinion, _ := gin.CreateTestContext(nil)
	common.SetContextKey(noOpinion, constant.ContextKeyFitRequirement, fitpolicy.Requirement{Family: "deepseek-v4"})
	assert.Nil(t, FitChannelFilterForRequest(noOpinion), "a requirement with no marks must not constrain selection")
}
