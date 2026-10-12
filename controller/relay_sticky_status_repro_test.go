package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 复现 2026-10-12 20:27:57 的 cdp 投诉（request_id
// 202610121227574862194198268d9d6lEWDlnxW）：
//
// 第 1 跳（ch48）HTTP 200 + 流内错误事件 —— service.NormalizeOpenAIStreamError
// 把 200 归一成 502（service/error.go:198-200），当时 relay-openai.go:262-264 在
// 没有任何字节写给客户端时用 c.Status(502) 把这个状态钉在 gin 的 writer 上；
// 重试闸门只看 helper.ResponseCommitStateOf（controller/relay.go:938），此时是
// ResponseNotCommitted ⇒ 允许换道；而全仓没有任何一处复位 writer 的状态。
// 于是第 3 跳（ch19）真正服务成功、我们照常记账，但它的首字节把**上一次失败
// 尝试的 502** 作为状态行刷给了客户端。
//
// 客户端（cdp 的 new-api）收到 502 + 非 JSON 的 SSE body，回落到兜底文案
// "bad response status code 502"，记 0 token / ¥0；我们这侧记成功并计费。
//
// 修复（relay/channel/openai/relay-openai.go）删掉了那处 c.Status：失败的一跳只
// 把错误交回重试循环，状态交给终局错误路径写。本用例随之改为**正向断言** ——
// 失败跳不得在客户 writer 上留下任何状态/字节，换道成功后的首帧必须是 200。
// handler 层的同源用例见 relay/channel/openai/stream_status_commit_test.go。
func TestFailedStreamAttemptMustNotCommitItsStatusToTheClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	c.Writer.Header().Set("Content-Type", "text/event-stream")

	// 第 1 跳：上游 HTTP 200，但流里给的是一个错误事件。
	streamErr := service.NormalizeOpenAIStreamError(
		[]byte(`{"error":{"message":"服务繁忙，请稍后重试。","type":"unknown_error"}}`),
		http.StatusOK,
	)
	require.NotNil(t, streamErr, "an in-band error event must surface as an error")
	require.Equal(t, http.StatusBadGateway, streamErr.StatusCode,
		"NormalizeOpenAIStreamError maps a 2xx transport status to 502")

	// 修复后的契约：失败的一跳只把错误交回重试循环，绝不在客户 writer 上留状态。
	// gin 的状态直到第一次写正文才落盘，钉在这里就会被换道成功后的首帧继承。
	require.False(t, c.Writer.Written(), "失败的一跳不得向客户写出任何字节")
	require.Equal(t, http.StatusOK, c.Writer.Status(),
		"失败的一跳不得把 5xx 钉在客户 writer 上")
	require.Empty(t, recorder.Body.String())

	// controller/relay.go:938 的换道闸门：什么都没写给客户端，仍允许重试。
	require.Equal(t, helper.ResponseNotCommitted, helper.ResponseCommitStateOf(c),
		"with no bytes on the wire the retry gate must still allow a channel swap")

	// 第 3 跳服务成功，写出第一帧。
	_, err := c.Writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, recorder.Code,
		"a request that a later channel serves must not be published under the failed attempt's status")
	assert.Contains(t, recorder.Body.String(), `"content":"ok"`,
		"换道成功那一跳的正文必须完整送达")
}
