package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// runResponsesStreamHandler feeds raw SSE text (no implicit [DONE]) through the
// real Responses stream handler and also returns what the client received.
func runResponsesStreamHandler(t *testing.T, sse string) (*dto.Usage, *types.NewAPIError, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	service.InitTokenEncoders()
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "responses-truncation-test")
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-6-astra",
		DisablePing:     true,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-6-astra"},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(sse)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
	return usage, apiErr, w.Body.String()
}

func responsesSSE(events ...string) string {
	var body strings.Builder
	for _, event := range events {
		body.WriteString("data: ")
		body.WriteString(event)
		body.WriteString("\n\n")
	}
	return body.String()
}

// A stream that produced partial output and then closed without any terminal
// event reproduces the client's "stream closed before response.completed".
func TestOaiResponsesStreamTruncatedAfterPartialOutputIsFailure(t *testing.T) {
	sse := responsesSSE(
		`{"type":"response.created","response":{"status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"Hel"}`,
		`{"type":"response.output_text.delta","delta":"lo"}`,
	)
	usage, apiErr, clientBody := runResponsesStreamHandler(t, sse)

	require.NotNil(t, apiErr, "a stream without a terminal event must not be recorded as success")
	require.True(t, apiErr.IsUpstreamFailure())
	require.False(t, apiErr.IsEmptyOutput())
	require.True(t, apiErr.ShouldEvictChannelAffinity())
	require.NotNil(t, usage)
	require.Greater(t, usage.CompletionTokens, 0, "partial output must remain auditable")
	// Delivered content stays committed; no synthetic failure event is injected.
	require.NotContains(t, clientBody, "response.failed")
}

// The pre-existing zero-output truncation keeps its empty-output semantics and
// still injects a synthetic response.failed event for the client.
func TestOaiResponsesStreamTruncatedWithoutOutputIsEmptyFailure(t *testing.T) {
	sse := responsesSSE(
		`{"type":"response.created","response":{"status":"in_progress"}}`,
	)
	_, apiErr, clientBody := runResponsesStreamHandler(t, sse)

	require.NotNil(t, apiErr)
	require.True(t, apiErr.IsEmptyOutput())
	require.False(t, apiErr.IsUpstreamFailure())
	require.True(t, apiErr.ShouldEvictChannelAffinity())
	require.Contains(t, clientBody, "response.failed")
}

// A well-formed stream ending in response.completed must stay a success; the
// Responses protocol sends no [DONE] marker, so the terminal event is the only
// reliable completion signal.
func TestOaiResponsesStreamCompletedIsSuccess(t *testing.T) {
	sse := responsesSSE(
		`{"type":"response.created","response":{"status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"ok"}`,
		`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`,
	)
	_, apiErr, _ := runResponsesStreamHandler(t, sse)

	require.Nil(t, apiErr, "a completed Responses stream must remain a success")
}
