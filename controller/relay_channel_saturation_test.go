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
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// getChannel must honor saturation exclusions even before the relay handler
// ever ran (RelayInfo.ChannelMeta == nil): otherwise a saturated distributor
// channel would be re-returned forever and the switch loop would never move
// on. With every candidate saturated, the failure must be a local 429.
func TestGetChannelSwitchesAwayFromSaturatedChannel(t *testing.T) {
	previousDB := model.DB
	previousMemoryCache := common.MemoryCacheEnabled
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	// The cache-based selection path (the production path) keeps this test
	// independent of the DB dialect column helpers.
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousMemoryCache
	})

	for _, channel := range []*model.Channel{
		{Id: 1, Name: "ch-1", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI, Group: "default", Models: "test-model", Key: "sk-1"},
		{Id: 2, Name: "ch-2", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI, Group: "default", Models: "test-model", Key: "sk-2"},
	} {
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "test-model", ChannelId: channel.Id, Enabled: true}).Error)
	}
	model.InitChannelCache()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	common.SetContextKey(c, constant.ContextKeySelectedChannel, &model.Channel{Id: 1})
	common.SetContextKey(c, constant.ContextKeyChannelId, 1)
	common.SetContextKey(c, constant.ContextKeyChannelName, "ch-1")
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)

	info := &relaycommon.RelayInfo{TokenGroup: "default", OriginModelName: "test-model"}
	retryParam := &service.RetryParam{Ctx: c, TokenGroup: "default", ModelName: "test-model", RequestPath: c.Request.URL.Path, Retry: common.GetPointer(0)}

	// Attempt 1: the distributor channel is selected normally...
	channel, channelErr := getChannel(c, info, retryParam)
	if channelErr != nil {
		t.Fatalf("getChannel failed: type=%T detail=%+v", channelErr, channelErr)
	}
	require.Equal(t, 1, channel.Id)

	// ...found saturated, and excluded by the relay loop's saturation branch.
	retryParam.ExcludeSaturatedChannel(1)

	// Attempt 2 must not re-select the saturated distributor channel even
	// though the relay handler never ran (ChannelMeta is still nil).
	channel, channelErr = getChannel(c, info, retryParam)
	if channelErr != nil {
		t.Fatalf("getChannel failed: type=%T detail=%+v", channelErr, channelErr)
	}
	require.Equal(t, 2, channel.Id, "the excluded (saturated) distributor channel must not be re-selected")

	// With every candidate saturated, the failure must be a local 429.
	retryParam.ExcludeSaturatedChannel(2)
	_, channelErr = getChannel(c, info, retryParam)
	require.Error(t, channelErr)
	require.Equal(t, http.StatusTooManyRequests, channelErr.StatusCode, "pure saturation exhaustion must surface as 429")
	require.True(t, types.IsSkipRetryError(channelErr))
}
