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
	require.Equal(t, http.StatusOK, w.Code)
	// Each SSE event name appears twice: the `event:` line and the JSON `type`.
	require.Equal(t, 2, strings.Count(body, "response.created"))
	require.Equal(t, 2, strings.Count(body, "response.in_progress"))
	createdAt := strings.Index(body, "response.created")
	inProgressAt := strings.Index(body, "response.in_progress")
	errorAt := strings.Index(body, "The engine is currently overloaded")
	require.GreaterOrEqual(t, createdAt, 0)
	require.GreaterOrEqual(t, inProgressAt, 0)
	require.GreaterOrEqual(t, errorAt, 0)
	require.Less(t, createdAt, inProgressAt, "lifecycle events keep their order")
	require.Less(t, inProgressAt, errorAt, "the failure event terminates the replayed stream")
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

	// A retry may still be possible, so the synthetic terminal is only replayed
	// when the relay gives up. Without this replay the client would receive a
	// 200 with nothing but keep-alive pings.
	replay, ok := c.Get(string(constant.ContextKeyResponsesStreamDeferredReplay))
	require.True(t, ok, "a ping-only truncation must install a terminal replay")
	replayFn, ok := replay.(func())
	require.True(t, ok)
	replayFn()
	body := w.Body.String()
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, strings.HasPrefix(body, ping), "replay appends after the committed pings")
	require.Contains(t, body, "response.failed")
	require.Contains(t, body, "data: [DONE]")
	require.Less(t, strings.Index(body, "response.failed"), strings.Index(body, "data: [DONE]"))
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
	require.Equal(t, http.StatusOK, w.Code)
	// Each SSE event name appears twice: the `event:` line and the JSON `type`.
	require.Equal(t, 2, strings.Count(body, "response.created"))
	require.Equal(t, 2, strings.Count(body, "response.in_progress"))
	require.Equal(t, 2, strings.Count(body, "response.failed"))
	require.Equal(t, 1, strings.Count(body, "data: [DONE]"))
	createdAt := strings.Index(body, "response.created")
	inProgressAt := strings.Index(body, "response.in_progress")
	failedAt := strings.Index(body, "response.failed")
	doneAt := strings.Index(body, "data: [DONE]")
	require.GreaterOrEqual(t, createdAt, 0)
	require.Less(t, createdAt, inProgressAt)
	require.Less(t, inProgressAt, failedAt)
	require.Less(t, failedAt, doneAt, "[DONE] terminates the replayed stream")
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

// A deterministic in-band rejection (context window) must keep the pre-buffering
// no-failover behaviour instead of spending the retry budget on every channel.
// The relay's own never-retry keyword rule only inspects 4xx, while every in-band
// stream failure is normalized to 503, so the handler marks it explicitly.
func TestOaiResponsesStreamHandlerMarksDeterministicRejectionNonRetryable(t *testing.T) {
	c, w, info := newResponsesStreamTestContext(t)
	stream := responsesSSEStream(
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"error","sequence_number":2,"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Input too long: 200000 > 131072"}}`,
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(stream)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	require.True(t, types.IsSkipRetryError(apiErr), "a context-window rejection must not fail over")
	require.Empty(t, w.Body.String())

	// The client-visible stream is still replayed when the relay stops.
	replay, ok := c.Get(string(constant.ContextKeyResponsesStreamDeferredReplay))
	require.True(t, ok)
	replayFn, ok := replay.(func())
	require.True(t, ok)
	replayFn()
	body := w.Body.String()
	require.Equal(t, http.StatusOK, w.Code)
	require.Less(t, strings.Index(body, "response.created"), strings.Index(body, "context_length_exceeded"))
}

// Same keep-alive-only situation, but the upstream reports an in-band failure
// instead of truncating: the ping-only writer must still install a replay for the
// error event, otherwise giving up would leave the client with a silent 200.
func TestOaiResponsesStreamHandlerKeepAliveOnlyInBandFailureReplaysError(t *testing.T) {
	c, w, info := newResponsesStreamTestContext(t)
	const ping = ": PING\n\n"
	_, err := c.Writer.Write([]byte(ping))
	require.NoError(t, err)
	helper.RecordKeepAliveBytes(c, len(ping))

	stream := responsesSSEStream(
		`{"type":"error","sequence_number":0,"error":{"type":"api_error","code":"server_is_overloaded","message":"The engine is currently overloaded, please try again later"}}`,
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(stream)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.NotNil(t, apiErr)
	require.False(t, types.IsSkipRetryError(apiErr))
	require.Equal(t, ping, w.Body.String(), "nothing may be written while a retry is possible")

	replay, ok := c.Get(string(constant.ContextKeyResponsesStreamDeferredReplay))
	require.True(t, ok, "a ping-only in-band failure must install a terminal replay")
	replayFn, ok := replay.(func())
	require.True(t, ok)
	replayFn()
	body := w.Body.String()
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, strings.HasPrefix(body, ping))
	require.Contains(t, body, "The engine is currently overloaded")
}
