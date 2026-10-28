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
	requestWithChoice := func(choice any) *dto.ClaudeRequest {
		return &dto.ClaudeRequest{
			Model:      "kimi-k3.1-pro",
			Messages:   []dto.ClaudeMessage{{Role: "user", Content: "北京天气怎么样"}},
			ToolChoice: choice,
		}
	}

	t.Run("named tool with default thinking is rejected with the official text", func(t *testing.T) {
		err := ValidateKimiK3ClaudeToolChoice(ctxWithProfile(true), "kimi-k3.1-pro",
			requestWithChoice(map[string]any{"type": "tool", "name": "get_weather"}))
		require.NotNil(t, err)
		assert.Equal(t, http.StatusBadRequest, err.StatusCode)
		assert.Equal(t, kimiK3ToolChoiceSpecifiedMessage, err.ToOpenAIError().Message)
		assert.True(t, types.IsSkipRetryError(err))
	})

	t.Run("named tool with thinking disabled passes", func(t *testing.T) {
		request := requestWithChoice(map[string]any{"type": "tool", "name": "get_weather"})
		request.Thinking = &dto.Thinking{Type: "disabled"}
		err := ValidateKimiK3ClaudeToolChoice(ctxWithProfile(true), "kimi-k3.1-pro", request)
		assert.Nil(t, err)
	})

	// "any" is not rejected locally: the OpenAI-compatible endpoint accepts the
	// forcing `required` form with tools while thinking, so an Anthropic-endpoint
	// verdict needs its own probe before this check may answer for it.
	for _, choice := range []any{
		map[string]any{"type": "auto"},
		map[string]any{"type": "none"},
		map[string]any{"type": "any"},
		"auto",
		nil,
	} {
		t.Run("non-named shape passes", func(t *testing.T) {
			err := ValidateKimiK3ClaudeToolChoice(ctxWithProfile(true), "kimi-k3.1-pro", requestWithChoice(choice))
			assert.Nil(t, err)
		})
	}

	t.Run("validate dimension off passes", func(t *testing.T) {
		err := ValidateKimiK3ClaudeToolChoice(ctxWithProfile(false), "kimi-k3.1-pro",
			requestWithChoice(map[string]any{"type": "tool", "name": "get_weather"}))
		assert.Nil(t, err)
	})

	t.Run("other families pass", func(t *testing.T) {
		err := ValidateKimiK3ClaudeToolChoice(ctxWithProfile(true), "deepseek-v4-flash",
			requestWithChoice(map[string]any{"type": "tool", "name": "get_weather"}))
		assert.Nil(t, err)
	})

	t.Run("nil request passes", func(t *testing.T) {
		assert.Nil(t, ValidateKimiK3ClaudeToolChoice(ctxWithProfile(true), "kimi-k3.1-pro", nil))
	})
}
