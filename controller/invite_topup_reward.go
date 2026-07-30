package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

type inviteTopUpRewardOverviewResponse struct {
	ProgramEnabled bool                              `json:"program_enabled"`
	RewardRateBps  int                               `json:"reward_rate_bps"`
	Summary        model.InviteTopUpRewardSummary    `json:"summary"`
	Items          []model.InviteTopUpRewardListItem `json:"items"`
	Page           int                               `json:"page"`
	PageSize       int                               `json:"page_size"`
	Total          int64                             `json:"total"`
}

func GetInviteTopUpRewards(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	if pageInfo.Page < 1 {
		pageInfo.Page = 1
	}
	if c.Query("page_size") == "" && c.Query("ps") == "" && c.Query("size") == "" {
		pageInfo.PageSize = 5
	} else if pageInfo.PageSize < 1 {
		pageInfo.PageSize = 5
	}

	userId := c.GetInt("id")
	summary, items, total, err := model.GetInviteTopUpRewardsForInviter(
		userId,
		pageInfo.GetStartIdx(),
		pageInfo.GetPageSize(),
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	policy := model.GetInviteFirstTopUpRewardPolicy()
	common.ApiSuccess(c, inviteTopUpRewardOverviewResponse{
		ProgramEnabled: policy.Enabled,
		RewardRateBps:  policy.RewardRateBps,
		Summary:        summary,
		Items:          items,
		Page:           pageInfo.GetPage(),
		PageSize:       pageInfo.GetPageSize(),
		Total:          total,
	})
}
