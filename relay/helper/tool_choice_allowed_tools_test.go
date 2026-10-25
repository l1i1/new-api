package helper

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	coreconstant "github.com/QuantumNous/new-api/constant"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newKimiK3FitContext(t *testing.T, body string) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, coreconstant.ContextKeyUserSetting, dto.UserSetting{
		OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"kimi-k3": {Validate: true},
		}},
	})
	t.Cleanup(func() { common.CleanupBodyStorage(c) })
	return c
}

func allowedToolsRequest(t *testing.T, body string) *dto.GeneralOpenAIRequest {
	t.Helper()
	request := &dto.GeneralOpenAIRequest{}
	require.NoError(t, json.Unmarshal([]byte(body), request))
	return request
}

const allowedToolsWeather = `{
  "model": "kimi-k3.1-pro",
  "messages": [{"role": "user", "content": "please call a tool"}],
  "tools": [
    {"type": "function", "function": {"name": "get_weather", "parameters": {"type": "object"}}},
    {"type": "function", "function": {"name": "get_time", "parameters": {"type": "object"}}}
  ],
  "tool_choice": {
    "type": "allowed_tools",
    "allowed_tools": {
      "mode": "auto",
      "tools": [{"type": "function", "function": {"name": "get_weather"}}]
    }
  }
}`

func TestNormalizeAllowedToolsToolChoice(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantNames     []string
		wantChoice    any
		wantUntouched bool
	}{
		{
			name:       "auto keeps only the allowed declaration",
			body:       allowedToolsWeather,
			wantNames:  []string{"get_weather"},
			wantChoice: "auto",
		},
		{
			name: "required maps to the forcing string",
			body: `{
			  "messages": [{"role": "user", "content": "hi"}],
			  "tools": [
			    {"type": "function", "function": {"name": "get_weather"}},
			    {"type": "function", "function": {"name": "get_time"}}
			  ],
			  "tool_choice": {"type": "allowed_tools", "allowed_tools": {"mode": "required",
			    "tools": [{"type": "function", "function": {"name": "get_time"}}]}}
			}`,
			wantNames:  []string{"get_time"},
			wantChoice: "required",
		},
		{
			name: "auto with no declared match becomes none",
			body: `{
			  "messages": [{"role": "user", "content": "hi"}],
			  "tools": [{"type": "function", "function": {"name": "get_weather"}}],
			  "tool_choice": {"type": "allowed_tools", "allowed_tools": {"mode": "auto",
			    "tools": [{"type": "function", "function": {"name": "search"}}]}}
			}`,
			wantNames:  []string{},
			wantChoice: "none",
		},
		{
			name: "required keeps no declared match visible to the endpoint",
			body: `{
			  "messages": [{"role": "user", "content": "hi"}],
			  "tools": [{"type": "function", "function": {"name": "get_weather"}}],
			  "tool_choice": {"type": "allowed_tools", "allowed_tools": {"mode": "required",
			    "tools": [{"type": "function", "function": {"name": "search"}}]}}
			}`,
			wantNames:  []string{},
			wantChoice: "required",
		},
		{
			name: "multi allowed list keeps declared order",
			body: `{
			  "messages": [{"role": "user", "content": "hi"}],
			  "tools": [
			    {"type": "function", "function": {"name": "get_weather"}},
			    {"type": "function", "function": {"name": "get_time"}}
			  ],
			  "tool_choice": {"type": "allowed_tools", "allowed_tools": {"mode": "auto",
			    "tools": [
			      {"type": "function", "function": {"name": "get_time"}},
			      {"type": "function", "function": {"name": "get_weather"}}
			    ]}}
			}`,
			wantNames:  []string{"get_weather", "get_time"},
			wantChoice: "auto",
		},
		{
			name: "non-function allowed entry is left alone",
			body: `{
			  "messages": [{"role": "user", "content": "hi"}],
			  "tools": [{"type": "function", "function": {"name": "get_weather"}}],
			  "tool_choice": {"type": "allowed_tools", "allowed_tools": {"mode": "auto",
			    "tools": [{"type": "mcp", "name": "server"}]}}
			}`,
			wantUntouched: true,
		},
		{
			name: "malformed allowed_tools is left alone",
			body: `{
			  "messages": [{"role": "user", "content": "hi"}],
			  "tools": [{"type": "function", "function": {"name": "get_weather"}}],
			  "tool_choice": {"type": "allowed_tools", "allowed_tools": "auto"}
			}`,
			wantUntouched: true,
		},
		{
			name: "unknown mode is left alone",
			body: `{
			  "messages": [{"role": "user", "content": "hi"}],
			  "tools": [{"type": "function", "function": {"name": "get_weather"}}],
			  "tool_choice": {"type": "allowed_tools", "allowed_tools": {"mode": "sometimes",
			    "tools": [{"type": "function", "function": {"name": "get_weather"}}]}}
			}`,
			wantUntouched: true,
		},
		{
			name: "ordinary string choice is left alone",
			body: `{
			  "messages": [{"role": "user", "content": "hi"}],
			  "tools": [{"type": "function", "function": {"name": "get_weather"}}],
			  "tool_choice": "auto"
			}`,
			wantUntouched: true,
		},
		{
			name: "named-function choice is left alone",
			body: `{
			  "messages": [{"role": "user", "content": "hi"}],
			  "tools": [{"type": "function", "function": {"name": "get_weather"}}],
			  "tool_choice": {"type": "function", "function": {"name": "get_weather"}}
			}`,
			wantUntouched: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := allowedToolsRequest(t, testCase.body)
			before, err := json.Marshal(request)
			require.NoError(t, err)

			normalizeAllowedToolsToolChoice(request)

			after, err := json.Marshal(request)
			require.NoError(t, err)
			if testCase.wantUntouched {
				assert.JSONEq(t, string(before), string(after))
				return
			}

			names := make([]string, 0, len(request.Tools))
			for _, tool := range request.Tools {
				names = append(names, tool.Function.Name)
			}
			assert.Equal(t, testCase.wantNames, names)
			assert.Equal(t, testCase.wantChoice, request.ToolChoice)
		})
	}
}

// TestAllowedToolsUnblocksKimiK3Thinking pins the actual defect: with thinking
// on (k3.1-pro's only state), the official-fit validator classified the
// allowed_tools object as a forced "specified" choice and answered 400 before
// any upstream call. The rewrite must leave a shape that validator accepts.
func TestAllowedToolsUnblocksKimiK3Thinking(t *testing.T) {
	request := allowedToolsRequest(t, allowedToolsWeather)
	require.Error(t, validateKimiK3OfficialFields(request))

	normalizeAllowedToolsToolChoice(request)
	require.NoError(t, validateKimiK3OfficialFields(request))
	assert.Equal(t, "auto", request.ToolChoice)
	assert.Len(t, request.Tools, 1)
	assert.Equal(t, "get_weather", request.Tools[0].Function.Name)
}

// TestAllowedToolsRequiredUnblocksKimiK3Thinking covers the forcing mode: the
// translated "required" string is legal while thinking is on.
func TestAllowedToolsRequiredUnblocksKimiK3Thinking(t *testing.T) {
	request := allowedToolsRequest(t, `{
	  "model": "kimi-k3.1-pro",
	  "messages": [{"role": "user", "content": "please call a tool"}],
	  "tools": [{"type": "function", "function": {"name": "get_time"}}],
	  "tool_choice": {"type": "allowed_tools", "allowed_tools": {"mode": "required",
	    "tools": [{"type": "function", "function": {"name": "get_time"}}]}}
	}`)
	require.Error(t, validateKimiK3OfficialFields(request))

	normalizeAllowedToolsToolChoice(request)
	require.NoError(t, validateKimiK3OfficialFields(request))
	assert.Equal(t, "required", request.ToolChoice)
}

// TestGetAndValidateTextRequestNormalizesAllowedTools exercises the actual call
// site: the rewrite must happen before the official-fit validators, so a valid
// allowed_tools request is returned normalized rather than answered with the K3
// "specified" 400. The forcing shapes the validator really does reject stay
// rejected.
func TestGetAndValidateTextRequestNormalizesAllowedTools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request, err := GetAndValidateTextRequest(newKimiK3FitContext(t, allowedToolsWeather), relayconstant.RelayModeChatCompletions)
	require.NoError(t, err)
	assert.Equal(t, "auto", request.ToolChoice)
	require.Len(t, request.Tools, 1)
	assert.Equal(t, "get_weather", request.Tools[0].Function.Name)

	forcing := `{
	  "model": "kimi-k3.1-pro",
	  "messages": [{"role": "user", "content": "please call a tool"}],
	  "tools": [{"type": "function", "function": {"name": "get_weather"}}],
	  "tool_choice": {"type": "function", "function": {"name": "get_weather"}}
	}`
	_, err = GetAndValidateTextRequest(newKimiK3FitContext(t, forcing), relayconstant.RelayModeChatCompletions)
	require.Error(t, err)
	assert.Contains(t, err.Error(), kimiK3ToolChoiceSpecifiedMessage)
}
