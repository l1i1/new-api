package helper

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

// These checks deliberately exceed Zhipu's observed tolerance for orphan tool
// messages and tool_choice="none". Only a family's Validate profile opts in;
// Shape controls response formatting, not whether tools may run.
func newGlm53ContractContext(t *testing.T, body string, setting *dto.UserSetting) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if setting != nil {
		common.SetContextKey(c, coreconstant.ContextKeyUserSetting, *setting)
	}
	t.Cleanup(func() { common.CleanupBodyStorage(c) })
	return c
}

func TestGlm53OrphanToolMessageRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setting := &dto.UserSetting{OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
		"glm-5.3": {Validate: true},
	}}}
	user := `{"role":"user","content":"weather?"}`
	toolA := `{"role":"tool","content":"25","tool_call_id":"a"}`
	toolB := `{"role":"tool","content":"26","tool_call_id":"b"}`
	callA := assistantToolCallsJSON("a")
	calls := `{"role":"assistant","content":null,"tool_calls":[{"id":"a","type":"function","function":{"name":"weather","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"weather","arguments":"{}"}}]}`
	orphanText := "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'"

	tests := []struct {
		name        string
		messages    string
		wantMessage string
	}{
		{"orphan after user", `[` + user + `,` + toolA + `]`, orphanText},
		{"only a tool message", `[` + toolA + `]`, orphanText},
		{"empty messages retains its own error", `[]`, "输入不能为空"},
		{"null messages retains its own error", `null`, "输入不能为空"},
		{"tool-free conversation", `[` + user + `]`, ""},
		{"one matching response", `[` + user + `,` + callA + `,` + toolA + `]`, ""},
		{"parallel calls answered out of order", `[` + calls + `,` + toolB + `,` + toolA + `]`, ""},
		{"duplicate response after completion", `[` + callA + `,` + toolA + `,` + toolA + `]`, orphanText},
		{"duplicate response while another id is pending", `[` + calls + `,` + toolA + `,` + toolA + `]`, orphanText},
		{"assistant without calls", `[{"role":"assistant","content":"hello"},` + toolA + `]`, orphanText},
		{"assistant null calls", `[{"role":"assistant","tool_calls":null},` + toolA + `]`, orphanText},
		{"assistant empty calls", `[{"role":"assistant","tool_calls":[]},` + toolA + `]`, orphanText},
		{"assistant object calls", `[{"role":"assistant","tool_calls":{}},` + toolA + `]`, orphanText},
		{"assistant string calls", `[{"role":"assistant","tool_calls":"bad"},` + toolA + `]`, orphanText},
		{"assistant calls without usable ids", `[{"role":"assistant","tool_calls":[{}, {"id":" "}]},` + toolA + `]`, orphanText},
		{"assistant calls with wrong id type", `[{"role":"assistant","tool_calls":[{"id":1}]},` + toolA + `]`, orphanText},
		{"assistant without calls closes the exchange", `[` + callA + `,{"role":"assistant","content":"done"},` + toolA + `]`, orphanText},
		{"user closes the exchange", `[` + callA + `,` + user + `,` + toolA + `]`, orphanText},
		{"system closes the exchange", `[` + callA + `,{"role":"system","content":"new instruction"},` + toolA + `]`, orphanText},
		{"system between parallel responses closes the exchange", `[` + calls + `,` + toolA + `,{"role":"system","content":"new instruction"},` + toolB + `]`, orphanText},
		{"unknown response id does not consume a declared call", `[` + callA + `,` + toolB + `,` + toolA + `]`, ""},
		{"missing response id does not consume a declared call", `[` + callA + `,{"role":"tool","content":"unknown"},` + toolA + `]`, ""},
		{"unanswered calls remain an upstream concern", `[` + callA + `]`, ""},
		{"unfinished exchange may end on a user turn", `[` + callA + `,` + user + `]`, ""},
		{"legal multiple exchanges can reuse an id", `[` + user + `,` + callA + `,` + toolA + `,{"role":"assistant","content":"25"},` + user + `,` + callA + `,` + toolA + `]`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newGlm53ContractContext(t, toolChainBodyModel("glm-5.3-flash", tt.messages), setting)
			// Re-reading a replay body must not retain the previous validation's
			// pending ids, whether it accepted or rejected the conversation.
			for range 2 {
				request, err := GetAndValidateTextRequest(c, constant.RelayModeChatCompletions)
				if tt.wantMessage == "" {
					require.NoError(t, err)
					var expected dto.GeneralOpenAIRequest
					require.NoError(t, common.UnmarshalJsonStr(toolChainBodyModel("glm-5.3-flash", tt.messages), &expected))
					assert.Equal(t, expected.Messages, request.Messages)
					continue
				}
				require.Error(t, err)
				var apiErr *types.NewAPIError
				require.True(t, errors.As(err, &apiErr))
				assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
				assert.Equal(t, tt.wantMessage, apiErr.ToOpenAIError().Message)
				assert.Equal(t, "1214", apiErr.ToOpenAIError().Code)
			}
		})
	}
}

func TestGlm53OrphanRuleProfileGate(t *testing.T) {
	orphanBody := toolChainBodyModel("glm-5.3-flash", `[{"role":"user","content":"weather?"},{"role":"tool","tool_call_id":"a","content":"25"}]`)
	tests := []struct {
		name    string
		setting *dto.UserSetting
	}{
		{"missing user context", nil},
		{"authenticated user without fit", &dto.UserSetting{}},
		{"empty fit configuration", &dto.UserSetting{OfficialFit: &dto.OfficialFitConfig{}}},
		{"matching profile with validation disabled", &dto.UserSetting{OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{"glm-5.3": {}}}}},
		{"other dimensions do not enable validation", &dto.UserSetting{OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{"glm-5.3": {Shape: true, Errors: true, Route: true}}}}},
		{"unmatched validating profile", &dto.UserSetting{OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{"kimi-k3": {Validate: true}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newGlm53ContractContext(t, orphanBody, tt.setting)
			request, err := GetAndValidateTextRequest(c, constant.RelayModeChatCompletions)
			require.NoError(t, err)
			assert.Equal(t, "tool", request.Messages[1].Role)
			assert.Equal(t, "a", request.Messages[1].ToolCallId)
		})
	}

	// All families have Validate enabled here; their own validators still
	// distinguish a GLM policy from a DeepSeek/Kimi chain rejection.
	setting := &dto.UserSetting{OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{"*": {Validate: true}}}}
	for _, tt := range []struct {
		model   string
		code    string
		message string
	}{
		{"glm-5.3", "1214", "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'"},
		{"deepseek-v4-flash", "invalid_request_error", "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'"},
		{"kimi-k3", "invalid_request_error", "Invalid request: tool_call_id  is not found"},
		{"glm-5.2", "", ""},
	} {
		t.Run(tt.model, func(t *testing.T) {
			body := toolChainBodyModel(tt.model, `[{"role":"user","content":"weather?"},{"role":"tool","tool_call_id":"a","content":"25"}]`)
			_, err := GetAndValidateTextRequest(newGlm53ContractContext(t, body, setting), constant.RelayModeChatCompletions)
			if tt.message == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			assert.Equal(t, tt.code, apiErr.ToOpenAIError().Code)
			assert.Equal(t, tt.message, apiErr.ToOpenAIError().Message)
		})
	}
}

func TestGlm53OrphanErrorUsesZhipuWireShape(t *testing.T) {
	setting := &dto.UserSetting{OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{"glm-5.3": {Validate: true, Errors: true}}}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"glm-5.3-flash","stream":true,"messages":[{"role":"tool","content":"25"}]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, coreconstant.ContextKeyUserSetting, *setting)
	t.Cleanup(func() { common.CleanupBodyStorage(c) })
	_, err := GetAndValidateTextRequest(c, constant.RelayModeChatCompletions)
	require.Error(t, err)
	apiErr := types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithStatusCode(http.StatusBadRequest), types.ErrOptionWithSkipRetry())
	require.True(t, types.IsSkipRetryError(apiErr))
	message := apiErr.Error()
	// The controller gives this classifier priority over the identical
	// DeepSeek orphan text, which would choose an octet-stream OpenAI envelope.
	require.True(t, IsStrictGlmValidationMessage(message))
	oaiErr := apiErr.ToOpenAIError()
	code, ok := oaiErr.Code.(string)
	require.True(t, ok)
	WriteZhipuBusinessError(c, apiErr.StatusCode, code, message)
	response := recorder.Result()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, response.StatusCode)
	assert.Empty(t, response.Header.Get("Content-Type"))
	assert.JSONEq(t, `{"error":{"code":"1214","message":"Messages with role 'tool' must be a response to a preceding message with 'tool_calls'"}}`, string(body))
}

func TestGlm53ToolChoiceNoneWithholdsToolDeclarations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	allDeclarations := `"tools":[{"type":"function","function":{"name":"get_weather"}}],"functions":[{"name":"legacy_weather"}],"function_call":{"name":"legacy_weather"}`
	tests := []struct {
		name         string
		model        string
		choice       string
		profile      dto.OfficialFitProfile
		noFit        bool
		wantClear    bool
		stream       bool
		declarations string
		messages     string
		// temperature is the literal sent in the body; empty means 0. kimi-k3
		// pins a different value per thinking state, so its case sends 1.
		temperature string
	}{
		{name: "flash none with validation only", model: "glm-5.3-flash", choice: `"none"`, profile: dto.OfficialFitProfile{Validate: true}, wantClear: true},
		{name: "plain model", model: "glm-5.3", choice: `"none"`, profile: dto.OfficialFitProfile{Validate: true}, wantClear: true},
		{name: "padded mixed case", model: "glm-5.3-flash", choice: `" \tNoNe\n "`, profile: dto.OfficialFitProfile{Validate: true}, wantClear: true},
		{name: "streaming request", model: "glm-5.3-flash", choice: `"none"`, profile: dto.OfficialFitProfile{Validate: true}, wantClear: true, stream: true},
		{name: "legacy declarations only", model: "glm-5.3-flash", choice: `"none"`, profile: dto.OfficialFitProfile{Validate: true}, wantClear: true, declarations: `"functions":[{"name":"legacy_weather"}],"function_call":"auto"`},
		{name: "none preserves tool exchange history", model: "glm-5.3-flash", choice: `"none"`, profile: dto.OfficialFitProfile{Validate: true}, wantClear: true, messages: `[{"role":"system","tools":[{"type":"function","function":{"name":"dynamic_weather"}}]},` + assistantToolCallsJSON("a") + `,{"role":"tool","tool_call_id":"a","content":"25"},{"role":"user","content":"summarize","client_message_extension":false}]`},
		{name: "auto", model: "glm-5.3-flash", choice: `"auto"`, profile: dto.OfficialFitProfile{Validate: true}},
		{name: "required", model: "glm-5.3-flash", choice: `"required"`, profile: dto.OfficialFitProfile{Validate: true}},
		{name: "named object", model: "glm-5.3-flash", choice: `{"type":"function","function":{"name":"get_weather"}}`, profile: dto.OfficialFitProfile{Validate: true}},
		{name: "object type none is not the strategy string", model: "glm-5.3-flash", choice: `{"type":"none"}`, profile: dto.OfficialFitProfile{Validate: true}},
		{name: "number", model: "glm-5.3-flash", choice: `1`, profile: dto.OfficialFitProfile{Validate: true}},
		{name: "null", model: "glm-5.3-flash", choice: `null`, profile: dto.OfficialFitProfile{Validate: true}},
		{name: "absent", model: "glm-5.3-flash", profile: dto.OfficialFitProfile{Validate: true}},
		{name: "empty string", model: "glm-5.3-flash", choice: `""`, profile: dto.OfficialFitProfile{Validate: true}},
		{name: "DeepSeek with validation enabled", model: "deepseek-v4-flash", choice: `"none"`, profile: dto.OfficialFitProfile{Validate: true}},
		{name: "Kimi with validation enabled", model: "kimi-k3", choice: `"none"`, profile: dto.OfficialFitProfile{Validate: true}, temperature: "1"},
		{name: "GLM 5.2 with wildcard validation enabled", model: "glm-5.2", choice: `"none"`, profile: dto.OfficialFitProfile{Validate: true}},
		{name: "authenticated user without fit", model: "glm-5.3-flash", choice: `"none"`, noFit: true},
		{name: "profile present but validation disabled", model: "glm-5.3-flash", choice: `"none"`},
		{name: "shape errors and route without validation", model: "glm-5.3-flash", choice: `"none"`, profile: dto.OfficialFitProfile{Shape: true, Errors: true, Route: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			declarations := tt.declarations
			if declarations == "" {
				declarations = allDeclarations
			}
			choice := ""
			if tt.choice != "" {
				choice = `,"tool_choice":` + tt.choice
			}
			stream := "false"
			if tt.stream {
				stream = "true"
			}
			temperature := tt.temperature
			if temperature == "" {
				temperature = "0"
			}
			messageBody := tt.messages
			if messageBody == "" {
				messageBody = `[{"role":"system","tools":[{"type":"function","function":{"name":"dynamic_weather"}}]},{"role":"user","content":"weather?","client_message_extension":false}]`
			}
			body := `{"model":"` + tt.model + `","stream":` + stream + `,"max_tokens":32,"temperature":` + temperature + `,` + declarations + choice + `,"client_extension":{"integer":9007199254740993,"zero":0,"flag":false},"messages":` + messageBody + `}`
			setting := &dto.UserSetting{}
			if !tt.noFit {
				setting.OfficialFit = &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{"*": tt.profile}}
			}
			c := newGlm53ContractContext(t, body, setting)
			var expected dto.GeneralOpenAIRequest
			require.NoError(t, common.UnmarshalJsonStr(body, &expected))
			if tt.wantClear {
				expected.Tools, expected.Functions, expected.FunctionCall = nil, nil, nil
				for i := range expected.Messages {
					expected.Messages[i].Tools = nil
				}
			}
			// A second parse models replay of the same request. Both the DTO
			// and the raw body read by passthrough attempts must stay cleaned.
			for range 2 {
				request, err := GetAndValidateTextRequest(c, constant.RelayModeChatCompletions)
				require.NoError(t, err)
				assert.Equal(t, expected, *request)
				storage, err := common.GetBodyStorage(c)
				require.NoError(t, err)
				replayBody, err := storage.Bytes()
				require.NoError(t, err)
				if !tt.wantClear {
					assert.Equal(t, []byte(body), replayBody)
					continue
				}
				var fields map[string]common.RawMessage
				require.NoError(t, common.Unmarshal(replayBody, &fields))
				assert.NotContains(t, fields, "tools")
				assert.NotContains(t, fields, "functions")
				assert.NotContains(t, fields, "function_call")
				assert.JSONEq(t, tt.choice, string(fields["tool_choice"]))
				assert.Equal(t, `{"integer":9007199254740993,"zero":0,"flag":false}`, string(fields["client_extension"]))
				var messages []map[string]common.RawMessage
				require.NoError(t, common.Unmarshal(fields["messages"], &messages))
				require.Len(t, messages, len(expected.Messages))
				for _, message := range messages {
					assert.NotContains(t, message, "tools")
				}
				assert.Equal(t, `false`, string(messages[len(messages)-1]["client_message_extension"]))
				assert.Zero(t, request.GetTokenCountMeta().ToolsCount)
				assert.Equal(t, int64(len(replayBody)), c.Request.ContentLength)
			}
		})
	}
}
