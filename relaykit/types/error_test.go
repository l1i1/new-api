package types

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewAPIErrorSetMessageUpdatesRelayErrorPayload(t *testing.T) {
	openAIError := NewOpenAIError(errors.New("upstream"), ErrorCodeBadResponse, 503)
	openAIError.SetMessage("filtered (request id: local-1)")
	require.Equal(t, "filtered (request id: local-1)", openAIError.ToOpenAIError().Message)

	claudeError := WithClaudeError(ClaudeError{Type: "upstream_error", Message: "upstream"}, 503)
	claudeError.SetMessage("filtered (request id: local-1)")
	require.Equal(t, "filtered (request id: local-1)", claudeError.ToClaudeError().Message)
}

func TestSkipRetryOptionPreservesUnsupportedEndpointRetryability(t *testing.T) {
	for _, errorCode := range []ErrorCode{
		ErrorCodeChannelUnsupportedEndpoint,
		ErrorCodeChannelUnsupportedFeature,
	} {
		err := NewErrorWithStatusCode(errors.New("channel capability not supported"), errorCode, 400)
		ErrOptionWithSkipRetry()(err)
		require.False(t, IsSkipRetryError(err))
	}
}

func TestUpstreamFailureMarkerDrivesAffinityEviction(t *testing.T) {
	// Empty-output failures keep evicting the binding (pre-existing behavior).
	emptyErr := NewOpenAIError(errors.New("empty"), ErrorCode("server_error"), 502, ErrOptionWithEmptyOutput())
	require.True(t, emptyErr.ShouldEvictChannelAffinity())

	// An explicit in-band upstream failure must also evict, even though the
	// committed stream makes the error non-retryable within this request.
	inBandErr := NewOpenAIError(errors.New("Upstream request failed"), ErrorCode("upstream_error"), 503, ErrOptionWithUpstreamFailure())
	require.True(t, inBandErr.IsUpstreamFailure())
	require.True(t, inBandErr.ShouldEvictChannelAffinity())

	// Unmarked errors and nil receivers must not trigger eviction.
	plainErr := NewOpenAIError(errors.New("upstream returned 500"), ErrorCode("server_error"), 500)
	require.False(t, plainErr.ShouldEvictChannelAffinity())
	require.False(t, (*NewAPIError)(nil).ShouldEvictChannelAffinity())
}
