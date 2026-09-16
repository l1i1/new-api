package model

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// whiteLabelAttributionMonths is the contractual per-user commission window:
// the white-label UI treatment ends with the user's commission period.
const whiteLabelAttributionMonths = 6

// stampWhiteLabelForInviter stamps the white-label marker on a newly
// registered user when the inviter belongs to a partner account set.
// It never fails registration: lookup or persistence errors are logged and
// the user keeps the default interface.
func stampWhiteLabelForInviter(userID, inviterID int) {
	if userID <= 0 || inviterID <= 0 {
		return
	}
	entry, found := operation_setting.FindPartnerByInviter(inviterID)
	if !found {
		return
	}
	user, err := GetUserById(userID, true)
	if err != nil {
		common.SysLog("failed to load user for white-label stamp")
		return
	}
	setting := user.GetSetting()
	if setting.WhiteLabel != nil && setting.WhiteLabel.PartnerID == entry.ID {
		return
	}
	setting.WhiteLabel = newWhiteLabelConfig(entry.ID, user.CreatedAt)
	user.SetSetting(setting)
	if err := UpdateUserSetting(userID, setting); err != nil {
		common.SysLog("failed to stamp white-label setting")
	}
}

// newWhiteLabelConfig builds the marker expiring six months after the user's
// registration timestamp. A non-positive timestamp falls back to now.
func newWhiteLabelConfig(partnerID string, registeredAt int64) *dto.WhiteLabelConfig {
	registered := time.Unix(registeredAt, 0)
	if registeredAt <= 0 {
		registered = time.Now()
	}
	return &dto.WhiteLabelConfig{
		HideReferral: true,
		PartnerID:    partnerID,
		Until:        registered.AddDate(0, whiteLabelAttributionMonths, 0).Unix(),
	}
}
