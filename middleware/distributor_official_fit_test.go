package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarkV4OfficialPinFromDistributorUnknownModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newContext := func(body string) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{
			OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
				"deepseek-v4-": {Validate: true, Route: true},
			}},
		})
		return c, w
	}

	t.Run("unknown deepseek model aborts with official text", func(t *testing.T) {
		c, w := newContext(`{"model":"deepseek-v4-notexist","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":16}`)
		markV4OfficialPinFromDistributor(c)
		assert.True(t, c.IsAborted())
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, "application/octet-stream", c.Writer.Header().Get("Content-Type"))
		var payload struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		assert.Equal(t, relayhelper.DeepSeekV4UnknownModelMessage("deepseek-v4-notexist"), payload.Error.Message)
		assert.Equal(t, "invalid_request_error", payload.Error.Type)
		assert.False(t, strings.Contains(payload.Error.Message, "request id"))
	})

	t.Run("official model name passes through", func(t *testing.T) {
		c, _ := newContext(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":16}`)
		markV4OfficialPinFromDistributor(c)
		assert.False(t, c.IsAborted())
	})
}
