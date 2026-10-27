package channel

import (
	"testing"

	common2 "github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
)

// TestRelayStreamResponseHeaderTimeoutSelection guards the scope of the tighter
// first-byte budget: streaming attempts only. Non-streaming attempts can
// legitimately return headers only after the whole generation, so they must keep
// the general client (0 = shared default).
func TestRelayStreamResponseHeaderTimeoutSelection(t *testing.T) {
	previous := common2.RelayStreamResponseHeaderTimeout
	common2.RelayStreamResponseHeaderTimeout = 120
	t.Cleanup(func() { common2.RelayStreamResponseHeaderTimeout = previous })

	assert.Equal(t, 120, relayStreamResponseHeaderTimeout(&relaycommon.RelayInfo{IsStream: true}))
	assert.Equal(t, 0, relayStreamResponseHeaderTimeout(&relaycommon.RelayInfo{IsStream: false}))
	assert.Equal(t, 0, relayStreamResponseHeaderTimeout(nil))

	common2.RelayStreamResponseHeaderTimeout = 0
	assert.Equal(t, 0, relayStreamResponseHeaderTimeout(&relaycommon.RelayInfo{IsStream: true}))
}
