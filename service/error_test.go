package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResetStatusCode(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		statusCode       int
		statusCodeConfig string
		expectedCode     int
	}{
		{
			name:             "map string value",
			statusCode:       429,
			statusCodeConfig: `{"429":"503"}`,
			expectedCode:     503,
		},
		{
			name:             "map int value",
			statusCode:       429,
			statusCodeConfig: `{"429":503}`,
			expectedCode:     503,
		},
		{
			name:             "skip invalid string value",
			statusCode:       429,
			statusCodeConfig: `{"429":"bad-code"}`,
			expectedCode:     429,
		},
		{
			name:             "skip status code 200",
			statusCode:       200,
			statusCodeConfig: `{"200":503}`,
			expectedCode:     200,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			newAPIError := &types.NewAPIError{
				StatusCode: tc.statusCode,
			}
			ResetStatusCode(newAPIError, tc.statusCodeConfig)
			require.Equal(t, tc.expectedCode, newAPIError.StatusCode)
		})
	}
}

func TestRelayErrorHandlerTruncatesInvalidJSONBodyInLog(t *testing.T) {
	withDebugEnabled(t, false)

	body := strings.Repeat("b", common.LocalLogContentLimit+256)
	var logBuffer bytes.Buffer

	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logBuffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
	})

	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, "bad response status code 500", newAPIError.Error())
	require.Contains(t, logBuffer.String(), "[truncated")
	require.Contains(t, logBuffer.String(), fmt.Sprintf("original_length=%d", len(body)))
	require.NotContains(t, logBuffer.String(), strings.Repeat("b", common.LocalLogContentLimit+1))
}

func TestRelayErrorHandlerKeepsStructuredErrorMessage(t *testing.T) {
	message := strings.Repeat("c", common.LocalLogContentLimit+256)
	body := `{"message":"` + message + `"}`
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, message, newAPIError.Error())
}

func TestRelayErrorHandlerKeepsOpenAIErrorMessage(t *testing.T) {
	message := strings.Repeat("d", common.LocalLogContentLimit+256)
	body := `{"error":{"message":"` + message + `","type":"server_error","code":"server_error"}}`
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, message, newAPIError.Error())
}

func TestRelayErrorHandlerClassifiesDFlashLogprobCapability(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"DFLASH speculative decoding does not support return_logprob yet.","type":"invalid_request_error"}}`)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, types.ErrorCodeChannelUnsupportedFeature, newAPIError.GetErrorCode())
	require.False(t, types.IsSkipRetryError(newAPIError))
}

func TestRelayErrorHandlerForwardsCyberPolicyBodyOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := `{"error":{"code":"CYBER_POLICY","message":"blocked"}}`
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	apiErr := RelayErrorHandler(c, resp, false)
	require.NotNil(t, apiErr)
	require.Equal(t, types.ErrorCode("cyber_policy"), apiErr.GetErrorCode())
	require.True(t, OpsCyberPolicyForwarded(c))
	require.Equal(t, body, w.Body.String())
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRelayErrorHandlerWithFormatUsesClaudeCyberEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"cyber_policy","message":"blocked"}}`)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	apiErr := RelayErrorHandlerWithFormat(c, resp, false, types.RelayFormatClaude)
	require.NotNil(t, apiErr)
	require.True(t, OpsCyberPolicyForwarded(c))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), `"type":"error"`)
	require.Contains(t, w.Body.String(), `"type":"cyber_policy"`)
	require.NotContains(t, w.Body.String(), `"code":"cyber_policy"`)
}

func TestRelayErrorHandlerRetainsCyberPolicyUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"cyber_policy"},"usage":{"input_tokens":7,"output_tokens":5}}`)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	apiErr := RelayErrorHandler(c, resp, false)
	require.NotNil(t, apiErr)
	require.Equal(t, 7, GetOpsCyberPolicy(c).UpstreamInTok)
	require.Equal(t, 5, GetOpsCyberPolicy(c).UpstreamOutTok)
}

func TestNormalizeServerOverloadError(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"server_is_overloaded", "server_overloaded", "serverOverloaded", "slow_down"} {
		errorValue := types.OpenAIError{Code: code}
		require.True(t, NormalizeServerOverloadError(&errorValue), code)
		require.Equal(t, "server_error", errorValue.Code, code)
	}

	unsupported := types.OpenAIError{Code: "invalid_request"}
	require.False(t, NormalizeServerOverloadError(&unsupported))
	require.Equal(t, "invalid_request", unsupported.Code)
}

func TestRelayErrorHandlerBoundsAndMasksInvalidJSONBodyInDebugLog(t *testing.T) {
	withDebugEnabled(t, true)

	secret := "sk-upstream-error-secret-123456"
	body := `{"token":"` + secret + `","data":"` + strings.Repeat("e", common.LocalLogContentLimit+256)
	var logBuffer bytes.Buffer

	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logBuffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
	})

	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Contains(t, logBuffer.String(), "[truncated")
	require.NotContains(t, logBuffer.String(), secret)
	require.NotContains(t, logBuffer.String(), body)
}

func TestRelayErrorHandlerBoundsBodyIncludedForChannelTest(t *testing.T) {
	withDebugEnabled(t, true)

	secret := "sk-channel-test-secret-123456"
	body := `{"token":"` + secret + `","data":"` + strings.Repeat("f", common.LocalLogContentLimit+256)
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, true)

	require.NotNil(t, newAPIError)
	require.Contains(t, newAPIError.Error(), "[truncated")
	require.NotContains(t, newAPIError.Error(), secret)
	require.NotContains(t, newAPIError.Error(), body)
}

func withDebugEnabled(t *testing.T, enabled bool) {
	t.Helper()

	oldDebug := common.DebugEnabled
	common.DebugEnabled = enabled
	t.Cleanup(func() {
		common.DebugEnabled = oldDebug
	})
}
