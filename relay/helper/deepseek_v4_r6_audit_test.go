package helper

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live-probed 2026-09-01 (audit r6): the official endpoint enforces the
// penalty range, the message-role serde enum, Empty input messages, and
// rejects image input on the text-only V4 models.

func TestDeepSeekV4PenaltyRangeValidationMatchesOfficial(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"presence 2.5 is rejected", `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"1+1=?"}],"presence_penalty":2.5,"max_tokens":32}`, "Invalid presence_penalty value, the valid range of presence_penalty is [-2, 2]"},
		{"presence -2.5 is rejected", `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"1+1=?"}],"presence_penalty":-2.5,"max_tokens":32}`, "Invalid presence_penalty value, the valid range of presence_penalty is [-2, 2]"},
		{"frequency 2.5 is rejected", `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"1+1=?"}],"frequency_penalty":2.5,"max_tokens":32}`, "Invalid frequency_penalty value, the valid range of frequency_penalty is [-2, 2]"},
		{"frequency -2.5 is rejected", `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"1+1=?"}],"frequency_penalty":-2.5,"max_tokens":32}`, "Invalid frequency_penalty value, the valid range of frequency_penalty is [-2, 2]"},
		{"boundary 2 is accepted", `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"1+1=?"}],"presence_penalty":2,"frequency_penalty":-2,"max_tokens":32}`, ""},
		{"vision variant uses the same range", `{"model":"deepseek-v4-flash-vision-exp","messages":[{"role":"user","content":"1+1=?"}],"presence_penalty":2.5,"max_tokens":32}`, "Invalid presence_penalty value, the valid range of presence_penalty is [-2, 2]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GetAndValidateTextRequest(newDeepSeekV4FitContext(tt.body), constant.RelayModeChatCompletions)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			assert.Equal(t, tt.wantErr, apiErr.ToOpenAIError().Message)
		})
	}

	t.Run("non-V4 model penalties are untouched", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			bytes.NewBufferString(`{"model":"gpt-4o","messages":[{"role":"user","content":"1+1=?"}],"presence_penalty":2.5}`))
		c.Request.Header.Set("Content-Type", "application/json")
		request, err := GetAndValidateTextRequest(c, constant.RelayModeChatCompletions)
		require.NoError(t, err)
		require.NotNil(t, request.PresencePenalty)
		assert.Equal(t, 2.5, *request.PresencePenalty)
	})
}

func TestDeepSeekV4MessageContractValidationMatchesOfficial(t *testing.T) {
	gin.SetMode(gin.TestMode)
	roleErr := "Failed to deserialize the JSON body into the target type: messages[0].role: unknown variant `developer`, expected one of `system`, `user`, `assistant`, `tool`, `latest_reminder`"
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"developer role is a deserialization failure", `{"model":"deepseek-v4-pro","messages":[{"role":"developer","content":"x"},{"role":"user","content":"1+1=?"}],"max_tokens":32}`, roleErr},
		{"function role is a deserialization failure", `{"model":"deepseek-v4-pro","messages":[{"role":"function","content":"x"},{"role":"user","content":"1+1=?"}],"max_tokens":32}`, "Failed to deserialize the JSON body into the target type: messages[0].role: unknown variant `function`, expected one of `system`, `user`, `assistant`, `tool`, `latest_reminder`"},
		{"latest_reminder is an accepted official variant", `{"model":"deepseek-v4-pro","messages":[{"role":"latest_reminder","content":"x"},{"role":"user","content":"1+1=?"}],"max_tokens":32}`, ""},
		{"empty message list is Empty input messages", `{"model":"deepseek-v4-pro","messages":[],"max_tokens":32}`, "Empty input messages"},
		{"image on the text-only flash is rejected", `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/x.png"}},{"type":"text","text":"这是什么？"}]}],"max_tokens":32}`, "This model does not support image"},
		{"image on the text-only pro is rejected", `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/x.png"}},{"type":"text","text":"这是什么？"}]}],"max_tokens":32}`, "This model does not support image"},
		{"vision variant accepts image parts", `{"model":"deepseek-v4-flash-vision-exp","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/x.png"}},{"type":"text","text":"这是什么？"}]}],"max_tokens":32}`, ""},
		{"empty string content is accepted", `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":""}],"max_tokens":32}`, ""},
		{"text parts are accepted", `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":[{"type":"text","text":"1+1等于几？简答。"}]}],"max_tokens":32}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GetAndValidateTextRequest(newDeepSeekV4FitContext(tt.body), constant.RelayModeChatCompletions)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			assert.Equal(t, tt.wantErr, apiErr.ToOpenAIError().Message)
		})
	}
}

func TestStrictFitContentTypeSplitsSerdeClass(t *testing.T) {
	// Official serde failures ride application/json; every other strict-fit
	// rejection rides application/octet-stream (probed 2026-09-01).
	assert.Equal(t, "application/json", StrictFitContentType("Failed to deserialize the JSON body into the target type: top_logprobs: invalid value: integer `-1`, expected u8"))
	assert.Equal(t, "application/json", StrictFitContentType("Failed to deserialize the JSON body into the target type: messages[0].role: unknown variant `developer`, expected one of `system`, `user`, `assistant`, `tool`, `latest_reminder`"))
	assert.Equal(t, "application/json", StrictFitContentType(deepSeekV4ReasoningEffortDeserMessage("extreme")))
	assert.Equal(t, "application/octet-stream", StrictFitContentType("Invalid temperature value, the valid range of temperature is [0, 2]"))
	assert.Equal(t, "application/octet-stream", StrictFitContentType(deepSeekV4ReasoningPassbackText))
	assert.Equal(t, "application/octet-stream", StrictFitContentType(deepSeekV4JSONSchemaMessage))
	assert.Equal(t, "application/octet-stream", StrictFitContentType(deepSeekV4EmptyMessagesMessage))
	assert.Equal(t, "application/octet-stream", StrictFitContentType(deepSeekV4ImageUnsupportedMessage))
	assert.Equal(t, "application/octet-stream", StrictFitContentType(deepSeekV4ToolChoiceThinkingMessage))
}

func TestDeepSeekV4ThinkingValidationMatchesOfficial(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// Official thinking contract (live-probed 2026-09-02, buyer round 6):
	// the type enum is exactly adaptive/enabled/disabled (adaptive behaves
	// like enabled, unknown extra fields are ignored); a missing type key,
	// scalar thinking values, and non-string types each carry their own
	// official text; thinking:null is ignored.
	tests := []struct {
		name    string
		thinking string
		wantErr string
	}{
		{"adaptive is an accepted official variant", `{"type":"adaptive"}`, ""},
		{"enabled is accepted", `{"type":"enabled"}`, ""},
		{"disabled is accepted", `{"type":"disabled"}`, ""},
		{"unknown extra fields are ignored", `{"type":"adaptive","bogus":1}`, ""},
		{"bogus type string is the unknown-variant serde error", `{"type":"didn't"}`, "Failed to deserialize the JSON body into the target type: thinking.type: unknown variant `didn't`, expected one of `adaptive`, `enabled`, `disabled`"},
		{"case differs from the enum is rejected", `{"type":"Enabled"}`, "Failed to deserialize the JSON body into the target type: thinking.type: unknown variant `Enabled`, expected one of `adaptive`, `enabled`, `disabled`"},
		{"missing type key is a serde error", `{}`, "Failed to deserialize the JSON body into the target type: thinking: missing field `type`"},
		{"numeric type is the plain-text parse failure", `{"type":123}`, deepSeekV4ThinkingParseExpectedValueText},
		{"boolean type is the plain-text parse failure", `{"type":true}`, deepSeekV4ThinkingParseExpectedValueText},
		{"null type is the plain-text parse failure", `{"type":null}`, deepSeekV4ThinkingParseExpectedValueText},
		{"scalar string thinking is a serde error", `"disabled"`, "Failed to deserialize the JSON body into the target type: thinking: invalid type: string \"disabled\", expected struct ThinkingOptions"},
		{"scalar boolean thinking is a serde error", `true`, "Failed to deserialize the JSON body into the target type: thinking: invalid type: boolean `true`, expected struct ThinkingOptions"},
		{"scalar integer thinking is a serde error", `1`, "Failed to deserialize the JSON body into the target type: thinking: invalid type: integer `1`, expected struct ThinkingOptions"},
		{"scalar floating point thinking is a serde error", `1.5`, "Failed to deserialize the JSON body into the target type: thinking: invalid type: floating point `1.5`, expected struct ThinkingOptions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"用一个词描述春天。"}],"max_tokens":32,"thinking":` + tt.thinking + `}`
			_, err := GetAndValidateTextRequest(newDeepSeekV4FitContext(body), constant.RelayModeChatCompletions)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			// apiErr.Error() carries the raw official text; ToOpenAIError()
			// runs the generic info masker whose domain pattern rewrites
			// thinking.type into ***.type (the controller render path
			// restores the unmasked message for strict-fit errors).
			assert.Equal(t, tt.wantErr, apiErr.Error())
		})
	}

	t.Run("null thinking is ignored like the official endpoint", func(t *testing.T) {
		body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"用一个词描述春天。"}],"max_tokens":32,"thinking":null}`
		_, err := GetAndValidateTextRequest(newDeepSeekV4FitContext(body), constant.RelayModeChatCompletions)
		require.NoError(t, err)
	})

	t.Run("vision variant carries the same enum", func(t *testing.T) {
		body := `{"model":"deepseek-v4-flash-vision-exp","messages":[{"role":"user","content":"用一个词描述春天。"}],"max_tokens":32,"thinking":{"type":"didn't"}}`
		_, err := GetAndValidateTextRequest(newDeepSeekV4FitContext(body), constant.RelayModeChatCompletions)
		require.Error(t, err)
		var apiErr *types.NewAPIError
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, "Failed to deserialize the JSON body into the target type: thinking.type: unknown variant `didn't`, expected one of `adaptive`, `enabled`, `disabled`", apiErr.Error())
	})
}

func TestStrictFitPlainRenderClass(t *testing.T) {
	assert.True(t, StrictFitRendersPlainText(deepSeekV4ThinkingParseExpectedValueText))
	assert.Equal(t, "application/octet-stream", StrictFitContentType(deepSeekV4ThinkingParseExpectedValueText))
	assert.False(t, StrictFitRendersPlainText("Failed to deserialize the JSON body into the target type: thinking: missing field `type`"))
	assert.Equal(t, "application/json", StrictFitContentType("Failed to deserialize the JSON body into the target type: thinking: missing field `type`"))
}

func TestIsStrictFitValidationMessageCoversRound6ThinkingTexts(t *testing.T) {
	recognized := []string{
		"Failed to deserialize the JSON body into the target type: thinking.type: unknown variant `didn't`, expected one of `adaptive`, `enabled`, `disabled`",
		"Failed to deserialize the JSON body into the target type: thinking: missing field `type`",
		"Failed to deserialize the JSON body into the target type: thinking: invalid type: string \"disabled\", expected struct ThinkingOptions",
		"Failed to deserialize the JSON body into the target type: thinking: invalid type: boolean `true`, expected struct ThinkingOptions",
		"Failed to deserialize the JSON body into the target type: thinking: invalid type: integer `1`, expected struct ThinkingOptions",
		"Failed to deserialize the JSON body into the target type: thinking: invalid type: floating point `1.5`, expected struct ThinkingOptions",
		deepSeekV4ThinkingParseExpectedValueText,
	}
	for _, msg := range recognized {
		assert.True(t, IsStrictFitValidationMessage(msg), "%q", msg)
	}
}

func TestIsStrictFitValidationMessageCoversAuditR6Texts(t *testing.T) {
	recognized := []string{
		"Invalid presence_penalty value, the valid range of presence_penalty is [-2, 2]",
		"Invalid frequency_penalty value, the valid range of frequency_penalty is [-2, 2]",
		"This response_format type is unavailable now",
		"Thinking mode does not support this tool_choice",
		"Empty input messages",
		"This model does not support image",
		"Failed to deserialize the JSON body into the target type: messages[0].role: unknown variant `developer`, expected one of `system`, `user`, `assistant`, `tool`, `latest_reminder`",
		"Failed to deserialize the JSON body into the target type: tool_choice: expected one of `none`, `auto`, `required` or a tool",
	}
	for _, msg := range recognized {
		assert.True(t, IsStrictFitValidationMessage(msg), "%q", msg)
	}
}
