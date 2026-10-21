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

// Availability first: a transient channel-level failure must not permanently
// disable the channel. AutomaticEnableChannelEnabled is off in this deployment,
// so an auto-disable is one-way capacity loss, while the failed request has
// already failed over.
func TestTransientChannelErrorsDoNotAutoDisableChannel(t *testing.T) {
	previous := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previous })

	for _, test := range []struct {
		name string
		code types.ErrorCode
	}{
		{"all keys just failed", types.ErrorCodeChannelNoAvailableKey},
		{"transport timeout", types.ErrorCodeChannelResponseTimeExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := types.NewErrorWithStatusCode(errors.New("transient"), test.code, http.StatusServiceUnavailable)
			require.False(t, ShouldDisableChannel(err), "a transient channel failure must keep the channel enabled")
		})
	}

	// A durable channel defect still disables the channel.
	invalidKey := types.NewErrorWithStatusCode(errors.New("invalid key"), types.ErrorCodeChannelInvalidKey, http.StatusUnauthorized)
	require.True(t, ShouldDisableChannel(invalidKey))
}
