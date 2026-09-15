package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestEmptyOutputIsNotAStandaloneGatewayError(t *testing.T) {
	// Empty visible content is now valid upstream output. Only explicit upstream
	// failures and protocol truncation are classified as relay errors.
	err := types.NewOpenAIError(errors.New("boom"), types.ErrorCode("server_error"), http.StatusBadGateway)
	require.False(t, err.IsEmptyOutput())
}

func TestResponsesTruncationIsAnUpstreamFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	err := incompleteResponsesStreamError(ctx, false, false)
	require.True(t, err.IsUpstreamFailure())
	require.False(t, err.IsEmptyOutput())
	require.True(t, err.ShouldEvictChannelAffinity())
}
