package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestShouldRetryRelayErrorHonorsChannelPinOnChannelError(t *testing.T) {
	err := types.NewError(errors.New("channel failed"), types.ErrorCodeChannelNoAvailableKey)
	for _, test := range []struct {
		name      string
		pin       *dto.ChannelPin
		wantRetry bool
	}{
		{name: "unrestricted channel error", wantRetry: true},
		{
			name: "single attempt pin suppresses channel error retry",
			pin: &dto.ChannelPin{
				ChannelId: 1, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt,
			},
			wantRetry: false,
		},
		{
			name: "origin task pin permits retry on the same channel",
			pin: &dto.ChannelPin{
				ChannelId: 1, Source: dto.PinSourceOriginTask, Rank: dto.PinRankOriginTask, RetryMode: dto.PinRetrySameChannel,
			},
			wantRetry: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if test.pin != nil {
				GetChannelConstraints(c).AddPin(*test.pin)
			}
			assert.Equal(t, test.wantRetry, ShouldRetryRelayError(c, err, 1))
		})
	}
}

func TestProcessChannelErrorMasksDisableReasonAndNotification(t *testing.T) {
	require.NoError(t, i18n.Init())
	previousDB, previousType := model.DB, common.MainDatabaseType()
	previousCache, previousRedis := common.MemoryCacheEnabled, common.RedisEnabled
	previousAutoDisable, previousErrorLog := common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled
	previousNotifyLimit := constant.NotifyLimitCount
	previousClient, previousWorker := httpClient, system_setting.WorkerUrl
	fetch := system_setting.GetFetchSetting()
	previousFetch := *fetch
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.MemoryCacheEnabled, common.RedisEnabled = previousCache, previousRedis
		common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = previousAutoDisable, previousErrorLog
		constant.NotifyLimitCount = previousNotifyLimit
		httpClient, system_setting.WorkerUrl = previousClient, previousWorker
		*fetch = previousFetch
	})
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.ChannelCredential{}, &model.ChannelCredentialRevision{}, &model.Ability{}, &model.User{}))
	model.DB = database
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled, common.RedisEnabled = false, false
	common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = true, false
	constant.NotifyLimitCount = 10
	channel := &model.Channel{Name: "relay-review", Key: "fixture-key", Type: 1, Status: common.ChannelStatusEnabled, Group: "default", Models: "test-model"}
	require.NoError(t, channel.Insert())
	notifications := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		notifications <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	httpClient, system_setting.WorkerUrl = server.Client(), ""
	fetch.EnableSSRFProtection = false
	settings, err := common.Marshal(kitdto.UserSetting{NotifyType: kitdto.NotifyTypeWebhook, WebhookUrl: server.URL})
	require.NoError(t, err)
	root := &model.User{Username: "notification-test-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Setting: string(settings)}
	require.NoError(t, database.Create(root).Error)
	notifyKey := fmt.Sprintf("%d:%s:%s", root.Id, formatNotifyType(channel.Id, common.ChannelStatusAutoDisabled), time.Now().Format("2006010215"))
	notifyLimitStore.Delete(notifyKey)
	t.Cleanup(func() { notifyLimitStore.Delete(notifyKey) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream https://private.example.com/path?token=review-token api_key:review-secret"), types.ErrorCodeChannelInvalidKey, http.StatusUnauthorized)
	ProcessChannelError(c, types.ChannelError{ChannelId: channel.Id, ChannelName: channel.Name, AutoBan: true}, apiErr, nil)
	var notification WebhookPayload
	select {
	case payload := <-notifications:
		require.NoError(t, common.Unmarshal(payload, &notification))
	case <-time.After(5 * time.Second):
		t.Fatal("automatic channel-disable notification was not delivered")
	}
	loaded, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusAutoDisabled, loaded.Status)
	wantReason := "status_code=401, upstream https://***.com/***?token=*** api_key:***"
	assert.Equal(t, wantReason, loaded.GetOtherInfo()["status_reason"])
	assert.Contains(t, notification.Content, wantReason)
	assert.NotContains(t, notification.Content, "review-token")
	assert.NotContains(t, notification.Content, "review-secret")
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
}

func TestDecideRelayRetryReasons(t *testing.T) {
	upstream := func(status int) *types.NewAPIError {
		return types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, status)
	}
	for _, tc := range []struct {
		name    string
		err     *types.NewAPIError
		retries int
		setup   func(*gin.Context)
		want    PolicyDecision
	}{
		{name: "retry status matched", err: upstream(http.StatusTooManyRequests), retries: 1, want: PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}},
		{name: "status outside retry rules", err: upstream(http.StatusRequestTimeout), retries: 1, want: PolicyDecision{Action: "stop", Reason: "status_not_retryable", Source: "global"}},
		{name: "upstream force retry status", err: upstream(http.StatusBadRequest), retries: 1, want: PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}},
		{name: "malformed upstream response retries", err: types.NewOpenAIError(errors.New("invalid upstream JSON"), types.ErrorCodeBadResponseBody, http.StatusInternalServerError), retries: 1, want: PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}},
		{name: "transport failure retries", err: types.NewError(errors.New("net/http: timeout awaiting response headers"), types.ErrorCodeDoRequestFailed, types.ErrOptionWithHideErrMsg("upstream error: do request failed")), retries: 1, want: PolicyDecision{Action: "retry", Reason: "transport_error", Source: "system"}},
		{name: "transport failure respects attempt budget", err: types.NewError(errors.New("net/http: timeout awaiting response headers"), types.ErrorCodeDoRequestFailed, types.ErrOptionWithHideErrMsg("upstream error: do request failed")), retries: 0, want: PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}},
		{name: "local force retry status stays local", err: types.NewErrorWithStatusCode(errors.New("invalid request"), types.ErrorCodeInvalidRequest, http.StatusBadRequest), retries: 1, want: PolicyDecision{Action: "stop", Reason: "status_not_retryable", Source: "global"}},
		{name: "attempt budget exhausted", err: upstream(http.StatusTooManyRequests), retries: 0, want: PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}},
		{name: "always skipped status", err: upstream(http.StatusGatewayTimeout), retries: 1, want: PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}},
		{name: "success status never retries", err: upstream(http.StatusOK), retries: 1, want: PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}},
		{name: "skip retry error", err: types.NewErrorWithStatusCode(errors.New("local"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry()), retries: 1, want: PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}},
		{name: "skip retry invalid status", err: types.NewErrorWithStatusCode(errors.New("local"), types.ErrorCodeInvalidRequest, 0, types.ErrOptionWithSkipRetry()), retries: 1, want: PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}},
		{name: "channel error retries without budget", err: types.NewError(errors.New("no key"), types.ErrorCodeChannelNoAvailableKey), retries: 0, want: PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}},
		{name: "single attempt pin", err: upstream(http.StatusTooManyRequests), retries: 1, setup: func(c *gin.Context) {
			GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: 1, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt})
		}, want: PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}},
		{name: "strict session", err: upstream(http.StatusTooManyRequests), retries: 1, setup: func(c *gin.Context) {
			c.Set(ginKeyChannelAffinitySkipRetry, true)
			RequestPolicy(c).SessionModeSource = "global"
		}, want: PolicyDecision{Action: "stop", Reason: "strict_session", Source: "global"}},
		{name: "nil error", retries: 1, want: PolicyDecision{Action: "stop", Reason: "request_completed", Source: "system"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if tc.setup != nil {
				tc.setup(c)
			}
			decision := DecideRelayRetry(c, tc.err, tc.retries)
			assert.Equal(t, tc.want, decision)
			assert.Equal(t, tc.want.Action == "retry", ShouldRetryRelayError(c, tc.err, tc.retries))
		})
	}
}

// TestDecideRelayRetryTransportBudgetExhausted guards the wall-clock bound on a
// chain of transport failures: eight retries against silent upstreams must not
// become attempt-count × first-byte-timeout of waiting for the client.
func TestDecideRelayRetryTransportBudgetExhausted(t *testing.T) {
	previous := common.RelayTransportRetryBudgetSeconds
	common.RelayTransportRetryBudgetSeconds = 600
	t.Cleanup(func() { common.RelayTransportRetryBudgetSeconds = previous })

	transportErr := types.NewError(errors.New("net/http: timeout awaiting response headers"), types.ErrorCodeDoRequestFailed, types.ErrOptionWithHideErrMsg("upstream error: do request failed"))

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	RequestPolicy(c).StartedAt = time.Now().Add(-11 * time.Minute)
	assert.Equal(t, PolicyDecision{Action: "stop", Reason: "transport_retry_budget_exhausted", Source: "system"}, DecideRelayRetry(c, transportErr, 3))

	underBudget, _ := gin.CreateTestContext(httptest.NewRecorder())
	RequestPolicy(underBudget).StartedAt = time.Now()
	assert.Equal(t, PolicyDecision{Action: "retry", Reason: "transport_error", Source: "system"}, DecideRelayRetry(underBudget, transportErr, 3))
}

func TestRequestPolicyEventsReachLogAdminInfo(t *testing.T) {
	previousAutoDisable := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previousAutoDisable })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("auto_ban", true)
	c.Set("channel_id", 7)
	state := RequestPolicy(c)
	state.BeginAttempt(&model.Channel{Id: 7}, "default")
	apiErr := types.NewOpenAIError(errors.New("invalid credential"), types.ErrorCodeBadResponseStatusCode, http.StatusUnauthorized)
	RecordPolicyFailure(c, 7, apiErr, DecideRelayRetry(c, apiErr, 0))

	failed := model.NewLogOther()
	AppendRelayLogAdminInfo(c, nil, failed)
	events, ok := failed.Snapshot()["admin_info"].(map[string]any)["request_policy"].([]PolicyEvent)
	require.True(t, ok, "a failed relay exposes its decision events to administrators")
	require.Len(t, events, 3)
	assert.Equal(t, PolicyDecision{Action: "attempt", Reason: "channel_selected", Source: "routing"}, events[0].Decision)
	assert.Equal(t, "default", events[0].Group)
	assert.Equal(t, PolicyDecision{Action: "failure", Reason: "upstream_failure", Source: "upstream"}, events[1].Decision)
	assert.Equal(t, http.StatusUnauthorized, events[1].Status)
	assert.Equal(t, PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}, events[2].Decision)
	assert.Equal(t, "channel_disable_requested", events[2].Health, "the health entry follows the automatic disable rules")
	common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, true)
	RecordPolicyFailure(c, 7, apiErr, DecideRelayRetry(c, apiErr, 0))
	assert.Equal(t, "key_disable_requested", state.Events()[4].Health)

	state.BeginAttempt(&model.Channel{Id: 8}, "default")
	c.Set("channel_id", 8)
	MarkRequestPolicySuccess(c, nil)
	MarkRequestPolicySuccess(c, nil)
	succeeded := model.NewLogOther()
	AppendRelayLogAdminInfo(c, nil, succeeded)
	events, ok = succeeded.Snapshot()["admin_info"].(map[string]any)["request_policy"].([]PolicyEvent)
	require.True(t, ok, "a successful relay exposes its decision events to administrators")
	require.Len(t, events, 7, "the outcome is recorded once")
	assert.Equal(t, PolicyDecision{Action: "success", Reason: "request_completed", Source: "upstream"}, events[6].Decision)
	assert.Equal(t, 8, events[6].ChannelID)
	assert.Equal(t, 2, events[6].Attempt)
	assert.True(t, state.Successful)

	untouched, _ := gin.CreateTestContext(httptest.NewRecorder())
	other := model.NewLogOther()
	AppendRelayLogAdminInfo(untouched, nil, other)
	assert.NotContains(t, other.Snapshot()["admin_info"], "request_policy", "requests without decisions do not carry an empty record")
}

// TestOfficialFitPinKeepsUpstreamVerdict pins the rule that an operator retry
// keyword must not fail an official-fit request over to another channel: the pin
// admits only official-behaving channels and a family may have just one, so the
// retry would re-pin, exclude it, and replace the upstream verdict with a routing
// error. Credential rotation is the one exception, and only while the pinned
// channel really has another enabled key to hand out.
func TestOfficialFitPinKeepsUpstreamVerdict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// The keyword branch now honours RetryErrorFidelityMode like the status-code
	// branch: the guard is only enforced in "on" mode. Shadow/off (the live
	// settings) record the verdict but still fail over, which is what
	// TestRetryErrorFidelityGuard pins.
	withRetryErrorFidelityMode(t, operation_setting.RetryErrorFidelityOn)
	origKeywords := operation_setting.AutomaticRetryKeywords
	origMultiKeyKeywords := operation_setting.MultiKeyCredentialRetryKeywords
	t.Cleanup(func() {
		operation_setting.AutomaticRetryKeywords = origKeywords
		operation_setting.MultiKeyCredentialRetryKeywords = origMultiKeyKeywords
	})
	operation_setting.AutomaticRetryKeywordsFromString("insufficient balance")

	// A 400 is outside the automatic retry ranges, so the keyword is the only
	// thing that could admit failover.
	balanceErr := types.NewOpenAIError(
		errors.New("credit insufficient balance: balance=0 required=114"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadRequest,
	)

	t.Run("unpinned request still fails over on the keyword", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		decision := DecideRelayRetry(c, balanceErr, 1)
		assert.Equal(t, PolicyDecision{Action: "retry", Reason: "retry_keyword_matched", Source: "global"}, decision)
	})

	t.Run("official-pinned single-channel request keeps the upstream verdict", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
		decision := DecideRelayRetry(c, balanceErr, 1)
		assert.Equal(t, PolicyDecision{Action: "stop", Reason: "pinned_official_verdict", Source: "channel_constraint"}, decision)
	})

	// A channel reported as multi-key with a single credential must NOT be treated
	// as rotatable: letting it retry would spend the attempt, exhaust the only key,
	// and land on the same routing error the guard exists to prevent. The key that
	// just failed is already in the tried set when this decision runs
	// (MarkCurrentMultiKeyTried executes before the attempt).
	t.Run("official-pinned one-key channel is not treated as rotatable", func(t *testing.T) {
		c, channelID := newPinnedMultiKeyContext(t, []string{"only-key"}, nil, []string{"only-key"})
		decision := DecideRelayRetry(c, balanceErr, 1)
		assert.Equal(t, PolicyDecision{Action: "stop", Reason: "pinned_official_verdict", Source: "channel_constraint"}, decision,
			"a one-key channel has nowhere to rotate, so the official verdict is kept")
		require.NotZero(t, channelID)
	})

	t.Run("official-pinned channel with a spare key rotates", func(t *testing.T) {
		c, _ := newPinnedMultiKeyContext(t, []string{"drained-key", "spare-key"}, nil, []string{"drained-key"})
		decision := DecideRelayRetry(c, balanceErr, 1)
		assert.Equal(t, PolicyDecision{Action: "retry", Reason: "retry_keyword_matched", Source: "global"}, decision,
			"another key of the same official channel keeps the fit contract and still serves the request")
	})

	t.Run("official-pinned channel whose spare key is disabled keeps the verdict", func(t *testing.T) {
		c, _ := newPinnedMultiKeyContext(t, []string{"drained-key", "disabled-key"}, map[int]int{1: common.ChannelStatusManuallyDisabled}, []string{"drained-key"})
		decision := DecideRelayRetry(c, balanceErr, 1)
		assert.Equal(t, PolicyDecision{Action: "stop", Reason: "pinned_official_verdict", Source: "channel_constraint"}, decision,
			"a disabled credential cannot serve the request, so rotation would not help")
	})

	t.Run("official DeepSeek out-of-balance keeps rotating keys", func(t *testing.T) {
		// The real wording and status the official DeepSeek channel returns when
		// its account runs dry (`status_code=402, Insufficient Balance`, observed
		// on the live channel). It matches the operator balance keyword
		// case-insensitively, and the live channel holds two keys, so the pinned
		// request must still rotate to the next key of the SAME official endpoint.
		pinned := types.NewOpenAIError(
			errors.New("Insufficient Balance"),
			types.ErrorCodeBadResponseStatusCode,
			http.StatusPaymentRequired,
		)
		assert.True(t, ShouldRotateMultiKeyCredentialOn(pinned.StatusCode, pinned.Error()))
		c, _ := newPinnedMultiKeyContext(t, []string{"key-a", "key-b"}, nil, []string{"key-a"})
		assert.Equal(t, "retry", DecideRelayRetry(c, pinned, 1).Action,
			"a drained official credential is recovered by rotating keys on the same official channel")
	})

	t.Run("the guard never changes a decision the keyword list did not make", func(t *testing.T) {
		// A wording outside the keyword list must decide exactly as it would
		// without the pin. Asserting the two against each other keeps this test
		// independent of the compiled status-code default (which still lists
		// 400; production excludes it through the operational option).
		otherErr := types.NewOpenAIError(
			errors.New("max_tokens must be greater than 2"),
			types.ErrorCodeBadResponseStatusCode,
			http.StatusInternalServerError,
		)
		plain, _ := gin.CreateTestContext(httptest.NewRecorder())
		pinned, _ := gin.CreateTestContext(httptest.NewRecorder())
		common.SetContextKey(pinned, constant.ContextKeyV4OfficialPin, true)
		assert.Equal(t, DecideRelayRetry(plain, otherErr, 1), DecideRelayRetry(pinned, otherErr, 1),
			"the official-fit guard only intercepts keyword matches")
	})
}

// newPinnedMultiKeyContext builds a context whose request is marked official-fit
// pinned and whose selected channel is a real cached multi-key channel, so the
// credential-availability check reads the same state the relay would.
// triedKeys names the credentials this request already used.
func newPinnedMultiKeyContext(t *testing.T, keys []string, statusList map[int]int, triedKeys []string) (*gin.Context, int) {
	t.Helper()

	previousDB := model.DB
	previousCache := common.MemoryCacheEnabled
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:pinnedmq_%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousCache
		if previousCache && previousDB != nil && previousDB.Migrator().HasTable(&model.Channel{}) {
			model.InitChannelCache()
		}
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	priority := int64(0)
	weight := uint(100)
	channel := &model.Channel{
		Id:       901,
		Type:     constant.ChannelTypeDeepSeek,
		Key:      strings.Join(keys, "\n"),
		Status:   common.ChannelStatusEnabled,
		Name:     "pinned-multi-key",
		Weight:   &weight,
		Models:   "deepseek-v4-flash",
		Group:    "default",
		Priority: &priority,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:         true,
			MultiKeySize:       len(keys),
			MultiKeyStatusList: statusList,
			MultiKeyMode:       constant.MultiKeyModeAffinity,
		},
	}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group: "default", Model: "deepseek-v4-flash", ChannelId: channel.Id, Enabled: true, Priority: &priority, Weight: weight,
	}).Error)
	model.InitChannelCache()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
	common.SetContextKey(c, constant.ContextKeyChannelId, channel.Id)
	common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, true)
	if len(triedKeys) > 0 {
		tried := map[int]map[string]struct{}{channel.Id: {}}
		for _, key := range triedKeys {
			tried[channel.Id][model.ChannelCredentialFingerprint(key)] = struct{}{}
		}
		common.SetContextKey(c, constant.ContextKeyChannelMultiKeyTried, tried)
	}
	return c, channel.Id
}

// TestOfficialFitPinCredentialExhaustionFailover pins down the 2026-10-10 policy
// change: an official-fit pin no longer keeps an upstream verdict that is not an
// answer at all. Live case this comes from: channel DEF_ysis returned
//
//	status_code=503, auth_unavailable: no auth available have reached your weekly
//	usage limit, upgrade for higher limits … or add usage credits
//
// which matched the operator keyword `credits`, so the pin guard stopped the
// request and 110 streaming requests in 20 minutes reached the paying customer
// as a 503 instead of failing over. Credential rotation still wins when the
// pinned channel really has a spare key (covered by
// TestOfficialFitPinKeepsUpstreamVerdict), so this only changes the case where
// the pin would otherwise be fatal.
func TestOfficialFitPinCredentialExhaustionFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withRetryErrorFidelityMode(t, operation_setting.RetryErrorFidelityOn)
	origKeywords := operation_setting.AutomaticRetryKeywords
	t.Cleanup(func() { operation_setting.AutomaticRetryKeywords = origKeywords })
	operation_setting.AutomaticRetryKeywordsFromString("credits\ninsufficient balance\n额度")

	// 额度 is on the live operator list (line 25 of the production
	// AutomaticRetryKeywords) and is what makes the platform's own billing text
	// reach this decision path at all.
	credentialExhausted := types.NewOpenAIError(
		errors.New("auth_unavailable: no auth available have reached your weekly usage limit, upgrade for higher limits: https://example.com or add usage credits: https://example.com"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusServiceUnavailable,
	)

	t.Run("unpinned request fails over on the keyword", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		decision := DecideRelayRetry(c, credentialExhausted, 1)
		assert.Equal(t, PolicyDecision{Action: "retry", Reason: "retry_keyword_matched", Source: "global"}, decision)
	})

	t.Run("official-pinned channel out of credentials fails over", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
		decision := DecideRelayRetry(c, credentialExhausted, 1)
		assert.Equal(t, PolicyDecision{Action: "retry", Reason: "retry_keyword_matched", Source: "global"}, decision,
			"a channel with no usable credential carries no official verdict, so the pin must not turn the outage into the customer's answer")
	})

	t.Run("official-pinned channel out of balance still keeps the verdict", func(t *testing.T) {
		// A 400 (the shape the existing suite covers) is outside the availability
		// escape hatch's statuses: only credential/quota statuses qualify, so the
		// historical contract is untouched where it was already tested.
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
		balanceErr := types.NewOpenAIError(
			errors.New("credit insufficient balance: balance=0 required=114"),
			types.ErrorCodeBadResponseStatusCode,
			http.StatusBadRequest,
		)
		decision := DecideRelayRetry(c, balanceErr, 1)
		assert.Equal(t, PolicyDecision{Action: "stop", Reason: "pinned_official_verdict", Source: "channel_constraint"}, decision)
	})

	t.Run("official-pinned platform billing 403 still keeps the verdict", func(t *testing.T) {
		// The operator keyword list contains 额度/余额, which also matches this
		// platform's own billing text (用户额度不足, a 403 raised by
		// billing_session.go before any channel is tried). That error is a
		// locally generated NewAPIError, not an upstream verdict about the
		// channel's credentials, so the escape hatch must not fire: failing over
		// cannot serve a request the platform itself refused, and on a family
		// with a single official channel it would only replace the platform's
		// 403 with a routing error.
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
		billingErr := types.NewErrorWithStatusCode(
			errors.New("用户额度不足, 剩余额度: ¥0"),
			types.ErrorCodeInsufficientUserQuota,
			http.StatusForbidden,
			types.ErrOptionWithSkipRetry(),
		)
		decision := DecideRelayRetry(c, billingErr, 1)
		assert.Equal(t, PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}, decision,
			"the platform's own refusal carries its skip-retry marker and stops before the keyword list")
	})
}

// TestDecideRelayRetryStopsWhenRequestContextDone pins the rule that a dead
// request context ends the walk: no retry can deliver an answer to a request
// that is already over, and the extra attempts would fail instantly, spend
// upstream quota nobody waits for, and count as channel failures in the
// per-channel stats.
func TestDecideRelayRetryStopsWhenRequestContextDone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// A channel error is retryable with no configuration involved, which keeps
	// the baseline independent of the operator keyword and status lists.
	apiErr := types.NewOpenAIError(errors.New("upstream unavailable"), types.ErrorCodeChannelResponseTimeExceeded, http.StatusBadGateway)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	assert.Equal(t, PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}, DecideRelayRetry(c, apiErr, 8), "baseline: a live request retries a channel error")

	// Client disconnect: context.Canceled.
	cancel()
	assert.Equal(t, PolicyDecision{Action: "stop", Reason: "request_context_done", Source: "system"}, DecideRelayRetry(c, apiErr, 8), "a canceled request stops the walk")

	// DeadlineExceeded is the same outcome: the request is over either way,
	// whatever canceled the context.
	expired, _ := gin.CreateTestContext(httptest.NewRecorder())
	dctx, dcancel := context.WithTimeout(context.Background(), time.Nanosecond)
	<-dctx.Done()
	dcancel()
	expired.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(dctx)
	assert.Equal(t, PolicyDecision{Action: "stop", Reason: "request_context_done", Source: "system"}, DecideRelayRetry(expired, apiErr, 8))

	// A context without a request stays nil-safe and never looks done.
	bare, _ := gin.CreateTestContext(httptest.NewRecorder())
	assert.Equal(t, PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}, DecideRelayRetry(bare, apiErr, 8))
}

// TestRetryErrorFidelityGuard covers the decision the 2026-10-12 production
// incident needed and did not get. The official GLM channel answered 400
// ("角色信息不正确", Zhipu error 1214) for a fit-pinned request; the operator's
// failover list contains 400, so the retry ran, its first act excluded the
// family's only official channel, the re-selection found nothing, and the client
// was answered get_channel_failed (500) while the error log held the upstream
// 400. The keyword branch of DecideRelayRetry already stops a pinned request
// from failing over; the status-code branch never had that guard.
//
// These cases fix both halves of the new guard's contract: the verdicts it
// stops on, and everything it must leave exactly as it was.
func TestRetryErrorFidelityGuard(t *testing.T) {
	// The premise that makes a pinned verdict retryable at all. The compiled
	// failover default is {100-199, 300-407, 409-503, 505-523, 525-599}, which
	// contains 400. If that ever changes, the verdict-status half of the guard
	// stops being reachable and this test should say so rather than pass
	// silently for a different reason.
	require.True(t, operation_setting.ShouldRetryByStatusCode(http.StatusBadRequest),
		"premise: 400 is on the failover list, which is what makes a pinned verdict retryable")

	// The live wording and status the official GLM channel produced for the
	// reported request. It is a verdict about the request body — every channel
	// reads the same body, so failing over cannot change the answer.
	verdict := types.NewOpenAIError(errors.New("角色信息不正确"), types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest)

	pinned := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
		return c
	}

	for _, test := range []struct {
		name    string
		mode    string
		pin     bool
		err     *types.NewAPIError
		want    PolicyDecision
		comment string
	}{
		{
			name: "on: a pinned request keeps the official verdict instead of failing over",
			mode: operation_setting.RetryErrorFidelityOn, pin: true, err: verdict,
			want: PolicyDecision{Action: "stop", Reason: "pinned_official_verdict", Source: "channel_constraint"},
		},
		{
			name: "shadow: live traffic decides exactly as before",
			mode: operation_setting.RetryErrorFidelityShadow, pin: true, err: verdict,
			want: PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"},
		},
		{
			name: "off: live traffic decides exactly as before",
			mode: operation_setting.RetryErrorFidelityOff, pin: true, err: verdict,
			want: PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"},
		},
		{
			name: "on: an unpinned request still fails over on the same verdict",
			mode: operation_setting.RetryErrorFidelityOn, pin: false, err: verdict,
			want:    PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"},
			comment: "the pin is what makes failover pointless; without it the aggregator verdict is still worth another channel",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			withRetryErrorFidelityMode(t, test.mode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if test.pin {
				common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
			}
			assert.Equal(t, test.want, DecideRelayRetry(c, test.err, 1), test.comment)
		})
	}

	// A channel outage is evidence about the channel, not an answer about the
	// request: failover can still serve it, and the operator's capacity policy
	// (「必要时可牺牲拟合换成功率」) keeps that path. Asserting only the action
	// leaves the reason to whichever retry rule matches, which is the point —
	// this guard must not be the thing that decides them.
	t.Run("on: an outage is not a verdict, so failover stays available", func(t *testing.T) {
		withRetryErrorFidelityMode(t, operation_setting.RetryErrorFidelityOn)
		for _, outage := range []struct {
			code int
			text string
		}{
			{http.StatusServiceUnavailable, "temporarily_unavailable"},
			{http.StatusBadGateway, "服务繁忙，请稍后重试。"},
			{http.StatusTooManyRequests, "rate limit exceeded"},
		} {
			err := types.NewOpenAIError(errors.New(outage.text), types.ErrorCodeBadResponseStatusCode, outage.code)
			decision := DecideRelayRetry(pinned(), err, 1)
			assert.Equal(t, "retry", decision.Action,
				"status %d is a channel failure, not a verdict about the request (decision=%+v)", outage.code, decision)
		}

		// The classification is what excludes an outage, so pin it directly. 408
		// is excluded here as well as by the failover list, which deliberately
		// omits it (the legacy "timeout is not retryable" rule); keeping the
		// exclusion local means an operator who adds 408 back to that list still
		// does not get a verdict kept on it.
		assert.False(t, operation_setting.IsVerdictStatusCode(http.StatusRequestTimeout))
		assert.False(t, operation_setting.IsVerdictStatusCode(http.StatusTooManyRequests))
		assert.False(t, operation_setting.IsVerdictStatusCode(http.StatusInternalServerError))
		assert.False(t, operation_setting.IsVerdictStatusCode(http.StatusOK))
		assert.True(t, operation_setting.IsVerdictStatusCode(http.StatusBadRequest))
		assert.True(t, operation_setting.IsVerdictStatusCode(http.StatusForbidden))

		// A 408 decided by the platform's own rules must not be attributed to this
		// guard: were the decision the guard's, the reason would say so.
		timeout := types.NewOpenAIError(errors.New("upstream timeout"), types.ErrorCodeBadResponseStatusCode, http.StatusRequestTimeout)
		assert.NotEqual(t, "pinned_official_verdict", DecideRelayRetry(pinned(), timeout, 1).Reason)
	})

	t.Run("on: the operator credential escape hatch is not overridden", func(t *testing.T) {
		withRetryErrorFidelityMode(t, operation_setting.RetryErrorFidelityOn)
		// 2026-10-10 operator policy: a credential/quota failure is not an
		// official verdict — there is no answer to protect — so a pinned request
		// may still fail over.
		drained := types.NewOpenAIError(errors.New("余额不足"), types.ErrorCodeBadResponseStatusCode, http.StatusForbidden)
		decision := DecideRelayRetry(pinned(), drained, 1)
		assert.NotEqual(t, "pinned_official_verdict", decision.Reason,
			"a drained credential must still fail over; the guard only keeps real verdicts")
		assert.Equal(t, "retry", decision.Action)
	})
}

// withRetryErrorFidelityMode pins the guard mode for one subtest and restores the
// process-wide value afterwards, so a case cannot leak into its neighbours or
// into another test in this package. The mode has no exported variable on
// purpose (it is read on the relay failure path), so the snapshot goes through
// the getter and the restore through the validated setter.
func withRetryErrorFidelityMode(t *testing.T, mode string) {
	t.Helper()
	previous := operation_setting.RetryErrorFidelityModeOrDefault()
	t.Cleanup(func() { require.NoError(t, operation_setting.SetRetryErrorFidelityMode(previous)) })
	require.NoError(t, operation_setting.SetRetryErrorFidelityMode(mode))
}

// Availability first: a channel-pool exhaustion (503 "all accounts are
// temporarily unavailable") matched the operator keyword list and used to keep
// the official-fit pin, so the buyer saw 503 even though other qualifying
// channels could serve the request (measured 2026-10-21: 194 failed requests).
// The guard may only preserve a *verdict* status, and pool exhaustion is also a
// credential-level condition the escape hatch recognizes.
func TestOfficialFitPinDoesNotBlockFailoverOnPoolExhaustion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := operation_setting.AutomaticRetryKeywords
	t.Cleanup(func() { operation_setting.AutomaticRetryKeywords = original })
	operation_setting.AutomaticRetryKeywordsFromString("temporarily unavailable")

	poolExhausted := types.NewOpenAIError(
		errors.New("all accounts are temporarily unavailable, please retry later"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusServiceUnavailable,
	)
	require.True(t, isCredentialExhaustedFailure(poolExhausted), "the pool-exhaustion wording is a credential-level failure")

	pinned, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(pinned, constant.ContextKeyV4OfficialPin, true)
	assert.Equal(t, PolicyDecision{Action: "retry", Reason: "retry_keyword_matched", Source: "global"},
		DecideRelayRetry(pinned, poolExhausted, 1),
		"a 503 pool outage must fail over instead of being kept as an official verdict")

	// A verdict-shaped status still keeps the official answer when the guard is
	// enforced: that is what the guard is for (the upstream answered about the
	// request itself).
	withRetryErrorFidelityMode(t, operation_setting.RetryErrorFidelityOn)
	verdict := types.NewOpenAIError(
		errors.New("temporarily unavailable"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadRequest,
	)
	kept, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(kept, constant.ContextKeyV4OfficialPin, true)
	assert.Equal(t, PolicyDecision{Action: "stop", Reason: "pinned_official_verdict", Source: "channel_constraint"},
		DecideRelayRetry(kept, verdict, 1),
		"a 4xx verdict must still keep the pinned official answer in on mode")
}

// A capability gap ("this channel cannot serve this shape") is not a verdict
// about the request; in `on` mode the keyword branch must still fail over so a
// capable channel can answer.
func TestOfficialFitPinDoesNotKeepVerdictOnCapabilityGap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousMode := operation_setting.RetryErrorFidelityModeOrDefault()
	previousKeywords := operation_setting.AutomaticRetryKeywords
	t.Cleanup(func() {
		_ = operation_setting.SetRetryErrorFidelityMode(previousMode)
		operation_setting.AutomaticRetryKeywords = previousKeywords
	})
	require.NoError(t, operation_setting.SetRetryErrorFidelityMode(operation_setting.RetryErrorFidelityOn))
	operation_setting.AutomaticRetryKeywordsFromString("endpoint not supported\ncapability not supported")

	pinned, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(pinned, constant.ContextKeyV4OfficialPin, true)

	convertGap := types.NewErrorWithStatusCode(
		errors.New("codex channel: endpoint not supported"),
		types.ErrorCodeConvertRequestFailed,
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
	)
	assert.Equal(t, PolicyDecision{Action: "retry", Reason: "retry_keyword_matched", Source: "global"},
		DecideRelayRetry(pinned, convertGap, 1),
		"a conversion capability gap must fail over even in on mode")

	featureGap := types.NewErrorWithStatusCode(
		errors.New("capability not supported"),
		types.ErrorCodeChannelUnsupportedFeature,
		http.StatusBadRequest,
	)
	assert.Equal(t, "retry", DecideRelayRetry(pinned, featureGap, 1).Action,
		"an unsupported-feature channel error must not keep the pin")
}

// TestEngineOverloadFailureIsChannelWide pins the 2026-10-29 storm fix: an
// upstream engine that is "currently overloaded" refuses every credential of the
// channel identically, so the retry must exclude the channel instead of rotating
// its keys, and a fidelity pin must step aside immediately.
func TestEngineOverloadFailureIsChannelWide(t *testing.T) {
	for _, msg := range []string{
		"status_code=429, The engine is currently overloaded, please try again later",
		"service is temporarily unavailable",
		"server overloaded",
	} {
		require.True(t, IsEngineOverloadFailure(msg), msg)
	}
	for _, msg := range []string{
		"Your account org-x / proj-y <ak-z> request reached organization TPM rate limit, current: 1, limit: 2",
		"insufficient balance",
		"",
	} {
		require.False(t, IsEngineOverloadFailure(msg), msg)
	}
}

// An engine-wide overload relaxes a pinned request even while sibling credentials
// are still "available": they will refuse identically, so keeping the pin only
// burns the attempt budget.
func TestEngineOverloadRelaxesOfficialPin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
	err := types.NewOpenAIError(errors.New("The engine is currently overloaded, please try again later"),
		types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	require.True(t, shouldRelaxOfficialPinForAvailability(c, err))
}

// An organization concurrency cap is channel-wide: the account refuses every
// credential identically, so rotating sibling keys only walks the retry into the
// same wall. Operator policy 2026-10-30 (「可用性优先兜底」): treat it like an
// engine overload — exclude the channel, release the pin at once, let the
// ordinary pool serve.
func TestOrganizationConcurrencyLimitIsChannelWide(t *testing.T) {
	msg := "status_code=429, Your account org-37af7c6850d74943a694e6c173b7c73d<ak-fd> request reached max organization concurrency: 50, please try again after 1 seconds"
	require.True(t, IsEngineOverloadFailure(msg))
	// An org-scoped TPM rate limit stays credential-scoped on purpose: a sibling
	// credential of another organization can still succeed there.
	require.False(t, IsEngineOverloadFailure(
		"Your account org-x / proj-y <ak-z> request reached organization TPM rate limit, current: 1, limit: 2"))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
	err := types.NewOpenAIError(errors.New(msg), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	require.True(t, shouldRelaxOfficialPinForAvailability(c, err))
}

// TestDecideRelayRetryTransportBudgetRuntimeOverride pins the operator knob: the
// DB option (general_setting.transport_retry_budget_seconds) must widen the
// wall-clock bound at runtime, so a 420s first-byte budget can spend the retry
// quota on more than one channel without a container restart.
func TestDecideRelayRetryTransportBudgetRuntimeOverride(t *testing.T) {
	previous := common.RelayTransportRetryBudgetSeconds
	common.RelayTransportRetryBudgetSeconds = 600
	t.Cleanup(func() { common.RelayTransportRetryBudgetSeconds = previous })

	setting := operation_setting.GetGeneralSetting()
	previousOverride := setting.TransportRetryBudgetSeconds
	setting.TransportRetryBudgetSeconds = 3000
	t.Cleanup(func() { setting.TransportRetryBudgetSeconds = previousOverride })

	transportErr := types.NewError(errors.New("net/http: timeout awaiting response headers"), types.ErrorCodeDoRequestFailed, types.ErrOptionWithHideErrMsg("upstream error: do request failed"))

	// 11 minutes exceeds the boot-time 600s budget but fits the 3000s override.
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	RequestPolicy(c).StartedAt = time.Now().Add(-11 * time.Minute)
	assert.Equal(t, PolicyDecision{Action: "retry", Reason: "transport_error", Source: "system"}, DecideRelayRetry(c, transportErr, 3))

	// Beyond the override the budget still stops the chain.
	over, _ := gin.CreateTestContext(httptest.NewRecorder())
	RequestPolicy(over).StartedAt = time.Now().Add(-51 * time.Minute)
	assert.Equal(t, PolicyDecision{Action: "stop", Reason: "transport_retry_budget_exhausted", Source: "system"}, DecideRelayRetry(over, transportErr, 3))
}
