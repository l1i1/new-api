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
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
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

// TestShouldRetryOllamaResponsesEndpointUnsupported pins the production report
// "status_code=400, ollama channel: /v1/responses endpoint not supported": an
// endpoint-capability failure must fail over to another channel, not surface.
func TestShouldRetryOllamaResponsesEndpointUnsupported(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	err := types.NewErrorWithStatusCode(
		errors.New("ollama channel: /v1/responses endpoint not supported"),
		types.ErrorCodeChannelUnsupportedEndpoint,
		http.StatusBadRequest,
	)

	require.True(t, types.IsChannelError(err))
	require.True(t, shouldRetry(c, err, 1))
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

func TestShouldRetryUpstreamServiceTemporarilyUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	err := types.NewOpenAIError(
		errors.New("Upstream service temporarily unavailable"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadGateway,
	)

	require.True(t, shouldRetry(c, err, 1))
}

// TestLocalErrorsDoNotFollowTheFailoverList pins the boundary of the merged
// failover list: it governs upstream rejections, not local (platform) errors.
// A local request-shaped 400 must surface, and a local capability gap fails
// over through its channel: error-code prefix, which is a framework rule and
// deliberately not operator-configurable.
func TestLocalErrorsDoNotFollowTheFailoverList(t *testing.T) {
	gin.SetMode(gin.TestMode)

	localCapabilityErr := types.NewErrorWithStatusCode(
		errors.New("ollama channel: image endpoint not supported"),
		types.ErrorCodeChannelUnsupportedEndpoint,
		http.StatusBadRequest,
	)
	localCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.True(t, shouldRetry(localCtx, localCapabilityErr, 1))

	localRequestErr := types.NewErrorWithStatusCode(
		errors.New("malformed request"),
		types.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
	)
	requestCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.False(t, shouldRetry(requestCtx, localRequestErr, 1), "a local request error must not fail over")

	// The same shape from upstream does fail over: 400 is on the failover list.
	upstreamCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.True(t, shouldRetry(upstreamCtx, types.NewOpenAIError(
		errors.New("upstream rejected this channel request"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadRequest,
	), 1))
}

func TestNeverRetryStatusCodesOverrideAutomaticRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	origNever := operation_setting.NeverRetryStatusCodeRanges
	t.Cleanup(func() { operation_setting.NeverRetryStatusCodeRanges = origNever })

	serverErr := types.NewOpenAIError(
		errors.New("upstream gateway failure"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusInternalServerError,
	)

	serverErrCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.True(t, shouldRetry(serverErrCtx, serverErr, 1))

	// A 500 listed in the never-retry option stops retrying even though the
	// default automatic ranges include it.
	require.NoError(t, operation_setting.NeverRetryStatusCodesFromString("500"))
	neverCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.False(t, shouldRetry(neverCtx, serverErr, 1))
}

func TestShouldRetryUpstreamErrorMatchingKeyword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	origKeywords := operation_setting.AutomaticRetryKeywords
	t.Cleanup(func() { operation_setting.AutomaticRetryKeywords = origKeywords })

	// 408 is absent from the automatic retry ranges, so without a keyword the
	// error stays non-retryable.
	upstreamErr := types.NewOpenAIError(
		errors.New("upstream: model not found"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusRequestTimeout,
	)
	beforeCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.False(t, shouldRetry(beforeCtx, upstreamErr, 1))

	operation_setting.AutomaticRetryKeywordsFromString("model not found")
	keywordCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.True(t, shouldRetry(keywordCtx, upstreamErr, 1))

	// A different message must not match.
	otherErr := types.NewOpenAIError(
		errors.New("upstream: quota exhausted"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusRequestTimeout,
	)
	otherCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.False(t, shouldRetry(otherCtx, otherErr, 1))
}

func TestRetryKeywordIsBoundedByHardGates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	origKeywords := operation_setting.AutomaticRetryKeywords
	origNever := operation_setting.NeverRetryStatusCodeRanges
	t.Cleanup(func() {
		operation_setting.AutomaticRetryKeywords = origKeywords
		operation_setting.NeverRetryStatusCodeRanges = origNever
	})

	operation_setting.AutomaticRetryKeywordsFromString("model not found")

	// The never-retry status list still outranks a matching keyword.
	require.NoError(t, operation_setting.NeverRetryStatusCodesFromString("408"))
	upstreamErr := types.NewOpenAIError(
		errors.New("upstream: model not found"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusRequestTimeout,
	)
	neverCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.False(t, shouldRetry(neverCtx, upstreamErr, 1), "the never-retry list outranks a matching keyword")

	// Exhausted retry budget and a committed response still stop the loop.
	require.NoError(t, operation_setting.NeverRetryStatusCodesFromString(""))
	noBudgetCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.False(t, shouldRetry(noBudgetCtx, upstreamErr, 0), "an exhausted retry budget outranks a matching keyword")

	recorder := httptest.NewRecorder()
	committedCtx, _ := gin.CreateTestContext(recorder)
	_, err := committedCtx.Writer.Write([]byte("partial stream"))
	require.NoError(t, err)
	require.False(t, shouldRetry(committedCtx, upstreamErr, 1), "a committed response outranks a matching keyword")

	// A malformed upstream response body is an upstream (channel) failure, not a
	// local one: it must fail over to another channel rather than surface. The
	// request-side counterpart (convert_request_failed) carries its own explicit
	// skip-retry marker, so removing the error-code list does not make local
	// conversion failures retryable.
	badBodyErr := types.NewOpenAIError(
		errors.New("invalid character 'd' looking for beginning of value"),
		types.ErrorCodeBadResponseBody,
		http.StatusInternalServerError,
	)
	badBodyCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.True(t, shouldRetry(badBodyCtx, badBodyErr, 1), "a malformed upstream body must retry another channel")
}

// TestRetryKeywordOverridesConversionSkipRetry covers adaptors that report a
// channel capability gap as a plain conversion error. newConvertRequestFailedError
// wraps those with skip-retry, so without the keyword override the request would
// surface instead of failing over to a channel that supports the endpoint.
func TestRetryKeywordOverridesConversionSkipRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	origKeywords := operation_setting.AutomaticRetryKeywords
	t.Cleanup(func() { operation_setting.AutomaticRetryKeywords = origKeywords })

	conversionErr := types.NewErrorWithStatusCode(
		errors.New("codex channel: endpoint not supported"),
		types.ErrorCodeConvertRequestFailed,
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
	)
	require.True(t, types.IsSkipRetryError(conversionErr))

	beforeCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.False(t, shouldRetry(beforeCtx, conversionErr, 1))

	operation_setting.AutomaticRetryKeywordsFromString("endpoint not supported")
	afterCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.True(t, shouldRetry(afterCtx, conversionErr, 1))
}

func TestShouldNotRetryAfterResponseWriterCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	_, err := c.Writer.Write([]byte("partial stream"))
	require.NoError(t, err)

	upstreamErr := types.NewOpenAIError(
		errors.New("upstream returned empty final content"),
		types.ErrorCode("server_error"),
		http.StatusBadGateway,
	)
	require.True(t, c.Writer.Written())
	require.False(t, shouldRetry(c, upstreamErr, 1))
}

func TestShouldRetryKeepAliveOnlyCommitAllowsChannelSwap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.NoError(t, helper.PingData(c))

	// Mirrors the empty-output failure raised after the keep-alive ping already
	// committed the writer: it marks itself non-retryable, but nothing the client
	// can act on was delivered, so another channel may still serve the request.
	emptyErr := types.NewOpenAIError(
		errors.New("upstream returned empty final content"),
		types.ErrorCode("server_error"),
		http.StatusBadGateway,
		types.ErrOptionWithSkipRetry(),
	)
	require.True(t, types.IsSkipRetryError(emptyErr))

	require.True(t, shouldRetry(c, emptyErr, 1), "a keep-alive-only commit must still allow a channel swap")
	require.False(t, types.IsSkipRetryError(emptyErr), "the skip-retry marker must be cleared for a keep-alive-only commit")
}

func TestShouldNotRetryWhenPayloadFollowsKeepAlive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.NoError(t, helper.PingData(c))
	_, err := c.Writer.Write([]byte("data: {\"choices\":[]}\n\n"))
	require.NoError(t, err)

	upstreamErr := types.NewOpenAIError(
		errors.New("upstream stream terminated"),
		types.ErrorCode("server_error"),
		http.StatusBadGateway,
	)
	require.False(t, shouldRetry(c, upstreamErr, 1), "real delivered data must block a channel swap")
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

func TestPrepareChannelRetryRotatesKeyOnPaymentRequired(t *testing.T) {
	// Regression: an official multi-key channel whose first credential ran out
	// of balance returned 402. The old hardcoded 403/429 switch excluded the
	// whole channel, and because an OfficialFit pin left no other candidate the
	// request surfaced get_channel_failed. 402 must now rotate the key instead.
	param := &service.RetryParam{Retry: new(int)}
	multiKeyChannel := &model.Channel{Id: 43, ChannelInfo: model.ChannelInfo{IsMultiKey: true}}

	require.True(t, prepareChannelRetry(param, multiKeyChannel, http.StatusPaymentRequired, false))
	require.Equal(t, 43, param.PreferredChannelID())
	require.False(t, param.IsChannelExcluded(43))
}

func TestMultiKeyCredentialRetryStatusCodesRespectConfig(t *testing.T) {
	orig := operation_setting.MultiKeyCredentialRetryStatusCodeRanges
	origKeywords := operation_setting.MultiKeyCredentialRetryKeywords
	t.Cleanup(func() {
		operation_setting.MultiKeyCredentialRetryStatusCodeRanges = orig
		operation_setting.MultiKeyCredentialRetryKeywords = origKeywords
	})

	require.True(t, service.ShouldRotateMultiKeyCredentialOn(http.StatusPaymentRequired, "402"))

	// An operator can narrow rotation to the classic throttling codes.
	require.NoError(t, operation_setting.MultiKeyCredentialRetryStatusCodesFromString("403,429"))
	require.False(t, service.ShouldRotateMultiKeyCredentialOn(http.StatusPaymentRequired, "402"))
	require.True(t, service.ShouldRotateMultiKeyCredentialOn(http.StatusTooManyRequests, ""))

	// Or opt a credential-scoped 401 into rotation when every key has its own
	// account.
	require.NoError(t, operation_setting.MultiKeyCredentialRetryStatusCodesFromString("401"))
	require.True(t, service.ShouldRotateMultiKeyCredentialOn(http.StatusUnauthorized, ""))
	require.False(t, service.ShouldRotateMultiKeyCredentialOn(http.StatusForbidden, ""))

	// The keyword list is the text form of the same decision, so it rotates even
	// when the status code alone would not.
	require.True(t, service.ShouldRotateMultiKeyCredentialOn(http.StatusBadRequest, "You have insufficient credits to make this request"))
}

func TestAffinitySkipStillAllowsMultiKeyCredentialRetryExceptUnauthorized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("channel_affinity_skip_retry_on_failure", true)
	common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, true)

	errWithStatus := func(code int, message string) *types.NewAPIError {
		return types.NewOpenAIError(errors.New(message), types.ErrorCodeBadResponseStatusCode, code)
	}

	require.True(t, shouldSkipRetryAfterAffinity(c, errWithStatus(http.StatusUnauthorized, "")))
	require.False(t, shouldSkipRetryAfterAffinity(c, errWithStatus(http.StatusForbidden, "")))
	require.False(t, shouldSkipRetryAfterAffinity(c, errWithStatus(http.StatusTooManyRequests, "")))
	require.True(t, shouldSkipRetryAfterAffinity(c, errWithStatus(http.StatusBadRequest, "")))
	// A credential-scoped keyword exempts the pin just like the status code does:
	// the pinned channel can still serve the request with another key.
	require.False(t, shouldSkipRetryAfterAffinity(c, errWithStatus(http.StatusBadRequest, "You have insufficient credits")))
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

func TestCredentialErrorsRetryAnotherKeyOnSameChannelExceptUnauthorized(t *testing.T) {
	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	t.Cleanup(func() { common.RetryTimes = originalRetryTimes })

	gin.SetMode(gin.TestMode)
	// 402 is the credential-scoped balance failure: it must rotate to the other
	// key exactly like 403/429, instead of excluding the whole channel.
	for _, statusCode := range []int{
		http.StatusPaymentRequired,
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

func TestUnauthorizedMultiKeyFailureMovesToNextChannel(t *testing.T) {
	param := &service.RetryParam{Retry: new(int)}
	channel := &model.Channel{
		Id: 4103,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
		},
	}

	require.False(t, prepareChannelRetry(param, channel, http.StatusUnauthorized, false))
	require.Zero(t, param.PreferredChannelID())
	require.True(t, param.IsChannelExcluded(channel.Id))

	param.IncreaseRetry()
	require.Equal(t, 1, param.GetRetry())
}

func TestFirstAttemptWithoutRelayChannelMetaKeepsMultiKeyRetry(t *testing.T) {
	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	t.Cleanup(func() { common.RetryTimes = originalRetryTimes })

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	channel := &model.Channel{
		Id:     94101,
		Type:   constant.ChannelTypeOpenAI,
		Status: common.ChannelStatusEnabled,
		Name:   "first-attempt-multi-key",
		Key:    "key-a\nkey-b",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeyMode: constant.MultiKeyModeRandom,
		},
	}
	require.Nil(t, middleware.SetupContextForSelectedChannel(ctx, channel, "model"))

	// The normal first relay attempt has no RelayInfo.ChannelMeta until the
	// selected adaptor calls InitChannelMeta.
	info := &relaycommon.RelayInfo{}
	param := &service.RetryParam{Retry: new(int)}
	selected, err := getChannel(ctx, info, param)
	require.Nil(t, err)
	require.NotNil(t, selected)
	require.True(t, selected.ChannelInfo.IsMultiKey)

	firstIndex := common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex)
	service.MarkCurrentMultiKeyTried(ctx)
	require.True(t, prepareChannelRetry(param, selected, http.StatusTooManyRequests, false))
	param.IncreaseRetry()
	require.Zero(t, param.GetRetry())

	require.Nil(t, middleware.SetupContextForSelectedChannel(ctx, selected, "model"))
	require.NotEqual(t, firstIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
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

// TestShouldRetryContextOverflowStopsFailover pins the production report
// "status_code=400, The prompt is too long: 1270974, model maximum context
// length: 1048576": one request spent five channels and 89 seconds because the
// force-retry rule for upstream 400s treats every rejection as a channel
// problem. The prompt is over every channel's limit, so the first rejection is
// the final answer and the client must shorten its request.
// TestInsufficientCreditsRotatesKeyInsteadOfDroppingChannel pins the production
// report "status_code=400, You have insufficient credits to make this request":
// 562 rejections in seven days, all on one multi-key channel, 28 of which spent
// a second channel before dying. The balance belongs to the key, so the channel
// rotates its credential instead of being excluded.
func TestInsufficientCreditsRotatesKeyInsteadOfDroppingChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	channel := &model.Channel{
		Id: 2,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
		},
	}
	creditsErr := types.NewOpenAIError(
		errors.New("You have insufficient credits to make this request. Please purchase more credits to continue using the service."),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadRequest,
	)

	retryCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.True(t, shouldRetry(retryCtx, creditsErr, 1))

	param := &service.RetryParam{Retry: new(int)}
	require.True(t, prepareChannelRetryWithMessage(param, channel, creditsErr.StatusCode, creditsErr.Error(), false))
	require.Equal(t, channel.Id, param.PreferredChannelID(), "the channel keeps the next attempt")
	require.False(t, param.IsChannelExcluded(channel.Id), "the channel must not be dropped")

	// A single-key channel cannot rotate: the same failure fails the channel over.
	singleKeyParam := &service.RetryParam{Retry: new(int)}
	singleKey := &model.Channel{Id: 3}
	require.False(t, prepareChannelRetryWithMessage(singleKeyParam, singleKey, creditsErr.StatusCode, creditsErr.Error(), false))
	require.True(t, singleKeyParam.IsChannelExcluded(singleKey.Id))
}

// TestNeverRetryOutranksCredentialRotation pins the priority order: a
// request-level verdict stops the request even when the text also looks
// credential-scoped, so the retry budget is never spent on an answer no key can
// change.
func TestNeverRetryOutranksCredentialRotation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// The verdict reads the immutable request-policy snapshot, which is what
	// the settings page edits and what every option write refreshes.
	policy := model.CurrentRequestPolicy()
	original := append([]string(nil), policy.NeverRetryKeywords...)
	t.Cleanup(func() { policy.NeverRetryKeywords = original })
	policy.NeverRetryKeywords = append(append([]string(nil), original...), "insufficient credits")

	param := &service.RetryParam{Retry: new(int)}
	channel := &model.Channel{Id: 2, ChannelInfo: model.ChannelInfo{IsMultiKey: true}}
	creditsErr := types.NewOpenAIError(
		errors.New("You have insufficient credits to make this request"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadRequest,
	)

	neverCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.False(t, shouldRetry(neverCtx, creditsErr, 1))
	require.False(t, prepareChannelRetryWithMessage(param, channel, creditsErr.StatusCode, creditsErr.Error(), true))
}

func TestShouldRetryContextOverflowStopsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	origForce := operation_setting.ForceRetryStatusCodeRanges
	t.Cleanup(func() { operation_setting.ForceRetryStatusCodeRanges = origForce })
	require.NoError(t, operation_setting.ForceRetryStatusCodesFromString("400"))
	overflow := types.NewOpenAIError(
		errors.New("The prompt is too long: 1270974, model maximum context length: 1048576"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadRequest,
	)
	plainCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.False(t, shouldRetry(plainCtx, overflow, 1))

	// A keep-alive ping already committed bytes to the client. The verdict on
	// the request does not change with the writer state, and reopening the retry
	// here is exactly how the five-channel chain happened: the streaming client
	// waited long enough for the gateway to ping before the first 400 arrived.
	pingCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.NoError(t, helper.PingData(pingCtx))
	require.Equal(t, helper.ResponseKeepAliveOnly, helper.ResponseCommitStateOf(pingCtx))
	require.False(t, shouldRetry(pingCtx, overflow, 1))

	// Channel-shaped upstream 400s keep failing over.
	channelCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.True(t, shouldRetry(channelCtx, types.NewOpenAIError(
		errors.New("upstream rejected this channel request"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadRequest,
	), 1))
}
