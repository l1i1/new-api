package model

import (
	"errors"

	"gorm.io/gorm"
)

// ClaimQuotaWarning atomically claims one low-balance notification for the
// current wallet or subscription balance episode. A balance at or above the
// threshold clears the episode so a later drop can notify again.
func ClaimQuotaWarning(userID int, subscription bool, subscriptionID int, threshold int64, balanceBefore int64, balanceAfter int64) (bool, error) {
	if userID <= 0 {
		return false, errors.New("invalid user id")
	}
	if threshold <= 0 {
		return false, errors.New("invalid quota warning threshold")
	}
	if subscription && subscriptionID <= 0 {
		return false, errors.New("invalid subscription id")
	}

	shouldSend := false
	// Fast path (wallet only): a locked read is only needed when the row may
	// be written. When the balance is healthy and no warning is outstanding,
	// this request performs no write and must not queue on the user row lock.
	// This must run before the transaction opens — using the outer DB handle
	// inside the transaction closure would wait on the pool connection that
	// the transaction itself holds.
	if !subscription {
		var flag struct {
			Quota            int  `gorm:"column:quota"`
			QuotaWarningSent bool `gorm:"column:quota_warning_sent"`
		}
		if err := DB.Model(&User{}).
			Select("quota", "COALESCE(quota_warning_sent, FALSE) AS quota_warning_sent").
			Where("id = ?", userID).
			Take(&flag).Error; err != nil {
			return shouldSend, err
		}
		if int64(flag.Quota) >= threshold {
			if !flag.QuotaWarningSent {
				return shouldSend, nil
			}
			// Rearm one-shot state with a single conditional UPDATE; no row
			// lock is held while deciding.
			return shouldSend, DB.Model(&User{}).Where("id = ? AND quota_warning_sent = ? AND quota >= ?", userID, true, threshold).
				Update("quota_warning_sent", false).Error
		}
	}

	err := DB.Transaction(func(tx *gorm.DB) error {
		if subscription {
			var sub UserSubscription
			if err := lockForUpdate(tx).
				Select("id", "amount_total", "amount_used", "COALESCE(quota_warning_sent, FALSE) AS quota_warning_sent").
				Where("id = ? AND user_id = ?", subscriptionID, userID).
				First(&sub).Error; err != nil {
				return err
			}
			currentBalance := sub.AmountTotal - sub.AmountUsed
			if currentBalance >= threshold {
				return tx.Model(&UserSubscription{}).Where("id = ?", subscriptionID).Update("quota_warning_sent", false).Error
			}
			if balanceAfter >= threshold && !sub.QuotaWarningSent {
				return nil
			}
			if sub.QuotaWarningSent {
				requestDelta := balanceBefore - balanceAfter
				if currentBalance+requestDelta < threshold {
					return nil
				}
			}
			if err := tx.Model(&UserSubscription{}).Where("id = ?", subscriptionID).Update("quota_warning_sent", true).Error; err != nil {
				return err
			}
			shouldSend = true
			return nil
		}

		var user User
		if err := lockForUpdate(tx).
			Select("id", "quota", "COALESCE(quota_warning_sent, FALSE) AS quota_warning_sent").
			Where("id = ?", userID).
			First(&user).Error; err != nil {
			return err
		}

		currentBalance := int64(user.Quota)
		if currentBalance >= threshold {
			return tx.Model(&User{}).Where("id = ?", userID).Update("quota_warning_sent", false).Error
		}
		if balanceAfter >= threshold && !user.QuotaWarningSent {
			return nil
		}

		// A request that started above the threshold marks a new episode even
		// if the previous episode was never observed at a recovered balance.
		if user.QuotaWarningSent {
			// A stale high snapshot from a concurrent request must not rearm the
			// same low-balance episode. Re-arm only when this request's inferred
			// starting balance was actually high in the locked database state.
			requestDelta := balanceBefore - balanceAfter
			if currentBalance+requestDelta < threshold {
				return nil
			}
		}

		if err := tx.Model(&User{}).Where("id = ?", userID).Update("quota_warning_sent", true).Error; err != nil {
			return err
		}
		shouldSend = true
		return nil
	})
	return shouldSend, err
}
