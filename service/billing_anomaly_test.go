package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http/httptest"
)

// The 2026-10-13 kimi-k3 incident: upstream reported garbage usage
// (prompt_tokens=1.39e15, completion_tokens=5.6e14, cache_tokens=3.2e14) and
// the tiered settlement clamped to MaxQuota but still charged it. The anomaly
// gate must bill zero instead.
var kimiK3GarbageUsage = &dto.Usage{
	PromptTokens:     1388347719831046,
	CompletionTokens: 562949953421056,
	TotalTokens:      1951297673252102,
	PromptTokensDetails: dto.InputTokenDetails{
		CachedTokens: 322313980642816,
	},
}

func newAnomalyTestRelayInfo(expression string, groupRatio float64) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		OriginModelName: "kimi-k3",
		StartTime:       time.Now(),
		PriceData: hosttypes.PriceData{
			ModelRatio:      1,
			CompletionRatio: 1,
			GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: groupRatio},
		},
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode:  "tiered_expr",
			ExprString:   expression,
			ExprHash:     billingexpr.ExprHashString(expression),
			GroupRatio:   groupRatio,
			QuotaPerUnit: 500_000,
		},
	}
}

// TestTryTieredSettleGarbageUsageBillsZero: garbage usage (P=1.39e15) must
// settle to quota=0 with the billing_anomaly marker recorded, not the clamped
// MaxQuota.
func TestTryTieredSettleGarbageUsageBillsZero(t *testing.T) {
	gin.SetMode(gin.TestMode)
	exprStr := `tier("base", p * 2 + c * 8 + cr * 0.2)`
	relayInfo := newAnomalyTestRelayInfo(exprStr, 1)

	ok, quota, result := TryTieredSettle(relayInfo, BuildTieredTokenParams(kimiK3GarbageUsage, false, nil))

	require.True(t, ok)
	require.NotNil(t, relayInfo.BillingAnomaly)
	assert.Equal(t, billingexpr.UsageAnomalyOutOfRange, relayInfo.BillingAnomaly.Kind)
	assert.Equal(t, "p", relayInfo.BillingAnomaly.Field)
	assert.Zero(t, quota, "garbage usage must bill zero, not the clamped MaxQuota")
	assert.Nil(t, result, "no tiered result is produced for anomalous usage")
	assert.Nil(t, relayInfo.QuotaClamp, "the sanity gate fires before any quota conversion")
}

// TestTryTieredSettleSaturationBillsZero: when the expression itself produces
// an out-of-range quota (input plausible, coefficient absurd), the clamped
// value (MaxQuota) must not be charged — quota=0 instead.
func TestTryTieredSettleSaturationBillsZero(t *testing.T) {
	gin.SetMode(gin.TestMode)
	exprStr := `tier("base", p * 1000000000000)`
	relayInfo := newAnomalyTestRelayInfo(exprStr, 1)

	ok, quota, result := TryTieredSettle(relayInfo, billingexpr.TokenParams{P: 1_000_000})

	require.True(t, ok)
	require.NotNil(t, result)
	assert.NotNil(t, relayInfo.QuotaClamp, "saturation is still recorded for admin auditing")
	require.NotNil(t, relayInfo.BillingAnomaly)
	assert.Equal(t, billingexpr.UsageAnomalyQuotaSaturation, relayInfo.BillingAnomaly.Kind)
	assert.Zero(t, quota, "a saturated settlement must bill zero, not MaxQuota (2147483647)")
}

// TestTryTieredSettleNormalUsageUnchanged: regression — normal usage keeps the
// exact pre-change settlement result and records no anomaly.
func TestTryTieredSettleNormalUsageUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	exprStr := `tier("base", p * 2 + c * 8)`
	relayInfo := newAnomalyTestRelayInfo(exprStr, 1)

	ok, quota, result := TryTieredSettle(relayInfo, billingexpr.TokenParams{P: 1000, C: 500})

	require.True(t, ok)
	require.NotNil(t, result)
	assert.Nil(t, relayInfo.BillingAnomaly)
	assert.Nil(t, relayInfo.QuotaClamp)
	// p*2 + c*8 = 6000 ($/1M) => 6000/1e6*500000 = 3000 quota
	assert.Equal(t, 3000, quota)
}

// TestPostTextConsumeQuotaGarbageUsageZeroesConsumeLog proves the end-to-end
// text path: garbage usage produces a consume log with quota=0, zeroed token
// columns, and the billing_anomaly marker under admin_info.
func TestPostTextConsumeQuotaGarbageUsageZeroesConsumeLog(t *testing.T) {
	setupAnomalyLogDB(t)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ctx.Set(common.RequestIdKey, "202610130506078697316698268d9d6umtygYza")

	user := model.User{Username: "anomaly_garbage", Quota: 10_000_000, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "anomaly-garbage-test", Name: "anomaly", RemainQuota: 10_000_000, Status: common.TokenStatusEnabled}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{Name: "anomaly-garbage", Key: "unused", Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(&channel).Error)
	t.Cleanup(func() {
		require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Delete(&model.Log{}).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&token).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&user).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&channel).Error)
	})

	exprStr := `tier("base", p * 2 + c * 8)`
	relayInfo := newAnomalyTestRelayInfo(exprStr, 1)
	relayInfo.UserId = user.Id
	relayInfo.TokenId = token.Id
	relayInfo.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: channel.Id}
	relayInfo.UserGroup = "default"
	relayInfo.UsingGroup = "default"
	relayInfo.RelayFormat = types.RelayFormatOpenAI
	relayInfo.UserSetting = dto.UserSetting{BillingPreference: "wallet_only"}

	PostTextConsumeQuota(ctx, relayInfo, kimiK3GarbageUsage, nil)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Take(&log).Error)
	assert.Zero(t, log.Quota, "garbage usage must be billed as zero")
	assert.Zero(t, log.PromptTokens, "token columns zeroed so dashboard buckets stay clean")
	assert.Zero(t, log.CompletionTokens)

	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	adminInfo, ok := other["admin_info"].(map[string]any)
	require.True(t, ok, "admin_info should exist")
	anomaly, ok := adminInfo["billing_anomaly"].(map[string]any)
	require.True(t, ok, "billing_anomaly marker should be nested under admin_info")
	assert.Equal(t, "usage_out_of_range", anomaly["kind"])
	assert.Equal(t, "p", anomaly["field"])
	assert.Equal(t, "zeroed", anomaly["action"])
	raw, ok := anomaly["raw"].(map[string]any)
	require.True(t, ok, "raw usage must be preserved in the marker")
	assert.Equal(t, float64(1388347719831046), raw["p"])
	assert.Equal(t, float64(562949953421056), raw["c"])
}

// TestPostTextConsumeQuotaSaturationZeroesConsumeLog: a saturated tiered
// settlement (plausible inputs, absurd coefficient) must charge 0, not
// MaxQuota.
func TestPostTextConsumeQuotaSaturationZeroesConsumeLog(t *testing.T) {
	setupAnomalyLogDB(t)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	user := model.User{Username: "anomaly_saturation", Quota: 10_000_000, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "anomaly-sat-test", Name: "anomaly", RemainQuota: 10_000_000, Status: common.TokenStatusEnabled}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{Name: "anomaly-sat", Key: "unused", Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(&channel).Error)
	t.Cleanup(func() {
		require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Delete(&model.Log{}).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&token).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&user).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&channel).Error)
	})

	exprStr := `tier("base", p * 1000000000000)`
	relayInfo := newAnomalyTestRelayInfo(exprStr, 1)
	relayInfo.UserId = user.Id
	relayInfo.TokenId = token.Id
	relayInfo.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: channel.Id}
	relayInfo.UserGroup = "default"
	relayInfo.UsingGroup = "default"
	relayInfo.RelayFormat = types.RelayFormatOpenAI
	relayInfo.UserSetting = dto.UserSetting{BillingPreference: "wallet_only"}

	PostTextConsumeQuota(ctx, relayInfo, &dto.Usage{PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100}, nil)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Take(&log).Error)
	assert.Zero(t, log.Quota, "saturated settlement must charge 0, not 2147483647")

	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	adminInfo, ok := other["admin_info"].(map[string]any)
	require.True(t, ok)
	anomaly, ok := adminInfo["billing_anomaly"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "quota_saturation", anomaly["kind"])
	assert.Equal(t, "zeroed", anomaly["action"])
	// The legacy quota_saturation marker stays for continuity.
	sat, ok := adminInfo["quota_saturation"].(map[string]any)
	require.True(t, ok, "existing quota_saturation marker must be preserved")
	assert.Equal(t, "QuotaRound", sat["op"])
}

// TestPostTextConsumeQuotaNormalUsageUnchanged: regression — a normal request
// through the full text path keeps identical quota and token columns.
func TestPostTextConsumeQuotaNormalUsageUnchanged(t *testing.T) {
	setupAnomalyLogDB(t)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	user := model.User{Username: "anomaly_normal", Quota: 10_000_000, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "anomaly-normal-test", Name: "anomaly", RemainQuota: 10_000_000, Status: common.TokenStatusEnabled}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{Name: "anomaly-normal", Key: "unused", Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(&channel).Error)
	t.Cleanup(func() {
		require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Delete(&model.Log{}).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&token).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&user).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&channel).Error)
	})

	exprStr := `tier("base", p * 2 + c * 8)`
	relayInfo := newAnomalyTestRelayInfo(exprStr, 1)
	relayInfo.UserId = user.Id
	relayInfo.TokenId = token.Id
	relayInfo.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: channel.Id}
	relayInfo.UserGroup = "default"
	relayInfo.UsingGroup = "default"
	relayInfo.RelayFormat = types.RelayFormatOpenAI
	relayInfo.UserSetting = dto.UserSetting{BillingPreference: "wallet_only"}

	PostTextConsumeQuota(ctx, relayInfo, &dto.Usage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}, nil)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Take(&log).Error)
	assert.Equal(t, 3000, log.Quota)
	assert.Equal(t, 1000, log.PromptTokens)
	assert.Equal(t, 500, log.CompletionTokens)

	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	if adminInfo, ok := other["admin_info"].(map[string]any); ok {
		assert.NotContains(t, adminInfo, "billing_anomaly", "normal usage must not be marked anomalous")
	}
}

// TestRecalculateTaskQuotaByTokensGarbageTokensZeroes verifies the async task
// token-recalc path: garbage totalTokens settles to 0 with the anomaly marker
// on the task billing log, refunding the full reservation.
func TestRecalculateTaskQuotaByTokensGarbageTokensZeroes(t *testing.T) {
	setupAnomalyLogDB(t)
	ctx := t.Context()

	previousRatios := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"kimi-k3":1}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios))
	})

	user := model.User{Username: "anomaly_task", Quota: 10_000_000, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(&user).Error)
	channel := model.Channel{Name: "anomaly-task", Key: "unused", Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(&channel).Error)
	t.Cleanup(func() {
		require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Delete(&model.Log{}).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&user).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&channel).Error)
	})

	task := &model.Task{
		UserId:    user.Id,
		ChannelId: channel.Id,
		Progress:  "100%",
		Action:    "video",
		TaskID:    func() string { return fmt.Sprintf("task_%d", time.Now().UnixNano()) }(),
		Quota:     2000,
		Group:     "default",
		Status:    model.TaskStatusSuccess,
	}
	task.Properties.OriginModelName = "kimi-k3"
	task.PrivateData.TokenId = 0
	require.NoError(t, model.DB.Create(task).Error)
	t.Cleanup(func() { require.NoError(t, model.DB.Unscoped().Delete(task).Error) })

	handled := RecalculateTaskQuotaByTokens(ctx, task, 1388347719831046)

	require.True(t, handled)
	assert.Zero(t, task.Quota, "garbage token count must settle to 0, not the clamped MaxQuota")

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).
		Where("type = ?", model.LogTypeRefund).Take(&log).Error)
	assert.Equal(t, 2000, log.Quota, "the full reservation is refunded")

	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	adminInfo, ok := other["admin_info"].(map[string]any)
	require.True(t, ok)
	anomaly, ok := adminInfo["billing_anomaly"].(map[string]any)
	require.True(t, ok, "task billing log must carry the billing_anomaly marker")
	assert.Equal(t, "usage_out_of_range", anomaly["kind"])
	assert.Equal(t, "zeroed", anomaly["action"])
}

// TestPostAudioConsumeQuotaGarbageUsagePreservesRawInMarker: the audio path
// zeroes the logged usage but must snapshot the raw upstream counts into the
// anomaly marker before zeroing (regression for capturing after the zeroing).
func TestPostAudioConsumeQuotaGarbageUsagePreservesRawInMarker(t *testing.T) {
	setupAnomalyLogDB(t)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/audio/transcriptions", nil)

	user := model.User{Username: "anomaly_audio", Quota: 10_000_000, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "anomaly-audio-test", Name: "anomaly", RemainQuota: 10_000_000, Status: common.TokenStatusEnabled}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{Name: "anomaly-audio", Key: "unused", Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(&channel).Error)
	t.Cleanup(func() {
		require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Delete(&model.Log{}).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&token).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&user).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&channel).Error)
	})

	relayInfo := newAnomalyTestRelayInfo(`tier("base", p * 2)`, 1)
	relayInfo.UserId = user.Id
	relayInfo.TokenId = token.Id
	relayInfo.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: channel.Id}
	relayInfo.UserGroup = "default"
	relayInfo.UsingGroup = "default"
	relayInfo.RelayFormat = types.RelayFormatOpenAI
	relayInfo.UserSetting = dto.UserSetting{BillingPreference: "wallet_only"}

	PostAudioConsumeQuota(ctx, relayInfo, kimiK3GarbageUsage, "")

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Take(&log).Error)
	assert.Zero(t, log.Quota)
	assert.Zero(t, log.PromptTokens)

	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	adminInfo := other["admin_info"].(map[string]any)
	anomaly := adminInfo["billing_anomaly"].(map[string]any)
	raw := anomaly["raw"].(map[string]any)
	assert.Equal(t, float64(1388347719831046), raw["p"], "raw upstream counts must survive the zeroing")
	assert.Equal(t, float64(562949953421056), raw["c"])
}

// setupAnomalyLogDB swaps in an isolated SQLite main+log database for the
// settlement tests, mirroring the fixture style of text_quota_test.go.
func setupAnomalyLogDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:anomaly_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	logDB, err := gorm.Open(sqlite.Open("file:anomaly_test_log?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	model.DB, model.LOG_DB = db, logDB
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	oldExport := common.DataExportEnabled
	common.DataExportEnabled = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.SetDatabaseTypes(oldMainType, oldLogType)
		common.QuotaPerUnit = oldQuotaPerUnit
		common.DataExportEnabled = oldExport
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
		sqlLog, _ := logDB.DB()
		_ = sqlLog.Close()
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Task{}))
	require.NoError(t, logDB.AutoMigrate(&model.Log{}))
}
