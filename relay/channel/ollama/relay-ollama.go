package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

func toOllamaResponseFormat(responseFormat *dto.ResponseFormat) (any, error) {
	if responseFormat == nil {
		return nil, nil
	}
	switch responseFormat.Type {
	case "json", "json_object":
		return "json", nil
	case "json_schema":
		if len(responseFormat.JsonSchema) == 0 {
			return nil, fmt.Errorf("invalid ollama response format: json_schema is missing schema")
		}
		var jsonSchema dto.FormatJsonSchema
		if err := common.Unmarshal(responseFormat.JsonSchema, &jsonSchema); err != nil {
			return nil, fmt.Errorf("invalid ollama response format: %w", err)
		}
		if jsonSchema.Schema == nil {
			return nil, fmt.Errorf("invalid ollama response format: json_schema is missing schema")
		}
		return jsonSchema.Schema, nil
	default:
		return nil, fmt.Errorf("unsupported ollama response format type %q", responseFormat.Type)
	}
}

func resolveOllamaThink(r *dto.GeneralOpenAIRequest) (json.RawMessage, error) {
	if len(r.Think) > 0 {
		return r.Think, nil
	}

	effort := r.ReasoningEffort
	if len(r.Reasoning) > 0 {
		var reasoning dto.Reasoning
		if err := common.Unmarshal(r.Reasoning, &reasoning); err != nil {
			return nil, fmt.Errorf("invalid ollama reasoning: %w", err)
		}
		effort = lo.CoalesceOrEmpty(reasoning.Effort, effort)
	}
	if effort == "" {
		return nil, nil
	}

	var thinkValue any
	switch effort {
	case "none":
		thinkValue = false
	case "low", "medium", "high", "max":
		thinkValue = effort
	default:
		return nil, fmt.Errorf("unsupported ollama reasoning effort %q", effort)
	}
	think, err := common.Marshal(thinkValue)
	if err != nil {
		return nil, fmt.Errorf("marshal ollama think: %w", err)
	}
	return json.RawMessage(think), nil
}

func ollamaStopOption(stop any) ([]string, error) {
	switch value := stop.(type) {
	case string:
		return []string{value}, nil
	case []string:
		return value, nil
	case []any:
		result := make([]string, len(value))
		for i, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("unsupported ollama stop item at index %d: %T", i, item)
			}
			result[i] = text
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported ollama stop type %T", stop)
	}
}

func ollamaMessageParts(message *dto.Message) ([]dto.MediaContent, error) {
	if message.Content == nil {
		return nil, nil
	}
	if message.IsStringContent() {
		return []dto.MediaContent{{Type: dto.ContentTypeText, Text: message.StringContent()}}, nil
	}

	var parts []dto.MediaContent
	switch content := message.Content.(type) {
	case []dto.MediaContent:
		parts = content
	case []any:
		for i, item := range content {
			switch value := item.(type) {
			case dto.MediaContent:
				parts = append(parts, value)
			case map[string]any:
				contentType, ok := value["type"].(string)
				if !ok {
					return nil, fmt.Errorf("invalid ollama message content part at index %d: missing type", i)
				}
				if contentType != dto.ContentTypeText && contentType != dto.ContentTypeImageURL {
					return nil, fmt.Errorf("unsupported ollama message content part type %q", contentType)
				}
				if contentType == dto.ContentTypeText {
					if _, ok := value["text"].(string); !ok {
						return nil, fmt.Errorf("invalid ollama text content part at index %d", i)
					}
				}
			case nil:
				return nil, fmt.Errorf("invalid ollama message content part at index %d", i)
			default:
				return nil, fmt.Errorf("unsupported ollama message content part at index %d: %T", i, item)
			}
		}
		parsed := message.ParseContent()
		if len(parsed) != len(content) {
			return nil, fmt.Errorf("unsupported ollama message content part")
		}
		parts = parsed
	default:
		return nil, fmt.Errorf("unsupported ollama message content type %T", message.Content)
	}

	for _, part := range parts {
		switch part.Type {
		case dto.ContentTypeText:
		case dto.ContentTypeImageURL:
			if part.ToFileSource() == nil {
				return nil, fmt.Errorf("invalid ollama image content part")
			}
		default:
			return nil, fmt.Errorf("unsupported ollama message content part type %q", part.Type)
		}
	}
	return parts, nil
}

func openAIChatToOllamaChat(c *gin.Context, r *dto.GeneralOpenAIRequest) (*OllamaChatRequest, error) {
	think, err := resolveOllamaThink(r)
	if err != nil {
		return nil, err
	}

	chatReq := &OllamaChatRequest{
		Model:   r.Model,
		Stream:  lo.FromPtrOr(r.Stream, false),
		Options: map[string]any{},
		Think:   think,
	}
	format, err := toOllamaResponseFormat(r.ResponseFormat)
	if err != nil {
		return nil, err
	}
	chatReq.Format = format

	// options mapping
	if r.Temperature != nil {
		chatReq.Options["temperature"] = r.Temperature
	}
	if r.TopP != nil {
		chatReq.Options["top_p"] = lo.FromPtr(r.TopP)
	}
	if r.TopK != nil {
		chatReq.Options["top_k"] = lo.FromPtr(r.TopK)
	}
	if r.FrequencyPenalty != nil {
		chatReq.Options["frequency_penalty"] = lo.FromPtr(r.FrequencyPenalty)
	}
	if r.PresencePenalty != nil {
		chatReq.Options["presence_penalty"] = lo.FromPtr(r.PresencePenalty)
	}
	if r.Seed != nil {
		chatReq.Options["seed"] = int(lo.FromPtr(r.Seed))
	}
	if mt := r.GetMaxTokens(); mt != 0 {
		chatReq.Options["num_predict"] = int(mt)
	}

	if r.Stop != nil {
		stop, err := ollamaStopOption(r.Stop)
		if err != nil {
			return nil, err
		}
		if len(stop) > 0 {
			chatReq.Options["stop"] = stop
		}
	}

	if len(r.Tools) > 0 {
		tools := make([]OllamaTool, 0, len(r.Tools))
		for _, tool := range r.Tools {
			if tool.Type != "function" {
				return nil, fmt.Errorf("unsupported ollama tool type %q", tool.Type)
			}
			if strings.TrimSpace(tool.Function.Name) == "" {
				return nil, fmt.Errorf("invalid ollama tool: function name is required")
			}
			tools = append(tools, OllamaTool{
				Type: "function",
				Function: OllamaToolFunction{
					Name:        tool.Function.Name,
					Description: tool.Function.Description,
					Parameters:  tool.Function.Parameters,
				},
			})
		}
		chatReq.Tools = tools
	}

	chatReq.Messages = make([]OllamaChatMessage, 0, len(r.Messages))
	toolNamesByCallID := make(map[string]string)
	for _, m := range r.Messages {
		var textBuilder strings.Builder
		var images []string
		parts, err := ollamaMessageParts(&m)
		if err != nil {
			return nil, err
		}
		for _, part := range parts {
			if part.Type == dto.ContentTypeImageURL {
				source := part.ToFileSource()
				base64Data, _, err := service.GetBase64Data(c, source, "fetch image for ollama chat")
				if err != nil {
					return nil, err
				}
				if base64Data == "" {
					return nil, fmt.Errorf("empty ollama image content")
				}
				images = append(images, base64Data)
			} else {
				textBuilder.WriteString(part.Text)
			}
		}
		cm := OllamaChatMessage{Role: m.Role, Content: textBuilder.String()}
		if len(images) > 0 {
			cm.Images = images
		}
		if m.Role == "assistant" {
			if reasoning, ok := lo.Coalesce(m.ReasoningContent, m.Reasoning); ok {
				thinking, err := common.Marshal(*reasoning)
				if err != nil {
					return nil, fmt.Errorf("marshal ollama thinking: %w", err)
				}
				cm.Thinking = thinking
			}
		}
		if m.Role == "tool" {
			cm.ToolCallID = m.ToolCallId
			cm.ToolName = lo.CoalesceOrEmpty(lo.FromPtr(m.Name), toolNamesByCallID[m.ToolCallId])
		}
		if m.ToolCalls != nil && len(m.ToolCalls) > 0 {
			var parsed []dto.ToolCallRequest
			if err := common.Unmarshal(m.ToolCalls, &parsed); err != nil {
				return nil, fmt.Errorf("invalid ollama tool calls: %w", err)
			}
			if len(parsed) > 0 {
				calls := make([]OllamaToolCall, 0, len(parsed))
				for _, tc := range parsed {
					var args interface{}
					if tc.Function.Arguments != "" {
						if err := common.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
							return nil, fmt.Errorf("invalid arguments for ollama tool %q: %w", tc.Function.Name, err)
						}
						if args == nil {
							return nil, fmt.Errorf("invalid arguments for ollama tool %q: expected a JSON value", tc.Function.Name)
						}
					}
					if args == nil {
						args = map[string]any{}
					}
					oc := OllamaToolCall{ID: tc.ID}
					oc.Function.Name = tc.Function.Name
					oc.Function.Arguments = args
					calls = append(calls, oc)
					if tc.ID != "" {
						toolNamesByCallID[tc.ID] = tc.Function.Name
					}
				}
				cm.ToolCalls = calls
			}
		}
		chatReq.Messages = append(chatReq.Messages, cm)
	}
	return chatReq, nil
}

// openAIToGenerate converts OpenAI completions request to Ollama generate
func openAIToGenerate(c *gin.Context, r *dto.GeneralOpenAIRequest) (*OllamaGenerateRequest, error) {
	think, err := resolveOllamaThink(r)
	if err != nil {
		return nil, err
	}
	gen := &OllamaGenerateRequest{
		Model:   r.Model,
		Stream:  lo.FromPtrOr(r.Stream, false),
		Options: map[string]any{},
		Think:   think,
	}
	// Prompt may be in r.Prompt (string or []any)
	if r.Prompt != nil {
		switch v := r.Prompt.(type) {
		case string:
			gen.Prompt = v
		case []string:
			if len(v) == 0 {
				return nil, fmt.Errorf("ollama completion prompt array must not be empty")
			}
			if len(v) > 1 {
				return nil, fmt.Errorf("ollama channel does not support multiple completion prompts")
			}
			gen.Prompt = v[0]
		case []any:
			if len(v) == 0 {
				return nil, fmt.Errorf("ollama completion prompt array must not be empty")
			}
			if len(v) > 1 {
				return nil, fmt.Errorf("ollama channel does not support multiple completion prompts")
			}
			for i, it := range v {
				if s, ok := it.(string); ok {
					gen.Prompt = s
					continue
				}
				return nil, fmt.Errorf("unsupported ollama completion prompt item at index %d: %T", i, it)
			}
		default:
			return nil, fmt.Errorf("unsupported ollama completion prompt type %T", r.Prompt)
		}
	}
	if r.Suffix != nil {
		s, ok := r.Suffix.(string)
		if !ok {
			return nil, fmt.Errorf("unsupported ollama completion suffix type %T", r.Suffix)
		}
		gen.Suffix = s
	}
	format, err := toOllamaResponseFormat(r.ResponseFormat)
	if err != nil {
		return nil, err
	}
	gen.Format = format
	if r.Temperature != nil {
		gen.Options["temperature"] = r.Temperature
	}
	if r.TopP != nil {
		gen.Options["top_p"] = lo.FromPtr(r.TopP)
	}
	if r.TopK != nil {
		gen.Options["top_k"] = lo.FromPtr(r.TopK)
	}
	if r.FrequencyPenalty != nil {
		gen.Options["frequency_penalty"] = lo.FromPtr(r.FrequencyPenalty)
	}
	if r.PresencePenalty != nil {
		gen.Options["presence_penalty"] = lo.FromPtr(r.PresencePenalty)
	}
	if r.Seed != nil {
		gen.Options["seed"] = int(lo.FromPtr(r.Seed))
	}
	if mt := r.GetMaxTokens(); mt != 0 {
		gen.Options["num_predict"] = int(mt)
	}
	if r.Stop != nil {
		stop, err := ollamaStopOption(r.Stop)
		if err != nil {
			return nil, err
		}
		if len(stop) > 0 {
			gen.Options["stop"] = stop
		}
	}
	return gen, nil
}

func requestOpenAI2Embeddings(r dto.EmbeddingRequest) (*OllamaEmbeddingRequest, error) {
	opts := map[string]any{}
	if r.Temperature != nil {
		opts["temperature"] = r.Temperature
	}
	if r.TopP != nil {
		opts["top_p"] = lo.FromPtr(r.TopP)
	}
	if r.FrequencyPenalty != nil {
		opts["frequency_penalty"] = lo.FromPtr(r.FrequencyPenalty)
	}
	if r.PresencePenalty != nil {
		opts["presence_penalty"] = lo.FromPtr(r.PresencePenalty)
	}
	if r.Seed != nil {
		opts["seed"] = int(lo.FromPtr(r.Seed))
	}
	dimensions := lo.FromPtrOr(r.Dimensions, 0)
	if r.Dimensions != nil {
		opts["dimensions"] = dimensions
	}
	var input any
	switch value := r.Input.(type) {
	case string:
		input = value
	case []string:
		if len(value) == 1 {
			input = value[0]
		} else {
			input = value
		}
	case []any:
		texts := make([]string, len(value))
		for index, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("unsupported Ollama embedding input item type %T at index %d", item, index)
			}
			texts[index] = text
		}
		if len(texts) == 1 {
			input = texts[0]
		} else {
			input = texts
		}
	default:
		return nil, fmt.Errorf("unsupported Ollama embedding input type %T", r.Input)
	}
	return &OllamaEmbeddingRequest{Model: r.Model, Input: input, Options: opts, Dimensions: dimensions}, nil
}

func ollamaEmbeddingHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("empty Ollama embedding response"), types.ErrorCodeBadResponse, http.StatusBadGateway)
	}
	var oResp OllamaEmbeddingResponse
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	service.CloseResponseBodyGracefully(resp)
	if err = common.Unmarshal(body, &oResp); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	if oResp.Error != "" {
		return nil, types.NewOpenAIError(fmt.Errorf("ollama error: %s", ollamaPreview(oResp.Error)), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	data := make([]dto.OpenAIEmbeddingResponseItem, 0, len(oResp.Embeddings))
	for i, emb := range oResp.Embeddings {
		data = append(data, dto.OpenAIEmbeddingResponseItem{Index: i, Object: "embedding", Embedding: emb})
	}
	usageValue := normalizeOllamaUsage(oResp.PromptEvalCount, 0)
	usage := &usageValue
	embResp := &dto.OpenAIEmbeddingResponse{Object: "list", Data: data, Model: info.UpstreamModelName, Usage: *usage}
	out, err := common.Marshal(embResp)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	service.IOCopyBytesGracefully(c, resp, out)
	return usage, nil
}

func FetchOllamaModels(ctx context.Context, baseURL, apiKey, proxyURL string) ([]OllamaModel, error) {
	url := fmt.Sprintf("%s/api/tags", baseURL)

	client, err := service.GetHttpClientWithProxy(proxyURL)
	if err != nil {
		return nil, ollamaError("配置代理失败", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, ollamaError("创建请求失败", err)
	}

	// Ollama 通常不需要 Bearer token，但为了兼容性保留
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, ollamaError("请求失败", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, err := readOllamaErrorBody(response.Body)
		if err != nil {
			return nil, ollamaError("读取错误响应失败", err)
		}
		return nil, fmt.Errorf("服务器返回错误 %d: %s", response.StatusCode, body)
	}

	var tagsResponse OllamaTagsResponse
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, ollamaError("读取响应失败", err)
	}

	err = common.Unmarshal(body, &tagsResponse)
	if err != nil {
		return nil, fmt.Errorf("解析响应失败: %s (body: %s)", ollamaPreview(err.Error()), ollamaPreview(string(body)))
	}
	if tagsResponse.Error != "" {
		return nil, fmt.Errorf("获取模型列表失败: %s", ollamaPreview(tagsResponse.Error))
	}

	return tagsResponse.Models, nil
}

func ollamaPreview(value string) string {
	return common.LocalLogPreview(common.MaskSensitiveInfo(strings.TrimSpace(value)))
}

const ollamaErrorBodyLimit = 16 << 10

func readOllamaErrorBody(reader io.Reader) (string, error) {
	body, err := io.ReadAll(io.LimitReader(reader, ollamaErrorBodyLimit+1))
	if err != nil {
		return "", err
	}
	truncated := len(body) > ollamaErrorBodyLimit
	if truncated {
		body = body[:ollamaErrorBodyLimit]
	}
	preview := ollamaPreview(string(body))
	if truncated {
		preview += " ... [upstream body truncated]"
	}
	return preview, nil
}

func ollamaError(prefix string, err error) error {
	if err == nil {
		return fmt.Errorf("%s", prefix)
	}
	return fmt.Errorf("%s: %s", prefix, ollamaPreview(err.Error()))
}

// 拉取 Ollama 模型 (非流式)
func PullOllamaModel(ctx context.Context, baseURL, apiKey, modelName, proxyURL string) error {
	url := fmt.Sprintf("%s/api/pull", baseURL)

	pullRequest := OllamaPullRequest{
		Name:   modelName,
		Stream: false, // 非流式，简化处理
	}

	requestBody, err := common.Marshal(pullRequest)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %v", err)
	}

	client, err := service.GetHttpClientWithProxy(proxyURL)
	if err != nil {
		return ollamaError("配置代理失败", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, url, strings.NewReader(string(requestBody)))
	if err != nil {
		return ollamaError("创建请求失败", err)
	}

	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}

	response, err := client.Do(request)
	if err != nil {
		return ollamaError("请求失败", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, err := readOllamaErrorBody(response.Body)
		if err != nil {
			return ollamaError("读取错误响应失败", err)
		}
		return fmt.Errorf("拉取模型失败 %d: %s", response.StatusCode, body)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return ollamaError("读取拉取响应失败", err)
	}

	var pullResponse OllamaPullResponse
	if err := common.Unmarshal(body, &pullResponse); err != nil {
		return fmt.Errorf("解析拉取响应失败: %s (body: %s)", ollamaPreview(err.Error()), ollamaPreview(string(body)))
	}
	if pullResponse.Error != "" {
		return fmt.Errorf("拉取模型失败: %s", ollamaPreview(pullResponse.Error))
	}
	if strings.EqualFold(pullResponse.Status, "error") {
		return fmt.Errorf("拉取模型失败: 上游返回 error 状态")
	}
	if !strings.EqualFold(pullResponse.Status, "success") {
		return fmt.Errorf("拉取模型未完成: status=%s", ollamaPreview(pullResponse.Status))
	}

	return nil
}

// 流式拉取 Ollama 模型 (支持进度回调)
func PullOllamaModelStream(ctx context.Context, baseURL, apiKey, modelName, proxyURL string, progressCallback func(OllamaPullResponse)) error {
	url := fmt.Sprintf("%s/api/pull", baseURL)

	pullRequest := OllamaPullRequest{
		Name:   modelName,
		Stream: true, // 启用流式
	}

	requestBody, err := common.Marshal(pullRequest)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %v", err)
	}

	client, err := service.GetHttpClientWithProxy(proxyURL)
	if err != nil {
		return ollamaError("配置代理失败", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 60*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, url, strings.NewReader(string(requestBody)))
	if err != nil {
		return ollamaError("创建请求失败", err)
	}

	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}

	response, err := client.Do(request)
	if err != nil {
		return ollamaError("请求失败", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, err := readOllamaErrorBody(response.Body)
		if err != nil {
			return ollamaError("读取错误响应失败", err)
		}
		return fmt.Errorf("拉取模型失败 %d: %s", response.StatusCode, body)
	}

	// 读取流式响应
	scanner := helper.NewStreamScanner(response.Body)
	successful := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var pullResponse OllamaPullResponse
		if err := common.Unmarshal([]byte(line), &pullResponse); err != nil {
			return fmt.Errorf("解析流式响应失败: %s (line: %s)", ollamaPreview(err.Error()), ollamaPreview(line))
		}
		if pullResponse.Error != "" {
			pullResponse.Error = ollamaPreview(pullResponse.Error)
		}

		if progressCallback != nil {
			progressCallback(pullResponse)
		}

		// 检查是否出现错误或完成
		if pullResponse.Error != "" || strings.EqualFold(pullResponse.Status, "error") {
			detail := pullResponse.Error
			if detail == "" {
				detail = line
			}
			return fmt.Errorf("拉取模型失败: %s", ollamaPreview(detail))
		}
		if strings.EqualFold(pullResponse.Status, "success") {
			successful = true
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return ollamaError("读取流式响应失败", err)
	}

	if !successful {
		return fmt.Errorf("拉取模型未完成: 未收到成功状态")
	}

	return nil
}

// 删除 Ollama 模型
func DeleteOllamaModel(ctx context.Context, baseURL, apiKey, modelName, proxyURL string) error {
	url := fmt.Sprintf("%s/api/delete", baseURL)

	deleteRequest := OllamaDeleteRequest{
		Name: modelName,
	}

	requestBody, err := common.Marshal(deleteRequest)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %v", err)
	}

	client, err := service.GetHttpClientWithProxy(proxyURL)
	if err != nil {
		return ollamaError("配置代理失败", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, strings.NewReader(string(requestBody)))
	if err != nil {
		return ollamaError("创建请求失败", err)
	}

	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}

	response, err := client.Do(request)
	if err != nil {
		return ollamaError("请求失败", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, err := readOllamaErrorBody(response.Body)
		if err != nil {
			return ollamaError("读取错误响应失败", err)
		}
		return fmt.Errorf("删除模型失败 %d: %s", response.StatusCode, body)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return ollamaError("读取删除响应失败", err)
	}

	if strings.TrimSpace(string(body)) == "" {
		return nil
	}
	var deleteResponse struct {
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	if err := common.Unmarshal(body, &deleteResponse); err != nil {
		return fmt.Errorf("解析删除响应失败: %s (body: %s)", ollamaPreview(err.Error()), ollamaPreview(string(body)))
	}
	if deleteResponse.Error != "" {
		return fmt.Errorf("删除模型失败: %s", ollamaPreview(deleteResponse.Error))
	}
	if strings.EqualFold(deleteResponse.Status, "error") {
		return fmt.Errorf("删除模型失败: 上游返回 error 状态")
	}

	return nil
}

func FetchOllamaVersion(ctx context.Context, baseURL, apiKey, proxyURL string) (string, error) {
	trimmedBase := strings.TrimRight(baseURL, "/")
	if trimmedBase == "" {
		return "", fmt.Errorf("baseURL 为空")
	}

	url := fmt.Sprintf("%s/api/version", trimmedBase)

	client, err := service.GetHttpClientWithProxy(proxyURL)
	if err != nil {
		return "", ollamaError("配置代理失败", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", ollamaError("创建请求失败", err)
	}

	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}

	response, err := client.Do(request)
	if err != nil {
		return "", ollamaError("请求失败", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, err := readOllamaErrorBody(response.Body)
		if err != nil {
			return "", ollamaError("读取错误响应失败", err)
		}
		return "", fmt.Errorf("查询版本失败 %d: %s", response.StatusCode, body)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", ollamaError("读取响应失败", err)
	}

	var versionResp struct {
		Version string `json:"version"`
		Error   string `json:"error,omitempty"`
	}

	if err := common.Unmarshal(body, &versionResp); err != nil {
		return "", fmt.Errorf("解析响应失败: %s (body: %s)", ollamaPreview(err.Error()), ollamaPreview(string(body)))
	}
	if versionResp.Error != "" {
		return "", fmt.Errorf("查询版本失败: %s", ollamaPreview(versionResp.Error))
	}

	if versionResp.Version == "" {
		return "", fmt.Errorf("未返回版本信息")
	}

	return versionResp.Version, nil
}
