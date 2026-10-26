package helper

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	coreconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The official Anthropic endpoint rejects a url image source with
// "Invalid request: unsupported image url: <url>" (live-probed 2026-10-26);
// the Claude wire format never runs the OpenAI field validator, so this local
// check is its counterpart and is gated on the caller's Validate dimension.
func TestValidateKimiK3ClaudeImages(t *testing.T) {
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
	requestWithSource := func(source map[string]any) *dto.ClaudeRequest {
		return &dto.ClaudeRequest{
			Model: "kimi-k3.1-pro",
			Messages: []dto.ClaudeMessage{{
				Role: "user",
				Content: []any{
					map[string]any{"type": "image", "source": source},
					map[string]any{"type": "text", "text": "图中是什么？"},
				},
			}},
		}
	}

	t.Run("url source is rejected with the official text", func(t *testing.T) {
		err := ValidateKimiK3ClaudeImages(ctxWithProfile(true), "kimi-k3.1-pro",
			requestWithSource(map[string]any{"type": "url", "url": "https://example.com/x.png"}))
		require.NotNil(t, err)
		assert.Equal(t, http.StatusBadRequest, err.StatusCode)
		assert.Equal(t, kimiK3UnsupportedImageURLPrefix+"https://***.com/***", err.ToOpenAIError().Message)
	})

	t.Run("base64 source passes", func(t *testing.T) {
		err := ValidateKimiK3ClaudeImages(ctxWithProfile(true), "kimi-k3.1-pro",
			requestWithSource(map[string]any{"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgo="}))
		assert.Nil(t, err)
	})

	t.Run("validate dimension off passes", func(t *testing.T) {
		err := ValidateKimiK3ClaudeImages(ctxWithProfile(false), "kimi-k3.1-pro",
			requestWithSource(map[string]any{"type": "url", "url": "https://example.com/x.png"}))
		assert.Nil(t, err)
	})

	t.Run("other families pass", func(t *testing.T) {
		err := ValidateKimiK3ClaudeImages(ctxWithProfile(true), "deepseek-v4-flash",
			requestWithSource(map[string]any{"type": "url", "url": "https://example.com/x.png"}))
		assert.Nil(t, err)
	})
}
