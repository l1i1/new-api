package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
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
