package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestShouldRetryUnsupportedChannelEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	err := types.NewErrorWithStatusCode(
		errors.New("endpoint not supported"),
		types.ErrorCodeChannelUnsupportedEndpoint,
		http.StatusBadRequest,
	)

	require.True(t, shouldRetry(c, err, 1))
	require.False(t, shouldRetry(c, types.NewErrorWithStatusCode(
		errors.New("malformed request"),
		types.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
	), 1))
}

func TestShouldRetryUnsupportedChannelFeature(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	err := types.NewErrorWithStatusCode(
		errors.New("DFlash logprob capability not supported"),
		types.ErrorCodeChannelUnsupportedFeature,
		http.StatusBadRequest,
	)

	require.True(t, shouldRetry(c, err, 1))
}

func TestShouldRetryUpstreamBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	upstreamErr := types.NewOpenAIError(
		errors.New("upstream rejected this channel request"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadRequest,
	)
	require.True(t, shouldRetry(c, upstreamErr, 1))

	localErr := types.NewErrorWithStatusCode(
		errors.New("malformed request"),
		types.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
	)
	require.False(t, shouldRetry(c, localErr, 1))
}

func TestPrepareChannelRetrySeparatesKeyAndChannelFailures(t *testing.T) {
	param := &service.RetryParam{Retry: new(int)}
	multiKeyChannel := &model.Channel{Id: 41, ChannelInfo: model.ChannelInfo{IsMultiKey: true}}
	singleKeyChannel := &model.Channel{Id: 42}

	require.True(t, prepareChannelRetry(param, multiKeyChannel, http.StatusTooManyRequests, false))
	require.Equal(t, 41, param.PreferredChannelID())
	param.IncreaseRetry()
	require.Zero(t, param.GetRetry())

	require.False(t, prepareChannelRetry(param, multiKeyChannel, http.StatusInternalServerError, false))
	require.Zero(t, param.PreferredChannelID())
	param.IncreaseRetry()
	require.Equal(t, 1, param.GetRetry())

	require.False(t, prepareChannelRetry(param, singleKeyChannel, http.StatusUnauthorized, false))
	require.Zero(t, param.PreferredChannelID())
}

func TestAffinitySkipStillAllowsMultiKeyCredentialRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("channel_affinity_skip_retry_on_failure", true)
	common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, true)

	require.False(t, shouldSkipRetryAfterAffinity(c, http.StatusUnauthorized))
	require.False(t, shouldSkipRetryAfterAffinity(c, http.StatusForbidden))
	require.False(t, shouldSkipRetryAfterAffinity(c, http.StatusTooManyRequests))
	require.True(t, shouldSkipRetryAfterAffinity(c, http.StatusBadRequest))
}

func TestRetryParamCancelResetAfterMultiKeyExhaustion(t *testing.T) {
	param := &service.RetryParam{Retry: new(int)}
	param.ResetRetryNextTry()
	param.CancelRetryReset()
	param.IncreaseRetry()
	require.Equal(t, 1, param.GetRetry())

	param.ExcludeChannel(41)
	require.True(t, param.IsChannelExcluded(41))
	require.False(t, param.IsChannelExcluded(42))
}
