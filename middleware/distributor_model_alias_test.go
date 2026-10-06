package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A configured model alias is rewritten before channel selection, so every
// downstream consumer — routing, billing, consume logs, rate limits — sees the
// canonical catalog id. With no channel serving the model, the diagnostic names
// the canonical id instead of the alias the client sent.
func TestDistributeReportsConfiguredModelAliasAsCanonical(t *testing.T) {
	require.NoError(t, i18n.Init())
	setupOriginTaskDB(t)
	previousCacheEnabled := common.MemoryCacheEnabled
	// The channel cache stays uninitialized on purpose: selection must stop
	// before it looks at channels, and this test must not seed shared cache
	// state for later tests in this package.
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = previousCacheEnabled })

	settings := model_setting.GetGlobalSettings()
	previousAliases := settings.ModelAliasMap
	settings.ModelAliasMap = map[string]string{
		"claude-haiku-4-5-20251001": "claude-haiku-4-5",
	}
	t.Cleanup(func() { settings.ModelAliasMap = previousAliases })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"claude-haiku-4-5-20251001","messages":[{"role":"user","content":"hi"}]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Accept-Language", "en")
	common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")

	Distribute()(c)

	// Channel selection cannot succeed without a channel cache, so the request
	// stops here with the selection diagnostic; what matters is that the model
	// name the client is told about is the canonical id.
	assert.GreaterOrEqual(t, recorder.Code, 400, recorder.Body.String())
	body := recorder.Body.String()
	assert.Contains(t, body, "claude-haiku-4-5")
	assert.NotContains(t, body, "claude-haiku-4-5-20251001")
}

func TestTokenModelLimitAllowsCompactAlias(t *testing.T) {
	assert.True(t, tokenModelLimitAllowsCandidate(
		map[string]bool{"gpt-5.5": true},
		"gpt-5.5-openai-compact",
	))
	assert.True(t, tokenModelLimitAllowsCandidate(
		map[string]bool{"gpt-5.6-sol-openai-compact": true},
		"gpt-5.6-sol-openai-compact",
	))
	assert.False(t, tokenModelLimitAllowsCandidate(
		map[string]bool{"gpt-5.4": true},
		"gpt-5.5-openai-compact",
	))
	assert.False(t, tokenModelLimitAllowsCandidate(
		map[string]bool{"gpt-5.5": true},
		"gpt-5.6-sol",
	))

	baseOnlyLimit := map[string]bool{"gpt-5.5": true}
	assert.True(t, tokenModelLimitAllowsResolved(
		baseOnlyLimit,
		"gpt-5.5-openai-compact",
		"gpt-5.5",
	))
	assert.False(t, tokenModelLimitAllowsResolved(
		baseOnlyLimit,
		"gpt-5.5-openai-compact",
		"",
	))
}
