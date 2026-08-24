package helper

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeResponseForClientStripsBillingUsageWithoutMutatingInternalResponse(t *testing.T) {
	response := &dto.ClaudeResponse{
		Type: "message",
		Usage: &dto.ClaudeUsage{
			InputTokens: 10,
			BillingUsage: dto.NewGeminiChatBillingUsage(&dto.GeminiUsageMetadata{
				PromptTokenCount: 10,
			}),
		},
	}

	clientResponse := ClaudeResponseForClient(response)
	require.NotNil(t, clientResponse)
	require.NotNil(t, clientResponse.Usage)
	assert.Nil(t, clientResponse.Usage.BillingUsage)
	require.NotNil(t, response.Usage)
	assert.NotNil(t, response.Usage.BillingUsage)
}

func TestGeminiResponseForClientStripsBillingUsageWithoutMutatingInternalResponse(t *testing.T) {
	response := &dto.GeminiChatResponse{
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount: 10,
			BillingUsage: dto.NewOpenAIChatBillingUsage(&dto.Usage{
				PromptTokens: 10,
			}),
		},
	}

	clientResponse := GeminiResponseForClient(response)
	require.NotNil(t, clientResponse)
	assert.Nil(t, clientResponse.UsageMetadata.BillingUsage)
	assert.NotNil(t, response.UsageMetadata.BillingUsage)
}

func TestOpenAIResponsesForClientStripBillingUsageWithoutMutatingInternalResponses(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens: 10,
		BillingUsage: dto.NewOpenAIResponsesBillingUsage(&dto.Usage{
			PromptTokens: 10,
		}),
	}
	textResponse := &dto.OpenAITextResponse{Usage: *usage}
	responsesResponse := &dto.OpenAIResponsesResponse{Usage: usage}
	chatChunk := &dto.ChatCompletionsStreamResponse{Usage: usage}
	responsesEvent := &dto.ResponsesStreamResponse{Response: responsesResponse}

	clientTextResponse := OpenAITextResponseForClient(textResponse)
	clientResponsesResponse := OpenAIResponsesResponseForClient(responsesResponse)
	clientChatChunk := ChatCompletionsStreamResponseForClient(chatChunk)
	clientResponsesEvent := ResponsesStreamResponseForClient(responsesEvent)

	assert.Nil(t, clientTextResponse.Usage.BillingUsage)
	require.NotNil(t, clientResponsesResponse.Usage)
	assert.Nil(t, clientResponsesResponse.Usage.BillingUsage)
	require.NotNil(t, clientChatChunk.Usage)
	assert.Nil(t, clientChatChunk.Usage.BillingUsage)
	require.NotNil(t, clientResponsesEvent.Response)
	require.NotNil(t, clientResponsesEvent.Response.Usage)
	assert.Nil(t, clientResponsesEvent.Response.Usage.BillingUsage)

	assert.NotNil(t, textResponse.Usage.BillingUsage)
	assert.NotNil(t, responsesResponse.Usage.BillingUsage)
	assert.NotNil(t, chatChunk.Usage.BillingUsage)
	assert.NotNil(t, responsesEvent.Response.Usage.BillingUsage)
}

func TestOpenAITextResponseForClientPreservesContentAndReasoningLogprobs(t *testing.T) {
	logprobs := any(map[string]any{
		"content":           []any{map[string]any{"token": "1"}},
		"reasoning_content": []any{map[string]any{"token": "thinking"}},
	})
	response := &dto.OpenAITextResponse{
		Choices: []dto.OpenAITextResponseChoice{{Logprobs: &logprobs}},
	}

	clientResponse := OpenAITextResponseForClient(response)
	require.NotNil(t, clientResponse)
	require.NotNil(t, clientResponse.Choices[0].Logprobs)

	encoded, err := json.Marshal(clientResponse)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"content"`)
	assert.Contains(t, string(encoded), `"reasoning_content"`)
}

func TestResponseForClientHandlesValueResponses(t *testing.T) {
	response := dto.ChatCompletionsStreamResponse{
		Usage: &dto.Usage{
			PromptTokens: 10,
			BillingUsage: dto.NewOpenAIChatBillingUsage(&dto.Usage{
				PromptTokens: 10,
			}),
		},
	}

	clientResponse, ok := ResponseForClient(response).(dto.ChatCompletionsStreamResponse)
	require.True(t, ok)
	require.NotNil(t, clientResponse.Usage)
	assert.Nil(t, clientResponse.Usage.BillingUsage)
	assert.NotNil(t, response.Usage.BillingUsage)
}
