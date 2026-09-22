package service

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestVideoCorrectionIsLabelledEstimatedInTheConsumeLog pins the label on the
// row, not just the helper: the guard that chooses the log's billing path used
// to read the caller's usage object, so a video correction — which replaces the
// upstream's fabricated count with the estimator's number — was still recorded
// as "upstream". The row then contradicted itself: a corrected prompt count
// labelled as the upstream's own report, and no way to tell an estimated charge
// from a reported one in the audit trail.
func TestVideoCorrectionIsLabelledEstimatedInTheConsumeLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousLogConsumeEnabled := common.LogConsumeEnabled
	previousMemoryCacheEnabled := common.MemoryCacheEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// A :memory: database is private per connection, so the settlement path
	// must reach the same connection the schema was created on.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Log{}))
	model.DB, model.LOG_DB = db, db
	common.LogConsumeEnabled = true
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.LogConsumeEnabled = previousLogConsumeEnabled
		common.MemoryCacheEnabled = previousMemoryCacheEnabled
		_ = sqlDB.Close()
	})

	user := model.User{Username: "video_log_path", Quota: 10_000_000, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "video-log-path-key", Name: "video-log-path", RemainQuota: 10_000_000, Status: common.TokenStatusEnabled}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Name: "video-log-path", Key: "unused", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)

	const localVideoTokens = 33858
	info := &relaycommon.RelayInfo{
		UserId:          user.Id,
		TokenId:         token.Id,
		TokenKey:        token.Key,
		OriginModelName: "kimi-k3",
		UsingGroup:      "default",
		UserGroup:       "default",
		UserSetting:     dto.UserSetting{BillingPreference: "wallet_only"},
		ForcePreConsume: true,
		StartTime:       time.Now(),
		RelayFormat:     types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: channel.Id,
			ChannelOtherSettings: dto.ChannelOtherSettings{
				SupportsVideo:  true,
				VideoUsageMode: dto.VideoUsageModeEstimate,
			},
		},
		PriceData: hosttypes.PriceData{
			GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1},
			ModelRatio:     10,
		},
	}
	// The channel reports a text-only count and the local container prices the
	// clip: the correction is the local price added to the reported text (no
	// estimate endpoint answer is cached in this test).
	info.SetVideoTokens(localVideoTokens)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	common.SetContextKey(ctx, "token_name", token.Name)
	require.Nil(t, PreConsumeBilling(ctx, 1_000_000, info))

	reported := &dto.Usage{PromptTokens: 26, CompletionTokens: 12, TotalTokens: 38}
	PostTextConsumeQuota(ctx, info, reported, nil)

	// The caller's object stays untouched: the log's prompt count and the
	// upstream audit value must remain distinguishable.
	require.Equal(t, 26, reported.PromptTokens, "the reported usage must not be mutated")

	var row model.Log
	require.NoError(t, db.Where("user_id = ?", user.Id).Take(&row).Error)
	require.Equal(t, 26+localVideoTokens, row.PromptTokens,
		"the consume log must record the corrected prompt count")

	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(row.Other, &other))
	adminInfo, ok := other["admin_info"].(map[string]any)
	require.True(t, ok, "admin_info must be present: %v", other)
	require.Equal(t, usageBillingPathOpenAIEstimated, adminInfo["usage_billing_path"],
		fmt.Sprintf("a corrected count must not be labelled as the upstream's report: %v", adminInfo))
}
