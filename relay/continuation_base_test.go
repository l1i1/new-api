package relay

import (
	"io"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

// The outbound snapshot must only be read when a continuation round asks for
// it, and the read must be stable across rounds (each round rebuilds its body
// from the same bytes).
func TestLazyOutboundBodySnapshotReadsOnDemandOnce(t *testing.T) {
	payload := []byte(`{"model":"kimi-k3.1-pro","messages":[{"role":"user","content":"hi"}]}`)
	storage, err := common.CreateBodyStorage(payload)
	require.NoError(t, err)
	defer storage.Close()

	reads := 0
	loader := lazyOutboundBodySnapshot(countingBody{
		ReplayableBody: common.NewReplayableBodyReader(storage),
		reads:          &reads,
	})

	require.Equal(t, 0, reads, "setup must not materialise the body")

	first, err := loader()
	require.NoError(t, err)
	require.Equal(t, payload, first)
	require.Equal(t, 1, reads)

	second, err := loader()
	require.NoError(t, err)
	require.Equal(t, payload, second)
	require.Equal(t, 1, reads, "a repeated round reuses the loaded snapshot")
}

// countingBody records how many independent readers the snapshot path opens.
type countingBody struct {
	common.ReplayableBody
	reads *int
}

func (b countingBody) NewReader() (io.ReadCloser, error) {
	*b.reads++
	return b.ReplayableBody.NewReader()
}
