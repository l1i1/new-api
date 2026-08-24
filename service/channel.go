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

func ShouldDisableChannel(err *types.NewAPIError) bool {
	if !common.AutomaticDisableChannelEnabled {
		return false
	}
	if err == nil {
		return false
	}
	if types.IsChannelError(err) {
		if err.GetErrorCode() == types.ErrorCodeChannelUnsupportedEndpoint ||
			err.GetErrorCode() == types.ErrorCodeChannelUnsupportedFeature {
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
