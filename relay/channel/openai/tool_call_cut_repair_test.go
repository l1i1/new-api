package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	rootcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// observePartialToolCall feeds one unfinished call fragment into the buffer, the
// shape a wall cut leaves behind: the function name arrived, the arguments JSON
// did not close.
func observePartialToolCall(buffer *relaycommon.ChatToolCallBuffer) {
	if buffer == nil {
		return
	}
	index := 0
	buffer.Observe([]dto.ChatCompletionsStreamResponseChoice{{
		Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
			ToolCalls: []dto.ToolCallResponse{{
				Index:    &index,
				ID:       "call_1",
				Type:     "function",
				Function: dto.FunctionResponse{Name: "search", Arguments: `{"q":`},
			}},
		},
	}})
}

// TestBufferedToolCallFragmentDiscardableOnlyWhenHeldAndUndelivered pins the
// safety property behind the wall-cut repair: a fragment may be dropped only when
// the channel's buffer holds it and the caller has not received it. A channel
// without buffering has already streamed the fragment, so it cannot be retracted.
func TestBufferedToolCallFragmentDiscardableOnlyWhenHeldAndUndelivered(t *testing.T) {
	held := &relaycommon.RelayInfo{
		ChannelMeta:    &relaycommon.ChannelMeta{ChannelId: 999},
		ToolCallBuffer: relaycommon.NewChatToolCallBuffer(true),
	}
	observePartialToolCall(held.ToolCallBuffer)

	require.True(t, bufferedToolCallDiscardable(held, 1), "a held fragment can be dropped")

	require.False(t, bufferedToolCallDiscardable(held, 0), "no call, nothing to discard")
	require.False(t, bufferedToolCallDiscardable(nil, 1), "no relay info, no discard")

	delivered := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{ChannelId: 999},
		ToolCallBuffer:     relaycommon.NewChatToolCallBuffer(true),
		ToolCallsDelivered: true,
	}
	observePartialToolCall(delivered.ToolCallBuffer)
	require.False(t, bufferedToolCallDiscardable(delivered, 1), "a call the caller already received is final")

	unbuffered := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 999}}
	require.False(t, bufferedToolCallDiscardable(unbuffered, 1), "without buffering the caller saw the fragment")

	empty := &relaycommon.RelayInfo{
		ChannelMeta:    &relaycommon.ChannelMeta{ChannelId: 999},
		ToolCallBuffer: relaycommon.NewChatToolCallBuffer(true),
	}
	require.False(t, bufferedToolCallDiscardable(empty, 1), "an empty buffer holds nothing to drop")

	discardBufferedToolCallFragment(held)
	require.True(t, held.ToolCallFragmentDiscarded)
	require.True(t, held.ToolCallBuffer.Invalid(), "the held fragment is marked unusable, never delivered")

	// Discarding twice is a no-op: the first call already cleared the state.
	discardBufferedToolCallFragment(held)
	require.True(t, held.ToolCallFragmentDiscarded)
}

// TestRepairCandidatesAcceptHeldToolCallFragment pins the guard relaxation: a
// repair may run while a call is in progress only when the fragment is still held
// by the buffer, and must keep refusing once the caller has received the call.
func TestRepairCandidatesAcceptHeldToolCallFragment(t *testing.T) {
	send := func(string, int) (*http.Response, error) { return nil, nil }
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 999, UpstreamModelName: "kimi-k3.1-pro"},
		IsStream:        true,
		RelayFormat:     types.RelayFormatOpenAI,
		ClientMaxTokens: 64000,
		StreamStatus:    relaycommon.NewStreamStatus(),
		ChatContinuation: &relaycommon.ChatContinuationConfig{
			MaxSteps: 1,
			Send:     send,
			Abnormal: &relaycommon.ChatAbnormalContinuationConfig{MaxSteps: 1, Send: send, MinReasoningChars: 1},
			Missing:  &relaycommon.ChatAbnormalContinuationConfig{MaxSteps: 1, Send: send, MinReasoningChars: 1},
		},
		ToolCallBuffer: relaycommon.NewChatToolCallBuffer(true),
	}
	observePartialToolCall(info.ToolCallBuffer)

	require.True(t, chatMissingVisibleContinuationCandidate(info, &dto.Usage{}, 1200, 1, 1, false),
		"a held fragment does not block the visible-prefix resume")
	require.True(t, chatMissingContinuationCandidate(info, &dto.Usage{}, 0, 500, 1, 1, false),
		"a held fragment does not block the reasoning resume")

	info.StreamFinishReason = "error"
	info.StreamStatus.MarkFailed("upstream_missing_finish_reason", "upstream_error", http.StatusBadGateway)
	require.True(t, chatAbnormalContinuationCandidate(info, &dto.Usage{}, 0, 500, 1, 1),
		"a held fragment does not block the abnormal-finish reasoning resume")

	// A delivered call is final: no resume may append anything after it.
	info.ToolCallsDelivered = true
	info.StreamFinishReason = ""
	require.False(t, chatMissingVisibleContinuationCandidate(info, &dto.Usage{}, 1200, 1, 1, false))
	require.False(t, chatMissingContinuationCandidate(info, &dto.Usage{}, 0, 500, 1, 1, false))
	require.False(t, chatAbnormalContinuationCandidate(info, &dto.Usage{}, 0, 500, 1, 1))

	// Without buffering the caller already saw the fragment: refuse, as before.
	unbuffered := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 999, UpstreamModelName: "kimi-k3.1-pro"},
		IsStream:        true,
		RelayFormat:     types.RelayFormatOpenAI,
		ClientMaxTokens: 64000,
		StreamStatus:    relaycommon.NewStreamStatus(),
		ChatContinuation: &relaycommon.ChatContinuationConfig{
			MaxSteps: 1, Send: send,
			Missing: &relaycommon.ChatAbnormalContinuationConfig{MaxSteps: 1, Send: send, MinReasoningChars: 1},
		},
	}
	require.False(t, chatMissingVisibleContinuationCandidate(unbuffered, &dto.Usage{}, 1200, 1, 1, false))
}

// TestContinuationElapsedCapGrantsHeldToolCallGrace pins the extra wall clock: a
// request cut at the provider wall while a call was in progress spent its whole
// budget on thinking, so the resumed round gets a bounded grace. Every other cut
// keeps the original cap.
func TestContinuationElapsedCapGrantsHeldToolCallGrace(t *testing.T) {
	oldSeconds := rootcommon.ChatContinuationRoundMaxElapsedSeconds
	rootcommon.ChatContinuationRoundMaxElapsedSeconds = 1500
	t.Cleanup(func() { rootcommon.ChatContinuationRoundMaxElapsedSeconds = oldSeconds })

	plain := &relaycommon.RelayInfo{StartTime: time.Now().Add(-1800 * time.Second)}
	require.True(t, continuationElapsedCapReached(plain), "a normal cut keeps the start-anchored cap")

	wallCut := &relaycommon.RelayInfo{
		StartTime:                 time.Now().Add(-1800 * time.Second),
		ToolCallFragmentDiscarded: true,
	}
	require.False(t, continuationElapsedCapReached(wallCut),
		"the wall-cut tool-call shape gets the post-cut grace")

	tooLate := &relaycommon.RelayInfo{
		StartTime:                 time.Now().Add(-2200 * time.Second),
		ToolCallFragmentDiscarded: true,
	}
	require.True(t, continuationElapsedCapReached(tooLate), "the grace itself is bounded")

	zero := &relaycommon.RelayInfo{}
	require.False(t, continuationElapsedCapReached(zero), "no start time, no cap")
}

// TestToolRepairOutcomeIsRecordedInCutDiagnostics pins the observability fix:
// a regeneration attempt that used to leave no trace now names itself on the
// failed row, so "repair_attempted=false" stops meaning "nothing was tried".
func TestToolRepairOutcomeIsRecordedInCutDiagnostics(t *testing.T) {
	info := &relaycommon.RelayInfo{StreamCutDiagnostics: &relaycommon.StreamCutDiagnostics{}}

	recordToolRepairOutcome(info, 2, "tool_repair_failed")

	require.True(t, info.RepairAttempted)
	require.Equal(t, "tool-repair", info.RepairMode)
	require.Equal(t, 2, info.RepairRounds)
	require.Equal(t, "tool_repair_failed", info.RepairFailedReason)
	require.True(t, info.StreamCutDiagnostics.RepairAttempted)
	require.Equal(t, "tool-repair", info.StreamCutDiagnostics.RepairMode)
	require.Equal(t, 2, info.StreamCutDiagnostics.RepairRounds)
	require.Equal(t, "tool_repair_failed", info.StreamCutDiagnostics.RepairFailedReason)

	// A continuation attempt that already named its mode is not overwritten.
	toolCall := &relaycommon.RelayInfo{RepairMode: "abnormal-content"}
	recordToolRepairOutcome(toolCall, 1, "tool_repair_failed")
	require.Equal(t, "abnormal-content", toolCall.RepairMode)
}

// wallCutToolCallRound is the production shape this repair targets: reasoning, a
// short preamble, then a tool call that dies with the provider's failure marker
// at its stream wall.
func wallCutToolCallRound() string {
	return strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"推演"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"前言"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"q\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
}

// resumedToolCallRound is the resumed round: it emits the whole call again, which
// is exactly what a repair is allowed to deliver (never a fabricated argument).
func resumedToolCallRound() string {
	return strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"q\":1}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
}

func newToolCallCutContext(t *testing.T, sender func(string, int) (*http.Response, error), bufferChannel int) (*gin.Context, *relaycommon.RelayInfo, *httptest.ResponseRecorder) {
	t.Helper()
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	oldIDs := rootcommon.ChatToolBufferChannelIDs
	rootcommon.ChatToolBufferChannelIDs = map[int]struct{}{bufferChannel: {}}
	t.Cleanup(func() { rootcommon.ChatToolBufferChannelIDs = oldIDs })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: bufferChannel, UpstreamModelName: "kimi-k3.1-pro"},
		IsStream:        true,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     types.RelayFormatOpenAI,
		DisablePing:     true,
		ClientMaxTokens: 64000,
		StreamStatus:    relaycommon.NewStreamStatus(),
		ChatContinuation: &relaycommon.ChatContinuationConfig{
			MaxSteps: 1,
			Send:     sender,
			Abnormal: &relaycommon.ChatAbnormalContinuationConfig{MaxSteps: 1, Send: sender, MinReasoningChars: 1},
			Missing:  &relaycommon.ChatAbnormalContinuationConfig{MaxSteps: 1, Send: sender, MinReasoningChars: 1},
		},
	}
	info.SetEstimatePromptTokens(5)
	return c, info, recorder
}

// TestOaiStreamHandlerRepairsWallCutHeldToolCall pins the new recovery: a wall
// cut while the model was writing a tool call used to be an unrepairable 502
// (100% of the regeneration attempts failed). When the fragment is still held by
// the buffer, the handler now drops it and resumes the answer, and the resumed
// round's complete call set closes the turn as tool_calls.
func TestOaiStreamHandlerRepairsWallCutHeldToolCall(t *testing.T) {
	calls := 0
	sender := func(string, int) (*http.Response, error) {
		calls++
		return continuationSSEResponse(resumedToolCallRound()), nil
	}
	c, info, recorder := newToolCallCutContext(t, sender, 999)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(wallCutToolCallRound()))
	require.Nil(t, err, "a held fragment cut is repaired instead of failing the request")
	require.NotNil(t, usage)
	require.Equal(t, 1, calls, "one resumed round serves the call")

	body := recorder.Body.String()
	require.Contains(t, body, `"arguments":"{\"q\":1}"`, "the client receives the complete call")
	require.NotContains(t, body, `"arguments":"{\"q\":"`, "the client never receives the half-written fragment")
	require.Contains(t, body, `"finish_reason":"tool_calls"`)
	require.True(t, info.ToolCallFragmentDiscarded, "the held fragment was dropped, not delivered")
	require.True(t, info.ToolCallsDelivered)
}

// TestOaiStreamHandlerStillFailsCutToolCallWhenNotBuffered is the regression
// guard: a channel without tool-call buffering has already streamed the fragment
// to the caller, so the handler must keep failing the request instead of
// appending a second call after bytes the caller already holds.
func TestOaiStreamHandlerStillFailsCutToolCallWhenNotBuffered(t *testing.T) {
	calls := 0
	sender := func(string, int) (*http.Response, error) {
		calls++
		return continuationSSEResponse(resumedToolCallRound()), nil
	}
	// The whitelist names 999, but this request is served by channel 998, so the
	// handler builds no tool-call buffer and the fragment went straight out.
	c, info, _ := newToolCallCutContext(t, sender, 999)
	info.ChannelId = 998

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(wallCutToolCallRound()))
	require.NotNil(t, err, "an unretractable fragment still fails the request")
	require.NotNil(t, usage)
	require.Zero(t, usage.TotalTokens, "an unrepaired cut stays unbilled")
	require.Zero(t, calls, "no repair may run on a fragment the caller already received")
	require.False(t, info.ToolCallFragmentDiscarded)
}

// resumedRoundThatDies is a resume round that delivers text and then dies on the
// provider's failure marker: the shape whose early return used to skip the cut
// diagnostics snapshot entirely.
func resumedRoundThatDies() string {
	return strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"续"},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
}

// TestRepairThatDiesStillCarriesCutDiagnostics pins the observability fix: the
// abnormal-finish branch returns before the later snapshot, so before this fix
// half of the production type=5 rows had no admin_info.stream_cut and read as
// "no repair was even tried". The failed row must now say which repair ran and
// why it lost.
func TestRepairThatDiesStillCarriesCutDiagnostics(t *testing.T) {
	sender := func(string, int) (*http.Response, error) {
		return continuationSSEResponse(resumedRoundThatDies()), nil
	}
	c, info, _ := newToolCallCutContext(t, sender, 999)

	_, err := OaiStreamHandler(c, info, continuationSSEResponse(wallCutToolCallRoundNoCall()))
	require.NotNil(t, err, "a resume round that dies is still a failure")

	require.NotNil(t, info.StreamCutDiagnostics, "the failed row must carry the cut diagnostics")
	require.True(t, info.StreamCutDiagnostics.RepairAttempted, "the attempt that ran is named")
	require.Equal(t, "abnormal-content", info.StreamCutDiagnostics.RepairMode)
	require.Equal(t, 1, info.StreamCutDiagnostics.RepairRounds)
	require.NotEmpty(t, info.StreamCutDiagnostics.RepairFailedReason)
}

// wallCutToolCallRoundNoCall is the same wall cut without a call fragment, so the
// abnormal-content branch runs and its own resume round is what dies.
func wallCutToolCallRoundNoCall() string {
	return strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"reasoning_content":"推演"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"前言"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"error"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
}

// wallCutWithFragmentAsLastChunk ends right on the argument fragment: the
// provider dies without a terminal frame, so the handler's held tail IS the
// half-written fragment. The discarded fragment must not be released by the
// held-tail flush.
func wallCutWithFragmentAsLastChunk() string {
	return strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"前言"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"q\":"}}]},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
}

// TestOaiStreamHandlerNeverReleasesTheDiscardedFragmentTail pins the second half
// of the safety property: even when the held tail is the fragment itself, the
// repair must strip it instead of flushing half an arguments document.
func TestOaiStreamHandlerNeverReleasesTheDiscardedFragmentTail(t *testing.T) {
	sender := func(string, int) (*http.Response, error) {
		return continuationSSEResponse(resumedToolCallRound()), nil
	}
	c, info, recorder := newToolCallCutContext(t, sender, 999)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(wallCutWithFragmentAsLastChunk()))
	require.Nil(t, err, "a missing-finish cut with a held fragment is repaired")
	require.NotNil(t, usage)

	body := recorder.Body.String()
	require.Contains(t, body, `"arguments":"{\"q\":1}"`, "the complete call reaches the caller")
	require.NotContains(t, body, `"arguments":"{\"q\":"`,
		"the half-written fragment is stripped from the held tail")
	require.True(t, info.ToolCallFragmentDiscarded)
}
