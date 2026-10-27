package service

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newUndeliveredTestRelayInfo builds the minimal settlement context shared by
// the delivered/undelivered tests. The caller still has to add user/token/channel.
func newUndeliveredTestRelayInfo(t *testing.T) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	setupAnomalyLogDB(t)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ctx.Set(common.RequestIdKey, "20261027190000000000000000000000undeliv")

	user := model.User{Username: "undelivered_test", Quota: 1_000_000, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "undelivered-test-key", Name: "undelivered", RemainQuota: 10_000_000, Status: common.TokenStatusEnabled}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{Name: "undelivered-test", Key: "unused", Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(&channel).Error)
	t.Cleanup(func() {
		require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Delete(&model.Log{}).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&token).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&user).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&channel).Error)
	})

	info := newAnomalyTestRelayInfo(`tier("base", p * 2 + c * 8)`, 1)
	info.UserId = user.Id
	info.TokenId = token.Id
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: channel.Id}
	info.UserGroup = "default"
	info.UsingGroup = "default"
	info.RelayFormat = types.RelayFormatOpenAI
	info.UserSetting = dto.UserSetting{BillingPreference: "wallet_only"}
	return ctx, info
}

// TestPostTextConsumeQuotaUndeliveredZeroesConsumeLog pins the billing policy:
// a stream that did not deliver a terminal result because of an upstream fault
// (scanner error, truncation, timeout) is recorded with quota=0 and zero token
// columns, the raw upstream counts survive only in admin_info.undelivered, and
// the caller's wallet is not debited.
func TestPostTextConsumeQuotaUndeliveredZeroesConsumeLog(t *testing.T) {
	ctx, info := newUndeliveredTestRelayInfo(t)
	info.StreamStatus = relaycommon.NewStreamStatus()
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, errors.New("unexpected EOF"))

	usage := &dto.Usage{PromptTokens: 1000, CompletionTokens: 200, TotalTokens: 1200}
	PostTextConsumeQuota(ctx, info, usage, nil)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ?", info.UserId).Take(&log).Error)
	assert.Zero(t, log.Quota, "an undelivered response must bill zero")
	assert.Zero(t, log.PromptTokens, "token columns zeroed so dashboard buckets stay clean")
	assert.Zero(t, log.CompletionTokens)
	assert.Contains(t, log.Content, "按 0 计费")

	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	adminInfo, ok := other["admin_info"].(map[string]any)
	require.True(t, ok, "admin_info should exist")
	marker, ok := adminInfo["undelivered"].(map[string]any)
	require.True(t, ok, "undelivered marker should be nested under admin_info")
	assert.Equal(t, relaycommon.UndeliveredStreamPrefix+"scanner_error", marker["reason"])
	assert.Equal(t, "zeroed", marker["action"])
	assert.Equal(t, float64(1000), marker["upstream_prompt_tokens"])
	assert.Equal(t, float64(200), marker["upstream_completion_tokens"])

	userQuota, err := model.GetUserQuota(info.UserId, false)
	require.NoError(t, err)
	assert.Equal(t, 1_000_000, userQuota, "an undelivered response must not debit the wallet")
	assert.False(t, info.ObservedUsage, "a zero-billed failure must not be reported as observed usage")
}

// TestPostTextConsumeQuotaDeliveredStreamStaysBillable is the control: a stream
// that ended normally still bills its reported usage.
func TestPostTextConsumeQuotaDeliveredStreamStaysBillable(t *testing.T) {
	ctx, info := newUndeliveredTestRelayInfo(t)
	info.StreamStatus = relaycommon.NewStreamStatus()
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	info.StreamFinishReason = "stop"

	PostTextConsumeQuota(ctx, info, &dto.Usage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}, nil)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ?", info.UserId).Take(&log).Error)
	assert.Positive(t, log.Quota, "a delivered response stays billable")
	assert.Equal(t, 1000, log.PromptTokens)
	assert.Equal(t, 500, log.CompletionTokens)
	assert.True(t, info.ObservedUsage)

	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	adminInfo, _ := other["admin_info"].(map[string]any)
	assert.Nil(t, adminInfo["undelivered"], "no undelivered marker on a delivered response")
}

// TestPostTextConsumeQuotaClientGoneStaysBillable is the counterpart policy: a
// caller-side disconnect is not an upstream fault. The provider already
// generated (and charged) the observed tokens, so the request stays billable
// and carries no undelivered marker.
func TestPostTextConsumeQuotaClientGoneStaysBillable(t *testing.T) {
	ctx, info := newUndeliveredTestRelayInfo(t)
	info.StreamStatus = relaycommon.NewStreamStatus()
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, errors.New("context canceled"))

	PostTextConsumeQuota(ctx, info, &dto.Usage{PromptTokens: 1000, CompletionTokens: 200, TotalTokens: 1200}, nil)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ?", info.UserId).Take(&log).Error)
	assert.Positive(t, log.Quota, "a caller-side disconnect keeps its observed usage billable")
	assert.Equal(t, 1000, log.PromptTokens)
	assert.Equal(t, 200, log.CompletionTokens)
	assert.True(t, info.ObservedUsage)

	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	adminInfo, _ := other["admin_info"].(map[string]any)
	assert.Nil(t, adminInfo["undelivered"], "no undelivered marker for a caller-side disconnect")
}
