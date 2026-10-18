package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Final review, defect A: under a hard official pin (or an active fit policy)
// the candidate set is pre-filtered by channel type and capability marks before
// selection, so the group/model enablement probe cannot prove that no permitted
// candidate exists. classifyChannelSelectionFailure must therefore NOT claim
// temporarily_unavailable - that claim rewrote the real upstream error and set
// the retry_no_candidate marker.
func TestClassifyChannelSelectionFailureRefusesExhaustionUnderOfficialPin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupChannelSelectionFailureDB(t)
	seedChannelSelectionFailureModel(t, "default", "deepseek-v4-flash", constant.ChannelTypeDeepSeek)
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
	setupChannelSelectionFailureDB(t)
	seedChannelSelectionFailureModel(t, "default", "deepseek-v4-flash", constant.ChannelTypeDeepSeek)
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

// The pin prefilter can only have dropped candidates for a model the routing
// group actually publishes. An unknown model id must not inherit the internal
// "cannot prove candidate exhaustion" wording: it is a plain unknown model and
// the platform's model_not_configured answer (400) is both honest and identical
// to the unpinned behaviour. This is what lets the Validate dimension drop its
// model-id checks entirely.
func TestClassifyChannelSelectionFailureUnknownModelUnderPinIsNotConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupChannelSelectionFailureDB(t)
	seedChannelSelectionFailureModel(t, "default", "deepseek-v4-flash", constant.ChannelTypeDeepSeek)
	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, constant.ContextKeyV4OfficialPin, true)
	param := &RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: "deepseek-v4-notexist"}

	err := classifyChannelSelectionFailure(param, "default")
	require.NotNil(t, err)
	assert.Equal(t, ChannelSelectionModelNotConfigured, err.Kind)
}

func setupChannelSelectionFailureDB(t *testing.T) {
	t.Helper()
	originalDB := model.DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	t.Cleanup(func() { model.DB = originalDB })
}

func seedChannelSelectionFailureModel(t *testing.T, group, modelName string, channelType int) {
	t.Helper()
	channel := &model.Channel{
		Id:          1,
		Type:        channelType,
		Status:      common.ChannelStatusEnabled,
		Name:        "selection-failure-test",
		Group:       group,
		Models:      modelName,
		Priority:    common.GetPointer(int64(0)),
		CreatedTime: 1,
	}
	require.NoError(t, model.DB.Create(channel).Error)
	ability := &model.Ability{
		Group:     group,
		Model:     modelName,
		ChannelId: channel.Id,
		Enabled:   true,
		Priority:  common.GetPointer(int64(0)),
	}
	require.NoError(t, model.DB.Create(ability).Error)
}
