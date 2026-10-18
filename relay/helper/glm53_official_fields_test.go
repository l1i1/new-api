package helper

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func floatPtr(v float64) *float64 { return &v }

// The rejection matrix is calibrated against the live open.bigmodel.cn
// api/paas/v4 endpoint. Since the 2026-09-15 re-check only these are rejected:
// disabled thinking, reasoning_effort outside low/high/max, empty messages and
// unknown model ids. temperature/top_p outside [0,1] used to be in this list
// (2026-08-28) but the live endpoint now answers 200 — see the accept matrix
// and the function comment.
func TestGlm53OfficialFieldsReject(t *testing.T) {
	base := dto.GeneralOpenAIRequest{Model: "glm-5.3", Messages: []dto.Message{{Role: "user", Content: "1+1=?"}}}
	rejected := []struct {
		name    string
		request *dto.GeneralOpenAIRequest
		code    string
		message string
	}{
		{
			"thinking disabled",
			&dto.GeneralOpenAIRequest{Model: "glm-5.3", Messages: base.Messages,
				THINKING: []byte(`{"type":"disabled"}`)},
			"1210", glm53ThinkingMessage,
		},
		{
			"effort ultra",
			&dto.GeneralOpenAIRequest{Model: "glm-5.3", Messages: base.Messages, ReasoningEffort: "ultra"},
			"1210", glm53ThinkingMessage,
		},
		{
			"effort medium",
			&dto.GeneralOpenAIRequest{Model: "glm-5.3", Messages: base.Messages, ReasoningEffort: "medium"},
			"1210", glm53ThinkingMessage,
		},
		{
			"empty messages",
			&dto.GeneralOpenAIRequest{Model: "glm-5.3"},
			"1214", glm53EmptyMessagesMessage,
		},
	}
	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGlm53OfficialFields(tt.request)
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			oaiErr := apiErr.ToOpenAIError()
			assert.Equal(t, tt.code, oaiErr.Code)
			assert.Equal(t, tt.message, oaiErr.Message)
		})
	}
}

// The official endpoint tolerates n, penalties, logprobs, top_logprobs,
// arbitrary tool names, specified tool_choice, json_object without the "json"
// keyword, explicit default sampling values and low-reasoning effort; none of
// these may be rejected locally when fit mode is on.
func TestGlm53OfficialFieldsAccept(t *testing.T) {
	base := dto.GeneralOpenAIRequest{Model: "glm-5.3", Messages: []dto.Message{{Role: "user", Content: "1+1=?"}}}
	accepted := []*dto.GeneralOpenAIRequest{
		&base,
		{Model: "glm-5.3", Messages: base.Messages, THINKING: []byte(`{"type":"enabled"}`)},
		{Model: "glm-5.3", Messages: base.Messages, ReasoningEffort: "low"},
		{Model: "glm-5.3", Messages: base.Messages, ReasoningEffort: "high"},
		{Model: "glm-5.3", Messages: base.Messages, ReasoningEffort: "max"},
		{Model: "glm-5.3", Messages: base.Messages, Temperature: floatPtr(0)},
		{Model: "glm-5.3", Messages: base.Messages, Temperature: floatPtr(1.0)},
		{Model: "glm-5.3", Messages: base.Messages, TopP: floatPtr(0.1)},
		{Model: "glm-5.3", Messages: base.Messages, TopP: floatPtr(0.95)},
		// The [0,1] range rejection was retired on 2026-10-12: the live
		// endpoint answers 200 for these (re-measured live 2026-09-15; A/B on
		// 2026-10-12 with validate off confirmed the official channel accepts
		// them). Keeping the check made fit mode stricter than the endpoint.
		{Model: "glm-5.3", Messages: base.Messages, Temperature: floatPtr(2.5)},
		{Model: "glm-5.3", Messages: base.Messages, Temperature: floatPtr(-0.1)},
		{Model: "glm-5.3", Messages: base.Messages, Temperature: floatPtr(1.2)},
		{Model: "glm-5.3", Messages: base.Messages, TopP: floatPtr(1.5)},
		{Model: "glm-5.3", Messages: base.Messages, N: intPtr(2)},
		{Model: "glm-5.3", Messages: base.Messages, PresencePenalty: floatPtr(1.5)},
		{Model: "glm-5.3", Messages: base.Messages, FrequencyPenalty: floatPtr(1.5)},
		{Model: "glm-5.3", Messages: base.Messages, LogProbs: boolPtr(true)},
		{Model: "glm-5.3", Messages: base.Messages, TopLogProbs: intPtr(5)},
		{Model: "glm-5.3", Messages: base.Messages, LogProbs: boolPtr(true), TopLogProbs: intPtr(21)},
		{Model: "glm-5.3", Messages: base.Messages,
			ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}},
		{Model: "glm-5.3", Messages: base.Messages,
			Tools: []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "get weather"}}}},
		{Model: "glm-5.3", Messages: base.Messages,
			ResponseFormat: &dto.ResponseFormat{Type: "json_object"}},
		// non-5.3 models are never inspected
		{Model: "glm-5.2", THINKING: []byte(`{"type":"disabled"}`)},
		{Model: "deepseek-v4-flash", Temperature: floatPtr(2.5)},
	}
	for i, req := range accepted {
		assert.NoError(t, validateGlm53OfficialFields(req), "case %d", i)
	}
}

func TestGlm53ValidationMessageRecognized(t *testing.T) {
	for _, msg := range []string{
		glm53ThinkingMessage,
		glm53TemperatureMessage,
		glm53TopPMessage,
		glm53ModelNotFoundMessage,
		glm53EmptyMessagesMessage,
	} {
		assert.True(t, IsStrictGlmValidationMessage(msg), "%q", msg)
	}
	// DS/K3 texts are not GLM texts and vice versa.
	assert.False(t, IsStrictGlmValidationMessage("Invalid temperature value, the valid range of temperature is [0, 2]"))
	assert.False(t, IsStrictFitValidationMessage(glm53ThinkingMessage))
}

// The official Zhipu business 400 carries no Content-Type header at all
// (live-measured 2026-09-15). Asserted on the wire rather than on a recorder,
// because the point of the nil header entry is to suppress net/http's content
// sniffing — a recorder never sniffs and would not catch a regression there.
func TestWriteZhipuBusinessErrorOmitsContentTypeOnTheWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/t", func(c *gin.Context) {
		WriteZhipuBusinessError(c, http.StatusBadRequest, "1210", glm53ThinkingMessage)
	})
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/t", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	_, present := resp.Header["Content-Type"]
	assert.False(t, present, "official Zhipu business 400 sends no Content-Type, got %q", resp.Header.Get("Content-Type"))
	assert.JSONEq(t, `{"error":{"code":"1210","message":"`+glm53ThinkingMessage+`"}}`, string(body))
}
