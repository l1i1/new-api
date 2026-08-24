package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
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

func TestCredentialErrorsRetryAnotherKeyOnSameChannel(t *testing.T) {
	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	t.Cleanup(func() { common.RetryTimes = originalRetryTimes })

	gin.SetMode(gin.TestMode)
	for _, statusCode := range []int{
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusTooManyRequests,
	} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			channel := &model.Channel{
				Id:     4101,
				Type:   constant.ChannelTypeOpenAI,
				Status: common.ChannelStatusEnabled,
				Key:    "key-a\nkey-b",
				ChannelInfo: model.ChannelInfo{
					IsMultiKey:   true,
					MultiKeyMode: constant.MultiKeyModeRandom,
				},
			}

			require.Nil(t, middleware.SetupContextForSelectedChannel(ctx, channel, "model"))
			firstIndex := common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex)
			service.MarkCurrentMultiKeyTried(ctx)

			param := &service.RetryParam{Retry: new(int)}
			require.True(t, prepareChannelRetry(param, channel, statusCode, false))
			require.Equal(t, channel.Id, param.PreferredChannelID())
			require.False(t, param.IsChannelExcluded(channel.Id))
			param.IncreaseRetry()
			require.Zero(t, param.GetRetry())

			// The retry remains on this channel, while the request-local tried set
			// makes the next setup choose the other credential.
			require.Nil(t, middleware.SetupContextForSelectedChannel(ctx, channel, "model"))
			require.NotEqual(t, firstIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
		})
	}
}

func TestGatewayAndUnsupportedFeatureErrorsMoveToAnotherChannel(t *testing.T) {
	cases := []struct {
		name       string
		err        *types.NewAPIError
		statusCode int
	}{
		{
			name: "upstream bad request",
			err: types.NewOpenAIError(
				errors.New("upstream rejected this channel request"),
				types.ErrorCodeBadResponseStatusCode,
				http.StatusBadRequest,
			),
			statusCode: http.StatusBadRequest,
		},
		{
			name: "upstream gateway failure",
			err: types.NewOpenAIError(
				errors.New("upstream gateway failure"),
				types.ErrorCodeBadResponseStatusCode,
				http.StatusInternalServerError,
			),
			statusCode: http.StatusInternalServerError,
		},
		{
			name: "dflash unsupported feature",
			err: types.NewErrorWithStatusCode(
				errors.New("DFlash speculative decoding does not support return_logprob yet"),
				types.ErrorCodeChannelUnsupportedFeature,
				http.StatusBadRequest,
			),
			statusCode: http.StatusBadRequest,
		},
	}

	gin.SetMode(gin.TestMode)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			channel := &model.Channel{
				Id: 4102,
				ChannelInfo: model.ChannelInfo{
					IsMultiKey: true,
				},
			}
			param := &service.RetryParam{Retry: new(int)}

			require.True(t, shouldRetry(ctx, tc.err, 1))
			require.False(t, prepareChannelRetry(param, channel, tc.statusCode, false))
			require.Zero(t, param.PreferredChannelID())
			require.True(t, param.IsChannelExcluded(channel.Id))
		})
	}
}
