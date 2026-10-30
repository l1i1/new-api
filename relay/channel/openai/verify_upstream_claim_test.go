package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 验证上游说法：最后一帧同时包含 finish_reason 和 usage，且客户端 include_usage=false 时，
// 网关是否会过滤掉该帧、导致下游只剩正文+[DONE]（即丢失 finish_reason）。
func runStream(t *testing.T, info *relaycommon.RelayInfo, body string) (string, error) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("token_id", 1)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	_, err := OaiStreamHandler(c, info, resp)
	return recorder.Body.String(), err
}

func TestVerifyClaimUsageSharesFinalChunk(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name    string
		body    string
		k3fit   bool
		wantErr bool
	}{
		{
			name: "generic: finish_reason + top-level usage in one frame",
			body: strings.Join([]string{
				`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"test-model","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null}]}`,
				`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
				`data: [DONE]`,
				``,
			}, "\n"),
		},
		{
			name: "k3 native: finish_reason + choices[0].usage in one frame (fit on)",
			body: strings.Join([]string{
				`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"reasoning_content":"thinking"},"finish_reason":null}]}`,
				`data: {"id":"chat_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop","usage":{"prompt_tokens":89,"completion_tokens":72,"total_tokens":161}}]}`,
				`data: [DONE]`,
				``,
			}, "\n"),
			k3fit: true,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
				IsStream:           true,
				RelayMode:          relayconstant.RelayModeChatCompletions,
				RelayFormat:        types.RelayFormatOpenAI,
				ShouldIncludeUsage: false, // 客户端 include_usage=false
				DisablePing:        true,
			}
			if tt.k3fit {
				info.ChannelMeta.UpstreamModelName = "kimi-k3"
				info.OriginModelName = "kimi-k3"
				info.ClientIncludeUsage = false
				info.UserSetting = dto.UserSetting{
					OfficialFit: &dto.OfficialFitConfig{
						Profile: map[string]dto.OfficialFitProfile{"kimi-k3": {Shape: true}},
					},
				}
			}
			out, err := runStream(t, info, tt.body)
			t.Logf("handler err=%v", err)
			t.Logf("downstream: %s", out)
			require.False(t, tt.wantErr)
			require.Contains(t, out, `"finish_reason":"stop"`, "下游必须保留终止帧")
			require.Equal(t, 1, strings.Count(out, `"finish_reason":"stop"`))
		})
	}
}
