package zhipu_4v

import (
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

func requestOpenAI2Zhipu(request dto.GeneralOpenAIRequest) *dto.GeneralOpenAIRequest {
	messages := make([]dto.Message, 0, len(request.Messages))
	for _, message := range request.Messages {
		if !message.IsStringContent() {
			mediaMessages := message.ParseContent()
			for j, mediaMessage := range mediaMessages {
				if mediaMessage.Type == dto.ContentTypeImageURL {
					imageUrl := mediaMessage.GetImageMedia()
					// check if base64
					if strings.HasPrefix(imageUrl.Url, "data:image/") {
						// 去除base64数据的URL前缀（如果有）
						if idx := strings.Index(imageUrl.Url, ","); idx != -1 {
							imageUrl.Url = imageUrl.Url[idx+1:]
						}
					}
					mediaMessage.ImageUrl = imageUrl
					mediaMessages[j] = mediaMessage
				}
			}
			message.SetMediaContent(mediaMessages)
		}
		messages = append(messages, dto.Message{
			Role:       message.Role,
			Content:    message.Content,
			ToolCalls:  message.ToolCalls,
			ToolCallId: message.ToolCallId,
		})
	}
	// The official endpoint only accepts stop as an array (a string is a
	// deserialization 400), and the JSON decoder yields []any — coerce both
	// shapes so the client's stop words are never dropped.
	str, ok := request.Stop.(string)
	var Stop []string
	if ok {
		Stop = []string{str}
	} else if arr, ok := request.Stop.([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				Stop = append(Stop, s)
			}
		}
	} else {
		Stop, _ = request.Stop.([]string)
	}
	out := &dto.GeneralOpenAIRequest{
		Model:           request.Model,
		Stream:          request.Stream,
		Messages:        messages,
		Temperature:     request.Temperature,
		TopP:            request.TopP,
		Stop:            Stop,
		Tools:           request.Tools,
		ToolChoice:      request.ToolChoice,
		THINKING:        request.THINKING,
		ReasoningEffort: request.ReasoningEffort,
		// Without response_format the upstream model answers prose for
		// json_object requests; GLM-5.3 official-fit must keep it (the suite's
		// json_object probe failed exactly this way).
		ResponseFormat: request.ResponseFormat,
	}
	if request.MaxTokens != nil || request.MaxCompletionTokens != nil {
		maxTokens := request.GetMaxTokens()
		out.MaxTokens = &maxTokens
	}
	return out
}
