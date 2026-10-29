package openai

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOaiStreamHandlerCoolsChannelAfterRepeatedTruncations pins the 2026-10-29
// gap: a channel that only ever loses the terminal frame never reached the
// cooldown, because every other promotion point sits behind a repair attempt a
// truncation does not have. ch102 kept absorbing 200-280 customer requests per
// minute while 88-98% of them were cut; two unrepaired truncations inside the
// window must take the channel out of selection.
func TestOaiStreamHandlerCoolsChannelAfterRepeatedTruncations(t *testing.T) {
	const channelID = 4242
	common.ClearChannelCooldown(channelID)
	t.Cleanup(func() { common.ClearChannelCooldown(channelID) })

	run := func() {
		c, info, _ := newMissingFinishTestContext(t)
		info.ChannelId = channelID
		body := strings.Join([]string{
			`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"thinking about the task"},"finish_reason":null}]}`,
			`data: [DONE]`,
			``,
		}, "\n")
		_, err := OaiStreamHandler(c, info, &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		})
		require.NotNil(t, err, "a reasoning-only cut stays an error")
	}

	run()
	require.False(t, common.ChannelInCooldown(channelID), "one cut is not a pattern")
	run()
	require.True(t, common.ChannelInCooldown(channelID), "two cuts inside the window must cool the channel")
}

// TestOaiStreamHandlerDoesNotCoolChannelWhenTruncationIsSalvaged is the control:
// the tool-call close delivers the answer to the caller, so the channel does not
// accumulate the non-retryable failures the cooldown exists for.
func TestOaiStreamHandlerDoesNotCoolChannelWhenTruncationIsSalvaged(t *testing.T) {
	const channelID = 4243
	common.ClearChannelCooldown(channelID)
	t.Cleanup(func() { common.ClearChannelCooldown(channelID) })

	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"bash_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	for i := 0; i < 3; i++ {
		c, info, _ := newMissingFinishTestContext(t)
		info.ChannelId = channelID
		_, err := OaiStreamHandler(c, info, &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		})
		require.Nil(t, err)
	}
	assert.False(t, common.ChannelInCooldown(channelID), "a salvaged answer must not cool the channel")
}
