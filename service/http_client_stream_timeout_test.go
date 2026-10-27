package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStreamResponseHeaderTimeoutClientVariants guards the two budgets the relay
// needs: the long general one for non-streaming attempts, and the tighter
// streaming one that turns a silent upstream into a retryable transport failure.
func TestStreamResponseHeaderTimeoutClientVariants(t *testing.T) {
	withRelayHTTPTransportSettings(t)
	prevGeneral := common.RelayResponseHeaderTimeout
	prevStream := common.RelayStreamResponseHeaderTimeout
	common.RelayResponseHeaderTimeout = 3600
	common.RelayStreamResponseHeaderTimeout = 120
	t.Cleanup(func() {
		common.RelayResponseHeaderTimeout = prevGeneral
		common.RelayStreamResponseHeaderTimeout = prevStream
		ResetProxyClientCache()
	})

	assert.Equal(t, 3600*time.Second, newRelayHTTPTransportWithResponseHeaderTimeout(common.RelayResponseHeaderTimeout).ResponseHeaderTimeout)
	assert.Equal(t, 120*time.Second, newRelayHTTPTransportWithResponseHeaderTimeout(common.RelayStreamResponseHeaderTimeout).ResponseHeaderTimeout)

	stream, err := GetHttpClientWithProxySettingsAndResponseHeaderTimeout("", dto.ChannelSettings{}, common.RelayStreamResponseHeaderTimeout)
	require.NoError(t, err)
	streamAgain, err := GetHttpClientWithProxySettingsAndResponseHeaderTimeout("", dto.ChannelSettings{}, common.RelayStreamResponseHeaderTimeout)
	require.NoError(t, err)
	assert.Same(t, stream, streamAgain, "the streaming client must be cached per (proxy, policy, budget)")

	streamTransport, ok := stream.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 120*time.Second, streamTransport.ResponseHeaderTimeout)

	if httpClient != nil {
		assert.NotSame(t, httpClient, stream, "the streaming budget must not replace the shared default client")
	}
}

// TestStreamResponseHeaderTimeoutBoundsHeadersOnly pins the contract that
// matters to the relay: the budget aborts only the wait for response headers. A
// stream whose headers arrive in time must keep streaming for as long as it
// needs, or every long reasoning stream would be cut at the header budget.
func TestStreamResponseHeaderTimeoutBoundsHeadersOnly(t *testing.T) {
	withRelayHTTPTransportSettings(t)
	streamClient, err := GetHttpClientWithProxySettingsAndResponseHeaderTimeout("", dto.ChannelSettings{}, 1)
	require.NoError(t, err)

	t.Run("silent upstream fails at the header budget", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-release
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(func() {
			close(release)
			srv.Close()
		})

		req, err := http.NewRequest(http.MethodPost, srv.URL, nil)
		require.NoError(t, err)
		start := time.Now()
		_, err = streamClient.Do(req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timeout awaiting response headers")
		assert.Less(t, time.Since(start), 5*time.Second)
	})

	t.Run("headers in time keep the body unbounded", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			time.Sleep(1500 * time.Millisecond)
			_, _ = w.Write([]byte("ok"))
		}))
		t.Cleanup(srv.Close)

		req, err := http.NewRequest(http.MethodPost, srv.URL, nil)
		require.NoError(t, err)
		resp, err := streamClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "ok", string(body))
	})
}
