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
