package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// 本地媒体 token 估算只是预扣费，结算用上游 usage 补扣。所以「取不到媒体」不能把
// 客户请求升级成 500：2026-10-22 00:07 / 00:09 生产上两条 500 就是 64MB 媒体 URL
// 让 CountRequestToken 返回错误（token_counter.go 旧代码 `if shouldFetchFiles {
// return 0, err }`）导致的，客户白等 8.6s / 53.3s 后拿到 500。
//
// 这里用一个必然失败的 URL（"http://" 无 host）触发下载错误，覆盖两条分支：
//  1. 类型未知时的探测失败 —— 保持未知类型，走 switch 默认平摊值；
//  2. 已知图片类型的尺寸测量失败 —— 用 imageTokenFallback 平摊。
func newMediaTokenCountContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", http.NoBody)
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "gpt-4o")
	return c
}

func withMediaTokenCounting(t *testing.T) {
	t.Helper()
	previousGetMediaToken, previousNotStream := constant.GetMediaToken, constant.GetMediaTokenNotStream
	constant.GetMediaToken = true
	// 流式请求会走本地媒体测量（默认非流不测）。
	constant.GetMediaTokenNotStream = false
	t.Cleanup(func() {
		constant.GetMediaToken = previousGetMediaToken
		constant.GetMediaTokenNotStream = previousNotStream
	})
}

func TestCountRequestTokenMediaProbeFailureKeepsFlatCharge(t *testing.T) {
	withMediaTokenCounting(t)

	c := newMediaTokenCountContext(t)
	meta := &types.TokenCountMeta{
		TokenType:   types.TokenTypeTextNumber,
		CombineText: "hi",
		Files: []*types.FileMeta{
			types.NewFileMeta("", types.NewURLFileSource("http://")),
		},
	}
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, IsStream: true}

	tokens, err := CountRequestToken(c, meta, info)
	assert.NoError(t, err, "媒体探测失败不得让请求失败")
	// 文本 2 + OpenAI 格式固定 3 + 未知类型默认 4096
	assert.GreaterOrEqual(t, tokens, 4096, "未知媒体仍按默认平摊值计费")
}

func TestCountRequestTokenImageMeasureFailureFallsBackToFlatEstimate(t *testing.T) {
	withMediaTokenCounting(t)

	c := newMediaTokenCountContext(t)
	meta := &types.TokenCountMeta{
		TokenType:   types.TokenTypeTextNumber,
		CombineText: "hi",
		Files: []*types.FileMeta{
			types.NewImageFileMeta(types.NewURLFileSource("http://"), "high"),
		},
	}
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, IsStream: true}

	tokens, err := CountRequestToken(c, meta, info)
	assert.NoError(t, err, "图像尺寸测量失败不得让请求失败")
	assert.GreaterOrEqual(t, tokens, imageTokenFallback, "图像估算失败按保守平摊值计费")
}
