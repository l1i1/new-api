package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// The Errors dimension promises the provider's own message without the gateway
// request-id suffix. The Messages wire relays the same first-party K3/DS/GLM
// validation texts as the chat wire, so it must take part in that guarantee;
// wires that only relay upstream bytes keep the historical behaviour.
func TestOfficialFitErrorsApplyOnChatAndClaudeWires(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{
		OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"kimi-k3": {Errors: true},
			"glm-5.3": {Validate: true},
		}},
	})

	assert.True(t, officialFitErrorsApplyOnWire(c, types.RelayFormatOpenAI, "kimi-k3.1-pro"))
	assert.True(t, officialFitErrorsApplyOnWire(c, types.RelayFormatClaude, "kimi-k3.1-pro"))
	assert.False(t, officialFitErrorsApplyOnWire(c, types.RelayFormatGemini, "kimi-k3.1-pro"))
	// Errors is the dimension that owns the text guarantee; Validate alone must
	// not suppress the gateway request-id suffix.
	assert.False(t, officialFitErrorsApplyOnWire(c, types.RelayFormatClaude, "glm-5.3"))
	assert.False(t, officialFitErrorsApplyOnWire(c, types.RelayFormatClaude, "unmatched-model"))
}
