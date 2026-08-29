package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMappedStreamErrorContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	return c, w
}

func mappedBadResponseError(statusCode int) *types.NewAPIError {
	err := types.InitOpenAIError(types.ErrorCodeBadResponseStatusCode, statusCode)
	err.SetMessage("bad response status code 524")
	return err
}

func TestSendMappedStreamErrorWritesOpenAIErrorEvent(t *testing.T) {
	c, w := newMappedStreamErrorContext(t)
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAI,
	}

	// 上游错误被 status_code_mapping 从 524 映射为 200:SDK 无 SSE 错误事件时客户端读到空流
	sendMappedStreamError(c, info, mappedBadResponseError(http.StatusOK))

	require.True(t, c.Writer.Written(), "SSE error event should be written to the stream")
	body := w.Body.String()
	assert.Contains(t, body, `data: {"error"`)
	assert.Contains(t, body, `bad response status code 524`)
	assert.Contains(t, body, `data: [DONE]`)
}

func TestSendMappedStreamErrorSkipsNonStream(t *testing.T) {
	c, w := newMappedStreamErrorContext(t)
	info := &relaycommon.RelayInfo{
		IsStream:    false,
		RelayFormat: types.RelayFormatOpenAI,
	}

	sendMappedStreamError(c, info, mappedBadResponseError(http.StatusOK))

	assert.False(t, c.Writer.Written(), "non-stream request must not get an SSE error event")
	assert.Empty(t, w.Body.String())
}

func TestSendMappedStreamErrorSkipsNonMappedErrorStatus(t *testing.T) {
	c, w := newMappedStreamErrorContext(t)
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAI,
	}

	// 未映射的错误保留 524(newApiErr.StatusCode=524) → 不应写 SSE,交给 defer 写标准 JSON 错误
	sendMappedStreamError(c, info, mappedBadResponseError(http.StatusGatewayTimeout))

	assert.False(t, c.Writer.Written())
	assert.Empty(t, w.Body.String())
}

func TestSendMappedStreamErrorSkipsAfterWriterCommit(t *testing.T) {
	c, w := newMappedStreamErrorContext(t)
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAI,
	}
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Write([]byte("committed"))

	sendMappedStreamError(c, info, mappedBadResponseError(http.StatusOK))

	// writer 已提交: 不应再写重复错误事件
	assert.Equal(t, "committed", w.Body.String())
}

func TestSendMappedStreamErrorWritesClaudeErrorEvent(t *testing.T) {
	c, w := newMappedStreamErrorContext(t)
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		RelayFormat: types.RelayFormatClaude,
	}

	sendMappedStreamError(c, info, mappedBadResponseError(http.StatusOK))

	require.True(t, c.Writer.Written())
	body := w.Body.String()
	assert.Contains(t, body, "event: error")
	assert.Contains(t, body, `"type":"error"`)
}

func TestSendMappedStreamErrorWritesGeminiErrorEvent(t *testing.T) {
	c, w := newMappedStreamErrorContext(t)
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		RelayFormat: types.RelayFormatGemini,
	}

	sendMappedStreamError(c, info, mappedBadResponseError(http.StatusOK))

	require.True(t, c.Writer.Written())
	body := w.Body.String()
	assert.Contains(t, body, `"error":{"code":`)
	assert.Contains(t, body, `"status":"UPSTREAM_ERROR"`)
}

func TestSendMappedStreamErrorSkipsNilError(t *testing.T) {
	c, w := newMappedStreamErrorContext(t)
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAI,
	}

	sendMappedStreamError(c, info, nil)

	assert.False(t, c.Writer.Written())
	assert.Empty(t, w.Body.String())
}
