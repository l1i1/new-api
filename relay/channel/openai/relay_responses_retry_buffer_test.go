package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newResponsesStreamTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		OriginModelName: "kimi-k3.1-pro",
		DisablePing:     true,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3.1-pro"},
	}
	return c, w, info
}

func responsesSSEStream(events ...string) string {
	var body strings.Builder
	for _, event := range events {
		body.WriteString("data: ")
		body.WriteString(event)
		body.WriteString("\n\n")
	}
	return body.String()
}

// Reproduces the production failure: the upstream sends only content-free
// lifecycle events before reporting an engine overload in-band. Because those
// events are held back, nothing reaches the client and the relay can fail over
// to another channel instead of stopping with response_committed.
func TestOaiResponsesStreamHandlerBuffersLifecycleUntilOutputSoPreOutputFailureIsRetryable(t *testing.T) {
	c, w, info := newResponsesStreamTestContext(t)
	stream := responsesSSEStream(
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"error","sequence_number":2,"error":{"type":"api_error","code":"server_is_overloaded","message":"The engine is currently overloaded, please try again later"}}`,
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(stream)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	require.True(t, apiErr.IsUpstreamFailure())
	require.False(t, types.IsSkipRetryError(apiErr), "a pre-output failure must stay retryable")
	require.Empty(t, w.Body.String(), "held-back lifecycle events must not reach the client")
	require.Equal(t, helper.ResponseNotCommitted, helper.ResponseCommitStateOf(c),
		"the retry guard must see an uncommitted response")

	// When the relay runs out of retries it replays the held-back events with the
	// failure, so the client-visible stream matches the pre-buffering behaviour.
	replay, ok := c.Get(string(constant.ContextKeyResponsesStreamDeferredReplay))
	require.True(t, ok, "a pre-output failure must leave a replay for the no-retry case")
	replayFn, ok := replay.(func())
	require.True(t, ok)
	replayFn()
	body := w.Body.String()
	require.Contains(t, body, "response.created")
	require.Contains(t, body, "response.in_progress")
	require.Contains(t, body, "The engine is currently overloaded")
}

// Once the stream produces real output, the held-back lifecycle events are
// released in order before that output, so the client still receives the full
// protocol sequence.
func TestOaiResponsesStreamHandlerFlushesLifecycleEventsAtFirstOutput(t *testing.T) {
	c, w, info := newResponsesStreamTestContext(t)
	stream := responsesSSEStream(
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","sequence_number":2,"delta":"OK"}`,
		`{"type":"response.completed","sequence_number":3,"response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(stream)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	body := w.Body.String()
	createdAt := strings.Index(body, "response.created")
	inProgressAt := strings.Index(body, "response.in_progress")
	deltaAt := strings.Index(body, "response.output_text.delta")
	completedAt := strings.Index(body, "response.completed")
	require.GreaterOrEqual(t, createdAt, 0)
	require.GreaterOrEqual(t, inProgressAt, 0)
	require.GreaterOrEqual(t, deltaAt, 0)
	require.GreaterOrEqual(t, completedAt, 0)
	require.Less(t, createdAt, inProgressAt, "lifecycle events keep their order")
	require.Less(t, inProgressAt, deltaAt, "held-back events precede the first output")
	require.Less(t, deltaAt, completedAt)
	require.Equal(t, helper.ResponsePayloadWritten, helper.ResponseCommitStateOf(c))
}

// A committed stream that only carried keep-alive probes has not delivered any
// payload, so a clean EOF without a terminal event stays retryable and must not
// get the synthetic response.failed event that a payload-committed stream gets.
func TestOaiResponsesStreamHandlerKeepAliveOnlyCommitStaysRetryableOnEOF(t *testing.T) {
	c, w, info := newResponsesStreamTestContext(t)
	const ping = ": PING\n\n"
	_, err := c.Writer.Write([]byte(ping))
	require.NoError(t, err)
	helper.RecordKeepAliveBytes(c, len(ping))
	require.Equal(t, helper.ResponseKeepAliveOnly, helper.ResponseCommitStateOf(c))

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
	require.True(t, apiErr.IsUpstreamFailure())
	require.False(t, types.IsSkipRetryError(apiErr), "a ping-only stream must stay retryable")
	require.Equal(t, ping, w.Body.String(), "no synthetic failed event may be appended")
	require.Equal(t, helper.ResponseKeepAliveOnly, helper.ResponseCommitStateOf(c))
}

// A truncated stream whose only events were content-free lifecycle events stays
// retryable, but the relay still replays its held-back events plus the synthetic
// response.failed when no retry is possible.
func TestOaiResponsesStreamHandlerReplaysLifecycleWithSyntheticFailureOnUncommittedEOF(t *testing.T) {
	c, w, info := newResponsesStreamTestContext(t)
	stream := responsesSSEStream(
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_1","status":"in_progress"}}`,
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(stream)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
	require.False(t, types.IsSkipRetryError(apiErr))
	require.Empty(t, w.Body.String())

	replay, ok := c.Get(string(constant.ContextKeyResponsesStreamDeferredReplay))
	require.True(t, ok)
	replayFn, ok := replay.(func())
	require.True(t, ok)
	replayFn()
	body := w.Body.String()
	require.Contains(t, body, "response.created")
	require.Contains(t, body, "response.in_progress")
	require.Contains(t, body, "response.failed")
	require.Contains(t, body, "data: [DONE]")
}

// Lifecycle events that arrive after real output has been committed must pass
// through immediately: buffering them then has no retry value and would delay
// (or, at EOF, drop) events such as a second output_item.added.
func TestOaiResponsesStreamHandlerForwardsLateLifecycleEventsAfterCommit(t *testing.T) {
	c, w, info := newResponsesStreamTestContext(t)
	stream := responsesSSEStream(
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","sequence_number":1,"delta":"OK"}`,
		`{"type":"response.output_item.added","sequence_number":2,"item":{"id":"msg_2","type":"message","status":"in_progress","content":[]}}`,
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(stream)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)

	// No terminal event: the stream is a committed truncation, so it stays a
	// failure and must not be retried, but every delivered event must be visible.
	require.NotNil(t, apiErr)
	require.True(t, types.IsSkipRetryError(apiErr))
	require.Equal(t, helper.ResponsePayloadWritten, helper.ResponseCommitStateOf(c))
	body := w.Body.String()
	require.Contains(t, body, "response.output_text.delta")
	require.Contains(t, body, "response.output_item.added")
	require.NotContains(t, body, "response.failed")
}
