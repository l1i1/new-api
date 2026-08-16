package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMessageWithRequestIdReplacesUpstreamRequestIds(t *testing.T) {
	t.Parallel()

	message := "status_code=503, No available OAuth accounts in pool (request id: upstream-1) (request id: upstream-2)"

	require.Equal(t,
		"status_code=503, No available OAuth accounts in pool (request id: local-1)",
		MessageWithRequestId(message, "local-1"),
	)
}

func TestMessageWithRequestIdKeepsMessagesWithoutRequestId(t *testing.T) {
	t.Parallel()

	require.Equal(t, "upstream error (request id: local-1)", MessageWithRequestId("upstream error", "local-1"))
}
