package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The official-fit pin is one decision that several consumers read in different
// shapes: channel selection, the affinity gate, the affinity recording rule, and
// the retry guard in service.officialFitPinKeepsVerdict. The retry guard is the
// one whose failure is user-visible, so it is the one asserted end to end here.
//
// The tests that predate this file build their context by writing the pin flag
// directly (service/relay_error_test.go, and the affinity tests below), which is
// exactly the precondition this test refuses to fake: it starts from the
// production entry point — the distributor's marking function, with the shipped
// policy installed the way production installs it — and asserts the decision the
// request ends up with. A guard whose input can only be produced by a test
// helper is a guard that is off in production.

// officialFitRouteRequest builds the distributor context for one request body,
// optionally with the user's official-fit Route dimension on for the model.
func officialFitRouteRequest(t *testing.T, body string, routeEnabled bool) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, fitPolicyScopedPath, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if routeEnabled {
		common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{
			OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
				"*": {Route: true},
			}},
		})
	}
	return c
}

// installBalanceKeyword turns on the operator retry keyword the fixtures use.
// "insufficient balance" is the real wording the official DeepSeek channel
// returns when its account runs dry, and it is the case the guard exists for.
func installBalanceKeyword(t *testing.T) {
	t.Helper()
	originalKeywords := operation_setting.AutomaticRetryKeywords
	originalMultiKey := operation_setting.MultiKeyCredentialRetryKeywords
	t.Cleanup(func() {
		operation_setting.AutomaticRetryKeywords = originalKeywords
		operation_setting.MultiKeyCredentialRetryKeywords = originalMultiKey
	})
	operation_setting.AutomaticRetryKeywordsFromString("insufficient balance")
}

// balanceKeywordError is outside the automatic retry ranges, so the operator
// keyword is the only thing that could admit failover.
func balanceKeywordError() *types.NewAPIError {
	return types.NewOpenAIError(
		errors.New("credit insufficient balance: balance=0 required=114"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadRequest,
	)
}

// TestOfficialFitPinKeepsTheUpstreamVerdictThroughTheProductionEntryPoint is the
// end-to-end form of the fork invariant "官方拟合 pin 的请求不被运营重试关键词换渠道".
//
// Why it has to run through the marker: the guard reads a flag, and after the
// compiled-in predicates were retired nothing wrote that flag any more. Every
// existing test of the guard set the flag itself, so the guard could be
// completely inert — an operator retry keyword would re-pin the request, exclude
// the only official channel, and answer the client with a 503 routing error
// instead of the official body — while the whole suite stayed green.
func TestOfficialFitPinKeepsTheUpstreamVerdictThroughTheProductionEntryPoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	requireLiveBuiltinPolicy(t)
	installBalanceKeyword(t)

	// A shape the shipped policy pins: DeepSeek V4 with logprobs has no verified
	// aggregator behaviour.
	pinnedBody := `{"model":"deepseek-v4-flash","logprobs":true,"messages":[{"role":"user","content":"hi"}]}`
	// The same family with explicit thinking-off is a shape the policy leaves
	// alone: the pool serves it faithfully, so it keeps ordinary routing.
	unpinnedBody := `{"model":"deepseek-v4-flash","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`

	t.Run("Route on, pinned shape: the official verdict is kept", func(t *testing.T) {
		c := officialFitRouteRequest(t, pinnedBody, true)
		markV4OfficialPinFromDistributor(c)

		// The decision is asserted first: it is the user-visible half. A retry
		// here re-pins, excludes the only official channel, and answers the
		// client with a 503 routing error instead of the official body.
		decision := service.DecideRelayRetry(c, balanceKeywordError(), 1)
		assert.Equal(t, service.PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}, decision,
			"a retry would exclude the only official channel and replace the official body with a routing error")
		assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin),
			"the production entry point must mark a shape the policy pins")
	})

	t.Run("Route on, unpinned shape: the keyword still fails over", func(t *testing.T) {
		c := officialFitRouteRequest(t, unpinnedBody, true)
		markV4OfficialPinFromDistributor(c)
		require.False(t, common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin),
			"a shape the policy has no opinion about must keep ordinary routing")

		decision := service.DecideRelayRetry(c, balanceKeywordError(), 1)
		assert.Equal(t, service.PolicyDecision{Action: "retry", Reason: "retry_keyword_matched", Source: "global"}, decision,
			"the guard must only intercept shapes that are actually pinned")
	})

	t.Run("Route off: the keyword still fails over", func(t *testing.T) {
		c := officialFitRouteRequest(t, pinnedBody, false)
		markV4OfficialPinFromDistributor(c)
		require.False(t, common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin),
			"the Route dimension is the only switch; sampling parameters alone never pin")

		decision := service.DecideRelayRetry(c, balanceKeywordError(), 1)
		assert.Equal(t, service.PolicyDecision{Action: "retry", Reason: "retry_keyword_matched", Source: "global"}, decision)
	})

	t.Run("kimi-k3 forcing tool choice is pinned the same way", func(t *testing.T) {
		// The marking is family-generic, so the same contract has to hold for a
		// different family's shape rather than only for the DeepSeek fixture.
		c := officialFitRouteRequest(t,
			`{"model":"kimi-k3","tool_choice":"required","tools":[{"type":"function","function":{"name":"f"}}],"messages":[{"role":"user","content":"hi"}]}`, true)
		markV4OfficialPinFromDistributor(c)

		decision := service.DecideRelayRetry(c, balanceKeywordError(), 1)
		assert.Equal(t, service.PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}, decision)
		assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin))
	})
}

// TestFitPolicyPinsOnlyThroughALiveDocument is the S2 counterpart: the shipped
// default must be the live document, and a shadow document must pin nothing.
// Without this, "the guard is installed" and "the guard is installed with a
// dry-run document" are indistinguishable from the outside — both produce no
// pin, and only one of them is a policy edit under review.
func TestFitPolicyPinsOnlyThroughALiveDocument(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fitpolicy.Install(nil)
	t.Cleanup(func() { fitpolicy.Install(nil) })

	pinnedBody := `{"model":"deepseek-v4-flash","logprobs":true,"messages":[{"role":"user","content":"hi"}]}`

	// The reference document carries shadow: true, which since the compiled-in
	// predicates were retired means "decide, record, act on nothing".
	installFitPolicyJSON(t, string(mustBuiltinPolicyJSON(t)))
	shadow := officialFitRouteRequest(t, pinnedBody, true)
	markV4OfficialPinFromDistributor(shadow)
	assert.False(t, common.GetContextKeyBool(shadow, constant.ContextKeyV4OfficialPin),
		"a shadow document decides and acts on nothing, including the pin")

	// The shipped default is the same rules live, and it is what production
	// installs when the option has never been written.
	liveSnapshot, err := fitpolicy.Compile(fitpolicy.DefaultPolicy())
	require.NoError(t, err)
	fitpolicy.Install(liveSnapshot)
	live := officialFitRouteRequest(t, pinnedBody, true)
	markV4OfficialPinFromDistributor(live)
	assert.True(t, common.GetContextKeyBool(live, constant.ContextKeyV4OfficialPin),
		"the shipped default must pin through the production entry point")
}

// TestFitPolicyAlertFiresWhenRouteIsOnButNothingCanAct covers the fail-loud half
// of S2: the three ways the layer can be installed and still pin nothing used to
// be completely silent, so an operator who enabled the Route dimension had no
// signal that the feature was off.
//
// The alert is budgeted, so one request produces one line and a following
// request produces none. Both halves are asserted: an alert per request is a
// flood, and no alert at all is the state this test exists for.
func TestFitPolicyAlertFiresWhenRouteIsOnButNothingCanAct(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fitpolicy.Install(nil)
	t.Cleanup(func() { fitpolicy.Install(nil) })

	pinnedBody := `{"model":"deepseek-v4-flash","logprobs":true,"messages":[{"role":"user","content":"hi"}]}`
	unpinnedBody := `{"model":"deepseek-v4-flash","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`

	originalBudget := fitPolicyInertLog
	t.Cleanup(func() { fitPolicyInertLog = originalBudget })
	// A fresh budget per assertion, so neither this test nor the process budget
	// the other tests share can make the result depend on running order.
	freshBudget := func() { fitPolicyInertLog = common.NewLogBudget(fitPolicyInertLogBurst, time.Hour) }

	var logs bytes.Buffer
	previousWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logs
	t.Cleanup(func() { gin.DefaultErrorWriter = previousWriter })

	mark := func(body string, routeEnabled bool) {
		t.Helper()
		markV4OfficialPinFromDistributor(officialFitRouteRequest(t, body, routeEnabled))
	}

	t.Run("no snapshot at all", func(t *testing.T) {
		logs.Reset()
		freshBudget()
		fitpolicy.Install(nil)
		mark(pinnedBody, true)
		first := logs.String()
		assert.Contains(t, first, "no requirement can be attached")
		assert.Contains(t, first, "reason=no_snapshot")
		assert.Contains(t, first, "official-fit pin is off")

		mark(pinnedBody, true)
		assert.Equal(t, first, logs.String(), "the alert must be rate limited, not written once per request")
	})

	t.Run("document disabled", func(t *testing.T) {
		logs.Reset()
		freshBudget()
		var document map[string]any
		require.NoError(t, json.Unmarshal(mustBuiltinPolicyJSON(t), &document))
		document["enabled"] = false
		encoded, err := json.Marshal(document)
		require.NoError(t, err)
		installFitPolicyJSON(t, string(encoded))
		mark(pinnedBody, true)
		assert.Contains(t, logs.String(), "reason=disabled")
	})

	t.Run("document in shadow", func(t *testing.T) {
		logs.Reset()
		freshBudget()
		installFitPolicyJSON(t, string(mustBuiltinPolicyJSON(t)))
		mark(pinnedBody, true)
		assert.Contains(t, logs.String(), "reason=shadow")
	})

	t.Run("route off is not a fault", func(t *testing.T) {
		logs.Reset()
		freshBudget()
		fitpolicy.Install(nil)
		mark(pinnedBody, false)
		assert.Empty(t, logs.String(), "a user who never enabled Route is not a misconfiguration")
	})

	t.Run("a shape the policy leaves alone is not a fault", func(t *testing.T) {
		logs.Reset()
		freshBudget()
		liveSnapshot, err := fitpolicy.Compile(fitpolicy.DefaultPolicy())
		require.NoError(t, err)
		fitpolicy.Install(liveSnapshot)
		mark(unpinnedBody, true)
		assert.Empty(t, logs.String(), "no opinion about one shape is not the same as nothing being able to act")
	})

	t.Run("a live policy that pins is not a fault", func(t *testing.T) {
		logs.Reset()
		freshBudget()
		liveSnapshot, err := fitpolicy.Compile(fitpolicy.DefaultPolicy())
		require.NoError(t, err)
		fitpolicy.Install(liveSnapshot)
		mark(pinnedBody, true)
		assert.Empty(t, logs.String())
	})
}
