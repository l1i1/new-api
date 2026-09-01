package model

import (
	"errors"

	"gorm.io/gorm"
)

var ErrInsufficientUserQuota = errors.New("insufficient user quota")

// DecreaseUserQuotaIfEnough atomically rejects a debit that would make the
// available wallet quota negative. Security-sensitive reservations bypass the
// process-local batch and use one conditional SQL update across all nodes.
func DecreaseUserQuotaIfEnough(id int, quota int) error {
	if quota < 0 {
		return errors.New("quota 不能为负数！")
	}
	if quota == 0 {
		return nil
	}

	result := DB.Model(&User{}).
		Where("id = ? AND quota >= ?", id, quota).
		Update("quota", gorm.Expr("quota - ?", quota))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrInsufficientUserQuota
	}

	invalidateUserQuotaCacheAfterMutation(id)
	return nil
}
