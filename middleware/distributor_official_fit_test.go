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

func TestMarkV4OfficialPinFromDistributorRouteOnlySource(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newContext := func(body string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		// Validate-only profile: the Route dimension is the only pin source,
		// so thinking/logprobs/extreme sampling must not pin here.
		common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{
			OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
				"deepseek-v4-": {Validate: true},
			}},
		})
		common.SetContextKey(c, constant.ContextKeyV4OfficialPin, false)
		return c
	}

	t.Run("explicit thinking object does not pin", func(t *testing.T) {
		c := newContext(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`)
		markV4OfficialPinFromDistributor(c)
		assert.False(t, common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin))
	})

	t.Run("logprobs true does not pin", func(t *testing.T) {
		c := newContext(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"logprobs":true,"top_logprobs":5}`)
		markV4OfficialPinFromDistributor(c)
		assert.False(t, common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin))
	})

	t.Run("extreme sampling does not pin", func(t *testing.T) {
		c := newContext(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"temperature":2,"top_p":0.1}`)
		markV4OfficialPinFromDistributor(c)
		assert.False(t, common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin))
	})
}

func TestDeepSeekV4SelectiveOfficialPin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Route-enabled DeepSeek V4 profile: the pin becomes selective and only
	// features the aggregator mix cannot reproduce land on the official
	// channel (live evidence 2026-09-06: reasoning_content drops and missing
	// dual-path logprobs on aggregators; image parts unverified there).
	newContext := func(body string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{
			OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
				"deepseek-v4-": {Validate: true, Errors: true, Shape: true, Route: true},
				"kimi-k3":      {Validate: true, Errors: true, Shape: true, Route: true},
			}},
		})
		common.SetContextKey(c, constant.ContextKeyV4OfficialPin, false)
		return c
	}

	pinned := func(t *testing.T, body string) {
		c := newContext(body)
		markV4OfficialPinFromDistributor(c)
		assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin), body)
	}
	unpinned := func(t *testing.T, body string) {
		c := newContext(body)
		markV4OfficialPinFromDistributor(c)
		assert.False(t, common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin), body)
	}

	t.Run("default thinking pins (official reasoning output)", func(t *testing.T) {
		pinned(t, `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`)
	})
	t.Run("explicit enabled thinking pins", func(t *testing.T) {
		pinned(t, `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled"}}`)
	})
	t.Run("adaptive thinking pins", func(t *testing.T) {
		pinned(t, `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"}}`)
	})
	t.Run("reasoning_effort high pins", func(t *testing.T) {
		pinned(t, `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)
	})
	t.Run("malformed thinking classifies as thinking-expected and pins", func(t *testing.T) {
		pinned(t, `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"bogus"}}`)
	})
	t.Run("logprobs pins regardless of thinking state", func(t *testing.T) {
		pinned(t, `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"},"logprobs":true,"top_logprobs":5}`)
	})
	t.Run("image parts pin on a disabled-thinking request", func(t *testing.T) {
		pinned(t, `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/x.png"}},{"type":"text","text":"这是什么？"}]}],"thinking":{"type":"disabled"}}`)
	})
	t.Run("thinking disabled does not pin", func(t *testing.T) {
		unpinned(t, `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`)
	})
	t.Run("reasoning_effort none without thinking object does not pin", func(t *testing.T) {
		unpinned(t, `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`)
	})
	t.Run("thinking null with effort none does not pin", func(t *testing.T) {
		unpinned(t, `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"thinking":null,"reasoning_effort":"none"}`)
	})
	t.Run("disabled thinking plus effort none does not pin", func(t *testing.T) {
		unpinned(t, `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"},"reasoning_effort":"none"}`)
	})

	t.Run("kimi-k3 keeps whole-family pin", func(t *testing.T) {
		c := newContext(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`)
		markV4OfficialPinFromDistributor(c)
		assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin))
	})
}
