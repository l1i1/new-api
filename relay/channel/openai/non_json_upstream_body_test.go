package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression (2026-09-11, ch33 DEF_deepsb): a non-stream request whose upstream
// answered 200 with a body that is not JSON (SSE frames, or an HTML/text error
// page) failed with `invalid character 'd' looking for beginning of value`.
// The error is classed as bad_response_body, which used to be hardcoded as
// never-retry, so the client got a bare 500 for what is really a channel
// failure that another channel could serve.
//
// The handler itself must keep reporting it as a bad upstream body; whether it
// retries is decided in controller.shouldRetry, pinned by
// TestRetryKeywordIsBoundedByHardGates. Together they assert the full chain:
// non-JSON upstream body -> bad_response_body -> retryable.
func TestNonJSONUpstreamBodyIsRetryableChannelError(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-v4-pro", ChannelId: 33, ChannelType: 1},
		OriginModelName: "deepseek-v4-pro",
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     types.RelayFormatOpenAI,
	}

	// The exact failure shape: the body begins with "data:" so json decoding
	// reports `invalid character 'd'`.
	body := "data: {\"id\":\"c1\",\"choices\":[]}\n\n"

	_, apiErr := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	})
	require.NotNil(t, apiErr, "a non-JSON upstream body must fail")
	assert.Equal(t, types.ErrorCodeBadResponseBody, apiErr.GetErrorCode())
	assert.Equal(t, http.StatusInternalServerError, apiErr.StatusCode)
	assert.False(t, types.IsSkipRetryError(apiErr),
		"a malformed upstream body must stay retryable so another channel can serve it")
	assert.Contains(t, apiErr.Error(), "invalid character")
}
