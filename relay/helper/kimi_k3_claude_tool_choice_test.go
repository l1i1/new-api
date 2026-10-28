package helper

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	coreconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The official K3 endpoint rejects a named tool_choice while thinking is on with
// "tool_choice 'specified' is incompatible with thinking enabled" (live-probed
// 2026-09-22 on the OpenAI-compatible endpoint), and the official validation
// texts are shared across the Anthropic and OpenAI-compatible wires (the
// 2026-10-26 image probe found the wording identical on both). The Messages wire
// never runs the OpenAI field validator, so this check is its counterpart and is
// gated on the caller's Validate dimension.
func TestValidateKimiK3ClaudeToolChoice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctxWithProfile := func(validate bool) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString("{}"))
		common.SetContextKey(c, coreconstant.ContextKeyUserSetting, dto.UserSetting{
			OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
				"kimi-k3": {Validate: validate},
			}},
		})
		return c
	}
	claudeRequest := func(model string, choice any, thinkingType string) *dto.ClaudeRequest {
		request := &dto.ClaudeRequest{
			Model:      model,
			Messages:   []dto.ClaudeMessage{{Role: "user", Content: "北京天气怎么样"}},
			ToolChoice: choice,
		}
		if thinkingType != "" {
			request.Thinking = &dto.Thinking{Type: thinkingType}
		}
		return request
	}

	tests := []struct {
		name        string
		model       string
		validate    bool
		request     *dto.ClaudeRequest
		wantMessage string // empty means no local rejection
	}{
		{
			name:        "named tool with default thinking is rejected with the official text",
			model:       "kimi-k3.1-pro",
			validate:    true,
			request:     claudeRequest("kimi-k3.1-pro", map[string]any{"type": "tool", "name": "get_weather"}, ""),
			wantMessage: kimiK3ToolChoiceSpecifiedMessage,
		},
		{
			name:     "named tool with thinking disabled passes",
			model:    "kimi-k3.1-pro",
			validate: true,
			request:  claudeRequest("kimi-k3.1-pro", map[string]any{"type": "tool", "name": "get_weather"}, "disabled"),
		},
		{
			name:        "named tool with explicit thinking enabled is rejected",
			model:       "kimi-k3.1-pro",
			validate:    true,
			request:     claudeRequest("kimi-k3.1-pro", map[string]any{"type": "tool", "name": "get_weather"}, "enabled"),
			wantMessage: kimiK3ToolChoiceSpecifiedMessage,
		},
		{
			name:     "auto passes",
			model:    "kimi-k3.1-pro",
			validate: true,
			request:  claudeRequest("kimi-k3.1-pro", map[string]any{"type": "auto"}, ""),
		},
		{
			name:     "none passes",
			model:    "kimi-k3.1-pro",
			validate: true,
			request:  claudeRequest("kimi-k3.1-pro", map[string]any{"type": "none"}, ""),
		},
		{
			// The forcing "any" form is left to the upstream: the OpenAI-compatible
			// endpoint accepts the forcing `required` form with tools while thinking,
			// so an Anthropic-endpoint verdict needs its own probe before this check
			// may answer for it.
			name:     "any is left to the upstream",
			model:    "kimi-k3.1-pro",
			validate: true,
			request:  claudeRequest("kimi-k3.1-pro", map[string]any{"type": "any"}, ""),
		},
		{
			name:     "absent tool_choice passes",
			model:    "kimi-k3.1-pro",
			validate: true,
			request:  claudeRequest("kimi-k3.1-pro", nil, ""),
		},
		{
			name:     "non-object tool_choice stays the upstream's to reject",
			model:    "kimi-k3.1-pro",
			validate: true,
			request:  claudeRequest("kimi-k3.1-pro", "auto", ""),
		},
		{
			name:     "validate dimension off passes",
			model:    "kimi-k3.1-pro",
			validate: false,
			request:  claudeRequest("kimi-k3.1-pro", map[string]any{"type": "tool", "name": "get_weather"}, ""),
		},
		{
			name:     "other families pass",
			model:    "deepseek-v4-flash",
			validate: true,
			request:  claudeRequest("deepseek-v4-flash", map[string]any{"type": "tool", "name": "get_weather"}, ""),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateKimiK3ClaudeToolChoice(ctxWithProfile(tc.validate), tc.model, tc.request)
			if tc.wantMessage == "" {
				assert.Nil(t, err)
				return
			}
			require.NotNil(t, err)
			assert.Equal(t, http.StatusBadRequest, err.StatusCode)
			assert.Equal(t, tc.wantMessage, err.ToOpenAIError().Message)
			assert.Equal(t, "invalid_request_error", err.ToClaudeError().Type)
			assert.True(t, types.IsSkipRetryError(err))
		})
	}

	t.Run("nil request passes", func(t *testing.T) {
		assert.Nil(t, ValidateKimiK3ClaudeToolChoice(ctxWithProfile(true), "kimi-k3.1-pro", nil))
	})
}
