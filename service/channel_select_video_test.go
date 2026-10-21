package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSelectRandomChannelForRequestAnswersVideoCapabilityGap pins the status and
// shape of a video request whose group publishes the model but no channel that
// declares video support. Before the gap was reported explicitly the pin path
// classified the empty set as "unprovable exhaustion" and answered 500 with an
// internal message; the designed answer is the same 400 + video_request the
// pinned and request-filter paths already produce.
func TestSelectRandomChannelForRequestAnswersVideoCapabilityGap(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "video-gap-model"
	createChannelSelectAutoGroupsChannel(t, db, 2701, "default", modelName)
	model.InitChannelCache()

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	common.SetContextKey(ctx, constant.ContextKeyVideoRequest, true)
	retry := 0

	selected, _, selectErr := SelectRandomChannelForRequest(ctx, modelName, &RetryParam{
		Ctx: ctx, TokenGroup: "default", ModelName: modelName,
		RequestPath: "/v1/chat/completions", Retry: &retry,
	})
	require.Nil(t, selected)
	require.NotNil(t, selectErr, "the capability gap must be reported, not turned into an empty success")
	assert.Equal(t, http.StatusBadRequest, selectErr.StatusCode)
	assert.Equal(t, types.ErrorCode(dto.FilterVideoRequest), selectErr.Code)
	assert.Equal(t, i18n.MsgDistributorNoAvailableChannel, selectErr.MessageID)
	assert.Equal(t, dto.FilterVideoRequest, selectErr.FilterKind)

	// A text request for the same model and channel keeps working.
	ctxText, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctxText.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	retry = 0
	selected, _, selectErr = SelectRandomChannelForRequest(ctxText, modelName, &RetryParam{
		Ctx: ctxText, TokenGroup: "default", ModelName: modelName,
		RequestPath: "/v1/chat/completions", Retry: &retry,
	})
	require.Nil(t, selectErr)
	require.NotNil(t, selected)
	assert.Equal(t, 2701, selected.Id)
}
