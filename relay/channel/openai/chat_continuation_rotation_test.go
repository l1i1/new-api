package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// rotationPlan scripts a multi-round continuation: channelIDs[i] is the channel
// that serves round i (round 0 is the original channel served by Send, every
// later round is picked by Fallback), and bodies[id] is that channel's SSE body.
type rotationPlan struct {
	channelIDs []int
	bodies     map[int]string
	maxRounds  int
	clientMax  int
	startAgo   time.Duration
	noFallback bool

	// observed
	fallbackCalls int
	sentChannels  []int
	reason        string
}

// newRotationContext wires ChatContinuation exactly like the relay does: Send is
// the original channel's sender, Fallback selects the next untried channel and
// records it, and both report every round on RoundChannel and Tried.
func newRotationContext(t *testing.T, plan *rotationPlan) (*gin.Context, *relaycommon.RelayInfo, *httptest.ResponseRecorder) {
	t.Helper()
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	tried := &relaycommon.TriedChannels{}
	roundChannel := &relaycommon.ContinuationRoundChannel{}
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3.1-pro"},
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
		ClientMaxTokens:    plan.clientMax,
		ChatContinuation: &relaycommon.ChatContinuationConfig{
			MaxSteps:     plan.maxRounds,
			MaxRounds:    plan.maxRounds,
			RoundChannel: roundChannel,
			Tried:        tried,
		},
	}
	info.ChannelId = plan.channelIDs[0]
	if plan.startAgo > 0 {
		info.StartTime = time.Now().Add(-plan.startAgo)
	}
	info.SetEstimatePromptTokens(5)

	serve := func(id int) (*http.Response, error) {
		plan.sentChannels = append(plan.sentChannels, id)
		body, ok := plan.bodies[id]
		if !ok {
			return nil, relaycommon.ErrNoContinuationCandidate
		}
		return continuationSSEResponse(body), nil
	}
	info.ChatContinuation.Send = func(string, int) (*http.Response, error) {
		tried.Add(plan.channelIDs[0])
		roundChannel.Set(plan.channelIDs[0])
		return serve(plan.channelIDs[0])
	}
	// The relay layer only enables the repair modes when the channel is whitelisted;
	// the handler-side candidates gate on the mode config, so the test opts in the
	// same way the relay does.
	info.ChatContinuation.Missing = &relaycommon.ChatAbnormalContinuationConfig{
		MaxSteps:     plan.maxRounds,
		MaxRounds:    plan.maxRounds,
		RoundChannel: roundChannel,
		Tried:        tried,
	}
	info.ChatContinuation.Fallback = func(string, int) (*http.Response, error) {
		plan.fallbackCalls++
		if plan.noFallback {
			return nil, relaycommon.ErrNoContinuationCandidate
		}
		for _, id := range plan.channelIDs[1:] {
			if tried.Contains(id) {
				continue
			}
			tried.Add(id)
			roundChannel.Set(id)
			return serve(id)
		}
		return nil, relaycommon.ErrNoContinuationCandidate
	}
	info.ChatContinuation.Missing.Send = info.ChatContinuation.Send
	info.ChatContinuation.Missing.Fallback = info.ChatContinuation.Fallback
	return c, info, recorder
}

// cutRound returns an SSE body that delivered text and then died without a
// terminal finish_reason, the shape every unrepaired stream cut leaves behind.
func cutRound(id, text string, completion int) string {
	return strings.Join([]string{
		`data: {"id":"` + id + `","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"` + text + `"},"finish_reason":null}]}`,
		`data: {"id":"` + id + `","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":5,"completion_tokens":` + itoa(completion) + `,"total_tokens":` + itoa(5+completion) + `}}`,
		`data: [DONE]`,
		``,
	}, "\n")
}

// completeRound returns an SSE body that delivers text and a clean terminal frame.
func completeRound(id, text string, completion int) string {
	return strings.Join([]string{
		`data: {"id":"` + id + `","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"` + text + `"},"finish_reason":null}]}`,
		`data: {"id":"` + id + `","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":` + itoa(completion) + `,"total_tokens":` + itoa(5+completion) + `}}`,
		`data: [DONE]`,
		``,
	}, "\n")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestContinuationRotatesToSecondChannel pins the core requirement: a cut on the
// original channel is repaired on a DIFFERENT channel, not by another round on
// the same one. The client sees one stream, one terminal frame, the text exactly
// once, and the repair records both channels.
func TestContinuationRotatesToSecondChannel(t *testing.T) {
	plan := &rotationPlan{
		channelIDs: []int{201, 202},
		bodies: map[int]string{
			201: cutRound("a", "（A续）", 2),
			202: completeRound("b", "第二段", 3),
		},
		maxRounds: 3,
		clientMax: 64000,
	}
	cutBody := cutRound("a0", "第一段", 2)
	plan.bodies[201] = cutRound("a1", "（A续）", 2)
	c, info, recorder := newRotationContext(t, plan)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(cutBody))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 1, plan.fallbackCalls, "round 2 must rotate to the second channel")
	require.Equal(t, []int{201, 202}, plan.sentChannels)

	body := recorder.Body.String()
	require.Equal(t, 1, strings.Count(body, "第一段"), "prefix text is delivered once")
	require.Equal(t, 1, strings.Count(body, "（A续）"))
	require.Equal(t, 1, strings.Count(body, "第二段"))
	require.Equal(t, 1, strings.Count(body, `"finish_reason":"stop"`), "one terminal frame")
	require.Equal(t, 1, strings.Count(body, "data: [DONE]"), "one [DONE]")
	require.Equal(t, []int{201, 202}, info.RepairChannels, "repair diagnostics name the rotation")
	require.Equal(t, 2, info.RepairRounds)
	require.Empty(t, info.RepairFailedReason)
}

// TestContinuationRotatesAgainWhenSecondChannelAlsoCuts pins that every round
// rotates: the second channel cutting does not stop the repair, the third round
// goes to a third channel and succeeds.
func TestContinuationRotatesAgainWhenSecondChannelAlsoCuts(t *testing.T) {
	plan := &rotationPlan{
		channelIDs: []int{201, 202, 203},
		bodies: map[int]string{
			201: cutRound("a", "（A续）", 2),
			202: cutRound("b", "（B续）", 2),
			203: completeRound("c", "第三段", 3),
		},
		maxRounds: 3,
		clientMax: 64000,
	}
	cutBody := cutRound("a0", "第一段", 2)
	c, info, recorder := newRotationContext(t, plan)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(cutBody))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 2, plan.fallbackCalls)
	require.Equal(t, []int{201, 202, 203}, plan.sentChannels)
	require.Contains(t, recorder.Body.String(), "第三段")
	require.Equal(t, []int{201, 202, 203}, info.RepairChannels)
	require.Empty(t, info.RepairFailedReason, "a recovery on the last budgeted round is a success, not rounds_exhausted")
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `"finish_reason":"stop"`))
}

// TestContinuationStopsAfterRoundsExhausted pins the failure contract: every
// round cut, the round budget is spent, the request fails honestly and the
// diagnostics say why (rounds_exhausted) instead of looping.
func TestContinuationStopsAfterRoundsExhausted(t *testing.T) {
	plan := &rotationPlan{
		channelIDs: []int{201, 202},
		bodies: map[int]string{
			201: cutRound("a", "（A续）", 2),
			202: cutRound("b", "（B续）", 2),
		},
		maxRounds: 2,
		clientMax: 64000,
	}
	cutBody := cutRound("a0", "第一段", 2)
	c, info, _ := newRotationContext(t, plan)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(cutBody))
	require.Nil(t, err, "a content-bearing cut closes as an honest truncation, not a 502")
	require.NotNil(t, usage)
	require.Equal(t, []int{201, 202}, plan.sentChannels)
	require.Equal(t, "rounds_exhausted", info.RepairFailedReason)
	require.Equal(t, []int{201, 202}, info.RepairChannels)
}

// TestContinuationDoesNotRotateWhenClientBudgetExhausted pins the client-budget
// gate: the caller capped its own output, so the repair must stop and the caller
// is closed with finish_reason=length instead of spending another channel.
func TestContinuationDoesNotRotateWhenClientBudgetExhausted(t *testing.T) {
	plan := &rotationPlan{
		channelIDs: []int{201, 202},
		bodies: map[int]string{
			201: cutRound("a", "（A续）", 50),
			202: completeRound("b", "不该出现", 3),
		},
		maxRounds: 3,
		clientMax: 50,
	}
	cutBody := cutRound("a0", "第一段", 50)
	c, info, recorder := newRotationContext(t, plan)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(cutBody))
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 0, plan.fallbackCalls, "the client budget forbids another round")
	body := recorder.Body.String()
	require.Contains(t, body, `"finish_reason":"length"`)
	require.NotContains(t, body, "不该出现")
}

// TestContinuationDoesNotRotateInsideToolCall pins the tool-call gate: half a
// tool call is not a resumable answer, so no other channel is asked.
func TestContinuationDoesNotRotateInsideToolCall(t *testing.T) {
	toolCut := strings.Join([]string{
		`data: {"id":"a","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expr\":\"1+"}}]},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	plan := &rotationPlan{
		channelIDs: []int{201, 202},
		bodies: map[int]string{
			201: toolCut,
			202: completeRound("b", "不该出现", 3),
		},
		maxRounds: 3,
		clientMax: 64000,
	}
	c, info, recorder := newRotationContext(t, plan)

	_, err := OaiStreamHandler(c, info, continuationSSEResponse(plan.bodies[201]))
	require.NotNil(t, err)
	require.Equal(t, 0, plan.fallbackCalls)
	require.NotContains(t, recorder.Body.String(), "不该出现")
}

// TestContinuationNoCandidateIsNotRetried pins the exhausted-rotation sentinel:
// when no other channel exists the repair ends with no_candidate and does not
// keep asking.
func TestContinuationNoCandidateIsNotRetried(t *testing.T) {
	plan := &rotationPlan{
		channelIDs: []int{201},
		bodies: map[int]string{
			201: cutRound("a", "（A续）", 2),
		},
		maxRounds:  3,
		clientMax:  64000,
		noFallback: true,
	}
	cutBody := cutRound("a0", "第一段", 2)
	c, info, _ := newRotationContext(t, plan)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(cutBody))
	require.Nil(t, err, "a content-bearing cut closes as an honest truncation, not a 502")
	require.NotNil(t, usage)
	require.Equal(t, 1, plan.fallbackCalls, "one attempt to find a candidate, then stop")
	require.Equal(t, "no_candidate", info.RepairFailedReason)
}

// TestContinuationStopsAtElapsedCap pins the wall-clock gate: past the cap the
// repair stops even when channels remain, instead of spending another full wall.
func TestContinuationStopsAtElapsedCap(t *testing.T) {
	plan := &rotationPlan{
		channelIDs: []int{201, 202, 203},
		bodies: map[int]string{
			201: cutRound("a", "（A续）", 2),
			202: cutRound("b", "（B续）", 2),
			203: completeRound("c", "第三段", 3),
		},
		maxRounds: 3,
		clientMax: 64000,
		startAgo:  2 * time.Hour,
	}
	cutBody := cutRound("a0", "第一段", 2)
	c, info, _ := newRotationContext(t, plan)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(cutBody))
	require.Nil(t, err, "a content-bearing cut closes as an honest truncation, not a 502")
	require.NotNil(t, usage)
	require.Equal(t, 0, plan.fallbackCalls, "past the cap no new channel is asked")
	require.Equal(t, "elapsed_cap", info.RepairFailedReason)
}
