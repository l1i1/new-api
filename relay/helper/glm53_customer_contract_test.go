package helper

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	coreconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two shapes below are the customer instruction-check failures reported on
// 2026-10-12 for glm-5.3-flash. Both are upstream behaviours the live Zhipu
// endpoint really has (measured through channel 7, the official channel, and
// channel 78), relayed verbatim by the gateway, so the fix has to be local:
//   - "孤儿 tool 消息校验": a `tool` turn with no pending tool_calls is
//     answered 200 and runs inference upstream;
//   - "tool_choice=none 遵循": the upstream ignores an explicit none and calls
//     the tool anyway.
// Both rules live inside the fit-profile gate (fitEnabled && profile.Validate),
// like every other family contract rule, so only users who opted into the
// family's fit contract get the stricter-than-upstream behaviour.

// newGlm53FitContext mirrors newDeepSeekV4FitContext for the GLM family. The
// profile key is matched as a prefix, so "glm-5.3" covers glm-5.3-flash too.
func newGlm53FitContext(body string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, coreconstant.ContextKeyUserSetting, dto.UserSetting{
		OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"glm-5.3":      {Validate: true},
			"deepseek-v4-": {Validate: true},
		}},
	})
	return c
}

// newNoFitContext is the same request without a fit profile: the strict rules
// must not fire for it.
func newNoFitContext(body string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

func TestGlm53OrphanToolMessageRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tool := `{"role":"tool","content":"{\"temperature\":25}","tool_call_id":"call_probe_1"}`

	tests := []struct {
		name     string
		model    string
		messages string
		wantErr  bool
	}{
		{
			name:     "orphan tool turn after a user turn is refused",
			model:    "glm-5.3-flash",
			messages: `[{"role":"user","content":"北京天气？"},` + tool + `]`,
			wantErr:  true,
		},
		{
			name:     "the plain glm-5.3 id is covered by the same rule",
			model:    "glm-5.3",
			messages: `[{"role":"user","content":"北京天气？"},` + tool + `]`,
			wantErr:  true,
		},
		{
			name:     "a tool turn answering a declared call passes",
			model:    "glm-5.3-flash",
			messages: `[{"role":"user","content":"北京天气？"},` + assistantToolCallsJSON("call_probe_1") + `,` + tool + `]`,
		},
		{
			name:     "a tool-free conversation passes",
			model:    "glm-5.3-flash",
			messages: `[{"role":"user","content":"北京天气？"}]`,
		},
		{
			name:     "a user turn closes the exchange, so a later tool turn is an orphan",
			model:    "glm-5.3-flash",
			messages: `[{"role":"user","content":"北京天气？"},` + assistantToolCallsJSON("call_probe_1") + `,` + tool + `,{"role":"user","content":"谢谢"},` + tool + `]`,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GetAndValidateTextRequest(newGlm53FitContext(toolChainBodyModel(tt.model, tt.messages)), constant.RelayModeChatCompletions)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			assert.Equal(t, glm53OrphanToolMessageText, apiErr.ToOpenAIError().Message)
			assert.Equal(t, "1214", apiErr.ToOpenAIError().Code)
		})
	}
}

// The orphan rule belongs to the glm-5.3 family only; an id outside the family
// keeps the upstream contract even with fit validation enabled.
func TestGlm53OrphanRuleIsFamilyScoped(t *testing.T) {
	orphan := []dto.Message{
		{Role: "user", Content: "北京天气？"},
		{Role: "tool", ToolCallId: "call_probe_1", Content: "25"},
	}
	require.Error(t, validateGlm53OfficialFields(&dto.GeneralOpenAIRequest{Model: "glm-5.3-flash", Messages: orphan}))
	require.NoError(t, validateGlm53OfficialFields(&dto.GeneralOpenAIRequest{Model: "glm-5.2", Messages: orphan}))
	require.NoError(t, validateGlm53OfficialFields(&dto.GeneralOpenAIRequest{Model: "deepseek-v4-flash", Messages: orphan}))
}

func TestGlm53ToolChoiceNoneWithholdsToolDeclarations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tools := `"tools":[{"type":"function","function":{"name":"get_weather"}}]`
	legacy := `"functions":[{"name":"get_weather"}]`
	none := `"tool_choice":"none"`

	body := func(model string, extras ...string) string {
		extra := ""
		if len(extras) > 0 {
			extra = "," + strings.Join(extras, ",")
		}
		return `{"model":"` + model + `","messages":[{"role":"user","content":"请查询北京的天气。"}],"max_tokens":32` + extra + `}`
	}

	tests := []struct {
		name      string
		body      string
		fit       bool
		wantTools bool
	}{
		{"glm-5.3-flash with tool_choice none loses the tools", body("glm-5.3-flash", tools, none), true, false},
		{"glm-5.3 behaves identically", body("glm-5.3", tools, none), true, false},
		{"the legacy functions field is withheld too", body("glm-5.3-flash", legacy, none), true, false},
		{"tool_choice auto keeps the tools", body("glm-5.3-flash", tools, `"tool_choice":"auto"`), true, true},
		{"an absent tool_choice keeps the tools", body("glm-5.3-flash", tools), true, true},
		{"a named tool_choice keeps the tools", body("glm-5.3-flash", tools, `"tool_choice":{"type":"function","function":{"name":"get_weather"}}`), true, true},
		{"a non-glm family keeps the tools", body("deepseek-v4-flash", tools, none), true, true},
		{"without a fit profile the request is untouched", body("glm-5.3-flash", tools, none), false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c *gin.Context
			if tt.fit {
				c = newGlm53FitContext(tt.body)
			} else {
				c = newNoFitContext(tt.body)
			}
			request, err := GetAndValidateTextRequest(c, constant.RelayModeChatCompletions)
			require.NoError(t, err)
			if tt.wantTools {
				require.NotEmpty(t, request.Tools)
				return
			}
			require.Empty(t, request.Tools)
			// The strategy string itself is left as sent: it is inert upstream
			// once the declarations are gone, and keeping it documents the
			// client's intent in the request we build.
			assert.Equal(t, "none", request.ToolChoice)
		})
	}
}
