package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdaptorRejectsUnsupportedEndpoints(t *testing.T) {
	adaptor := &Adaptor{}

	_, err := adaptor.ConvertGeminiRequest(nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Gemini endpoint not supported")
	var apiErr *types.NewAPIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.True(t, types.IsSkipRetryError(apiErr))

	_, err = adaptor.ConvertAudioRequest(nil, nil, dto.AudioRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audio endpoint not supported")

	_, err = adaptor.ConvertImageRequest(nil, nil, dto.ImageRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image endpoint not supported")

	_, err = adaptor.ConvertRerankRequest(nil, 0, dto.RerankRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/v1/rerank endpoint not supported")

	_, err = adaptor.ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/v1/responses endpoint not supported")
}

func TestOpenAIToGenerateConvertsPromptSuffixStopAndThinking(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Model:     "llama3",
		Prompt:    "hello world",
		Suffix:    " tail",
		Stop:      []any{"\n", "END"},
		Reasoning: json.RawMessage(`{"effort":"high"}`),
	}

	converted, err := openAIToGenerate(nil, request)
	require.NoError(t, err)
	require.NotNil(t, converted)
	assert.Equal(t, "hello world", converted.Prompt)
	assert.Equal(t, " tail", converted.Suffix)
	assert.Equal(t, []string{"\n", "END"}, converted.Options["stop"])
	assert.JSONEq(t, `"high"`, string(converted.Think))
}

func TestOpenAIToGenerateRejectsMultiplePrompts(t *testing.T) {
	for _, prompt := range []any{
		[]string{"first", "second"},
		[]any{"first", "second"},
	} {
		_, err := openAIToGenerate(nil, &dto.GeneralOpenAIRequest{Prompt: prompt})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "multiple completion prompts")
	}
}

func TestOpenAIToGenerateRejectsNonTextPromptItems(t *testing.T) {
	for _, prompt := range []any{
		[]any{"hello", 42},
		[]any{1, 2},
		[]any{},
		[]int{1, 2},
	} {
		_, err := openAIToGenerate(nil, &dto.GeneralOpenAIRequest{Prompt: prompt})
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "prompt"), err.Error())
	}
}

func TestOllamaConversionRejectsUnsupportedSuffixAndStop(t *testing.T) {
	_, err := openAIToGenerate(nil, &dto.GeneralOpenAIRequest{Suffix: 123})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "suffix")

	_, err = openAIToGenerate(nil, &dto.GeneralOpenAIRequest{Stop: []any{"END", 123}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop item")

	_, err = openAIChatToOllamaChat(nil, &dto.GeneralOpenAIRequest{Stop: map[string]any{"value": "END"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop type")
}

func TestOpenAIChatRejectsUnsupportedContentPart(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{{
			Role: "user",
			Content: []any{
				map[string]any{"type": dto.ContentTypeText, "text": "keep this"},
				map[string]any{"type": dto.ContentTypeInputAudio, "input_audio": map[string]any{}},
			},
		}},
	}

	_, err := openAIChatToOllamaChat(nil, request)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported ollama message content part type")
}

func TestOpenAIChatRejectsUnsupportedTool(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Tools: []dto.ToolCallRequest{{
			Type:     "custom",
			Function: dto.FunctionRequest{Name: "lookup"},
		}},
	}

	_, err := openAIChatToOllamaChat(nil, request)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported ollama tool type")

	request.Tools[0] = dto.ToolCallRequest{Type: "function"}
	_, err = openAIChatToOllamaChat(nil, request)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "function name is required")
}

func TestOllamaResponseFormatRejectsUnsupportedOrIncompleteSchema(t *testing.T) {
	_, err := toOllamaResponseFormat(&dto.ResponseFormat{Type: "text"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported ollama response format type")

	_, err = toOllamaResponseFormat(&dto.ResponseFormat{Type: "json_schema"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing schema")

	_, err = toOllamaResponseFormat(&dto.ResponseFormat{
		Type:       "json_schema",
		JsonSchema: json.RawMessage(`{"name":"lookup"}`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing schema")
}

func TestOpenAIChatRejectsInvalidToolArguments(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{{
			Role:      "assistant",
			ToolCalls: json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"not-json"}}]`),
		}},
	}

	_, err := openAIChatToOllamaChat(nil, request)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid arguments for ollama tool "lookup"`)
}

func TestOpenAIChatRejectsMalformedToolCalls(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{{
			Role:      "assistant",
			ToolCalls: json.RawMessage(`[{`),
		}},
	}

	_, err := openAIChatToOllamaChat(nil, request)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid ollama tool calls")
}

func newOllamaTestServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestPullOllamaModelReadsSuccessAndDoesNotMutateClientTimeout(t *testing.T) {
	server := newOllamaTestServer(t, http.StatusOK, `{"status":"success","digest":"sha256:ok"}`)
	defer server.Close()

	client, err := service.GetHttpClientWithProxy("")
	require.NoError(t, err)
	originalTimeout := client.Timeout

	require.NoError(t, PullOllamaModel(context.Background(), server.URL, "", "llama3", ""))
	require.Equal(t, originalTimeout, client.Timeout)
}

func TestOllamaManagementRequestsDoNotMutateSharedClientTimeout(t *testing.T) {
	client, err := service.GetHttpClientWithProxy("")
	require.NoError(t, err)
	originalTimeout := client.Timeout

	tests := []struct {
		name string
		body string
		call func(string) error
	}{
		{
			name: "stream pull",
			body: `{"status":"success"}` + "\n",
			call: func(url string) error {
				return PullOllamaModelStream(context.Background(), url, "", "llama3", "", nil)
			},
		},
		{
			name: "delete",
			body: "",
			call: func(url string) error {
				return DeleteOllamaModel(context.Background(), url, "", "llama3", "")
			},
		},
		{
			name: "version",
			body: `{"version":"0.12.0"}`,
			call: func(url string) error {
				_, err := FetchOllamaVersion(context.Background(), url, "", "")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newOllamaTestServer(t, http.StatusOK, tt.body)
			defer server.Close()

			require.NoError(t, tt.call(server.URL))
			require.Equal(t, originalTimeout, client.Timeout)
		})
	}
}

func TestPullOllamaModelPropagatesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := newOllamaTestServer(t, http.StatusOK, `{"status":"success"}`)
	defer server.Close()

	err := PullOllamaModel(ctx, server.URL, "", "llama3", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), context.Canceled.Error())
}

func TestOllamaEmbeddingHandlerNormalizesNegativeTokenCount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	usage, apiErr := ollamaEmbeddingHandler(c, &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "embed-model"},
	}, &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"embeddings":[[0.1]],"prompt_eval_count":-7}`)),
	})
	require.Nil(t, apiErr)
	require.Equal(t, 0, usage.PromptTokens)
	require.Equal(t, 0, usage.TotalTokens)

	var response dto.OpenAIEmbeddingResponse
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, 0, response.Usage.PromptTokens)
}

func TestOllamaEmbeddingRequestAcceptsOnlyTextInput(t *testing.T) {
	adaptor := &Adaptor{}

	converted, err := adaptor.ConvertEmbeddingRequest(nil, nil, dto.EmbeddingRequest{
		Model: "embed-model",
		Input: []any{"first", "second"},
	})
	require.NoError(t, err)
	request := converted.(*OllamaEmbeddingRequest)
	assert.Equal(t, []string{"first", "second"}, request.Input)

	for _, input := range []any{
		[]any{"valid", 42},
		[]any{[]int{1, 2}},
		[]int{1, 2},
		nil,
	} {
		_, err = adaptor.ConvertEmbeddingRequest(nil, nil, dto.EmbeddingRequest{Model: "embed-model", Input: input})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "embedding input")
	}
}

func TestPullOllamaModelRejectsMalformedAndErrorResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "malformed", body: `{`, want: "解析拉取响应失败"},
		{name: "error status", body: `{"status":"error","error":"upstream denied"}`, want: "upstream denied"},
		{name: "incomplete status", body: `{"status":"downloading"}`, want: "拉取模型未完成"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newOllamaTestServer(t, http.StatusOK, tt.body)
			defer server.Close()

			err := PullOllamaModel(context.Background(), server.URL, "", "llama3", "")
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestOllamaNonStreamingErrorsMaskAndLimitUpstreamBody(t *testing.T) {
	secret := "sk-ollama-test-secret-123456"
	body := `{"error":"api_key=` + secret + `"}` + strings.Repeat("x", 3000)

	tests := []struct {
		name string
		call func(string) error
	}{
		{name: "models", call: func(url string) error {
			_, err := FetchOllamaModels(context.Background(), url, "", "")
			return err
		}},
		{name: "pull", call: func(url string) error {
			return PullOllamaModel(context.Background(), url, "", "llama3", "")
		}},
		{name: "delete", call: func(url string) error {
			return DeleteOllamaModel(context.Background(), url, "", "llama3", "")
		}},
		{name: "version", call: func(url string) error {
			_, err := FetchOllamaVersion(context.Background(), url, "", "")
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newOllamaTestServer(t, http.StatusInternalServerError, body)
			defer server.Close()

			err := tt.call(server.URL)
			require.Error(t, err)
			require.NotContains(t, err.Error(), secret)
			require.Less(t, len(err.Error()), 2600, fmt.Sprintf("error was not bounded: %d bytes", len(err.Error())))
		})
	}
}

func TestDeleteOllamaModelAcceptsEmptySuccessBody(t *testing.T) {
	server := newOllamaTestServer(t, http.StatusOK, "")
	defer server.Close()

	require.NoError(t, DeleteOllamaModel(context.Background(), server.URL, "", "llama3", ""))
}

func TestDeleteOllamaModelRejectsMalformedAndErrorBody(t *testing.T) {
	tests := []string{
		`{`,
		`{"status":"error","error":"delete denied"}`,
	}

	for _, body := range tests {
		server := newOllamaTestServer(t, http.StatusOK, body)
		err := DeleteOllamaModel(context.Background(), server.URL, "", "llama3", "")
		server.Close()
		require.Error(t, err)
	}
}

func TestPullOllamaModelStreamRejectsMalformedLine(t *testing.T) {
	server := newOllamaTestServer(t, http.StatusOK, "{\"status\":\"pulling manifest\"}\nnot-json\n")
	defer server.Close()

	err := PullOllamaModelStream(context.Background(), server.URL, "", "llama3", "", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "解析流式响应失败")
}

func TestPullOllamaModelStreamRejectsErrorFieldWithoutErrorStatus(t *testing.T) {
	server := newOllamaTestServer(t, http.StatusOK, "{\"status\":\"pulling manifest\",\"error\":\"pull denied\"}\n{\"status\":\"success\"}\n")
	defer server.Close()

	err := PullOllamaModelStream(context.Background(), server.URL, "", "llama3", "", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pull denied")
}

func TestPullOllamaModelStreamRequiresSuccess(t *testing.T) {
	server := newOllamaTestServer(t, http.StatusOK, "{\"status\":\"pulling manifest\"}\n")
	defer server.Close()

	err := PullOllamaModelStream(context.Background(), server.URL, "", "llama3", "", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "拉取模型未完成")
}

func TestPullOllamaModelStreamMasksErrorFrame(t *testing.T) {
	secret := "sk-ollama-stream-secret-123456"
	server := newOllamaTestServer(t, http.StatusOK, `{"status":"error","error":"api_key=`+secret+`"}`+"\n")
	defer server.Close()

	err := PullOllamaModelStream(context.Background(), server.URL, "", "llama3", "", nil)
	require.Error(t, err)
	require.NotContains(t, err.Error(), secret)
}

func TestPullOllamaModelStreamSanitizesErrorCallback(t *testing.T) {
	secret := "sk-ollama-callback-secret-123456"
	server := newOllamaTestServer(t, http.StatusOK, `{"status":"error","error":"api_key=`+secret+`"}`+"\n")
	defer server.Close()

	var callbackResponse OllamaPullResponse
	err := PullOllamaModelStream(context.Background(), server.URL, "", "llama3", "", func(progress OllamaPullResponse) {
		callbackResponse = progress
	})
	require.Error(t, err)
	require.NotContains(t, callbackResponse.Error, secret)
}
