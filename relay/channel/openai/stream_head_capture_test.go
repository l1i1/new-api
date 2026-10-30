package openai

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// TestStreamHeadTeeCapturesPrefix pins the capture contract: the prefix is
// bounded, the byte count keeps counting past the bound, and a truncated body is
// marked so the row cannot be read as the whole upstream answer.
func TestStreamHeadTeeCapturesPrefix(t *testing.T) {
	tee := newStreamHeadTee(16)
	n, err := tee.Write([]byte("first-part-"))
	require.NoError(t, err)
	assert.Equal(t, len("first-part-"), n, "the tee must report every byte it saw")
	_, err = tee.Write([]byte("second-part-and-more"))
	require.NoError(t, err)

	assert.Equal(t, int64(len("first-part-second-part-and-more")), tee.bytesRead())
	assert.Equal(t, "first-part-secon …", tee.head())
	assert.LessOrEqual(t, utf8.RuneCountInString(tee.head()), 18, "the captured prefix stays inside the bound")
}

func TestStreamHeadTeeEmptyBody(t *testing.T) {
	tee := newStreamHeadTee(streamHeadCaptureLimit)
	assert.Zero(t, tee.bytesRead())
	assert.Empty(t, tee.head(), "an upstream that wrote nothing must leave both fields empty")
}

// TestStreamHeadTeeFlattensControlBytes keeps the failure row on one readable
// line: an SSE body or an HTML error page must not inject newlines into the log.
func TestStreamHeadTeeFlattensControlBytes(t *testing.T) {
	tee := newStreamHeadTee(streamHeadCaptureLimit)
	_, _ = tee.Write([]byte("data: {\"error\":\"boom\"}\n\ndata: [DONE]\n\x00\x07"))
	head := tee.head()
	assert.NotContains(t, head, "\n")
	assert.NotContains(t, head, "\x00")
	assert.Contains(t, head, "data: {\"error\":\"boom\"}")
}

// TestStreamHeadTeeWrapForwardsClose proves the wrapper does not leak the
// response body: the handler's cleanup still runs on the real ReadCloser.
func TestStreamHeadTeeWrapForwardsClose(t *testing.T) {
	inner := &closeRecorder{Reader: strings.NewReader("payload")}
	tee := newStreamHeadTee(streamHeadCaptureLimit)
	wrapped := tee.wrap(inner)

	got, err := io.ReadAll(wrapped)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(got))
	assert.Equal(t, int64(len("payload")), tee.bytesRead())

	require.NoError(t, wrapped.Close())
	assert.True(t, inner.closed, "the wrapped body must still close the upstream response")
}

type closeRecorder struct {
	io.Reader
	closed bool
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return nil
}

// TestAttachStreamHeadOnlyForEmptyCuts keeps answer text out of the failure row:
// a cut that delivered a prefix is explained by the counters, and the captured
// bytes would be the caller's own content.
func TestAttachStreamHeadOnlyForEmptyCuts(t *testing.T) {
	tee := newStreamHeadTee(streamHeadCaptureLimit)
	_, _ = tee.Write([]byte("partial answer"))

	delivered := &relaycommon.StreamCutDiagnostics{VisibleContentChars: 12}
	attachStreamHead(delivered, tee)
	assert.Empty(t, delivered.UpstreamHead)
	assert.Zero(t, delivered.UpstreamBytes)

	reasoning := &relaycommon.StreamCutDiagnostics{ReasoningChars: 40}
	attachStreamHead(reasoning, tee)
	assert.Empty(t, reasoning.UpstreamHead, "a reasoning-only cut already has content")

	empty := &relaycommon.StreamCutDiagnostics{MissingFinish: true}
	attachStreamHead(empty, tee)
	assert.Equal(t, "partial answer", empty.UpstreamHead)
	assert.Equal(t, int64(len("partial answer")), empty.UpstreamBytes)

	attachStreamHead(nil, tee)
	attachStreamHead(empty, nil)
}

// TestOaiStreamHandlerRecordsUpstreamHeadOnZeroEventCut is the regression this
// exists for: an upstream that answers HTTP 200 with a body this handler cannot
// turn into a single event used to leave only "upstream stream ended without a
// terminal finish_reason" in the row, with no way to tell an empty body from a
// JSON refusal. 2026-10-30 hit that across six upstreams, official included.
func TestOaiStreamHandlerRecordsUpstreamHeadOnZeroEventCut(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantHead string
	}{
		{
			name:     "json_error_behind_http_200",
			body:     `{"error":{"message":"The engine is currently overloaded, please try again later","type":"server_error"}}`,
			wantHead: `{"error":{"message":"The engine is currently overloaded, please try again later","type":"server_error"}}`,
		},
		{
			name:     "nothing_at_all",
			body:     "",
			wantHead: "",
		},
		{
			name:     "leading_role_frame_then_eof",
			body:     `data: {"id":"chat_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n",
			wantHead: "data: {",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, info, _ := newMissingFinishTestContext(t)

			_, err := OaiStreamHandler(c, info, &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(tc.body)),
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			})

			require.NotNil(t, err, "a stream with no answer payload must not be reported as success")
			require.NotNil(t, info.StreamCutDiagnostics, "the row must carry the cut diagnostics")
			require.True(t, info.StreamCutDiagnostics.MissingFinish)
			assert.Zero(t, info.StreamCutDiagnostics.VisibleContentChars)
			assert.Equal(t, int64(len(tc.body)), info.StreamCutDiagnostics.UpstreamBytes, "the byte count separates an empty body from an unparsed one")
			if tc.wantHead == "" {
				assert.Empty(t, info.StreamCutDiagnostics.UpstreamHead)
			} else {
				assert.Contains(t, info.StreamCutDiagnostics.UpstreamHead, tc.wantHead)
			}
		})
	}
}

// TestOaiStreamHandlerSkipsUpstreamHeadWhenContentDelivered is the negative
// guard: a cut after a delivered prefix must not start storing answer bytes.
func TestOaiStreamHandlerSkipsUpstreamHeadWhenContentDelivered(t *testing.T) {
	c, info, _ := newMissingFinishTestContext(t)

	body := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"partial answer"},"finish_reason":null}]}`,
		``,
	}, "\n")

	_, err := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	require.NotNil(t, err)
	require.NotNil(t, info.StreamCutDiagnostics)
	assert.NotZero(t, info.StreamCutDiagnostics.VisibleContentChars)
	assert.Empty(t, info.StreamCutDiagnostics.UpstreamHead, "delivered answer text must not be copied into the failure row")
	assert.Zero(t, info.StreamCutDiagnostics.UpstreamBytes)
}
