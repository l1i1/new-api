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
