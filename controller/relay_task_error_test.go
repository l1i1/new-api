package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRespondTaskErrorPreservesLocalRateLimitMessage(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	taskErr := &taskdto.TaskError{
		Code:       "",
		Message:    "Group vip model model-a rate limit exceeded",
		StatusCode: http.StatusTooManyRequests,
		LocalError: true,
		Error:      errors.New("rate limited"),
	}

	respondTaskError(c, taskErr)

	assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
	assert.Equal(t, "Group vip model model-a rate limit exceeded", taskErr.Message)
	assert.False(t, shouldRetryTaskRelay(c, 1, taskErr, 1))
}

func TestRespondTaskErrorRewritesUpstreamRateLimitMessage(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	taskErr := &taskdto.TaskError{
		Message:    "raw upstream response",
		StatusCode: http.StatusTooManyRequests,
	}

	respondTaskError(c, taskErr)

	assert.Equal(t, "当前分组上游负载已饱和，请稍后再试", taskErr.Message)
}

func TestShouldRetryTaskRelayUpstreamBadRequest(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	taskErr := &taskdto.TaskError{
		StatusCode: http.StatusBadRequest,
		LocalError: false,
		Error:      errors.New("upstream rejected this channel request"),
	}

	assert.True(t, shouldRetryTaskRelay(c, 1, taskErr, 1))
}

func TestShouldRetryTaskRelayFollowsConfiguredStatusCodes(t *testing.T) {
	origNever := operation_setting.NeverRetryStatusCodeRanges
	origAuto := operation_setting.AutomaticRetryStatusCodeRanges
	t.Cleanup(func() {
		operation_setting.NeverRetryStatusCodeRanges = origNever
		operation_setting.AutomaticRetryStatusCodeRanges = origAuto
	})

	gatewayErr := &taskdto.TaskError{
		StatusCode: http.StatusInternalServerError,
		Error:      errors.New("upstream gateway failure"),
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	// The default automatic ranges include 5xx.
	assert.True(t, shouldRetryTaskRelay(c, 1, gatewayErr, 1))

	// Moving 500 to never-retry stops the task path from retrying it too.
	require.NoError(t, operation_setting.NeverRetryStatusCodesFromString("500"))
	assert.False(t, shouldRetryTaskRelay(c, 1, gatewayErr, 1))

	// Dropping 5xx from the automatic ranges also stops retrying.
	require.NoError(t, operation_setting.NeverRetryStatusCodesFromString(""))
	require.NoError(t, operation_setting.AutomaticRetryStatusCodesFromString("429"))
	assert.False(t, shouldRetryTaskRelay(c, 1, gatewayErr, 1))
	assert.True(t, shouldRetryTaskRelay(c, 1, &taskdto.TaskError{StatusCode: http.StatusTooManyRequests}, 1))
}

func TestShouldRetryTaskRelayFollowsConfiguredKeywords(t *testing.T) {
	origKeywords := operation_setting.AutomaticRetryKeywords
	origNever := operation_setting.NeverRetryStatusCodeRanges
	t.Cleanup(func() {
		operation_setting.AutomaticRetryKeywords = origKeywords
		operation_setting.NeverRetryStatusCodeRanges = origNever
	})

	// 408 is outside the automatic ranges, so the task path keeps it local until
	// an operator keyword marks the message as "try another channel".
	taskErr := &taskdto.TaskError{
		StatusCode: http.StatusRequestTimeout,
		Message:    "upstream: model not found",
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	assert.False(t, shouldRetryTaskRelay(c, 1, taskErr, 1))

	operation_setting.AutomaticRetryKeywordsFromString("model not found")
	assert.True(t, shouldRetryTaskRelay(c, 1, taskErr, 1))

	// The never-retry list still outranks a matching keyword.
	require.NoError(t, operation_setting.NeverRetryStatusCodesFromString("408"))
	assert.False(t, shouldRetryTaskRelay(c, 1, taskErr, 1))

	// Local validation errors are never retried, keyword or not.
	require.NoError(t, operation_setting.NeverRetryStatusCodesFromString(""))
	assert.False(t, shouldRetryTaskRelay(c, 1, &taskdto.TaskError{
		StatusCode: http.StatusRequestTimeout,
		Message:    "upstream: model not found",
		LocalError: true,
	}, 1))
}
