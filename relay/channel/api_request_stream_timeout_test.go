package channel

import (
	"testing"

	common2 "github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
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

// TestRelayStreamResponseHeaderTimeoutOptionOverride pins the operator-facing
// hot override: general_setting.stream_first_byte_timeout_seconds replaces the
// boot-time RELAY_STREAM_RESPONSE_HEADER_TIMEOUT for streaming attempts, needs no
// release, and 0 falls back to the environment value. Clearing it must restore
// the environment value, which is what "收紧回去" does without a deploy.
func TestRelayStreamResponseHeaderTimeoutOptionOverride(t *testing.T) {
	previousEnv := common2.RelayStreamResponseHeaderTimeout
	previousOption := operation_setting.GetGeneralSetting().StreamFirstByteTimeoutSeconds
	t.Cleanup(func() {
		common2.RelayStreamResponseHeaderTimeout = previousEnv
		operation_setting.GetGeneralSetting().StreamFirstByteTimeoutSeconds = previousOption
	})

	common2.RelayStreamResponseHeaderTimeout = 300
	operation_setting.GetGeneralSetting().StreamFirstByteTimeoutSeconds = 0
	assert.Equal(t, 300, relayStreamResponseHeaderTimeout(&relaycommon.RelayInfo{IsStream: true}))

	operation_setting.GetGeneralSetting().StreamFirstByteTimeoutSeconds = 420
	assert.Equal(t, 420, relayStreamResponseHeaderTimeout(&relaycommon.RelayInfo{IsStream: true}))
	// The option only widens streaming attempts; non-streaming keeps the general
	// client so an adaptor that answers headers late is not cut off.
	assert.Equal(t, 0, relayStreamResponseHeaderTimeout(&relaycommon.RelayInfo{IsStream: false}))

	// A negative option is not a budget: it must not disable the env value.
	operation_setting.GetGeneralSetting().StreamFirstByteTimeoutSeconds = -1
	assert.Equal(t, 300, relayStreamResponseHeaderTimeout(&relaycommon.RelayInfo{IsStream: true}))

	operation_setting.GetGeneralSetting().StreamFirstByteTimeoutSeconds = 0
	common2.RelayStreamResponseHeaderTimeout = 0
	assert.Equal(t, 0, relayStreamResponseHeaderTimeout(&relaycommon.RelayInfo{IsStream: true}))
}
