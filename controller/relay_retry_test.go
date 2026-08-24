package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
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
