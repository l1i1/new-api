package service

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const (
	inviteTopUpRewardTaskType               = "invite_first_topup_reward"
	inviteTopUpRewardDefaultIntervalMinutes = 5
)

type inviteTopUpRewardHandler struct{}

func (inviteTopUpRewardHandler) Type() string { return inviteTopUpRewardTaskType }

func (inviteTopUpRewardHandler) Enabled() bool {
	return model.GetInviteFirstTopUpRewardPolicy().Enabled && model.HasPendingInviteTopUpRewards()
}

func (inviteTopUpRewardHandler) Interval() time.Duration {
	minutes := common.GetEnvOrDefault(
		"INVITE_FIRST_TOPUP_REWARD_RETRY_INTERVAL_MINUTES",
		inviteTopUpRewardDefaultIntervalMinutes,
	)
	if minutes < 1 {
		minutes = inviteTopUpRewardDefaultIntervalMinutes
	}
	return time.Duration(minutes) * time.Minute
}

func (inviteTopUpRewardHandler) NewPayload() any { return nil }

func (inviteTopUpRewardHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	select {
	case <-ctx.Done():
		failSystemTask(task, runnerID, ctx.Err())
		return
	default:
	}

	result, err := model.ProcessPendingInviteTopUpRewards()
	if err != nil {
		failSystemTask(task, runnerID, err)
		return
	}
	if err := model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, result, ""); err != nil {
		logSystemTaskLockError(ctx, task, err)
	}
}

func init() {
	RegisterSystemTaskHandler(inviteTopUpRewardHandler{})
}
