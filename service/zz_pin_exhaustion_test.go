package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Final review, defect A: under a hard official pin (or an active fit policy)
// the candidate set is pre-filtered by channel type and capability marks before
// selection, so the group/model enablement probe cannot prove that no permitted
// candidate exists. classifyChannelSelectionFailure must therefore NOT claim
// temporarily_unavailable - that claim rewrote the real upstream error and set
// the retry_no_candidate marker.
func TestClassifyChannelSelectionFailureRefusesExhaustionUnderOfficialPin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, constant.ContextKeyV4OfficialPin, true)
	param := &RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: "deepseek-v4-flash"}

	err := classifyChannelSelectionFailure(param, "default")
	require.NotNil(t, err)
	assert.NotEqual(t, ChannelSelectionTemporarilyUnavailable, err.Kind,
		"a pinned path must not infer candidate exhaustion from the enablement probe")
	// The distinct kind is what lets the controller answer with the last real
	// upstream verdict instead of a fabricated exhaustion answer.
	assert.Equal(t, ChannelSelectionUnprovableExhaustion, err.Kind)
}

// Final review (round 2): the first revision of this guard swallowed the
// fit/pin saturation answer as well, turning its 429 into a 500. A saturating
// walk is exempt because "every candidate was busy" is provable from the walk
// itself - this test pins that exemption.
func TestClassifyChannelSelectionFailureKeepsSaturationKindUnderOfficialPin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, constant.ContextKeyV4OfficialPin, true)
	param := &RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: "deepseek-v4-flash"}
	// A saturating walk: nothing was tried, and the busy reason is known.
	param.ExcludeSaturatedChannel(1)
	require.True(t, param.HasSaturatedChannel())

	err := classifyChannelSelectionFailure(param, "default")
	require.NotNil(t, err)
	assert.NotEqual(t, ChannelSelectionUnprovableExhaustion, err.Kind,
		"a saturating walk must keep its own answer instead of the unprovable-exhaustion guard")
}
