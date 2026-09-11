package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// opencodeSessionParamOverride mirrors the production DEF_OpenCode-GO_A channel
// override: the upstream console rejects a request whose session header cannot
// be resolved, so the channel copies the client Authorization as the last
// fallback. A probe must therefore present Authorization just like real relay
// traffic does.
const opencodeSessionParamOverride = `{
  "operations": [
    {"mode": "copy_header", "keep_origin": true, "from": "X-Opencode-Session", "to": "X-Opencode-Session"},
    {"mode": "copy_header", "keep_origin": true, "from": "Session-Id", "to": "X-Opencode-Session"},
    {"mode": "copy_header", "keep_origin": true, "from": "Session_id", "to": "X-Opencode-Session"},
    {"mode": "copy_header", "keep_origin": true, "from": "X-Conversation-Id", "to": "X-Opencode-Session"},
    {"mode": "copy_header", "keep_origin": true, "from": "X-Claude-Code-Session-Id", "to": "X-Opencode-Session"},
    {"mode": "copy_header", "keep_origin": true, "from": "Authorization", "to": "X-Opencode-Session"}
  ]
}`

func newProbeContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("Content-Type", "application/json")
	return ctx
}

func TestSeedProbeAuthorizationHeaderFromChannelKey(t *testing.T) {
	ctx := newProbeContext(t)
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "sk-probe-key")

	seedProbeAuthorizationHeader(ctx)

	assert.Equal(t, "Bearer sk-probe-key", ctx.Request.Header.Get("Authorization"))
}

func TestSeedProbeAuthorizationHeaderKeepsExistingValue(t *testing.T) {
	ctx := newProbeContext(t)
	ctx.Request.Header.Set("Authorization", "Bearer client-supplied")
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "sk-probe-key")

	seedProbeAuthorizationHeader(ctx)

	assert.Equal(t, "Bearer client-supplied", ctx.Request.Header.Get("Authorization"))
}

// TestChannelProbeResolvesSessionHeaderFromSeededAuthorization is the
// regression guard: without the seeded Authorization the copy_header fallback
// never fires and the upstream rejects the probe with
// "Request is missing x-opencode-session".
func TestChannelProbeResolvesSessionHeaderFromSeededAuthorization(t *testing.T) {
	ctx := newProbeContext(t)
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "sk-probe-key")
	seedProbeAuthorizationHeader(ctx)

	requestHeaders := make(map[string]string, len(ctx.Request.Header))
	for name, values := range ctx.Request.Header {
		if len(values) > 0 {
			requestHeaders[name] = values[0]
		}
	}

	var paramOverride map[string]any
	require.NoError(t, common.UnmarshalJsonStr(opencodeSessionParamOverride, &paramOverride))

	info := &relaycommon.RelayInfo{
		RequestHeaders: requestHeaders,
		ChannelMeta: &relaycommon.ChannelMeta{
			ParamOverride:   paramOverride,
			HeadersOverride: map[string]any{"*": true},
		},
	}

	_, err := relaycommon.ApplyParamOverrideWithRelayInfo([]byte(`{"model":"deepseek-v4-flash","messages":[]}`), info)
	require.NoError(t, err)

	assert.Equal(t, "Bearer sk-probe-key", info.RuntimeHeadersOverride["x-opencode-session"])
}
