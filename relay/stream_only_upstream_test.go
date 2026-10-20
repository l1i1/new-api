package relay

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// capturedUpstream is the request the fake upstream actually received. The
// point of the wiring tests is the outbound body: a channel flagged
// stream_only_upstream must be called with stream=true even though the client
// asked for a non-streaming response.
type capturedUpstream struct {
	path string
	body []byte
}

func newStreamOnlyUpstreamServer(t *testing.T, captured *capturedUpstream) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured.path = r.URL.Path
		captured.body = body
		// Mimic WorkBuddy: it rejects a non-streaming request before the stream
		// starts. The relay must surface this untouched (status + message), not
		// swallow it into the buffering path.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":11101,"msg":"Non-stream chat request is currently not supported"}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func newStreamOnlyUpstreamContext(t *testing.T, baseURL, model string, other dto.ChannelOtherSettings) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(
		`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, model)))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	common.SetContextKey(c, constant.ContextKeyChannelId, 1)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, baseURL)
	common.SetContextKey(c, constant.ContextKeyChannelKey, "test-key")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, model)
	common.SetContextKey(c, constant.ContextKeyChannelOtherSetting, other)

	request := &dto.GeneralOpenAIRequest{
		Model:    model,
		Messages: []dto.Message{{Role: "user", Content: "hi"}},
	}
	return c, relaycommon.GenRelayInfoOpenAI(c, request)
}

// A channel declaring stream_only_upstream receives the client's non-streaming
// request as an upstream stream, and a pre-stream upstream error keeps its
// status code and message.
func TestTextHelperStreamOnlyUpstreamForcesUpstreamStream(t *testing.T) {
	var captured capturedUpstream
	server := newStreamOnlyUpstreamServer(t, &captured)
	c, info := newStreamOnlyUpstreamContext(t, server.URL, "workbuddy-chat", dto.ChannelOtherSettings{
		StreamOnlyUpstream: true,
	})

	apiErr := TextHelper(c, info)
	require.NotNil(t, apiErr, "the upstream error must not be swallowed")
	require.Equal(t, http.StatusBadRequest, apiErr.StatusCode, "upstream status must be preserved")
	require.Contains(t, apiErr.Error(), "Non-stream chat request is currently not supported")

	require.Equal(t, "/v1/chat/completions", captured.path)
	var upstreamRequest map[string]any
	require.NoError(t, common.Unmarshal(captured.body, &upstreamRequest))
	require.Equal(t, true, upstreamRequest["stream"],
		"a stream-only channel must be called with stream=true")
	streamOptions, ok := upstreamRequest["stream_options"].(map[string]any)
	require.True(t, ok, "stream_options must be sent so the buffered stream reports usage")
	require.Equal(t, true, streamOptions["include_usage"])
}

// Regression: without the flag the outbound body is unchanged - no stream key,
// no stream_options - so every existing channel keeps its old behavior.
func TestTextHelperWithoutStreamOnlyUpstreamKeepsRequestBody(t *testing.T) {
	var captured capturedUpstream
	server := newStreamOnlyUpstreamServer(t, &captured)
	c, info := newStreamOnlyUpstreamContext(t, server.URL, "workbuddy-chat", dto.ChannelOtherSettings{})

	apiErr := TextHelper(c, info)
	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)

	var upstreamRequest map[string]any
	require.NoError(t, common.Unmarshal(captured.body, &upstreamRequest))
	_, hasStream := upstreamRequest["stream"]
	require.False(t, hasStream, "unflagged channels must not be rewritten to stream=true")
	_, hasStreamOptions := upstreamRequest["stream_options"]
	require.False(t, hasStreamOptions, "unflagged channels must not gain stream_options")
}

// The same model on two channels: only the flagged channel is rewritten, which
// is the per-channel contract the feature promises.
func TestTextHelperStreamOnlyUpstreamIsPerChannel(t *testing.T) {
	tests := []struct {
		name       string
		other      dto.ChannelOtherSettings
		wantStream bool
	}{
		{name: "flagged channel", other: dto.ChannelOtherSettings{StreamOnlyUpstream: true}, wantStream: true},
		{name: "unflagged channel", other: dto.ChannelOtherSettings{}, wantStream: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured capturedUpstream
			server := newStreamOnlyUpstreamServer(t, &captured)
			c, info := newStreamOnlyUpstreamContext(t, server.URL, "workbuddy-chat", tt.other)

			_ = TextHelper(c, info)

			var upstreamRequest map[string]any
			require.NoError(t, common.Unmarshal(captured.body, &upstreamRequest))
			stream, _ := upstreamRequest["stream"].(bool)
			require.Equal(t, tt.wantStream, stream)
		})
	}
}
