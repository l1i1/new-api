package model

import (
	"strings"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// ResolveInviterByAffCode maps the invite parameter of a registration to the
// inviter id it attributes to. Console-issued partner codes are checked first:
// they are an explicit, operator-visible namespace whose ids live in a reserved
// range, so a match there can never be a real account. Everything else falls
// back to the site's own aff codes, which are exactly four characters long and
// therefore cannot collide with a partner code.
func ResolveInviterByAffCode(affCode string) int {
	affCode = strings.TrimSpace(affCode)
	if affCode == "" {
		return 0
	}
	if inviterID, found := operation_setting.FindInviterIDByPartnerCode(affCode); found {
		return inviterID
	}
	inviterID, _ := GetUserIdByAffCode(affCode)
	return inviterID
}

// AffCodeTakenByUser reports whether any account already owns the code. It is
// the check that keeps a console-issued code from shadowing a real account's
// invite link. Deleted rows count: their aff_code still occupies the column.
func AffCodeTakenByUser(affCode string) (bool, error) {
	affCode = strings.TrimSpace(affCode)
	if affCode == "" {
		return false, nil
	}
	var count int64
	if err := DB.Unscoped().Model(&User{}).Where("aff_code = ?", affCode).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
