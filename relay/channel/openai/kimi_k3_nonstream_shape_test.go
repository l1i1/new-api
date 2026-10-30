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
)

// TestOpenaiHandlerK3NonStreamKeepsOfficialUsageShape pins the 2026-10-30
// production report: the same body streamed and non-streamed must both carry the
// official K3 usage shape. Non-stream used to ship the aggregator's extensions
// (claude_cache_creation_*, input_tokens, prompt_tokens_details.text_tokens...)
// while the stream was emitted in the official shape.
func TestOpenaiHandlerK3NonStreamKeepsOfficialUsageShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	requestBody := `{"model":"kimi-k3.1-pro","stream":false,"max_tokens":2048,"messages":[{"role":"user","content":"Hi"}]}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
	c.Request.Header.Set("Content-Type", "application/json")

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3.1-pro"},
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
		Request:     &dto.GeneralOpenAIRequest{Model: "kimi-k3.1-pro", Messages: []dto.Message{{Role: "user", Content: "Hi"}}},
		UserSetting: dto.UserSetting{OfficialFit: &dto.OfficialFitConfig{
			Profile: map[string]dto.OfficialFitProfile{"kimi-k3": {Shape: true}},
		}},
	}
	upstream := `{"id":"chat_1","object":"chat.completion","created":1,"model":"kimi-k3.1-pro",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"Hi!","reasoning_content":"thinking"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":88,"completion_tokens":74,"total_tokens":162,` +
		`"prompt_tokens_details":{"cached_tokens":0,"text_tokens":0,"audio_tokens":0,"image_tokens":0},` +
		`"completion_tokens_details":{"text_tokens":0,"audio_tokens":0,"image_tokens":0,"reasoning_tokens":50},` +
		`"input_tokens":0,"output_tokens":0,"input_tokens_details":null,` +
		`"claude_cache_creation_5_m_tokens":0,"claude_cache_creation_1_h_tokens":0}}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(upstream)),
	}

	if _, apiErr := OpenaiHandler(c, info, resp); apiErr != nil {
		t.Fatalf("handler returned an error: %v", apiErr)
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"claude_cache_creation", "input_tokens_details", `"text_tokens"`, `"audio_tokens"`, `"image_tokens"`, `"input_tokens"`, `"output_tokens"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("client body still carries the aggregator extension %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `"cache_write_tokens"`) {
		t.Fatalf("official usage shape must carry prompt_tokens_details.cache_write_tokens: %s", body)
	}
}
