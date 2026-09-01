package model

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

var group2model2channels map[string]map[string][]int // enabled channel
var channelsIDM map[int]*Channel                     // all channels include disabled
// channel2advancedCustomConfig caches parsed Advanced Custom (type 58) configs so
// path-aware selection avoids re-parsing JSON per request. Refreshed on full sync.
var channel2advancedCustomConfig map[int]*dto.AdvancedCustomConfig

type channelSelectionCandidate struct {
	channelID       int
	effectiveWeight int
}

type channelSelectionPriority struct {
	candidates  []channelSelectionCandidate
	totalWeight int
}

type channelSelectionMetadata struct {
	priorities []channelSelectionPriority
}

// group2model2channelSelection contains immutable priority and effective-weight
// metadata for the common unfiltered selection path. It is rebuilt with the
// channel cache and discarded when a channel cache update can invalidate it.
var group2model2channelSelection map[string]map[string]*channelSelectionMetadata
var channelSyncLock sync.RWMutex

func InitChannelCache() {
	if !common.MemoryCacheEnabled {
		InvalidatePricingCache()
		rebuildTaskAliasView()
		return
	}
	newChannelId2channel := make(map[int]*Channel)
	newChannel2advancedCustomConfig := make(map[int]*dto.AdvancedCustomConfig)
	var channels []*Channel
	DB.Find(&channels)
	for _, channel := range channels {
		loadChannelCredentials(channel)
		newChannelId2channel[channel.Id] = channel
		if channel.Type == constant.ChannelTypeAdvancedCustom {
			if config := channel.GetOtherSettings().AdvancedCustom; config != nil {
				newChannel2advancedCustomConfig[channel.Id] = config
			}
		}
	}
	var abilities []*Ability
	DB.Find(&abilities)
	groups := make(map[string]bool)
	for _, ability := range abilities {
		groups[ability.Group] = true
	}
	newGroup2model2channels := make(map[string]map[string][]int)
	for group := range groups {
		newGroup2model2channels[group] = make(map[string][]int)
	}
	for _, channel := range channels {
		if channel.Status != common.ChannelStatusEnabled {
			continue // skip disabled channels
		}
		groups := strings.Split(channel.Group, ",")
		for _, group := range groups {
			models := strings.Split(channel.Models, ",")
			for _, model := range models {
				if _, ok := newGroup2model2channels[group][model]; !ok {
					newGroup2model2channels[group][model] = make([]int, 0)
				}
				newGroup2model2channels[group][model] = append(newGroup2model2channels[group][model], channel.Id)
			}
		}
	}

	// sort by priority
	for group, model2channels := range newGroup2model2channels {
		for model, channels := range model2channels {
			sort.Slice(channels, func(i, j int) bool {
				return newChannelId2channel[channels[i]].GetPriority() > newChannelId2channel[channels[j]].GetPriority()
			})
			newGroup2model2channels[group][model] = channels
		}
	}
	newGroup2model2channelSelection := make(map[string]map[string]*channelSelectionMetadata, len(newGroup2model2channels))
	for group, model2channels := range newGroup2model2channels {
		model2selection := make(map[string]*channelSelectionMetadata, len(model2channels))
		for model, channels := range model2channels {
			model2selection[model] = buildChannelSelectionMetadata(channels, newChannelId2channel)
		}
		newGroup2model2channelSelection[group] = model2selection
	}

	channelSyncLock.Lock()
	group2model2channels = newGroup2model2channels
	//channelsIDM = newChannelId2channel
	for i, channel := range newChannelId2channel {
		if channel.ChannelInfo.IsMultiKey {
			channel.Keys = channel.GetKeys()
			if channel.ChannelInfo.MultiKeyMode == constant.MultiKeyModePolling {
				if oldChannel, ok := channelsIDM[i]; ok {
					// 存在旧的渠道，如果是多key且轮询，保留轮询索引信息
					if oldChannel.ChannelInfo.IsMultiKey && oldChannel.ChannelInfo.MultiKeyMode == constant.MultiKeyModePolling {
						channel.ChannelInfo.MultiKeyPollingIndex = oldChannel.ChannelInfo.MultiKeyPollingIndex
					}
				}
			}
		}
	}
	channelsIDM = newChannelId2channel
	channel2advancedCustomConfig = newChannel2advancedCustomConfig
	group2model2channelSelection = newGroup2model2channelSelection
	channelSyncLock.Unlock()
	// Lock ordering: InvalidatePricingCache acquires updatePricingLock, and
	// GetPricing (holding updatePricingLock) nests channelSyncLock.RLock via
	// loadPricingAdvancedCustomConfigs. channelSyncLock MUST be released before
	// invalidating the pricing cache, otherwise the reversed order deadlocks.
	InvalidatePricingCache()
	rebuildTaskAliasView()
	common.SysLog("channels synced from database")
}

func SyncChannelCache(frequency int) {
	for {
		time.Sleep(time.Duration(frequency) * time.Second)
		common.SysLog("syncing channels from database")
		InitChannelCache()
	}
}

func GetRandomSatisfiedChannel(group string, model string, retry int, requestPath string) (*Channel, error) {
	return GetRandomSatisfiedChannelPinned(group, model, retry, requestPath, nil, false)
}

// officialFitChannelType returns the channel type that counts as the official
// upstream for an official-fit model family: deepseek-v4-* -> official
// DeepSeek (type 43), kimi-k3 -> Moonshot (type 25), glm-5.3 -> Zhipu v4
// API (type 26). Zero for other models.
func officialFitChannelType(model string) int {
	m := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(m, "deepseek-v4-") {
		return constant.ChannelTypeDeepSeek
	}
	if strings.HasPrefix(m, "kimi-k3") {
		return constant.ChannelTypeMoonshot
	}
	if strings.HasPrefix(m, "glm-5.3") {
		return constant.ChannelTypeZhipu_v4
	}
	return 0
}

// preferOfficialFitChannels narrows official-fit candidates to the official
// upstream channel type when the request is marked for the official pin. For
// deepseek-v4-* the mark is set for the extreme-sampling class or by the
// user's Route profile; kimi-k3 only via the Route profile. Without an
// official channel the candidate set is unchanged. Caller must hold
// channelSyncLock (read lock).
func preferOfficialFitChannels(channels []int, model string, pinOfficial bool) []int {
	officialType := officialFitChannelType(model)
	if len(channels) == 0 || !pinOfficial || officialType == 0 {
		return channels
	}
	official := make([]int, 0, len(channels))
	for _, channelID := range channels {
		if channel, ok := channelsIDM[channelID]; ok && channel.Type == officialType {
			official = append(official, channelID)
		}
	}
	if len(official) == 0 {
		return channels
	}
	return official
}

// officialFitPreferenceApplied reports whether the previous narrowing kept
// only a strict subset of the cached candidates, meaning the prebuilt selection
// metadata no longer describes the candidate set and must not be used.
func officialFitPreferenceApplied(channels []int, model string, pinOfficial bool) bool {
	officialType := officialFitChannelType(model)
	if !pinOfficial || officialType == 0 {
		return false
	}
	for _, channelID := range channels {
		if channel, ok := channelsIDM[channelID]; ok && channel.Type != officialType {
			return false
		}
	}
	return true
}

// GetRandomSatisfiedChannelPinned behaves like
// GetRandomSatisfiedChannelWithBlockedChannels but can narrow deepseek-v4
// candidates to the official channel when pinOfficial is set for the request.
func GetRandomSatisfiedChannelPinned(group string, model string, retry int, requestPath string, blockedChannels map[int]struct{}, pinOfficial bool) (*Channel, error) {
	// if memory cache is disabled, get channel directly from database
	if !common.MemoryCacheEnabled {
		return GetChannelWithBlockedChannelsPinned(group, model, retry, requestPath, blockedChannels, pinOfficial)
	}

	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()
	if group2model2channels == nil || channelsIDM == nil {
		return nil, errors.New("channel cache is not initialized")
	}

	// First, try to find channels with the exact model name.
	channels := filterChannelsByRequestPathAndModel(group2model2channels[group][model], requestPath, model)
	channels = filterChannelIDsByBlockedChannels(channels, blockedChannels)
	channels = preferOfficialFitChannels(channels, model, pinOfficial)

	// If no channels found, try to find channels with the normalized model name.
	if len(channels) == 0 {
		normalizedModel := ratio_setting.FormatMatchingModelName(model)
		channels = filterChannelsByRequestPathAndModel(group2model2channels[group][normalizedModel], requestPath, model)
		channels = filterChannelIDsByBlockedChannels(channels, blockedChannels)
		channels = preferOfficialFitChannels(channels, model, pinOfficial)
	}

	if len(channels) == 0 {
		return nil, nil
	}

	if len(channels) == 1 {
		if channel, ok := channelsIDM[channels[0]]; ok {
			return channel, nil
		}
		return nil, fmt.Errorf("数据库一致性错误，渠道# %d 不存在，请联系管理员修复", channels[0])
	}

	if requestPath == "" && len(blockedChannels) == 0 && !officialFitPreferenceApplied(channels, model, pinOfficial) {
		if model2selection, ok := group2model2channelSelection[group]; ok {
			if selection := model2selection[model]; selection != nil {
				return selectChannelFromMetadata(group, model, retry, selection)
			}
			if normalizedModel := ratio_setting.FormatMatchingModelName(model); normalizedModel != model {
				if selection := model2selection[normalizedModel]; selection != nil {
					return selectChannelFromMetadata(group, model, retry, selection)
				}
			}
		}
	}

	uniquePriorities := make(map[int]bool)
	for _, channelId := range channels {
		if channel, ok := channelsIDM[channelId]; ok {
			uniquePriorities[int(channel.GetPriority())] = true
		} else {
			return nil, fmt.Errorf("数据库一致性错误，渠道# %d 不存在，请联系管理员修复", channelId)
		}
	}
	var sortedUniquePriorities []int
	for priority := range uniquePriorities {
		sortedUniquePriorities = append(sortedUniquePriorities, priority)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sortedUniquePriorities)))

	if retry >= len(uniquePriorities) {
		retry = len(uniquePriorities) - 1
	}
	targetPriority := int64(sortedUniquePriorities[retry])

	// get the priority for the given retry number
	var sumWeight = 0
	var targetChannels []*Channel
	for _, channelId := range channels {
		if channel, ok := channelsIDM[channelId]; ok {
			if channel.GetPriority() == targetPriority {
				sumWeight += channel.GetWeight()
				targetChannels = append(targetChannels, channel)
			}
		} else {
			return nil, fmt.Errorf("数据库一致性错误，渠道# %d 不存在，请联系管理员修复", channelId)
		}
	}

	if len(targetChannels) == 0 {
		return nil, errors.New(fmt.Sprintf("no channel found, group: %s, model: %s, priority: %d", group, model, targetPriority))
	}

	// smoothing factor and adjustment
	smoothingFactor := 1
	smoothingAdjustment := 0

	if sumWeight == 0 {
		// when all channels have weight 0, set sumWeight to the number of channels and set smoothing adjustment to 100
		// each channel's effective weight = 100
		sumWeight = len(targetChannels) * 100
		smoothingAdjustment = 100
	} else if sumWeight/len(targetChannels) < 10 {
		// when the average weight is less than 10, set smoothing factor to 100
		smoothingFactor = 100
	}

	// Calculate the total weight of all channels up to endIdx
	totalWeight := sumWeight * smoothingFactor

	// Generate a random value in the range [0, totalWeight)
	randomWeight := rand.Intn(totalWeight)

	// Find a channel based on its weight
	for _, channel := range targetChannels {
		randomWeight -= channel.GetWeight()*smoothingFactor + smoothingAdjustment
		if randomWeight < 0 {
			return channel, nil
		}
	}
	// return null if no channel is not found
	return nil, errors.New("channel not found")
}

func buildChannelSelectionMetadata(channelIDs []int, channelsByID map[int]*Channel) *channelSelectionMetadata {
	metadata := &channelSelectionMetadata{}
	var lastPriority int64
	hasPriority := false
	for _, channelID := range channelIDs {
		channel, ok := channelsByID[channelID]
		if !ok {
			continue
		}
		priority := channel.GetPriority()
		if !hasPriority || lastPriority != priority {
			metadata.priorities = append(metadata.priorities, channelSelectionPriority{})
			hasPriority = true
			lastPriority = priority
		}
		selection := &metadata.priorities[len(metadata.priorities)-1]
		selection.candidates = append(selection.candidates, channelSelectionCandidate{channelID: channelID})
		selection.totalWeight += channel.GetWeight()
	}

	for index := range metadata.priorities {
		selection := &metadata.priorities[index]
		smoothingFactor := 1
		smoothingAdjustment := 0
		if selection.totalWeight == 0 {
			smoothingAdjustment = 100
		} else if selection.totalWeight/len(selection.candidates) < 10 {
			smoothingFactor = 100
		}

		selection.totalWeight = 0
		for candidateIndex := range selection.candidates {
			channel := channelsByID[selection.candidates[candidateIndex].channelID]
			effectiveWeight := channel.GetWeight()*smoothingFactor + smoothingAdjustment
			selection.candidates[candidateIndex].effectiveWeight = effectiveWeight
			selection.totalWeight += effectiveWeight
		}
	}
	return metadata
}

func selectChannelFromMetadata(group string, model string, retry int, metadata *channelSelectionMetadata) (*Channel, error) {
	if len(metadata.priorities) == 0 {
		return nil, nil
	}
	if retry >= len(metadata.priorities) {
		retry = len(metadata.priorities) - 1
	}
	selection := metadata.priorities[retry]
	randomWeight := rand.Intn(selection.totalWeight)
	for _, candidate := range selection.candidates {
		channel, ok := channelsIDM[candidate.channelID]
		if !ok {
			return nil, fmt.Errorf("数据库一致性错误，渠道# %d 不存在，请联系管理员修复", candidate.channelID)
		}
		randomWeight -= candidate.effectiveWeight
		if randomWeight < 0 {
			return channel, nil
		}
	}
	return nil, fmt.Errorf("no channel found, group: %s, model: %s, priority retry: %d", group, model, retry)
}

func filterChannelIDsByBlockedChannels(channels []int, blockedChannels map[int]struct{}) []int {
	if len(blockedChannels) == 0 || len(channels) == 0 {
		return channels
	}
	filtered := make([]int, 0, len(channels))
	for _, channelID := range channels {
		if _, blocked := blockedChannels[channelID]; blocked {
			continue
		}
		filtered = append(filtered, channelID)
	}
	return filtered
}

// filterChannelsByRequestPathAndModel restricts candidates by request path and
// model. Only Advanced Custom (type 58) channels are path-checked: they are kept
// only when one of their configured routes matches requestPath and model. All
// other channel types always pass. When requestPath is empty, filtering is skipped.
// Caller must hold channelSyncLock (read lock). The cached slice is never mutated.
func filterChannelsByRequestPathAndModel(channels []int, requestPath string, model string) []int {
	if requestPath == "" || len(channels) == 0 {
		return channels
	}
	filtered := make([]int, 0, len(channels))
	for _, channelId := range channels {
		channel, ok := channelsIDM[channelId]
		if !ok {
			// keep it so the downstream consistency error is raised as before
			filtered = append(filtered, channelId)
			continue
		}
		if channel.Type != constant.ChannelTypeAdvancedCustom {
			filtered = append(filtered, channelId)
			continue
		}
		if config := channel2advancedCustomConfig[channelId]; config != nil && config.SupportsPathForModel(requestPath, model) {
			filtered = append(filtered, channelId)
		}
	}
	return filtered
}

func CacheGetChannel(id int) (*Channel, error) {
	if !common.MemoryCacheEnabled {
		return GetChannelById(id, true)
	}
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()

	c, ok := channelsIDM[id]
	if !ok {
		return nil, fmt.Errorf("渠道# %d，已不存在", id)
	}
	return c, nil
}

func CacheGetChannelInfo(id int) (*ChannelInfo, error) {
	if !common.MemoryCacheEnabled {
		channel, err := GetChannelById(id, true)
		if err != nil {
			return nil, err
		}
		return &channel.ChannelInfo, nil
	}
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()

	c, ok := channelsIDM[id]
	if !ok {
		return nil, fmt.Errorf("渠道# %d，已不存在", id)
	}
	return &c.ChannelInfo, nil
}

func CacheUpdateChannelStatus(id int, status int) {
	if !common.MemoryCacheEnabled {
		return
	}
	channelSyncLock.Lock()
	defer channelSyncLock.Unlock()
	if channel, ok := channelsIDM[id]; ok {
		channel.Status = status
	}
	if status != common.ChannelStatusEnabled {
		// delete the channel from group2model2channels
		for group, model2channels := range group2model2channels {
			for model, channels := range model2channels {
				for i, channelId := range channels {
					if channelId == id {
						// remove the channel from the slice
						group2model2channels[group][model] = append(channels[:i], channels[i+1:]...)
						break
					}
				}
			}
		}
	}
	group2model2channelSelection = nil
}

func CacheUpdateChannel(channel *Channel) {
	if !common.MemoryCacheEnabled {
		return
	}
	channelSyncLock.Lock()
	if channel == nil {
		channelSyncLock.Unlock()
		return
	}

	if channelsIDM == nil {
		channelsIDM = make(map[int]*Channel)
	}
	if oldChannel, ok := channelsIDM[channel.Id]; ok {
		logger.LogDebug(nil, "CacheUpdateChannel before: id=%d, name=%s, status=%d, polling_index=%d", channel.Id, channel.Name, channel.Status, oldChannel.ChannelInfo.MultiKeyPollingIndex)
	}
	channelsIDM[channel.Id] = channel
	if channel2advancedCustomConfig == nil {
		channel2advancedCustomConfig = make(map[int]*dto.AdvancedCustomConfig)
	}
	delete(channel2advancedCustomConfig, channel.Id)
	if channel.Type == constant.ChannelTypeAdvancedCustom {
		if config := channel.GetOtherSettings().AdvancedCustom; config != nil {
			channel2advancedCustomConfig[channel.Id] = config
		}
	}
	group2model2channelSelection = nil
	logger.LogDebug(nil, "CacheUpdateChannel after: id=%d, name=%s, status=%d, polling_index=%d", channel.Id, channel.Name, channel.Status, channel.ChannelInfo.MultiKeyPollingIndex)
	// Lock ordering: do NOT hold channelSyncLock while calling
	// InvalidatePricingCache. GetPricing acquires updatePricingLock first and then
	// channelSyncLock.RLock (via loadPricingAdvancedCustomConfigs); acquiring
	// updatePricingLock while holding channelSyncLock would be an AB-BA deadlock.
	channelSyncLock.Unlock()
	InvalidatePricingCache()
}
