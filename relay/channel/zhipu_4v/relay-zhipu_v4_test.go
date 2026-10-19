package zhipu_4v

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/pkg/payloadnorm"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestOpenAI2ZhipuPreservesOfficialFitFields(t *testing.T) {
	body := `{
		"model": "glm-5.3",
		"messages": [{"role": "user", "content": "hi"}],
		"response_format": {"type": "json_object"},
		"stop": ["end"],
		"reasoning_effort": "low",
		"top_p": 1.0
	}`
	var req dto.GeneralOpenAIRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))

	out := requestOpenAI2Zhipu(req)

	require.NotNil(t, out.ResponseFormat)
	assert.Equal(t, "json_object", out.ResponseFormat.Type)
	assert.Equal(t, []string{"end"}, out.Stop)
	assert.Equal(t, "low", out.ReasoningEffort)
	require.NotNil(t, out.TopP)
	assert.Equal(t, 1.0, *out.TopP)
}

func TestRequestOpenAI2ZhipuCoercesStopShapes(t *testing.T) {
	cases := []struct {
		name string
		stop any
		want []string
	}{
		{"string", "end", []string{"end"}},
		{"array of strings", []string{"a", "b"}, []string{"a", "b"}},
		{"decoded json array", []any{"a", "b"}, []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := dto.GeneralOpenAIRequest{
				Model:    "glm-5.3",
				Messages: []dto.Message{{Role: "user", Content: "hi"}},
				Stop:     tc.stop,
			}
			out := requestOpenAI2Zhipu(req)
			assert.Equal(t, tc.want, out.Stop)
		})
	}
}

// TestConvertOpenAIRequestLeavesPayloadLayoutAlone is the single-owner contract: payload
// normalization moved to relay.compatibleHandler (relay/payload_normalize_hook.go) so that
// every OpenAI-format channel is rewritten identically. This adaptor must therefore forward
// the message layout untouched even when the installed option would scope the token and
// channel, otherwise the same request could be rewritten twice or only for official traffic.
func TestConvertOpenAIRequestLeavesPayloadLayoutAlone(t *testing.T) {
	const systemPrompt = "你是一个专业的文档助手，总结文章并输出32token左右的内容。"
	const tail = "RAG/工作流真实对话场景"
	document := strings.Repeat("The lake remembers every winter it has seen. ", 200)
	require.NoError(t, payloadnorm.InstallSettingJSON(
		`{"enabled":true,"token_ids":[88],"channel_ids":[7],"min_user_chars":1000,"document_chars":`+strconv.Itoa(len(document))+`}`))
	t.Cleanup(payloadnorm.InstallDisabled)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	systemMessage := dto.Message{Role: "system"}
	systemMessage.SetStringContent(systemPrompt)
	userMessage := dto.Message{Role: "user"}
	userMessage.SetStringContent(document + tail)
	request := &dto.GeneralOpenAIRequest{Model: "glm-5.3-flash", Messages: []dto.Message{systemMessage, userMessage}}

	out, err := (&Adaptor{}).ConvertOpenAIRequest(ctx, &relaycommon.RelayInfo{
		TokenId:     88,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 7},
	}, request)
	require.NoError(t, err)
	converted, ok := out.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)

	require.Len(t, converted.Messages, 2)
	require.Equal(t, systemPrompt, converted.Messages[0].Content)
	require.Equal(t, document+tail, converted.Messages[1].Content)
}
