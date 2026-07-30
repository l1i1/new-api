package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	InviteFirstTopUpRewardRateBasisPoints = 2000
	InviteTopUpRewardStatusPending        = "pending"
	InviteTopUpRewardStatusApplied        = "applied"
	InviteTopUpRewardStatusSkipped        = "skipped"
	inviteTopUpRewardBasisPointScale      = 10000
	inviteTopUpRewardRetryBatchSize       = 100
)

type InviteTopUpReward struct {
	Id              int    `json:"id"`
	CampaignId      string `json:"campaign_id" gorm:"type:varchar(64);not null;uniqueIndex:idx_invite_reward_campaign_invitee"`
	CampaignStart   int64  `json:"campaign_start" gorm:"not null"`
	TopUpId         int    `json:"topup_id" gorm:"column:top_up_id;not null;uniqueIndex"`
	TradeNo         string `json:"trade_no" gorm:"type:varchar(255);not null"`
	InviteeId       int    `json:"invitee_id" gorm:"not null;uniqueIndex:idx_invite_reward_campaign_invitee"`
	InviterId       int    `json:"inviter_id" gorm:"not null;index"`
	PaymentProvider string `json:"payment_provider" gorm:"type:varchar(50);not null"`
	BaseQuota       int    `json:"base_quota" gorm:"not null"`
	RewardQuota     int    `json:"reward_quota" gorm:"not null"`
	RewardRateBps   int    `json:"reward_rate_bps" gorm:"not null"`
	Status          string `json:"status" gorm:"type:varchar(16);not null;index"`
	SkipReason      string `json:"skip_reason" gorm:"type:varchar(64);not null;default:''"`
	CreatedAt       int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt       int64  `json:"updated_at" gorm:"autoUpdateTime"`
	AppliedAt       int64  `json:"applied_at" gorm:"not null;default:0"`
}

type InviteFirstTopUpRewardPolicy struct {
	Enabled       bool
	CampaignId    string
	CampaignStart int64
	RewardRateBps int
}

type InviteTopUpRewardBatchResult struct {
	Pending int `json:"pending"`
	Applied int `json:"applied"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
}

type InviteTopUpRewardSummary struct {
	AppliedCount     int64 `json:"applied_count"`
	PendingCount     int64 `json:"pending_count"`
	SkippedCount     int64 `json:"skipped_count"`
	TotalRewardQuota int64 `json:"total_reward_quota"`
}

type InviteTopUpRewardListItem struct {
	Id          int    `json:"id"`
	RewardQuota int    `json:"reward_quota"`
	Status      string `json:"status"`
	CreatedAt   int64  `json:"created_at"`
	AppliedAt   int64  `json:"applied_at"`
}

type inviteTopUpRewardStatusAggregate struct {
	Status      string
	RewardCount int64
	RewardQuota int64
}

func GetInviteFirstTopUpRewardPolicy() InviteFirstTopUpRewardPolicy {
	start := int64(common.GetEnvOrDefault("INVITE_FIRST_TOPUP_REWARD_START_TIMESTAMP", 0))
	enabled := common.GetEnvOrDefaultBool("INVITE_FIRST_TOPUP_REWARD_ENABLED", false) && start > 0
	return InviteFirstTopUpRewardPolicy{
		Enabled:       enabled,
		CampaignId:    fmt.Sprintf("invite-first-topup-%d", start),
		CampaignStart: start,
		RewardRateBps: InviteFirstTopUpRewardRateBasisPoints,
	}
}

func inviteFirstTopUpRewardQuota(baseQuota int, rewardRateBps int) (int, error) {
	if baseQuota <= 0 || rewardRateBps <= 0 || rewardRateBps > inviteTopUpRewardBasisPointScale {
		return 0, errors.New("invalid invite first top-up reward amount")
	}
	reward := int64(baseQuota) * int64(rewardRateBps) / inviteTopUpRewardBasisPointScale
	if reward <= 0 || reward > int64(common.MaxQuota) {
		return 0, errors.New("invalid invite first top-up reward quota")
	}
	return int(reward), nil
}

// prepareInviteFirstTopUpRewardTx stores a unique pending payout in the same
// transaction as wallet settlement. Reward application happens after commit so
// a payout retry never rolls back quota that was already credited to the payer.
func prepareInviteFirstTopUpRewardTx(tx *gorm.DB, topUp *TopUp, creditedQuota int) error {
	policy := GetInviteFirstTopUpRewardPolicy()
	if !policy.Enabled || tx == nil || topUp == nil || creditedQuota <= 0 {
		return nil
	}

	completedAt := topUp.CompleteTime
	if completedAt <= 0 {
		completedAt = common.GetTimestamp()
	}
	if completedAt < policy.CampaignStart {
		return nil
	}

	var invitee User
	if err := lockForUpdate(tx).First(&invitee, topUp.UserId).Error; err != nil {
		return err
	}
	if invitee.CreatedAt <= 0 || invitee.CreatedAt < policy.CampaignStart || invitee.InviterId <= 0 || invitee.InviterId == invitee.Id {
		return nil
	}

	var priorSuccessfulTopUps int64
	if err := tx.Model(&TopUp{}).
		Where("user_id = ? AND id <> ? AND status = ? AND (amount > 0 OR credited_quota > 0)", invitee.Id, topUp.Id, common.TopUpStatusSuccess).
		Count(&priorSuccessfulTopUps).Error; err != nil {
		return err
	}
	if priorSuccessfulTopUps > 0 {
		return nil
	}

	rewardQuota, err := inviteFirstTopUpRewardQuota(creditedQuota, policy.RewardRateBps)
	if err != nil {
		return nil
	}

	reward := InviteTopUpReward{
		CampaignId:      policy.CampaignId,
		CampaignStart:   policy.CampaignStart,
		TopUpId:         topUp.Id,
		TradeNo:         topUp.TradeNo,
		InviteeId:       invitee.Id,
		InviterId:       invitee.InviterId,
		PaymentProvider: topUp.PaymentProvider,
		BaseQuota:       creditedQuota,
		RewardQuota:     rewardQuota,
		RewardRateBps:   policy.RewardRateBps,
		Status:          InviteTopUpRewardStatusPending,
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&reward).Error
}

func ProcessInviteFirstTopUpReward(topUpId int) (bool, error) {
	if topUpId <= 0 || !GetInviteFirstTopUpRewardPolicy().Enabled {
		return false, nil
	}

	var appliedReward InviteTopUpReward
	applied := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var reward InviteTopUpReward
		if err := lockForUpdate(tx).Where("top_up_id = ?", topUpId).First(&reward).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if reward.Status != InviteTopUpRewardStatusPending {
			return nil
		}

		var inviter User
		err := lockForUpdate(tx.Unscoped()).First(&inviter, reward.InviterId).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return markInviteTopUpRewardSkipped(tx, &reward, "inviter_not_found")
		}
		if err != nil {
			return err
		}
		if inviter.DeletedAt.Valid {
			return markInviteTopUpRewardSkipped(tx, &reward, "inviter_deleted")
		}
		if inviter.Status != common.UserStatusEnabled {
			return markInviteTopUpRewardSkipped(tx, &reward, "inviter_disabled")
		}
		if inviter.Id == reward.InviteeId {
			return markInviteTopUpRewardSkipped(tx, &reward, "self_invite")
		}
		if int64(inviter.Quota)+int64(reward.RewardQuota) > int64(common.MaxQuota) {
			return markInviteTopUpRewardSkipped(tx, &reward, "inviter_quota_overflow")
		}

		result := tx.Model(&User{}).Where("id = ?", inviter.Id).
			Update("quota", gorm.Expr("quota + ?", reward.RewardQuota))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("invite reward inviter not found")
		}

		reward.Status = InviteTopUpRewardStatusApplied
		reward.AppliedAt = common.GetTimestamp()
		reward.SkipReason = ""
		if err := tx.Save(&reward).Error; err != nil {
			return err
		}
		appliedReward = reward
		applied = true
		return nil
	})
	if err != nil || !applied {
		return false, err
	}

	if common.RedisEnabled {
		if err := cacheIncrUserQuota(appliedReward.InviterId, int64(appliedReward.RewardQuota)); err != nil {
			common.SysLog("failed to increase inviter quota cache after first top-up reward: " + err.Error())
		}
	}
	RecordLog(
		appliedReward.InviterId,
		LogTypeSystem,
		fmt.Sprintf("Invite first top-up reward applied: topup_id=%d invitee_id=%d reward=%s", appliedReward.TopUpId, appliedReward.InviteeId, logger.LogQuota(appliedReward.RewardQuota)),
	)
	return true, nil
}

func markInviteTopUpRewardSkipped(tx *gorm.DB, reward *InviteTopUpReward, reason string) error {
	reward.Status = InviteTopUpRewardStatusSkipped
	reward.SkipReason = reason
	return tx.Save(reward).Error
}

func HasPendingInviteTopUpRewards() bool {
	var id int
	err := DB.Model(&InviteTopUpReward{}).
		Where("status = ?", InviteTopUpRewardStatusPending).
		Limit(1).
		Pluck("id", &id).Error
	return err == nil && id > 0
}

func ProcessPendingInviteTopUpRewards() (InviteTopUpRewardBatchResult, error) {
	result := InviteTopUpRewardBatchResult{}
	if !GetInviteFirstTopUpRewardPolicy().Enabled {
		return result, nil
	}

	var topUpIds []int
	if err := DB.Model(&InviteTopUpReward{}).
		Where("status = ?", InviteTopUpRewardStatusPending).
		Order("id ASC").
		Limit(inviteTopUpRewardRetryBatchSize).
		Pluck("top_up_id", &topUpIds).Error; err != nil {
		return result, err
	}
	result.Pending = len(topUpIds)

	for _, topUpId := range topUpIds {
		applied, err := ProcessInviteFirstTopUpReward(topUpId)
		if err != nil {
			result.Failed++
			continue
		}
		if applied {
			result.Applied++
			continue
		}

		var reward InviteTopUpReward
		if err := DB.Where("top_up_id = ?", topUpId).First(&reward).Error; err == nil && reward.Status == InviteTopUpRewardStatusSkipped {
			result.Skipped++
		}
	}
	if result.Failed > 0 {
		return result, fmt.Errorf("%d invite top-up rewards remain pending after processing errors", result.Failed)
	}
	return result, nil
}

// GetInviteTopUpRewardsForInviter returns only the user-facing reward fields.
// Invitee identity, order details, provider data, and base top-up quota stay in
// the internal ledger and never cross the self-service API boundary.
func GetInviteTopUpRewardsForInviter(inviterId int, offset int, limit int) (InviteTopUpRewardSummary, []InviteTopUpRewardListItem, int64, error) {
	summary := InviteTopUpRewardSummary{}
	items := make([]InviteTopUpRewardListItem, 0)
	if inviterId <= 0 || offset < 0 || limit <= 0 {
		return summary, items, 0, errors.New("invalid invite top-up reward query")
	}

	var total int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		statusRows := make([]inviteTopUpRewardStatusAggregate, 0)
		if err := tx.Model(&InviteTopUpReward{}).
			Select("status, COUNT(*) AS reward_count, COALESCE(SUM(reward_quota), 0) AS reward_quota").
			Where("inviter_id = ?", inviterId).
			Group("status").
			Scan(&statusRows).Error; err != nil {
			return err
		}

		for _, row := range statusRows {
			total += row.RewardCount
			switch row.Status {
			case InviteTopUpRewardStatusApplied:
				summary.AppliedCount = row.RewardCount
				summary.TotalRewardQuota = row.RewardQuota
			case InviteTopUpRewardStatusPending:
				summary.PendingCount = row.RewardCount
			case InviteTopUpRewardStatusSkipped:
				summary.SkippedCount = row.RewardCount
			}
		}

		return tx.Model(&InviteTopUpReward{}).
			Select("id, reward_quota, status, created_at, applied_at").
			Where("inviter_id = ?", inviterId).
			Order("id DESC").
			Limit(limit).
			Offset(offset).
			Scan(&items).Error
	})
	return summary, items, total, err
}

func processInviteFirstTopUpRewardAfterSettlement(topUpId int) {
	if _, err := ProcessInviteFirstTopUpReward(topUpId); err != nil {
		common.SysError(fmt.Sprintf("invite first top-up reward remains pending: topup_id=%d error=%v", topUpId, err))
	}
}
