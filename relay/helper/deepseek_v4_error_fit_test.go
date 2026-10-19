package helper

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// fitImageRequest builds a chat request whose first (or second) user turn
// carries a remote image part.
func fitImageRequest(messages ...dto.Message) *dto.GeneralOpenAIRequest {
	return &dto.GeneralOpenAIRequest{
		Model:    "deepseek-v4.1-flash-1017",
		Messages: messages,
	}
}

func fitImageMessage(url string) dto.Message {
	return dto.Message{
		Role: "user",
		Content: []any{
			map[string]any{"type": "text", "text": "what is in this image?"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}},
		},
	}
}

func fitErrorContext(t *testing.T, info *relaycommon.RelayInfo) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if info != nil {
		common.SetContextKey(c, constant.ContextKeyRelayInfoPtr, info)
	}
	return c
}

func fitChatInfo(request dto.Request) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "deepseek-v4.1-flash-1017",
		Request:         request,
	}
}

func TestFitDeepSeekV4ImageDownloadErrorNormalizesProviderFailures(t *testing.T) {
	request := fitImageRequest(fitImageMessage("https://example.com/x.png"))
	c := fitErrorContext(t, fitChatInfo(request))

	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		message, ok := FitDeepSeekV4ImageDownloadError(c, status, "Model provider request failed; contact the site operator")
		if !ok {
			t.Fatalf("status %d: provider failure must normalize", status)
		}
		if !strings.HasPrefix(message, ".messages[0].image[0]: Failed to download image from https://example.com/x.png (request_id: ") {
			t.Fatalf("status %d: message = %q, want the official download-failure text", status, message)
		}
		// The marker set the strict-fit writer keys off must recognize the
		// normalized text, otherwise it would be rendered as a gateway error.
		if !IsStrictFitValidationMessage(message) {
			t.Fatalf("status %d: normalized message must be a strict-fit validation message", status)
		}
	}
}

func TestFitDeepSeekV4ImageDownloadErrorUsesTheImagePosition(t *testing.T) {
	request := fitImageRequest(
		dto.Message{Role: "system", Content: "be brief"},
		fitImageMessage("https://example.com/first.png"),
		fitImageMessage("https://example.com/second.png"),
	)
	c := fitErrorContext(t, fitChatInfo(request))
	// Note: each image message has one text part followed by one image part, so
	// the image index within the message is 0 for both.
	message, ok := FitDeepSeekV4ImageDownloadError(c, http.StatusBadGateway, "upstream request failed before the stream started")
	if !ok {
		t.Fatal("provider failure must normalize")
	}
	if !strings.HasPrefix(message, ".messages[1].image[0]: Failed to download image from https://example.com/first.png") {
		t.Fatalf("message = %q, want the first remote image at messages[1].image[0]", message)
	}
}

func TestFitDeepSeekV4ImageDownloadErrorSkipsEverythingElse(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		message string
		info    *relaycommon.RelayInfo
		request dto.Request
	}{
		{
			name: "already official text", status: http.StatusBadRequest,
			message: ".messages[0].image[0]: Failed to download image from https://example.com/x.png (request_id: 11111111-1111-1111-1111-111111111111)",
			request: fitImageRequest(fitImageMessage("https://example.com/x.png")),
		},
		{
			name: "account verdict", status: http.StatusBadRequest,
			message: "Insufficient Balance", request: fitImageRequest(fitImageMessage("https://example.com/x.png")),
		},
		{
			name: "quota verdict", status: http.StatusInternalServerError,
			message: "账户余额不足，请充值", request: fitImageRequest(fitImageMessage("https://example.com/x.png")),
		},
		{
			name: "rate limit", status: http.StatusTooManyRequests,
			message: "Too many requests", request: fitImageRequest(fitImageMessage("https://example.com/x.png")),
		},
		{
			name: "rate limit text on a provider status", status: http.StatusServiceUnavailable,
			message: "upstream rate limit exceeded", request: fitImageRequest(fitImageMessage("https://example.com/x.png")),
		},
		{
			name: "no image part", status: http.StatusBadGateway,
			message: "Model provider request failed",
			request: &dto.GeneralOpenAIRequest{Model: "deepseek-v4.1-flash-1017", Messages: []dto.Message{{Role: "user", Content: "hi"}}},
		},
		{
			name: "local image only", status: http.StatusBadGateway,
			message: "Model provider request failed",
			request: fitImageRequest(fitImageMessage("data:image/png;base64,AAAA")),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			info := fitChatInfo(testCase.request)
			if testCase.info != nil {
				info = testCase.info
			}
			c := fitErrorContext(t, info)
			if message, ok := FitDeepSeekV4ImageDownloadError(c, testCase.status, testCase.message); ok {
				t.Fatalf("must not normalize, got %q", message)
			}
		})
	}
}

func TestFitDeepSeekV4ImageDownloadErrorSkipsOtherModelsAndSurfaces(t *testing.T) {
	request := fitImageRequest(fitImageMessage("https://example.com/x.png"))

	kimiInfo := &relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "kimi-k3",
		Request:         request,
	}
	if _, ok := FitDeepSeekV4ImageDownloadError(fitErrorContext(t, kimiInfo), http.StatusBadGateway, "Model provider request failed"); ok {
		t.Fatal("non-DeepSeek-V4 families must keep their own wording")
	}

	responsesInfo := &relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeResponses,
		OriginModelName: "deepseek-v4.1-flash-1017",
		Request:         request,
	}
	if _, ok := FitDeepSeekV4ImageDownloadError(fitErrorContext(t, responsesInfo), http.StatusBadGateway, "Model provider request failed"); ok {
		t.Fatal("only chat completions normalizes on this surface")
	}

	// A request without a relay info in context (defensive path) must not panic.
	if _, ok := FitDeepSeekV4ImageDownloadError(fitErrorContext(t, nil), http.StatusBadGateway, "Model provider request failed"); ok {
		t.Fatal("missing relay info must not normalize")
	}

	// A non-chat request object under a chat relay mode is skipped, not panicked on.
	openAIInfo := fitChatInfo(nil)
	if _, ok := FitDeepSeekV4ImageDownloadError(fitErrorContext(t, openAIInfo), http.StatusBadGateway, "Model provider request failed"); ok {
		t.Fatal("a nil request must not normalize")
	}
}
