package service

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

// A broken override condition must never auto-disable a channel: the configuration is
// ours, the upstream is fine, and AutomaticEnableChannelEnabled is off in production, so a
// ban removes capacity permanently (2026-10-28, #37/#91).
func TestShouldDisableChannelIgnoresParamOverrideInvalid(t *testing.T) {
	previous := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previous })

	err := types.NewError(errors.New("comparison failed for path max_tokens: compare for different types, got Number and String"),
		types.ErrorCodeChannelParamOverrideInvalid, types.ErrOptionWithSkipRetry())
	require.False(t, ShouldDisableChannel(err))
}
