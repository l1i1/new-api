package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Regression cover for the one fast path inside getChannel that reuses a
// distributor-selected channel instead of re-running selection.
//
// The branch is sound by construction — it returns the channel the distributor
// put in the context and never chooses one itself — but "sound by construction"
// is exactly the kind of claim that silently stops being true. These tests pin
// both halves:
//
//  1. the reuse half returns the context channel verbatim, and
//  2. once that channel is excluded, the fall-through selection is still
//     constrained by the fit requirement: with no official candidate left the
//     2026-10-19 best-effort policy falls through to the ordinary priority pool
//     (never silently widening while an official candidate exists), and the
//     strict policy keeps the old availability cliff.
//
// The scenario is built with the legacy pin OFF and only a policy requirement
// attached, because that is the case where the fit layer is the sole constraint:
// with ContextKeyV4OfficialPin set, the pre-existing official pin would make the
// test pass whether or not the fit wiring works.
func TestGetChannelReuseBranchCannotIntroduceUnconstrainedChannel(t *testing.T) {
	previousDB := model.DB
	previousMemoryCache := common.MemoryCacheEnabled
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousMemoryCache
	})

	// Channel 1 is an aggregator, channel 2 is the official-behaving upstream for
	// deepseek-v4. Both are candidates for the model.
	for _, channel := range []*model.Channel{
		{Id: 1, Name: "aggregator", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI, Group: "default", Models: "deepseek-v4-flash", Key: "sk-1"},
		{Id: 2, Name: "official", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeDeepSeek, Group: "default", Models: "deepseek-v4-flash", Key: "sk-2"},
	} {
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "deepseek-v4-flash", ChannelId: channel.Id, Enabled: true}).Error)
	}
	model.InitChannelCache()

	officialType := model.OfficialFitChannelType("deepseek-v4-flash")
	require.NotZero(t, officialType, "the fixture needs a family with an official channel type")
	require.True(t, model.ChannelIsOfficialFitForModel(2, "deepseek-v4-flash"), "channel 2 must classify as official")
	require.False(t, model.ChannelIsOfficialFitForModel(1, "deepseek-v4-flash"), "channel 1 must classify as an aggregator")

	// newRequest mirrors what the distributor leaves behind after it selected the
	// official channel for this request. Note the legacy pin is deliberately not
	// set: only the policy requirement constrains selection.
	newRequest := func(t *testing.T) (*gin.Context, *relaycommon.RelayInfo, *service.RetryParam) {
		t.Helper()
		gin.SetMode(gin.TestMode)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		common.SetContextKey(c, constant.ContextKeySelectedChannel, &model.Channel{Id: 2})
		common.SetContextKey(c, constant.ContextKeyChannelId, 2)
		common.SetContextKey(c, constant.ContextKeyChannelName, "official")
		common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeDeepSeek)
		info := &relaycommon.RelayInfo{TokenGroup: "default", OriginModelName: "deepseek-v4-flash"}
		retryParam := &service.RetryParam{Ctx: c, TokenGroup: "default", ModelName: "deepseek-v4-flash", RequestPath: c.Request.URL.Path, Retry: common.GetPointer(0)}
		return c, info, retryParam
	}

	t.Run("reuse returns the distributor channel verbatim", func(t *testing.T) {
		c, info, retryParam := newRequest(t)
		attachFitRequirement(c)

		channel, channelErr := getChannel(c, info, retryParam)
		if channelErr != nil {
			t.Fatalf("getChannel failed: type=%T detail=%+v", channelErr, channelErr)
		}
		require.Equal(t, 2, channel.Id, "the reuse branch must return the channel the distributor selected, not choose one")
	})

	t.Run("control: without a requirement the aggregator would be re-selected", func(t *testing.T) {
		c, info, retryParam := newRequest(t)
		// No requirement attached, so nothing constrains the fall-through beyond
		// the (absent) legacy pin. This is the failure the fit layer prevents;
		// asserting it keeps the next test honest.
		retryParam.ExcludeChannel(2)

		channel, channelErr := getChannel(c, info, retryParam)
		if channelErr != nil {
			t.Fatalf("getChannel failed: type=%T detail=%+v", channelErr, channelErr)
		}
		require.Equal(t, 1, channel.Id, "control: the fall-through would otherwise pick the aggregator")
	})

	t.Run("fall-through degrades to priority once the official channel is excluded", func(t *testing.T) {
		c, info, retryParam := newRequest(t)
		attachFitRequirement(c)

		// The policy requirement (default best-effort since 2026-10-19) has no
		// official candidate left after the exclusion, so the ordinary pool is
		// kept: availability wins over the unattainable pin. This is the
		// deliberate degradation, not a silent widening while an official
		// candidate still exists (that case is covered by the reuse subtest).
		retryParam.ExcludeChannel(2)

		channel, channelErr := getChannel(c, info, retryParam)
		if channelErr != nil {
			t.Fatalf("getChannel failed: type=%T detail=%+v", channelErr, channelErr)
		}
		require.NotNil(t, channel)
		require.Equal(t, 1, channel.Id,
			"with the official set empty the best-effort policy hands the request to the ordinary priority pool")
	})

	t.Run("the no-candidate failure is what the retry error-fidelity guard keys on", func(t *testing.T) {
		c, info, retryParam := newRequest(t)
		attachFitRequirement(c)
		// The shape that reaches this branch after the 2026-10-19 best-effort
		// policy: the whole candidate set is gone (every channel was tried and
		// excluded), so the fallback has nothing to keep either. Before that policy
		// a single excluded official channel was enough; production still hits this
		// branch when the fleet has no surviving channel at all.
		retryParam.ExcludeChannel(2)
		retryParam.ExcludeChannel(1)

		channel, channelErr := getChannel(c, info, retryParam)
		require.Nil(t, channel)
		require.NotNil(t, channelErr)
		// The kind must survive the production wrap: without this the guard has to
		// fall back to "no kind means no candidate", which is exactly the
		// misreading that answers an internal routing fault with an older upstream
		// error. Reverting getChannel's %w to %s fails here.
		var selectionErr *service.ChannelSelectionError
		require.ErrorAs(t, channelErr, &selectionErr,
			"the selection kind must reach the caller through getChannel")
		// Final review, defect A: on a pinned/fit path the candidate set was
		// pre-filtered by channel type and capability marks, so the enablement
		// probe cannot prove exhaustion. The kind is therefore an honest internal
		// selection error, NOT temporarily_unavailable - claiming the latter
		// rewrote the real upstream error and set the retry_no_candidate marker.
		require.Equal(t, service.ChannelSelectionUnprovableExhaustion, selectionErr.Kind)

		// The guard must consequently NOT fire: there is no provable candidate
		// exhaustion to key on, so the client keeps the upstream verdict rather
		// than receiving a fabricated "exhausted" answer. applyFitPolicy writes the
		// requirement and the pin together in production; this fixture sets them
		// separately on purpose (see the comment above newRequest), so the pin is
		// added here to state the guard's precondition explicitly.
		common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
		require.False(t, retryFoundNoPinnedCandidate(c, channelErr, retryParam),
			"a pre-filtered empty candidate set is not a provable exhaustion and must not be treated as one")
		// The controller answers with the last real upstream verdict for this
		// kind, so the client never receives a fabricated exhaustion answer and
		// no retry_no_candidate row is written (final review, defect A).

		// Saturation must NOT be lumped in: nothing was tried there, so there may
		// be no upstream error to report at all, and answering with an older one
		// would describe a failure unrelated to this outcome.
		saturated := &service.RetryParam{
			Ctx: c, TokenGroup: "default", ModelName: "deepseek-v4-flash",
			RequestPath: c.Request.URL.Path, Retry: common.GetPointer(0),
		}
		saturated.ExcludeSaturatedChannel(2)
		require.True(t, saturated.HasSaturatedChannel())
		require.False(t, retryFoundNoPinnedCandidate(c, channelErr, saturated),
			"a saturation outcome keeps its own 429 answer")
	})
}

// attachFitRequirement puts a policy opinion in the request context exactly as
// middleware.applyFitPolicy does once the policy is live (not in shadow).
func attachFitRequirement(c *gin.Context) {
	common.SetContextKey(c, constant.ContextKeyFitRequirement, fitpolicy.Requirement{
		Family: "deepseek-v4",
		Model:  "deepseek-v4-flash",
		Marks:  []string{fitpolicy.BehaviorThinkingCounting},
	})
}
