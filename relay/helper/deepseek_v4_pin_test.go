package helper

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func floatPtr(v float64) *float64 { return &v }

func TestMarkDeepSeekV4OfficialPinThresholds(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		request *dto.GeneralOpenAIRequest
		pinned  bool
	}{
		{"K08 extreme sampling pins", "deepseek-v4-flash", &dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash",
			Temperature: floatPtr(2), TopP: floatPtr(0.1),
			PresencePenalty: floatPtr(1.5), FrequencyPenalty: floatPtr(1.5),
		}, true},
		{"temperature above threshold pins", "deepseek-v4-flash", &dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash", Temperature: floatPtr(1.7)}, true},
		{"temperature at boundary does not pin", "deepseek-v4-flash", &dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash", Temperature: floatPtr(1.5)}, false},
		{"low top_p pins", "deepseek-v4-flash", &dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash", TopP: floatPtr(0.2)}, true},
		{"top_p at boundary does not pin", "deepseek-v4-flash", &dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash", TopP: floatPtr(0.3)}, false},
		{"penalty above threshold pins", "deepseek-v4-flash", &dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash", FrequencyPenalty: floatPtr(1.2)}, true},
		{"penalty at boundary does not pin", "deepseek-v4-flash", &dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash", PresencePenalty: floatPtr(1.0)}, false},
		{"mild sampling does not pin", "deepseek-v4-flash", &dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash", Temperature: floatPtr(0.7), TopP: floatPtr(0.9)}, false},
		{"no sampling params does not pin", "deepseek-v4-flash", &dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash"}, false},
		{"non-V4 extreme sampling does not pin", "gpt-test", &dto.GeneralOpenAIRequest{Model: "gpt-test", Temperature: floatPtr(2)}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			common.SetContextKey(c, constant.ContextKeyV4OfficialPin, false)

			markDeepSeekV4OfficialPin(c, test.request)

			assert.Equal(t, test.pinned, common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin))
		})
	}
}
