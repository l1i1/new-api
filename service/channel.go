package service

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func formatNotifyType(channelId int, status int) string {
	return fmt.Sprintf("%s_%d_%d", dto.NotifyTypeChannelUpdate, channelId, status)
}

func shouldCloseActiveWebSocketsAfterDisable(channelId int) bool {
	channel, err := model.GetChannelById(channelId, true)
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to check channel status before closing active websockets: channel_id=%d, error=%v", channelId, err))
		return true
	}
	return channel.Status != common.ChannelStatusEnabled
}

// formatKeyNotifyType scopes the notification limit of a key-level notice to
// the individual key, so one dead credential cannot silence notices about the
// next one.
func formatKeyNotifyType(channelId int, keyPosition int) string {
	return fmt.Sprintf("%s_key_%d_%d", dto.NotifyTypeChannelUpdate, channelId, keyPosition)
}

// disable & notify
//
// DisableChannel is the channel-level entry point (manual operations, balance
// checks, tag updates). Relay errors must use DisableChannelBecauseRelayError:
// a multi-key channel must not lose every key because one of them failed.
func DisableChannel(channelError types.ChannelError, reason string) {
	common.SysLog(fmt.Sprintf("通道「%s」（#%d）发生错误，准备禁用，原因：%s", channelError.ChannelName, channelError.ChannelId, common.LocalLogPreview(reason)))

	// 检查是否启用自动禁用功能
	if !channelError.AutoBan {
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）未启用自动禁用功能，跳过禁用操作", channelError.ChannelName, channelError.ChannelId))
		return
	}

	if !model.UpdateChannelStatus(channelError.ChannelId, channelError.UsingKey, common.ChannelStatusAutoDisabled, reason) {
		return
	}
	finishChannelDisabled(channelError, reason)
}

// finishChannelDisabled publishes the channel-level disable that just happened:
// the notification wording and the websocket teardown must describe the channel
// itself, so they only run once the channel really stopped serving traffic.
func finishChannelDisabled(channelError types.ChannelError, reason string) {
	if shouldCloseActiveWebSocketsAfterDisable(channelError.ChannelId) {
		CloseActiveWebSocketsForChannel(channelError.ChannelId, ChannelDisabledCloseReason)
	}
	lang := i18n.ResolveUserLang(model.GetRootUser().Id)
	data := map[string]any{
		"ChannelName": channelError.ChannelName,
		"ChannelId":   channelError.ChannelId,
		"Reason":      reason,
	}
	subject := i18n.TranslateTemplate(lang, i18n.MsgNotifyChannelDisabledSubject)
	content := i18n.TranslateTemplate(lang, i18n.MsgNotifyChannelDisabledBody)
	NotifyRootUserWithData(formatNotifyType(channelError.ChannelId, common.ChannelStatusAutoDisabled), subject, content, data)
}

// DisableChannelBecauseRelayError is the auto-ban decision for one failed relay
// request. On a multi-key channel the failure is scoped to the credential that
// produced it:
//
//   - another enabled key exists and the failing key is known: only that key is
//     disabled and the root user gets a key-level notice (channel still serving);
//   - the failure cannot be attributed to a key (empty UsingKey) or the key is no
//     longer part of the channel: nothing is disabled, because a channel-wide ban
//     would take down every other key over an unattributed error;
//   - the last enabled key fails: the channel itself ends up disabled and the
//     channel-level notice is the truthful one.
//
// Single-key channels keep the plain channel-level behaviour.
func DisableChannelBecauseRelayError(channelError types.ChannelError, reason string) {
	if !channelError.AutoBan {
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）未启用自动禁用功能，跳过禁用操作", channelError.ChannelName, channelError.ChannelId))
		return
	}
	if !channelError.IsMultiKey {
		DisableChannel(channelError, reason)
		return
	}
	channel, err := model.GetChannelById(channelError.ChannelId, true)
	if err != nil || channel == nil || !channel.ChannelInfo.IsMultiKey {
		DisableChannel(channelError, reason)
		return
	}
	if channel.Status != common.ChannelStatusEnabled {
		// A channel that is already disabled (manually or because every key is
		// exhausted) has no capacity to ban and its state must not be rewritten
		// by an automatic relay decision.
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）当前不可用（状态 %d），跳过自动禁用", channelError.ChannelName, channelError.ChannelId, channel.Status))
		return
	}
	keys := channel.GetKeys()
	if enabledMultiKeyCount(channel, keys) == 0 {
		// No key can serve anyway; let the channel-level path report it.
		DisableChannel(channelError, reason)
		return
	}
	if channelError.UsingKey == "" {
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）是多密钥通道且仍有可用密钥（%d/%d），但这次错误无法归因到具体密钥，保留通道与其余密钥，跳过自动禁用，原因：%s",
			channelError.ChannelName, channelError.ChannelId, enabledMultiKeyCount(channel, keys), len(keys), common.LocalLogPreview(reason)))
		return
	}
	if !model.UpdateChannelStatus(channelError.ChannelId, channelError.UsingKey, common.ChannelStatusAutoDisabled, reason) {
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）的密钥禁用未生效（该密钥已不在通道内或状态未变化），通道保持原状", channelError.ChannelName, channelError.ChannelId))
		return
	}
	if after, afterErr := model.GetChannelById(channelError.ChannelId, true); afterErr == nil && after != nil && after.Status == common.ChannelStatusEnabled {
		finishChannelKeyDisabled(after, channelError.UsingKey, reason)
		return
	}
	// The key that failed was the last usable one: the channel is disabled now.
	finishChannelDisabled(channelError, reason)
}

// finishChannelKeyDisabled reports a disabled key without claiming the channel
// stopped: on a multi-key channel the remaining keys keep serving traffic.
func finishChannelKeyDisabled(channel *model.Channel, usingKey string, reason string) {
	keys := channel.GetKeys()
	position := -1
	for index, key := range keys {
		if key == usingKey {
			position = index
			break
		}
	}
	if position < 0 {
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）的密钥已禁用，但该密钥已不在通道内，未发送通知，原因：%s",
			channel.Name, channel.Id, common.LocalLogPreview(reason)))
		return
	}
	usable := enabledMultiKeyCount(channel, keys)
	common.SysLog(fmt.Sprintf("通道「%s」（#%d）的密钥 #%d 已禁用，通道保持启用（仍有 %d/%d 个密钥可用），原因：%s",
		channel.Name, channel.Id, position+1, usable, len(keys), common.LocalLogPreview(reason)))
	lang := i18n.ResolveUserLang(model.GetRootUser().Id)
	data := map[string]any{
		"ChannelName":   channel.Name,
		"ChannelId":     channel.Id,
		"KeyPosition":   position + 1,
		"EnabledKeyNum": usable,
		"TotalKeyNum":   len(keys),
		"Reason":        reason,
	}
	subject := i18n.TranslateTemplate(lang, i18n.MsgNotifyChannelKeyDisabledSubject)
	content := i18n.TranslateTemplate(lang, i18n.MsgNotifyChannelKeyDisabledBody)
	NotifyRootUserWithData(formatKeyNotifyType(channel.Id, position+1), subject, content, data)
}

// enabledMultiKeyCount reports how many keys of a multi-key channel can still be
// selected. The durable credential row is the runtime source of truth, so it
// wins over the legacy ChannelInfo status map when both know the position.
func enabledMultiKeyCount(channel *model.Channel, keys []string) int {
	enabled := 0
	for position := range keys {
		if multiKeyPositionEnabled(channel, keys, position) {
			enabled++
		}
	}
	return enabled
}

func multiKeyPositionEnabled(channel *model.Channel, keys []string, position int) bool {
	for index := range channel.Credentials {
		credential := &channel.Credentials[index]
		if credential.Position != position || credential.Fingerprint != model.ChannelCredentialFingerprint(keys[position]) {
			continue
		}
		return credential.Status == common.ChannelStatusEnabled
	}
	status, ok := channel.ChannelInfo.MultiKeyStatusList[position]
	return !ok || status == common.ChannelStatusEnabled
}

func EnableChannel(channelId int, usingKey string, channelName string) {
	previous, _ := model.GetChannelById(channelId, true)
	if !model.UpdateChannelStatus(channelId, usingKey, common.ChannelStatusEnabled, "") {
		return
	}
	// Re-enabling one key of a channel that is already enabled changes nothing
	// the root user needs to be told about as a channel-level change.
	if channel, err := model.GetChannelById(channelId, true); err == nil && channel != nil &&
		channel.Status == common.ChannelStatusEnabled && previous != nil && previous.Status == common.ChannelStatusEnabled {
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）的密钥已启用，通道保持启用", channelName, channelId))
		return
	}
	lang := i18n.ResolveUserLang(model.GetRootUser().Id)
	data := map[string]any{
		"ChannelName": channelName,
		"ChannelId":   channelId,
	}
	subject := i18n.TranslateTemplate(lang, i18n.MsgNotifyChannelEnabledSubject)
	content := i18n.TranslateTemplate(lang, i18n.MsgNotifyChannelEnabledBody)
	NotifyRootUserWithData(formatNotifyType(channelId, common.ChannelStatusEnabled), subject, content, data)
}

// transientChannelErrors are channel-level failures that may clear on their own:
// every key of a multi-key channel has just failed, a transport timeout, a
// client error from a cloud SDK. Disabling the channel for one of them removes
// capacity permanently in this deployment (AutomaticEnableChannelEnabled is
// off), while a failover already moved the request elsewhere — so the
// availability-first reading is to keep the channel enabled and let a later
// request try it again. The two capability codes stay exempt for the same
// reason: another channel may serve the shape, and this channel is not broken.
var transientChannelErrors = map[types.ErrorCode]struct{}{
	types.ErrorCodeChannelNoAvailableKey:       {},
	types.ErrorCodeChannelUnsupportedEndpoint:  {},
	types.ErrorCodeChannelUnsupportedFeature:   {},
	types.ErrorCodeChannelResponseTimeExceeded: {},
	// A broken override condition is a configuration mistake on our side, not a
	// broken upstream: one is enough to fail the request, and banning the channel
	// on top of it removes capacity for every other caller (2026-10-28 #37/#91).
	types.ErrorCodeChannelParamOverrideInvalid: {},
}

func shouldDisableForChannelError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	_, transient := transientChannelErrors[err.GetErrorCode()]
	return !transient
}

func ShouldDisableChannel(err *types.NewAPIError) bool {
	if !common.AutomaticDisableChannelEnabled {
		return false
	}
	if err == nil {
		return false
	}
	if types.IsChannelError(err) {
		if !shouldDisableForChannelError(err) {
			return false
		}
		return true
	}
	if types.IsSkipRetryError(err) {
		return false
	}
	if operation_setting.ShouldDisableByStatusCode(err.StatusCode) {
		return true
	}

	lowerMessage := strings.ToLower(err.Error())
	search, _ := AcSearch(lowerMessage, operation_setting.AutomaticDisableKeywords, true)
	return search
}

func ShouldEnableChannel(newAPIError *types.NewAPIError, status int) bool {
	if !common.AutomaticEnableChannelEnabled {
		return false
	}
	if newAPIError != nil {
		return false
	}
	if status != common.ChannelStatusAutoDisabled {
		return false
	}
	return true
}
