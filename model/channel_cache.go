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
	apidto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/officialfit"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

var group2model2channels map[string]map[string][]int // enabled channel
var channelsIDM map[int]*Channel                     // all channels include disabled
// channel2advancedCustomConfig caches parsed Advanced Custom (type 58) configs so
// path-aware selection avoids re-parsing JSON per request. Refreshed on full sync.
var channel2advancedCustomConfig map[int]*dto.AdvancedCustomConfig

// channel2concurrencyLimits caches each channel's configured concurrency limit
// (dto.ChannelSettings.ConcurrencyLimit, 0 = unlimited) so the relay hot path
// never parses channel settings JSON per attempt. Refreshed with the channel
// cache.
var channel2concurrencyLimits map[int]int

// channel2supportsVideo is the set of channels that declared they can read
// video parts (dto.ChannelOtherSettings.SupportsVideo). Only these are
// candidates for a request carrying video, because a media-blind upstream
// silently answers from the text alone. Presence in the map is the flag, so
// both "no settings" and "settings without the declaration" exclude a channel.
// Refreshed with the channel cache.
var channel2supportsVideo map[int]struct{}

// supportsVideoForCache reports the channel's video capability declaration. Like
// the other cache accessors it parses settings JSON directly instead of calling
// GetOtherSettings, whose malformed-JSON self-heal writes to the database: this
// runs on the selection hot path.
func supportsVideoForCache(channel *Channel) bool {
	if channel == nil || channel.OtherSettings == "" {
		return false
	}
	var settings dto.ChannelOtherSettings
	if err := common.Unmarshal([]byte(channel.OtherSettings), &settings); err != nil {
		return false
	}
	return settings.SupportsVideo
}

// setSupportsVideoIn records the channel's video declaration into the index,
// replacing any previous entry. It takes the target map explicitly so the full
// cache rebuild can populate its private snapshot before publishing it, while
// the incremental update mutates the live map under the write lock.
func setSupportsVideoIn(channel2 map[int]struct{}, channel *Channel) {
	if channel == nil {
		return
	}
	delete(channel2, channel.Id)
	if supportsVideoForCache(channel) {
		channel2[channel.Id] = struct{}{}
	}
}

// channel2videoUnsupportedModels holds the per-model exception to the channel's
// video declaration (dto.ChannelOtherSettings.VideoUnsupportedModels) as a
// lower-cased set. A channel can read video for one model and be media-blind
// for another (measured 2026-10-28: kimi-k3 reported 7168 prompt tokens for a
// clip while kimi-k3.1-pro on the same channel reported 122 and answered from
// text alone), so capability is a channel-wide declaration with model-level
// exceptions. A missing entry means "no exception"; the channel-level flag
// still has to pass. Refreshed with the channel cache.
var channel2videoUnsupportedModels map[int]map[string]struct{}

// videoUnsupportedModelsForCache reads the per-model exclusion set. Like the
// other cache accessors it parses settings JSON directly instead of calling
// GetOtherSettings, whose malformed-JSON self-heal writes to the database.
func videoUnsupportedModelsForCache(channel *Channel) map[string]struct{} {
	if channel == nil || channel.OtherSettings == "" {
		return nil
	}
	var settings dto.ChannelOtherSettings
	if err := common.Unmarshal([]byte(channel.OtherSettings), &settings); err != nil {
		return nil
	}
	if len(settings.VideoUnsupportedModels) == 0 {
		return nil
	}
	excluded := make(map[string]struct{}, len(settings.VideoUnsupportedModels))
	for _, model := range settings.VideoUnsupportedModels {
		trimmed := strings.ToLower(strings.TrimSpace(model))
		if trimmed == "" {
			continue
		}
		excluded[trimmed] = struct{}{}
	}
	if len(excluded) == 0 {
		return nil
	}
	return excluded
}

// setVideoUnsupportedModelsIn records the channel's per-model video exclusion
// set into the index, replacing any previous entry.
func setVideoUnsupportedModelsIn(target map[int]map[string]struct{}, channel *Channel) {
	if channel == nil {
		return
	}
	delete(target, channel.Id)
	if excluded := videoUnsupportedModelsForCache(channel); len(excluded) > 0 {
		target[channel.Id] = excluded
	}
}

// channelVideoCapableForModel reports whether the channel's upstream reads
// video for the given model. Caller must hold channelSyncLock (read lock).
func channelVideoCapableForModel(channelId int, modelName string) bool {
	if _, ok := channel2supportsVideo[channelId]; !ok {
		return false
	}
	excluded, ok := channel2videoUnsupportedModels[channelId]
	if !ok || len(excluded) == 0 {
		return true
	}
	_, blocked := excluded[strings.ToLower(strings.TrimSpace(modelName))]
	return !blocked
}

// concurrencyLimitForCache returns the channel's configured concurrency limit.
// It deliberately avoids Channel.GetSetting, whose malformed-JSON self-heal
// rewrites the database: this runs on the selection/acquire hot path, so one
// corrupt row must not cause a write per attempt. Unparseable settings
// contribute no limit; save-time ValidateSettings already rejects them.
func concurrencyLimitForCache(channel *Channel) int {
	if channel == nil || channel.Setting == nil || *channel.Setting == "" {
		return 0
	}
	var settings dto.ChannelSettings
	if err := common.Unmarshal([]byte(*channel.Setting), &settings); err != nil {
		return 0
	}
	if settings.ConcurrencyLimit < 0 {
		return 0
	}
	return settings.ConcurrencyLimit
}

// setConcurrencyLimitIn records the channel's concurrency limit into the
// index, replacing any previous entry. It takes the target map explicitly so
// the full cache rebuild can populate its private snapshot before publishing
// it, while the incremental update mutates the live map under the write lock.
func setConcurrencyLimitIn(channel2 map[int]int, channel *Channel) {
	if channel == nil {
		return
	}
	delete(channel2, channel.Id)
	if limit := concurrencyLimitForCache(channel); limit > 0 {
		channel2[channel.Id] = limit
	}
}

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
		// tokeness-fitpolicy:begin （上游 merge 后请保留；见 docs/fitpolicy-tech-spec.md）
		// The capability index is independent of the channel memory cache, so it
		// is rebuilt on this path too.
		InitFitCapabilityIndex()
		// tokeness-fitpolicy:end
		InvalidatePricingCache()
		rebuildTaskAliasView()
		return
	}
	newChannelId2channel := make(map[int]*Channel)
	newChannel2advancedCustomConfig := make(map[int]*dto.AdvancedCustomConfig)
	newChannel2concurrencyLimits := make(map[int]int)
	newChannel2supportsVideo := make(map[int]struct{})
	newChannel2videoUnsupportedModels := make(map[int]map[string]struct{})
	var channels []*Channel
	if err := DB.Find(&channels).Error; err != nil {
		// Publishing the empty maps this read would produce means "this node has
		// no channels at all", which stops every relay request on it. A failed
		// read is not an empty configuration: keep the previous cache and let the
		// next reload (or the periodic sync) try again.
		common.SysError("failed to load channels for the cache, keeping the previous cache: " + err.Error())
		return
	}
	for _, channel := range channels {
		loadChannelCredentials(channel)
		newChannelId2channel[channel.Id] = channel
		if channel.Type == constant.ChannelTypeAdvancedCustom {
			if config := channel.GetOtherSettings().AdvancedCustom; config != nil {
				newChannel2advancedCustomConfig[channel.Id] = config
			}
		}
		setConcurrencyLimitIn(newChannel2concurrencyLimits, channel)
		setSupportsVideoIn(newChannel2supportsVideo, channel)
		setVideoUnsupportedModelsIn(newChannel2videoUnsupportedModels, channel)
	}
	var abilities []*Ability
	if err := DB.Find(&abilities).Error; err != nil {
		// Same reasoning as the channel read above: an unreadable ability table
		// must not be published as "no group serves any model".
		common.SysError("failed to load abilities for the cache, keeping the previous cache: " + err.Error())
		return
	}
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
		groups := strings.SplitSeq(channel.Group, ",")
		for group := range groups {
			// tokeness-fix:begin （上游 merge 后请保留；见 FORK-CHANGES.md）
			// The outer map is seeded from Ability rows, so a channel whose group
			// has no abilities yet leaves newGroup2model2channels[group] nil and
			// the assignment below panics. That state is reachable from the admin
			// UI: a channel with an empty model list keeps its row and its group
			// but gets no abilities, and the next cache rebuild would take the
			// request path down with it.
			if _, ok := newGroup2model2channels[group]; !ok {
				newGroup2model2channels[group] = make(map[string][]int)
			}
			// tokeness-fix:end
			models := channel.GetModels()
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
	channel2concurrencyLimits = newChannel2concurrencyLimits
	channel2supportsVideo = newChannel2supportsVideo
	channel2videoUnsupportedModels = newChannel2videoUnsupportedModels
	group2model2channelSelection = newGroup2model2channelSelection
	channelSyncLock.Unlock()
	// Lock ordering: InvalidatePricingCache acquires updatePricingLock, and
	// GetPricing (holding updatePricingLock) nests channelSyncLock.RLock via
	// loadPricingAdvancedCustomConfigs. channelSyncLock MUST be released before
	// invalidating the pricing cache, otherwise the reversed order deadlocks.
	// tokeness-fitpolicy:begin （上游 merge 后请保留；见 docs/fitpolicy-tech-spec.md）
	// The capability index is rebuilt with the channel cache, so it rides the
	// existing config-epoch notification instead of needing a second
	// invalidation path. It fails open and keeps the previous index on error.
	InitFitCapabilityIndex()
	// tokeness-fitpolicy:end
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

func GetRandomSatisfiedChannel(group string, model string, retry int, requestPathOrFilters interface{}) (*Channel, error) {
	if filters, ok := requestPathOrFilters.([]apidto.ChannelFilter); ok {
		return getChannelWithFilters(group, model, retry, filters)
	}
	requestPath, _ := requestPathOrFilters.(string)
	return GetRandomSatisfiedChannelPinned(group, model, retry, requestPath, nil, false, false)
}

// OfficialFitChannelType returns the channel type that counts as the official
// upstream for an official-fit model family (deepseek-v4* -> official DeepSeek
// 43, kimi-k3 -> Moonshot 25, glm-5.3 -> Zhipu v4 26), zero for other models.
// The family table lives in package officialfit; this wrapper keeps the
// model-package spelling its callers use. Exported because the distributor's
// affinity/pin exclusion needs the same family classification when deciding
// whether a cached binding may be reused.
func OfficialFitChannelType(model string) int {
	return officialfit.ChannelType(model)
}

// IsOfficialFitChannelForModel reports whether this channel may serve model as
// an official-behaving upstream. The official_fit_models allowlist that used to
// sit here is retired (spec §19.3.5, removed 2026-10-03 by operator order):
// a channel qualifies by carrying the family's official channel type, or — for
// a family running measured admission — by passing the admission battery. This
// struct form reads only the channel row's own type, so it is the type half
// alone; the battery half needs the live policy and lives in the
// admission-aware predicates. Models outside an official-fit family never
// qualify.
func (channel *Channel) IsOfficialFitChannelForModel(model string) bool {
	if channel == nil {
		return false
	}
	return channel.Type == OfficialFitChannelType(model) && OfficialFitChannelType(model) != 0
}

// officialFitChannelMatchesAdmissionLocked is the cache-backed form of the
// admission-aware official-behaviour predicate for the selection hot path.
// Caller must hold channelSyncLock (read lock).
//
// The family's official channel type is admitted under both sources — it is the
// identity the baseline was measured against, not a claim the battery needs to
// prove. What the source selects is how every other channel qualifies:
//
//   - declared: the family's official channel type alone — the predicate the
//     allowlist era left behind once the allowlist was retired;
//   - measured: the admission battery — every battery behaviour must carry a
//     fresh, passing mark bound to the admission's policy and baseline.
//
// The battery is evaluated through ChannelSatisfiesFitMarks with its
// conservative reading (an unknown or stale mark never admits): admission is a
// promise, and the policy's UnknownMarkPolicy exists so phase-1 narrowing does
// not rule out unmeasured channels — never so an unmeasured channel is admitted.
// The mark lookups take the capability index lock nested inside the channel
// lock, which is the documented order (channel → index); the non-building
// lookup is what keeps that safe.
func officialFitChannelMatchesAdmissionLocked(channelID int, model string, admission fitpolicy.Admission) bool {
	officialType := OfficialFitChannelType(model)
	if officialType == 0 {
		return false
	}
	if admission.Measured() {
		if channel, ok := channelsIDM[channelID]; ok && channel.Type == officialType {
			return true
		}
		return channelAdmissionBatterySatisfied(channelID, model, admission)
	}
	// Declared admission is the channel type alone: the official_fit_models
	// allowlist that used to widen it is retired (spec §19.3.5), so a family
	// that has not armed a battery is served by its official-type channels
	// only.
	channel, ok := channelsIDM[channelID]
	return ok && channel.Type == officialType
}

// channelAdmissionBatterySatisfied reports whether the channel carries fresh,
// passing marks for every battery behaviour. A channel with no battery to
// evaluate (an empty battery cannot pass validation under measured, but a
// hand-built admission may carry one) is never admitted by measurement.
//
// Safe under channelSyncLock: the mark lookups never build the capability index.
func channelAdmissionBatterySatisfied(channelID int, model string, admission fitpolicy.Admission) bool {
	if len(admission.Battery) == 0 {
		return false
	}
	return ChannelSatisfiesFitMarks(channelID, FitMarkRequirement{
		Model:        model,
		Marks:        admission.Battery,
		PolicyHash:   admission.PolicyHash,
		BaselineHash: admission.BaselineHash,
	})
}

// currentOfficialFitAdmission resolves the admission the installed policy
// declares for a model. No snapshot, or a family the document says nothing
// about, resolves to the declared default — the behaviour that predates
// admission, which is what keeps an unwritten or partial document from
// changing routing.
func currentOfficialFitAdmission(model string) fitpolicy.Admission {
	if admission, ok := fitpolicy.Current().AdmissionFor(model); ok {
		return admission
	}
	return fitpolicy.Admission{Source: fitpolicy.AdmissionSourceDeclared}
}

// officialFitChannelMatchesLocked is the legacy form of the admission-aware
// predicate: it resolves the admission from the installed policy, which is
// correct for the callers that do not carry a request's requirement (the
// legacy pin branch and the selection-metadata guard). The narrowing passes
// its own admission so a retry stays under the rules it started with.
// Caller must hold channelSyncLock (read lock).
func officialFitChannelMatchesLocked(channelID int, model string) bool {
	return officialFitChannelMatchesAdmissionLocked(channelID, model, currentOfficialFitAdmission(model))
}

// ChannelIsOfficialFitForModel reports whether the channel may serve model as
// an official-behaving upstream: the family's official channel type, plus —
// for a family running measured admission — the admission battery. It falls
// back to a targeted DB read when the memory cache is disabled, so callers
// outside the selection lock (e.g. the affinity check) get the same answer as
// candidate filtering. The DB fallback selects only the classifying columns —
// never the channel key or credentials, which this check has no use for. On
// the DB path the battery is read through the capability index (built from the
// same database), so a node without the memory cache answers from the same
// marks.
func ChannelIsOfficialFitForModel(channelID int, model string) bool {
	if OfficialFitChannelType(model) == 0 {
		return false
	}
	admission := currentOfficialFitAdmission(model)
	if admission.Measured() {
		// Outside channelSyncLock on purpose: a first-use build is a full table
		// scan, and doing it under the channel lock would block every channel
		// write for its duration — the ordering the capability index documents
		// and forbids. The call is a cheap readiness check once built, and it
		// only runs for measured families, so the affinity path (which reaches
		// this before the first selection) sees the same marks the selection
		// path will.
		ensureFitCapabilityIndexBuilt()
	}
	if !common.MemoryCacheEnabled {
		// No database handle means the answer cannot be verified (a test
		// context, or a node that has not opened the DB); the conservative
		// answer is "not official-behaving", never a panic.
		if DB == nil {
			return false
		}
		channel := &Channel{Id: channelID}
		if err := DB.Select("id, type").First(channel, "id = ?", channelID).Error; err != nil {
			return false
		}
		if channel.Type == OfficialFitChannelType(model) {
			return true
		}
		if admission.Measured() {
			return channelAdmissionBatterySatisfied(channelID, model, admission)
		}
		return false
	}
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()
	return officialFitChannelMatchesAdmissionLocked(channelID, model, admission)
}

// preferOfficialFitChannels narrows official-fit candidates to the channels
// that are official-behaving for the model when the request is marked for the
// official pin. The candidate is kept when it carries the family's official
// channel type or, for a measured family, passes the admission battery.
//
// What happens when no official candidate remains (including retries where the
// failed official channel is excluded) is the family's empty-match policy
// (2026-10-19): the default is best-effort — the ordinary priority pool is kept
// so an unavailable official fleet does not fail the request — and the strict
// legacy value keeps the empty set so the request fails honestly. When a policy
// opinion is present the narrowing in pkg/fitpolicy.Narrow decides this; the
// legacy branch below applies the same best-effort reading, so a pin without a
// policy cannot become the only availability cliff.
//
// tokeness-fitpolicy:begin （上游 merge 后请保留；见 docs/fitpolicy-tech-spec.md）
// When fit carries a policy opinion, the two-phase narrowing replaces the plain
// official filter; with no mark data (step A) it returns exactly the same set.
// When fit has no opinion the legacy filter below still runs, so a pinned
// request is never left unconstrained.
// tokeness-fitpolicy:end
// Caller must hold channelSyncLock (read lock).
func preferOfficialFitChannels(channels []int, model string, pinOfficial bool, fit *FitChannelFilter) []int {
	if len(channels) == 0 || OfficialFitChannelType(model) == 0 {
		return channels
	}
	// tokeness-fitpolicy:begin （上游 merge 后请保留；见 docs/fitpolicy-tech-spec.md）
	// The fit narrowing is evaluated BEFORE the pin check on purpose: the policy
	// may require official behaviour for a shape the shipped predicate does not
	// pin, so gating it on pinOfficial would disable the layer in exactly the
	// case it exists for. Legacy callers pass fit == nil and still reach the
	// early return below with the candidate set untouched.
	if narrowed, applied := fit.narrowChannels(channels, model); applied {
		return narrowed
	}
	// tokeness-fitpolicy:end
	if !pinOfficial {
		return channels
	}
	official := make([]int, 0, len(channels))
	for _, channelID := range channels {
		if officialFitChannelMatchesLocked(channelID, model) {
			official = append(official, channelID)
		}
	}
	if len(official) == 0 {
		// No policy opinion is present (otherwise the narrowing above would have
		// decided), so the legacy reading is best-effort by default: keep the
		// caller's ordinary candidates instead of emptying the set. Official
		// fitting is not absolute; availability wins (2026-10-19).
		return channels
	}
	return official
}

// officialFitPreferenceApplied reports whether the official pin is active for
// this family. When it is, the candidate slice the caller holds may be a
// strict subset of the one the prebuilt selection metadata was built from
// (non-official channels were dropped), so that metadata must not be used.
// Every entry in the already-narrowed slice is official-behaving, which is
// exactly the condition checked here.
func officialFitPreferenceApplied(channels []int, model string, pinOfficial bool) bool {
	if !pinOfficial || OfficialFitChannelType(model) == 0 {
		return false
	}
	for _, channelID := range channels {
		if !officialFitChannelMatchesLocked(channelID, model) {
			return false
		}
	}
	return true
}

// GetRandomSatisfiedChannelPinned behaves like
// GetRandomSatisfiedChannelWithBlockedChannels but can narrow deepseek-v4
// candidates to the official channel when pinOfficial is set for the request,
// and to video-capable channels when videoOnly is set (a request carrying a
// video part must not reach an upstream that answers from text alone).
func GetRandomSatisfiedChannelPinned(group string, model string, retry int, requestPath string, blockedChannels map[int]struct{}, pinOfficial bool, videoOnly bool) (*Channel, error) {
	return GetRandomSatisfiedChannelPinnedWithFit(group, model, retry, requestPath, blockedChannels, pinOfficial, videoOnly, nil)
}

// tokeness-fitpolicy:begin （上游 merge 后请保留；见 docs/fitpolicy-tech-spec.md）
// GetRandomSatisfiedChannelPinnedWithFit is GetRandomSatisfiedChannelPinned plus
// the fit-policy narrowing carried by fit. The filter is additive on purpose:
// the legacy entry point above passes nil, so every existing caller and test
// keeps its exact behaviour, and a request the policy has no opinion about
// takes the legacy official filter untouched.
//
// The two-phase order lives in pkg/fitpolicy.Narrow: candidates that satisfy
// every required behaviour win; otherwise the official set is used unchanged;
// when that set is empty too, the family's empty-match policy decides —
// best-effort (the default) keeps the ordinary priority pool, the strict legacy
// value keeps the empty set so the caller reports its existing error.
func GetRandomSatisfiedChannelPinnedWithFit(group string, model string, retry int, requestPath string, blockedChannels map[int]struct{}, pinOfficial bool, videoOnly bool, fit *FitChannelFilter) (*Channel, error) {
	// if memory cache is disabled, get channel directly from database
	if !common.MemoryCacheEnabled {
		return GetChannelWithBlockedChannelsPinnedWithFit(group, model, retry, requestPath, blockedChannels, pinOfficial, videoOnly, fit)
	}

	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()
	if group2model2channels == nil || channelsIDM == nil {
		return nil, errors.New("channel cache is not initialized")
	}

	// First, try to find channels with the exact model name.
	channels := filterChannelsByRequestPathAndModel(group2model2channels[group][model], requestPath, model)
	channels = filterChannelIDsByBlockedChannels(channels, blockedChannels)
	// Video capability is a hard requirement and is applied BEFORE the fit
	// narrowing on purpose. The narrowing keeps only channels that prove the
	// requested behaviour, which may exclude the only channel that can read the
	// request's media; filtering the narrowed slice afterwards then produced an
	// empty candidate set and the pin path reported its internal "cannot prove
	// exhaustion" error. Narrowing inside the video-capable set instead lets the
	// genuinely capable channel (the official one, routinely the lowest
	// priority) serve the request.
	channels, videoEmptied := narrowVideoCandidates(channels, videoOnly, model)
	channels = preferOfficialFitChannels(channels, model, pinOfficial, fit)
	// Short cooldown is a preference inside the hard constraints, never a capability
	// filter in front of them: applying it before the video/fit narrowing turned a
	// transient hint into a 400 "video unsupported" and silently downgraded
	// official-fit requests (2026-10-29 review, B1). Filtering after narrowing and
	// keeping the narrowed set when every candidate is cooling preserves both
	// semantics.
	if cooling := filterChannelIDsByCooldown(channels); len(cooling) > 0 {
		channels = cooling
	}

	// If no channels found, try to find channels with the normalized model name.
	if len(channels) == 0 {
		normalizedModel := ratio_setting.RoutingMatchModelName(model)
		channels = filterChannelsByRequestPathAndModel(group2model2channels[group][normalizedModel], requestPath, model)
		channels = filterChannelIDsByBlockedChannels(channels, blockedChannels)
		normalizedEmptied := false
		channels, normalizedEmptied = narrowVideoCandidates(channels, videoOnly, model)
		videoEmptied = videoEmptied || normalizedEmptied
		channels = preferOfficialFitChannels(channels, model, pinOfficial, fit)
		if cooling := filterChannelIDsByCooldown(channels); len(cooling) > 0 {
			channels = cooling
		}
	}

	if len(channels) == 0 {
		if videoEmptied {
			// Definite capability gap, not an unprovable exhaustion: the model is
			// published for this group but no candidate declares it can read
			// video. Callers answer with the video_request rejection instead of
			// the internal selection error.
			return nil, ErrNoVideoCapableChannel
		}
		return nil, nil
	}

	if len(channels) == 1 {
		if channel, ok := channelsIDM[channels[0]]; ok {
			return channel, nil
		}
		return nil, fmt.Errorf("数据库一致性错误，渠道# %d 不存在，请联系管理员修复", channels[0])
	}

	// The metadata fast path picks from the whole model's candidate set, so it
	// must not be used when video narrowing may have removed candidates above.
	// tokeness-fitpolicy:begin （上游 merge 后请保留；见 docs/fitpolicy-tech-spec.md）
	// Same reason applies to the fit narrowing: it runs whether or not the legacy
	// pin is on, so the shortcut is disabled whenever the policy has an opinion —
	// officialFitPreferenceApplied alone would miss the policy-only case and let
	// the metadata path pick a candidate the narrowing had removed.
	if !videoOnly && requestPath == "" && len(blockedChannels) == 0 && !fit.hasOpinion() && !officialFitPreferenceApplied(channels, model, pinOfficial) {
		// tokeness-fitpolicy:end
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

// tokeness-fitpolicy:end

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

// filterChannelIDsByCooldown drops channels that recently failed in a way a
// retry cannot fix, so traffic moves off a broken upstream until it expires.
func filterChannelIDsByCooldown(channels []int) []int {
	if len(channels) == 0 {
		return channels
	}
	kept := make([]int, 0, len(channels))
	for _, channelID := range channels {
		if !common.ChannelInCooldown(channelID) {
			kept = append(kept, channelID)
		}
	}
	return kept
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

// filterChannelIDsByVideoCapability drops channels that cannot read video for
// the requested model when the request carries a video part. Capability is the
// channel-wide declaration minus the model-level exceptions, so a channel that
// reads video for one model but is media-blind for another is dropped for the
// blind model only. Caller must hold channelSyncLock (read lock). The input
// slice is never mutated. A channel missing from the id map is dropped: an
// unknown capability cannot be treated as capable.
func filterChannelIDsByVideoCapability(channels []int, videoOnly bool, modelName string) []int {
	if !videoOnly || len(channels) == 0 {
		return channels
	}
	filtered := make([]int, 0, len(channels))
	for _, channelId := range channels {
		if channelVideoCapableForModel(channelId, modelName) {
			filtered = append(filtered, channelId)
		}
	}
	return filtered
}

// ErrNoVideoCapableChannel reports a definite capability gap: the group does
// publish the model, but not a single candidate declares it can read video.
// It is deliberately distinct from an empty selection (nil, nil), which means
// "no candidate at all" and keeps its existing model-not-configured handling.
var ErrNoVideoCapableChannel = errors.New("no candidate declares video capability for this model")

// narrowVideoCandidates applies the hard video-capability requirement and
// reports whether it removed every candidate. The flag is what lets the caller
// tell a capability gap from an empty group/model: an empty input stays empty
// with the flag false, so nothing is inferred from absent candidates.
func narrowVideoCandidates(channels []int, videoOnly bool, modelName string) ([]int, bool) {
	if !videoOnly || len(channels) == 0 {
		return channels, false
	}
	filtered := filterChannelIDsByVideoCapability(channels, videoOnly, modelName)
	return filtered, len(filtered) == 0
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

// CacheGetChannelConcurrencyLimit returns the channel's configured concurrency
// limit (0 = unlimited) from the precomputed cache index. When the memory
// cache is disabled it parses the passed channel's settings without side
// effects, so the relay loop can resolve the limit from its already-loaded
// channel in both modes.
func CacheGetChannelConcurrencyLimit(channel *Channel) int {
	if channel == nil {
		return 0
	}
	if !common.MemoryCacheEnabled {
		return concurrencyLimitForCache(channel)
	}
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()
	return channel2concurrencyLimits[channel.Id]
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
	if channel2concurrencyLimits == nil {
		channel2concurrencyLimits = make(map[int]int)
	}
	setConcurrencyLimitIn(channel2concurrencyLimits, channel)
	if channel2supportsVideo == nil {
		channel2supportsVideo = make(map[int]struct{})
	}
	setSupportsVideoIn(channel2supportsVideo, channel)
	if channel2videoUnsupportedModels == nil {
		channel2videoUnsupportedModels = make(map[int]map[string]struct{})
	}
	setVideoUnsupportedModelsIn(channel2videoUnsupportedModels, channel)
	group2model2channelSelection = nil
	logger.LogDebug(nil, "CacheUpdateChannel after: id=%d, name=%s, status=%d, polling_index=%d", channel.Id, channel.Name, channel.Status, channel.ChannelInfo.MultiKeyPollingIndex)
	// Lock ordering: do NOT hold channelSyncLock while calling
	// InvalidatePricingCache. GetPricing acquires updatePricingLock first and then
	// channelSyncLock.RLock (via loadPricingAdvancedCustomConfigs); acquiring
	// updatePricingLock while holding channelSyncLock would be an AB-BA deadlock.
	channelSyncLock.Unlock()
	InvalidatePricingCache()
}
