package channel

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoRequestCapturesUpstreamRequestBytes pins the size the request log
// reports as admin_info.body_bytes.upstream_request: doRequest takes it from the
// outbound request's ContentLength before issuing the attempt, so each retry
// overwrites the previous size with the attempt that actually answered.
func TestDoRequestCapturesUpstreamRequestBytes(t *testing.T) {
	service.InitHttpClient()
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/relay", nil)

	body := []byte(`{"model":"kimi-k3","stream":true}`)
	req, err := http.NewRequest(http.MethodPost, upstream.URL, bytes.NewReader(body))
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}

	resp, err := doRequest(ctx, req, info)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.EqualValues(t, len(body), info.UpstreamRequestBytes)
	assert.Equal(t, req.ContentLength, info.UpstreamRequestBytes)
}

// TestDoRequestLeavesUpstreamRequestBytesUnknownWithoutBody keeps a bodyless
// attempt from inventing a size: ContentLength 0 means "unknown", which the log
// writer omits instead of storing a bogus 0.
func TestDoRequestLeavesUpstreamRequestBytesUnknownWithoutBody(t *testing.T) {
	service.InitHttpClient()
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/relay", nil)

	req, err := http.NewRequest(http.MethodGet, upstream.URL, http.NoBody)
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, UpstreamRequestBytes: 0}

	resp, err := doRequest(ctx, req, info)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Zero(t, info.UpstreamRequestBytes)
}
