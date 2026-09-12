package service

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

func TestUnsupportedEndpointDoesNotAutoDisableChannel(t *testing.T) {
	previous := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previous })

	err := types.NewErrorWithStatusCode(
		errors.New("endpoint not supported"),
		types.ErrorCodeChannelUnsupportedEndpoint,
		http.StatusBadRequest,
	)
	require.False(t, ShouldDisableChannel(err))
}

func TestUnsupportedFeatureDoesNotAutoDisableChannel(t *testing.T) {
	previous := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previous })

	err := types.NewErrorWithStatusCode(
		errors.New("DFlash logprob capability not supported"),
		types.ErrorCodeChannelUnsupportedFeature,
		http.StatusBadRequest,
	)
	require.False(t, ShouldDisableChannel(err))
}

func TestRetryParamPureSaturationClassification(t *testing.T) {
	param := &RetryParam{}
	require.False(t, param.HasSaturatedChannel())

	param.ExcludeSaturatedChannel(1)
	require.True(t, param.HasSaturatedChannel())
	require.True(t, param.IsChannelExcluded(1))

	// Re-excluding the same channel stays a pure saturation outcome.
	param.ExcludeSaturatedChannel(1)
	require.True(t, param.HasSaturatedChannel())

	// A genuine channel failure breaks the pure-saturation classification, so
	// running out of candidates keeps the generic failure instead of 429.
	param.ExcludeChannel(2)
	require.False(t, param.HasSaturatedChannel())
}
