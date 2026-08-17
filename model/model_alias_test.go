package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveCompactModelAliasFromModels(t *testing.T) {
	tests := []struct {
		name        string
		requested   string
		available   []string
		wantModel   string
		wantAliased bool
	}{
		{
			name:        "uses base model when exact compact model is absent",
			requested:   "gpt-5.5-openai-compact",
			available:   []string{"gpt-5.5"},
			wantModel:   "gpt-5.5",
			wantAliased: true,
		},
		{
			name:        "supports arbitrary compact model prefixes",
			requested:   "gpt-5.6-sol-openai-compact",
			available:   []string{"gpt-5.6-sol"},
			wantModel:   "gpt-5.6-sol",
			wantAliased: true,
		},
		{
			name:        "exact compact model takes precedence",
			requested:   "gpt-5.5-openai-compact",
			available:   []string{"gpt-5.5", "gpt-5.5-openai-compact"},
			wantModel:   "gpt-5.5-openai-compact",
			wantAliased: false,
		},
		{
			name:        "missing base keeps requested model",
			requested:   "gpt-5.5-openai-compact",
			available:   []string{"gpt-5.4"},
			wantModel:   "gpt-5.5-openai-compact",
			wantAliased: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, aliased := ResolveCompactModelAliasFromModels(tt.requested, tt.available)
			assert.Equal(t, tt.wantModel, resolved)
			assert.Equal(t, tt.wantAliased, aliased)
		})
	}
}

func TestResolveCompactModelAliasForGroupUsesMemoryCache(t *testing.T) {
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	originalGroupModels := group2model2channels
	group2model2channels = map[string]map[string][]int{
		"default": {
			"gpt-5.5": {101},
		},
		"exact": {
			"gpt-5.5":                {102},
			"gpt-5.5-openai-compact": {103},
		},
	}
	channelSyncLock.Unlock()
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		channelSyncLock.Lock()
		group2model2channels = originalGroupModels
		channelSyncLock.Unlock()
	})

	resolved, aliased := ResolveCompactModelAliasForGroup("default", "gpt-5.5-openai-compact")
	assert.Equal(t, "gpt-5.5", resolved)
	assert.True(t, aliased)

	resolved, aliased = ResolveCompactModelAliasForGroup("exact", "gpt-5.5-openai-compact")
	assert.Equal(t, "gpt-5.5-openai-compact", resolved)
	assert.False(t, aliased)

	resolved, aliased = ResolveCompactModelAliasForGroup("missing", "gpt-5.5-openai-compact")
	assert.Equal(t, "gpt-5.5-openai-compact", resolved)
	assert.False(t, aliased)
}

func TestResolveCompactModelAliasForGroupPathUsesEligibleAdvancedCustomRoute(t *testing.T) {
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	originalGroupModels := group2model2channels
	originalChannels := channelsIDM
	originalConfigs := channel2advancedCustomConfig
	group2model2channels = map[string]map[string][]int{
		"default": {
			"gpt-5.5-openai-compact": {301},
			"gpt-5.5":                {302},
		},
	}
	channelsIDM = map[int]*Channel{
		301: {Id: 301, Type: constant.ChannelTypeAdvancedCustom},
		302: {Id: 302, Type: constant.ChannelTypeAdvancedCustom},
	}
	channel2advancedCustomConfig = map[int]*dto.AdvancedCustomConfig{
		301: {
			Routes: []dto.AdvancedCustomRoute{{
				IncomingPath: "/v1/responses",
				Models:       []string{"gpt-5.5-openai-compact"},
			}},
		},
		302: {
			Routes: []dto.AdvancedCustomRoute{{
				IncomingPath: "/v1/chat/completions",
				Models:       []string{"gpt-5.5"},
			}},
		},
	}
	channelSyncLock.Unlock()
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		channelSyncLock.Lock()
		group2model2channels = originalGroupModels
		channelsIDM = originalChannels
		channel2advancedCustomConfig = originalConfigs
		channelSyncLock.Unlock()
	})

	resolved, aliased := ResolveCompactModelAliasForGroupPath(
		"default", "gpt-5.5-openai-compact", "/v1/chat/completions",
	)
	assert.Equal(t, "gpt-5.5", resolved)
	assert.True(t, aliased)

	resolved, aliased = ResolveCompactModelAliasForGroupPath(
		"default", "gpt-5.5-openai-compact", "/v1/responses",
	)
	assert.Equal(t, "gpt-5.5-openai-compact", resolved)
	assert.False(t, aliased)
}

func TestResolveCompactModelAliasForChannelUsesEligibleAdvancedCustomRoute(t *testing.T) {
	channel := &Channel{
		Type:   constant.ChannelTypeAdvancedCustom,
		Models: "gpt-5.5-openai-compact,gpt-5.5",
	}
	channel.SetOtherSettings(dto.ChannelOtherSettings{
		AdvancedCustom: &dto.AdvancedCustomConfig{
			Routes: []dto.AdvancedCustomRoute{
				{
					IncomingPath: "/v1/responses",
					Models:       []string{"gpt-5.5-openai-compact"},
				},
				{
					IncomingPath: "/v1/chat/completions",
					Models:       []string{"gpt-5.5"},
				},
			},
		},
	})

	resolved, aliased := ResolveCompactModelAliasForChannel(
		channel, "gpt-5.5-openai-compact", "/v1/chat/completions",
	)
	assert.Equal(t, "gpt-5.5", resolved)
	assert.True(t, aliased)

	resolved, aliased = ResolveCompactModelAliasForChannel(
		channel, "gpt-5.5-openai-compact", "/v1/responses",
	)
	assert.Equal(t, "gpt-5.5-openai-compact", resolved)
	assert.False(t, aliased)
}

func TestCompactModelAliasRoutesThroughDatabaseSelection(t *testing.T) {
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
	})

	const (
		groupName      = "compact-alias-db-test"
		baseChannelID  = 910001
		exactChannelID = 910002
	)
	priority := int64(0)
	weight := uint(100)
	for _, channelID := range []int{baseChannelID, exactChannelID} {
		require.NoError(t, DB.Where("id = ?", channelID).Delete(&Channel{}).Error)
	}
	require.NoError(t, DB.Where(commonGroupCol+" = ?", groupName).Delete(&Ability{}).Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Where(commonGroupCol+" = ?", groupName).Delete(&Ability{}).Error)
		require.NoError(t, DB.Where("id IN ?", []int{baseChannelID, exactChannelID}).Delete(&Channel{}).Error)
	})

	baseChannel := &Channel{
		Id:       baseChannelID,
		Type:     constant.ChannelTypeOpenAI,
		Key:      "base-key",
		Status:   common.ChannelStatusEnabled,
		Name:     "base-channel",
		Weight:   &weight,
		Models:   "gpt-5.5",
		Group:    groupName,
		Priority: &priority,
	}
	require.NoError(t, DB.Create(baseChannel).Error)
	require.NoError(t, DB.Create(&Ability{
		Group: groupName, Model: "gpt-5.5", ChannelId: baseChannelID,
		Enabled: true, Priority: &priority, Weight: weight,
	}).Error)

	resolved, aliased := ResolveCompactModelAliasForGroup(groupName, "gpt-5.5-openai-compact")
	assert.Equal(t, "gpt-5.5", resolved)
	assert.True(t, aliased)
	selected, err := GetRandomSatisfiedChannel(groupName, resolved, 0, "/v1/responses/compact")
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, baseChannelID, selected.Id)

	exactChannel := &Channel{
		Id:       exactChannelID,
		Type:     constant.ChannelTypeOpenAI,
		Key:      "exact-key",
		Status:   common.ChannelStatusEnabled,
		Name:     "exact-channel",
		Weight:   &weight,
		Models:   "gpt-5.5-openai-compact",
		Group:    groupName,
		Priority: &priority,
	}
	require.NoError(t, DB.Create(exactChannel).Error)
	require.NoError(t, DB.Create(&Ability{
		Group: groupName, Model: "gpt-5.5-openai-compact", ChannelId: exactChannelID,
		Enabled: true, Priority: &priority, Weight: weight,
	}).Error)

	resolved, aliased = ResolveCompactModelAliasForGroup(groupName, "gpt-5.5-openai-compact")
	assert.Equal(t, "gpt-5.5-openai-compact", resolved)
	assert.False(t, aliased)
	selected, err = GetRandomSatisfiedChannel(groupName, resolved, 0, "/v1/responses/compact")
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, exactChannelID, selected.Id)
}
