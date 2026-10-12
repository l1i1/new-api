package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 端到端回归：驱动真实的 controller.Relay 重试循环（换道闸门 decideHTTPRelayRetry →
// 重新选渠道 → 新渠道的 handler），验证 2026-10-12 20:29 cdp 事故的形态不会复现：
//
//   - 第 1 跳上游 HTTP 200 + 流内 error 事件（service.NormalizeOpenAIStreamError 归一成 502）；
//   - 若这一跳把 502 钉在客户 writer 上（旧代码 relay-openai.go 的 c.Status(...)），
//     换道成功后第 2 跳的首帧会把它当响应状态发出去 → 客户拿到 502 + 正文；
//   - 修复后：客户必须拿到 200 + 完整正文；全部渠道都失败时终局仍是 5xx + 错误体。
//
// 与 relay/channel/openai/stream_status_commit_test.go 的分工：那个用例只单帧调用
// OaiStreamHandler（handler 层契约），本用例覆盖"真实换道循环"这一段。
// 本用例由 pi(glm-5.3:max) 第二轮独立审查构造（临时文件），复核后采纳入库。
func setupStickyStatusEnv(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()

	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousMemoryCache := common.MemoryCacheEnabled
	previousBatchUpdate := common.BatchUpdateEnabled
	previousLogConsume := common.LogConsumeEnabled
	previousRedisEnabled := common.RedisEnabled
	previousRetryTimes := common.RetryTimes
	prevStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	gs := operation_setting.GetGeneralSetting()
	prevPingEnabled, prevPingSeconds := gs.PingIntervalEnabled, gs.PingIntervalSeconds
	gs.PingIntervalEnabled = false
	gs.PingIntervalSeconds = 60
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.Channel{}, &model.Log{}, &model.Ability{}, &model.Token{}, &model.UserSubscription{}, &model.GroupModelRateLimit{}))
	model.DB = database
	model.LOG_DB = database
	// 走生产用的内存缓存选渠道路径（与 relay_channel_saturation_test.go 同一手法），
	// 从而不依赖 model 包内部的 SQL 方言列名初始化。
	common.MemoryCacheEnabled = true
	common.BatchUpdateEnabled = false
	common.LogConsumeEnabled = false
	common.RedisEnabled = false
	common.RetryTimes = 3
	previousModelRatios := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"m":1}`))

	require.NoError(t, database.Create(&model.User{
		Id: 7, Username: "sticky-user", Group: "default", Quota: 100_000_000,
	}).Error)
	tok := model.Token{
		Id: 55, UserId: 7, Key: "sk-tok", Status: common.TokenStatusEnabled,
		RemainQuota: 100_000_000, UnlimitedQuota: true, Name: "e2e",
	}
	require.NoError(t, database.Create(&tok).Error)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), "FIRSTFAIL") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"服务繁忙，请稍后重试。\",\"type\":\"unknown_error\"}}\n\n"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"created\":1710000000,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"created\":1710000000,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))

	var calls int32
	handler := upstream.Config.Handler
	wrapped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		t.Logf("upstream hit %d: %s auth=%q", n, r.URL.Path, r.Header.Get("Authorization"))
		handler.ServeHTTP(w, r)
	}))

	// 两个渠道指向同一个上游，靠 API key 区分行为：FIRSTFAIL 的渠道回"200 + 流内错误事件"。
	ch1 := model.Channel{
		Type: constant.ChannelTypeOpenAI, Name: "sticky-fail-first",
		Key: "sk-FIRSTFAIL", Status: common.ChannelStatusEnabled,
		Models: "m", Group: "default", Priority: new(int64),
	}
	ch2 := model.Channel{
		Type: constant.ChannelTypeOpenAI, Name: "sticky-success",
		Key: "sk-OK", Status: common.ChannelStatusEnabled,
		Models: "m", Group: "default", Priority: new(int64),
	}
	ch1.BaseURL = &wrapped.URL
	ch2.BaseURL = &wrapped.URL
	require.NoError(t, database.Create(&ch1).Error)
	require.NoError(t, database.Create(&ch2).Error)
	require.NoError(t, database.Create(&model.Ability{
		Group: "default", Model: "m", ChannelId: ch1.Id, Enabled: true, Priority: int64Ptr(100), Weight: 0,
	}).Error)
	require.NoError(t, database.Create(&model.Ability{
		Group: "default", Model: "m", ChannelId: ch2.Id, Enabled: true, Priority: int64Ptr(10), Weight: 0,
	}).Error)
	// 让选渠道走生产路径（内存缓存）：ch1 优先级更高，先被选中；失败换道后才轮到 ch2。
	model.InitChannelCache()

	cleanup := func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.MemoryCacheEnabled = previousMemoryCache
		common.BatchUpdateEnabled = previousBatchUpdate
		common.LogConsumeEnabled = previousLogConsume
		common.RedisEnabled = previousRedisEnabled
		common.RetryTimes = previousRetryTimes
		gs.PingIntervalEnabled, gs.PingIntervalSeconds = prevPingEnabled, prevPingSeconds
		constant.StreamingTimeout = prevStreamingTimeout
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousModelRatios))
		wrapped.Close()
		upstream.Close()
	}
	return database, cleanup
}

// newStickyStatusRequest 复刻 distributor 中间件已经选好首个渠道之后的上下文。
func newStickyStatusRequest(t *testing.T, db *gorm.DB, requestID string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(common.RequestIdKey, requestID)
	common.SetContextKey(c, constant.ContextKeyUserId, 7)
	common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(c, constant.ContextKeyUserQuota, 100_000_000)
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "m")
	common.SetContextKey(c, constant.ContextKeyTokenId, 55)
	common.SetContextKey(c, constant.ContextKeyTokenKey, "sk-tok")
	common.SetContextKey(c, constant.ContextKeyTokenUnlimited, true)

	var first model.Channel
	require.NoError(t, db.First(&first, "name = ?", "sticky-fail-first").Error)
	require.Nil(t, middleware.SetupContextForSelectedChannel(c, &first, "m"))
	return c, recorder
}

func TestE2EStickyStatusRetryLoopServes200AfterInBandError(t *testing.T) {
	db, cleanup := setupStickyStatusEnv(t)
	defer cleanup()

	c, recorder := newStickyStatusRequest(t, db, "e2e-sticky-1")

	Relay(c, types.RelayFormatOpenAI)
	common.CleanupBodyStorage(c)

	t.Logf("recorder code=%d body=%.200s", recorder.Code, recorder.Body.String())
	require.Equal(t, http.StatusOK, recorder.Code, "retry success must be published as 200, body: %s", recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `"content":"hi"`)
}

func TestE2EStickyStatusAllFailuresYield5xx(t *testing.T) {
	db, cleanup := setupStickyStatusEnv(t)
	defer cleanup()
	// 让第二个渠道也回"200 + 流内错误事件"。
	require.NoError(t, db.Model(&model.Channel{}).Where("name = ?", "sticky-success").
		Update("key", "sk-FIRSTFAIL").Error)
	model.InitChannelCache()

	c, recorder := newStickyStatusRequest(t, db, "e2e-sticky-2")

	Relay(c, types.RelayFormatOpenAI)
	common.CleanupBodyStorage(c)

	t.Logf("recorder code=%d body=%.200s", recorder.Code, recorder.Body.String())
	require.GreaterOrEqual(t, recorder.Code, 500, "terminal failure must be a 5xx, got %d body %s", recorder.Code, recorder.Body.String())
	require.NotEmpty(t, recorder.Body.String(), "terminal failure must still write an error body")
}

func int64Ptr(v int64) *int64 { return &v }
