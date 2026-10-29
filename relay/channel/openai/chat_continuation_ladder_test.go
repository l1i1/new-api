package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

const ladderFirstRound = `data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"前半段"},"finish_reason":null}]}
data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}
data: [DONE]

`

const ladderEmptyRound = `data: [DONE]

`

const ladderCompleteRound = `data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"补完的正文"},"finish_reason":null}]}
data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}
data: [DONE]

`

// ladderContext wires a missing-content repair with the full retry ladder:
// original body on the original channel, instruction body on the same channel,
// then the same two forms after a channel switch.
func ladderContext(t *testing.T, same, sameInstruction, cross, crossInstruction func(string, int) (*http.Response, error)) (*gin.Context, *relaycommon.RelayInfo, *httptest.ResponseRecorder) {
	t.Helper()
	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return nil, http.ErrHandlerTimeout
	}, 1, 64000, 1<<20)
	info.ChatContinuation.MaxSteps = 1
	info.ChatContinuation.MaxRounds = 1
	info.ChatContinuation.Send = same
	info.ChatContinuation.SendInstruction = sameInstruction
	info.ChatContinuation.Fallback = cross
	info.ChatContinuation.FallbackInstruction = crossInstruction
	info.ChatContinuation.Missing = &relaycommon.ChatAbnormalContinuationConfig{
		MaxSteps: 1, Send: info.ChatContinuation.Abnormal.Send, MaxReasoningBytes: 1 << 20, MinReasoningChars: 1,
	}
	info.ChatContinuation.Abnormal = nil
	return c, info, recorder
}

func ladderResponse(body string) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// TestContinuationLadderTriesNativeThenInstructionOnSameChannel pins the first two
// rungs: the original (native prefill) body is attempted first, and only when it
// produces nothing does the instruction-style body run — still on the original
// channel, before any channel switch.
func TestContinuationLadderTriesNativeThenInstructionOnSameChannel(t *testing.T) {
	var order []string
	c, info, recorder := ladderContext(t,
		func(string, int) (*http.Response, error) {
			order = append(order, "same-native")
			return ladderResponse(ladderEmptyRound)
		},
		func(string, int) (*http.Response, error) {
			order = append(order, "same-instruction")
			return ladderResponse(ladderCompleteRound)
		},
		func(string, int) (*http.Response, error) {
			order = append(order, "cross-native")
			return ladderResponse(ladderCompleteRound)
		},
		func(string, int) (*http.Response, error) {
			order = append(order, "cross-instruction")
			return ladderResponse(ladderCompleteRound)
		},
	)

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(ladderFirstRound))
	if err != nil {
		t.Fatalf("the instruction rung should finish the answer: %v", err)
	}
	if usage == nil || usage.CompletionTokens != 13 {
		t.Fatalf("both rounds billed once: %+v", usage)
	}
	if len(order) != 2 || order[0] != "same-native" || order[1] != "same-instruction" {
		t.Fatalf("retry priority broken: %v", order)
	}
	if !strings.Contains(recorder.Body.String(), "补完的正文") {
		t.Fatalf("instruction rung content missing: %s", recorder.Body.String())
	}
}

// TestContinuationLadderSwitchesChannelOnlyAfterSameChannelRungs pins the third
// and fourth rungs: after both same-channel bodies produce nothing, the round
// continues on another channel (native first, then instruction).
func TestContinuationLadderSwitchesChannelOnlyAfterSameChannelRungs(t *testing.T) {
	var order []string
	c, info, recorder := ladderContext(t,
		func(string, int) (*http.Response, error) {
			order = append(order, "same-native")
			return ladderResponse(ladderEmptyRound)
		},
		func(string, int) (*http.Response, error) {
			order = append(order, "same-instruction")
			return ladderResponse(ladderEmptyRound)
		},
		func(string, int) (*http.Response, error) {
			order = append(order, "cross-native")
			return ladderResponse(ladderCompleteRound)
		},
		func(string, int) (*http.Response, error) {
			order = append(order, "cross-instruction")
			return ladderResponse(ladderCompleteRound)
		},
	)

	if _, err := OaiStreamHandler(c, info, continuationSSEResponse(ladderFirstRound)); err != nil {
		t.Fatalf("the cross-channel rung should finish the answer: %v", err)
	}
	if len(order) != 3 || order[2] != "cross-native" {
		t.Fatalf("channel switch must come after both same-channel rungs: %v", order)
	}
	if !strings.Contains(recorder.Body.String(), "补完的正文") {
		t.Fatal("cross-channel content missing")
	}
}

// TestContinuationLadderCrossInstructionIsTheLastRung pins the last rung: when the
// cross-channel native body also produces nothing, the instruction body runs there
// before the repair is declared failed.
func TestContinuationLadderCrossInstructionIsTheLastRung(t *testing.T) {
	var order []string
	c, info, _ := ladderContext(t,
		func(string, int) (*http.Response, error) {
			order = append(order, "same-native")
			return ladderResponse(ladderEmptyRound)
		},
		func(string, int) (*http.Response, error) {
			order = append(order, "same-instruction")
			return ladderResponse(ladderEmptyRound)
		},
		func(string, int) (*http.Response, error) {
			order = append(order, "cross-native")
			return ladderResponse(ladderEmptyRound)
		},
		func(string, int) (*http.Response, error) {
			order = append(order, "cross-instruction")
			return ladderResponse(ladderCompleteRound)
		},
	)

	if _, err := OaiStreamHandler(c, info, continuationSSEResponse(ladderFirstRound)); err != nil {
		t.Fatalf("the last rung should finish the answer: %v", err)
	}
	want := []string{"same-native", "same-instruction", "cross-native", "cross-instruction"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("retry ladder order = %v, want %v", order, want)
	}
}

// TestToolRegenerationTriesSameChannelFirst pins the tool-call regeneration
// ladder: the original channel is re-run before any channel switch, and the
// cross-channel hop only happens when the same-channel call set is unusable.
func TestToolRegenerationTriesSameChannelFirst(t *testing.T) {
	enableToolBuffer(t, 901, true, true)
	cut := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expr\":"}}]},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	good := `{"id":"chat_9","created":9,"model":"kimi-k3.1-pro","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_9","type":"function","function":{"name":"calculator","arguments":"{\"expr\":\"2+2\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`
	bad := `{"id":"chat_8","created":8,"model":"kimi-k3.1-pro","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_8","type":"function","function":{"name":"calculator","arguments":"{\"expr\":"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`

	var order []string
	c, info, recorder := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return nil, http.ErrHandlerTimeout
	}, 1, 64000, 1<<20)
	info.ChannelId = 901
	info.ChatContinuation.Abnormal = nil
	info.ChatContinuation.RegenerateSame = func(int) (*http.Response, error) {
		order = append(order, "same")
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(bad))}, nil
	}
	info.ChatContinuation.Regenerate = func(int) (*http.Response, error) {
		order = append(order, "cross")
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(good))}, nil
	}

	if _, err := OaiStreamHandler(c, info, continuationSSEResponse(cut)); err != nil {
		t.Fatalf("the cross-channel rung should recover the call: %v", err)
	}
	if strings.Join(order, ",") != "same,cross" {
		t.Fatalf("regeneration ladder order = %v, want same then cross", order)
	}
	if !strings.Contains(recorder.Body.String(), "2+2") {
		t.Fatal("regenerated call missing")
	}
}

// TestToolRegenerationStopsAtSameChannel pins the priority: a usable call set from
// the original channel must not spend the cross-channel hop.
func TestToolRegenerationStopsAtSameChannel(t *testing.T) {
	enableToolBuffer(t, 902, true, true)
	cut := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expr\":"}}]},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	good := `{"id":"chat_9","created":9,"model":"kimi-k3.1-pro","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_9","type":"function","function":{"name":"calculator","arguments":"{\"expr\":\"3+3\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`

	crossCalls := 0
	c, info, _ := newAbnormalContinuationTestContext(t, func(string, int) (*http.Response, error) {
		return nil, http.ErrHandlerTimeout
	}, 1, 64000, 1<<20)
	info.ChannelId = 902
	info.ChatContinuation.Abnormal = nil
	info.ChatContinuation.RegenerateSame = func(int) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(good))}, nil
	}
	info.ChatContinuation.Regenerate = func(int) (*http.Response, error) {
		crossCalls++
		return nil, io.EOF
	}

	if _, err := OaiStreamHandler(c, info, continuationSSEResponse(cut)); err != nil {
		t.Fatalf("same-channel regeneration should succeed: %v", err)
	}
	if crossCalls != 0 {
		t.Fatalf("cross-channel hop must not run after a usable same-channel rung, calls=%d", crossCalls)
	}
}
