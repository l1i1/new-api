package openai

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestEmptyOutputErrorsAreFlagged(t *testing.T) {
	require.True(t, emptyChatCompletionError().IsEmptyOutput())
	require.True(t, emptyChatCompletionError(true).IsEmptyOutput())
	// committed=false never touches the writer, so a nil-safe test context is enough
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.True(t, emptyResponsesStreamError(ctx, false).IsEmptyOutput())

	// committed variants must keep their skip-retry semantics
	require.True(t, types.IsSkipRetryError(emptyChatCompletionError(true)))
	require.False(t, types.IsSkipRetryError(emptyChatCompletionError()))

	// image variants behave the same way
	require.True(t, emptyImageResponseError().IsEmptyOutput())
	require.True(t, emptyImageResponseError(true).IsEmptyOutput())
	require.True(t, types.IsSkipRetryError(emptyImageResponseError(true)))
	require.False(t, types.IsSkipRetryError(emptyImageResponseError()))

	// unrelated errors must not carry the flag
	require.False(t, types.NewOpenAIError(
		errors.New("boom"), types.ErrorCode("server_error"), 502,
	).IsEmptyOutput())
}
