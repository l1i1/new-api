package model

import (
	"gorm.io/gorm"
)

// PartnerUserRef is the minimal identity of a new-api account that a partner
// channel can be mapped to: the numeric id attribution runs on, the login name
// the operator actually knows, and the aff code the invitees came through.
// Nothing else about the account crosses this boundary.
type PartnerUserRef struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	AffCode  string `json:"aff_code"`
	Status   int    `json:"status"`
}

// GetPartnerUserRefByUsername resolves one account by its login name.
//
// The console maps a channel by typing the name, so the numeric id and the aff
// code are read from the account itself instead of being hand-copied: a typo in
// either used to be undetectable, and a wrong id silently attributes another
// account's invitees while a wrong aff code mislabels the bucket in every
// statement.
func GetPartnerUserRefByUsername(username string) (PartnerUserRef, error) {
	var ref PartnerUserRef
	err := DB.Model(&User{}).
		Select("id", "username", "aff_code", "status").
		Where("username = ?", username).
		First(&ref).Error
	if err != nil {
		return PartnerUserRef{}, err
	}
	return ref, nil
}

// ErrPartnerUserNotFound is returned when no account carries that login name.
var ErrPartnerUserNotFound = gorm.ErrRecordNotFound
