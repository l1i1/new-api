package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 事故复现：2026-10-12 20:29 cdp 投诉（request_id
// 202610121227574862194198268d9d6lEWDlnxW）。
//
// 第 1 跳 ch48 用 HTTP 200 回了一个流内 error 事件，NormalizeOpenAIStreamError
// 把它归一成 502。当时这一跳把 502 钉在客户 writer 上（c.Status），而换道闸门
// 依据 helper.ResponseCommitStateOf 判定"还没有字节写给客户"，于是允许换道；
// 第 2 跳 ch37 以 503 失败、第 3 跳 ch19 真正服务成功并被我方记成功计费，但
// ch19 的首帧把上一跳的 502 当作响应状态发给了客户（客户侧记 502 / 0 token / ¥0）。
// 平台库同 request_id 的三行：20:28:44 type=5 ch48（流内 502 @47.5s）→ 20:29:24
// type=5 ch37（503 @87.4s）→ 20:29:55 type=2 ch19（118s、145201/610 tokens）；
// 入口侧只保留聚合计数（「客户可见 5xx=3」@20:29:42），逐条 access log 未归档。
//
// 因此契约是：**失败的那一跳不得把状态写到客户 writer 上**；状态只应由终局
// （已无重试可走）的错误路径写出。
func TestFailedStreamAttemptDoesNotPoisonLaterSuccessfulResponse(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Writer.Header().Set("Content-Type", "text/event-stream")

	info := func() *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3"},
			IsStream:    true,
			RelayMode:   relayconstant.RelayModeChatCompletions,
			RelayFormat: types.RelayFormatOpenAI,
			DisablePing: true,
		}
	}

	// 第 1 跳：上游 200，流里给的是错误事件。
	_, streamErr := OaiStreamHandler(c, info(), &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"data: {\"error\":{\"message\":\"服务繁忙，请稍后重试。\",\"type\":\"unknown_error\"}}\n\n")),
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
	})
	require.NotNil(t, streamErr)
	require.Equal(t, http.StatusBadGateway, streamErr.StatusCode,
		"2xx 流内 error 事件仍须归一化为 502，错误状态不能丢")

	// 关键断言：这一跳没有把状态钉在客户 writer 上，也没有写出任何字节。
	require.False(t, c.Writer.Written(), "失败的一跳不得向客户写出任何字节")
	require.Equal(t, http.StatusOK, c.Writer.Status(),
		"失败的一跳不得把 5xx 钉在客户 writer 上（会被换道成功后的首帧继承）")
	require.Empty(t, recorder.Body.String())

	// 换道闸门看到的仍是"未提交"，与事故当晚一致：允许换道重试。
	require.Equal(t, helper.ResponseNotCommitted, helper.ResponseCommitStateOf(c))

	// 第 3 跳真正服务成功：首帧写出。
	_, writeErr := c.Writer.Write([]byte(
		"data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"created\":1710000000," +
			"\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n"))
	require.NoError(t, writeErr)

	require.Equal(t, http.StatusOK, recorder.Code,
		"换道成功后客户必须看到 200，而不是上一跳的 502")
	require.Contains(t, recorder.Body.String(), "\"content\":\"hi\"",
		"成功那一跳的正文必须完整送达")
}

// 反面契约：确认修复没有牺牲可见性 —— 若最终无重试可走，错误路径照样能用
// 它自己的 StatusCode 写出 5xx（gin 的状态在第一次写正文时落盘）。
func TestInBandStreamErrorStillYieldsTerminal5xxWhenNothingRetries(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3"},
		IsStream:    true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
		DisablePing: true,
	}
	_, streamErr := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"data: {\"error\":{\"message\":\"服务繁忙，请稍后重试。\",\"type\":\"unknown_error\"}}\n\n")),
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
	})
	require.NotNil(t, streamErr)

	// 终局错误路径（controller/relay.go 的错误 defer）拿到未提交的响应后，
	// 用错误自己的状态码写正文。
	c.Status(streamErr.StatusCode)
	_, _ = c.Writer.Write([]byte(`{"error":{"message":"服务繁忙，请稍后重试。"}}`))
	require.Equal(t, http.StatusBadGateway, recorder.Code,
		"没有重试可走时，终局路径仍应写出 502，可见性不受本次修复影响")
}
