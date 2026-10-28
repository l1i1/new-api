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

// disable & notify
func DisableChannel(channelError types.ChannelError, reason string) {
	common.SysLog(fmt.Sprintf("通道「%s」（#%d）发生错误，准备禁用，原因：%s", channelError.ChannelName, channelError.ChannelId, common.LocalLogPreview(reason)))

	// 检查是否启用自动禁用功能
	if !channelError.AutoBan {
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）未启用自动禁用功能，跳过禁用操作", channelError.ChannelName, channelError.ChannelId))
		return
	}

	success := model.UpdateChannelStatus(channelError.ChannelId, channelError.UsingKey, common.ChannelStatusAutoDisabled, reason)
	if success {
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
}

func EnableChannel(channelId int, usingKey string, channelName string) {
	success := model.UpdateChannelStatus(channelId, usingKey, common.ChannelStatusEnabled, "")
	if success {
		lang := i18n.ResolveUserLang(model.GetRootUser().Id)
		data := map[string]any{
			"ChannelName": channelName,
			"ChannelId":   channelId,
		}
		subject := i18n.TranslateTemplate(lang, i18n.MsgNotifyChannelEnabledSubject)
		content := i18n.TranslateTemplate(lang, i18n.MsgNotifyChannelEnabledBody)
		NotifyRootUserWithData(formatNotifyType(channelId, common.ChannelStatusEnabled), subject, content, data)
	}
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
