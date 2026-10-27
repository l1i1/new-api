package common

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarkUndeliveredKeepsFirstReasonAndRawCounts(t *testing.T) {
	info := &RelayInfo{}
	info.MarkUndelivered(UndeliveredMissingFinish, &dto.Usage{PromptTokens: 120, CompletionTokens: 30, TotalTokens: 150})
	info.MarkUndelivered(UndeliveredClientGone, &dto.Usage{PromptTokens: 999, CompletionTokens: 999, TotalTokens: 1998})

	assert.Equal(t, UndeliveredMissingFinish, info.UndeliveredReason)
	assert.Equal(t, int64(120), info.UndeliveredPromptTokens)
	assert.Equal(t, int64(30), info.UndeliveredCompletionTokens)
}

func TestUndeliveredSettlementReason(t *testing.T) {
	require.Empty(t, (*RelayInfo)(nil).UndeliveredSettlementReason())
	require.Empty(t, (&RelayInfo{}).UndeliveredSettlementReason())

	info := &RelayInfo{StreamStatus: NewStreamStatus()}
	require.Empty(t, info.UndeliveredSettlementReason())

	info.StreamStatus.SetEndReason(StreamEndReasonDone, nil)
	require.Empty(t, info.UndeliveredSettlementReason(), "a normal stream end must remain billable")

	info.StreamStatus = NewStreamStatus()
	info.StreamStatus.SetEndReason(StreamEndReasonClientGone, nil)
	assert.Empty(t, info.UndeliveredSettlementReason(),
		"a caller-side disconnect is not an upstream fault and stays billable")

	info.StreamStatus = NewStreamStatus()
	info.StreamStatus.SetEndReason(StreamEndReasonScannerErr, nil)
	assert.Equal(t, UndeliveredStreamPrefix+"scanner_error", info.UndeliveredSettlementReason())

	info = &RelayInfo{UndeliveredReason: UndeliveredMissingFinish, StreamStatus: NewStreamStatus()}
	info.StreamStatus.SetEndReason(StreamEndReasonDone, nil)
	assert.Equal(t, UndeliveredMissingFinish, info.UndeliveredSettlementReason(),
		"an explicit upstream-fault marker wins over a normal transport end")

	info = &RelayInfo{UndeliveredReason: UndeliveredClientGone, StreamStatus: NewStreamStatus()}
	info.StreamStatus.SetEndReason(StreamEndReasonDone, nil)
	assert.Empty(t, info.UndeliveredSettlementReason(),
		"an accidental client_gone marker must not zero a settlement")
}
