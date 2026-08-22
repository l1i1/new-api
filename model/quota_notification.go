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

	column := "quota_warning_sent"
	if subscription {
		column = "subscription_quota_warning_sent"
	}

	shouldSend := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		selectColumns := "COALESCE(" + column + ", FALSE) AS " + column
		if !subscription {
			selectColumns += ", quota"
		}
		if err := lockForUpdate(tx).
			Select(selectColumns).
			Where("id = ?", userID).
			First(&user).Error; err != nil {
			return err
		}

		currentBalance := int64(user.Quota)
		if subscription {
			var sub UserSubscription
			if err := lockForUpdate(tx).
				Select("amount_total", "amount_used").
				Where("id = ? AND user_id = ?", subscriptionID, userID).
				First(&sub).Error; err != nil {
				return err
			}
			currentBalance = sub.AmountTotal - sub.AmountUsed
		}

		sent := user.QuotaWarningSent
		if subscription {
			sent = user.SubQuotaWarnSent
		}
		if currentBalance >= threshold {
			if err := tx.Model(&User{}).Where("id = ?", userID).Update(column, false).Error; err != nil {
				return err
			}
			return nil
		}
		if balanceAfter >= threshold && !sent {
			return nil
		}

		// A request that started above the threshold marks a new episode even
		// if the previous episode was never observed at a recovered balance.
		if sent {
			// A stale high snapshot from a concurrent request must not rearm the
			// same low-balance episode. Re-arm only when this request's inferred
			// starting balance was actually high in the locked database state.
			requestDelta := balanceBefore - balanceAfter
			if currentBalance+requestDelta < threshold {
				return nil
			}
		}

		if err := tx.Model(&User{}).Where("id = ?", userID).Update(column, true).Error; err != nil {
			return err
		}
		shouldSend = true
		return nil
	})
	return shouldSend, err
}
