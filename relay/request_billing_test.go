package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

// minimalContainerBase64 is an ISO-BMFF movie with one track (1920x1080,
// 5.533 s). It is small enough to inline and lets the billing path price a real
// video part without an external fixture.
const minimalContainerBase64 = "AAAAHGZ0eXBpc29tAAACAGlzb21pc28ybXA0MQAAANhtb292AAAAbG12aGQAAAAAAAAAAAAAAAAAAAPoAAAVnQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAZHRyYWsAAABcdGtoZAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAHgAAABDgAAA=="

func videoBillingRequest() *dto.GeneralOpenAIRequest {
	return &dto.GeneralOpenAIRequest{
		Model: "kimi-k3",
		Messages: []dto.Message{{
			Role: "user",
			Content: []dto.MediaContent{
				{Type: dto.ContentTypeText, Text: "what happens in this clip?"},
				{Type: dto.ContentTypeVideoUrl, VideoUrl: &dto.MessageVideoUrl{
					Url: "data:video/mp4;base64," + minimalContainerBase64,
				}},
			},
		}},
	}
}

// TestPrepareRequestBillingReadsTheSelectedChannelFromTheContext pins the call
// order that has already produced one production panic: billing runs before any
// handler builds the per-attempt ChannelMeta, so the video-estimate gate must
// read the selected channel from the request context. The prefetch used to take
// its model id from the embedded ChannelMeta pointer, which is nil at this
// point, so every request on a channel marked video_usage_mode=estimate would
// panic instead of being billed. No other test exercises this order, which is
// why the panic reached a release candidate.
func TestPrepareRequestBillingReadsTheSelectedChannelFromTheContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "kimi-k3")
	common.SetContextKey(c, constant.ContextKeyChannelOtherSetting, dto.ChannelOtherSettings{
		SupportsVideo:  true,
		VideoUsageMode: dto.VideoUsageModeEstimate,
	})

	info := &relaycommon.RelayInfo{
		Request:         videoBillingRequest(),
		OriginModelName: "kimi-k3",
	}
	if info.ChannelMeta != nil {
		t.Fatal("the fixture must not pre-build ChannelMeta: that is the state billing starts in")
	}

	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("billing must not dereference the not-yet-built ChannelMeta: %v", recovered)
			}
		}()
		// Pricing itself may fail for a model with no configured price; the
		// contract under test is the gate, which has to have run by then.
		_ = PrepareRequestBilling(c, info)
	}()

	if got := info.GetVideoTokens(); got <= 0 {
		t.Fatalf("the gate must price the video part from the context channel settings, got %d", got)
	}
}
