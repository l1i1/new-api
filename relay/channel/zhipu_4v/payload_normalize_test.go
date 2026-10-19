package zhipu_4v

import (
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// TestConvertOpenAIRequestAppliesScopedPayloadNormalization covers the wiring, not the rewrite
// rules: the rewrite package has its own tests. What matters here is that a scoped request
// reaches requestOpenAI2Zhipu with the stable block already in the system message, and that a
// request outside the token/channel scope leaves the payload untouched.
func TestConvertOpenAIRequestAppliesScopedPayloadNormalization(t *testing.T) {
	setting := operation_setting.GetPayloadNormalizeSetting()
	previous := *setting
	t.Cleanup(func() { *setting = previous })

	document := strings.Repeat("The lake remembers every winter it has seen. ", 200)
	tail := "RAG/工作流真实对话场景RAG/"
	const instruction = "你是一个专业的文档助手，总结文章并输出32token左右的内容。"

	*setting = operation_setting.PayloadNormalizeSetting{
		Enabled:      true,
		TokenIDs:     []int{88},
		ChannelIDs:   []int{7},
		MinUserChars: 1000,
		DocumentChars: func() int {
			return len(document)
		}(),
	}

	buildRequest := func() *dto.GeneralOpenAIRequest {
		system := dto.Message{Role: "system"}
		system.SetStringContent(instruction)
		user := dto.Message{Role: "user"}
		user.SetStringContent(document + tail)
		return &dto.GeneralOpenAIRequest{Model: "glm-5.3-flash", Messages: []dto.Message{system, user}}
	}

	newContext := func() *gin.Context {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
		return ctx
	}

	adaptor := &Adaptor{}

	t.Run("scoped request is rewritten", func(t *testing.T) {
		info := &relaycommon.RelayInfo{TokenId: 88, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 7}}
		converted, err := adaptor.ConvertOpenAIRequest(newContext(), info, buildRequest())
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		out, ok := converted.(*dto.GeneralOpenAIRequest)
		if !ok {
			t.Fatalf("unexpected converted type %T", converted)
		}
		systemContent, _ := out.Messages[0].Content.(string)
		if !strings.HasSuffix(systemContent, document) {
			t.Fatalf("system message must end with the stable block")
		}
		if !strings.HasPrefix(systemContent, instruction) {
			t.Fatalf("system message must keep the original instruction first")
		}
		userContent, _ := out.Messages[1].Content.(string)
		if userContent != tail {
			t.Fatalf("user message = %q, want the unchanged tail", userContent)
		}
	})

	t.Run("unscoped token leaves the payload untouched", func(t *testing.T) {
		info := &relaycommon.RelayInfo{TokenId: 99, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 7}}
		converted, err := adaptor.ConvertOpenAIRequest(newContext(), info, buildRequest())
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		out, ok := converted.(*dto.GeneralOpenAIRequest)
		if !ok {
			t.Fatalf("unexpected converted type %T", converted)
		}
		systemContent, _ := out.Messages[0].Content.(string)
		if systemContent != instruction {
			t.Fatalf("unscoped request must keep the original system message")
		}
		userContent, _ := out.Messages[1].Content.(string)
		if userContent != document+tail {
			t.Fatalf("unscoped request must keep the original user message")
		}
	})
}
