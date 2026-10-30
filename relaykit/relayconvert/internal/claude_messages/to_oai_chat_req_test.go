package claudemessages

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeMessagesRequestToOpenAIChatToolResultContent(t *testing.T) {
	const imageData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	const dataURL = "data:image/png;base64," + imageData
	imageBlock := dto.ClaudeMediaMessage{
		Type:   "image",
		Source: &dto.ClaudeMessageSource{Type: "base64", MediaType: "image/png", Data: imageData},
	}
	textBlock := func(text string) dto.ClaudeMediaMessage {
		return dto.ClaudeMediaMessage{Type: "text", Text: lo.ToPtr(text)}
	}

	tests := []struct {
		name        string
		content     any
		wantContent string
		wantImages  []string
	}{
		{
			name:        "string passes through",
			content:     "done",
			wantContent: "done",
		},
		{
			name:        "text and image keep text on tool message",
			content:     []dto.ClaudeMediaMessage{textBlock("screenshot taken"), imageBlock},
			wantContent: "screenshot taken",
			wantImages:  []string{dataURL},
		},
		{
			name:        "image only uses placeholder",
			content:     []dto.ClaudeMediaMessage{imageBlock},
			wantContent: "[image]",
			wantImages:  []string{dataURL},
		},
		{
			name: "url image source is forwarded as is",
			content: []dto.ClaudeMediaMessage{{
				Type:   "image",
				Source: &dto.ClaudeMessageSource{Type: "url", Url: "https://example.com/shot.png"},
			}},
			wantContent: "[image]",
			wantImages:  []string{"https://example.com/shot.png"},
		},
		{
			name:        "text blocks join with newline",
			content:     []dto.ClaudeMediaMessage{textBlock("first"), textBlock(""), textBlock("second")},
			wantContent: "first\nsecond",
		},
		{
			name:        "unknown block keeps whole array",
			content:     []dto.ClaudeMediaMessage{textBlock("see doc"), {Type: "document", Data: "opaque"}},
			wantContent: `[{"type":"text","text":"see doc"},{"type":"document","data":"opaque"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ClaudeMessagesRequestToOpenAIChat(context.Background(), dto.ClaudeRequest{
				Model:     "claude-test",
				MaxTokens: lo.ToPtr(uint(64)),
				Messages: []dto.ClaudeMessage{
					{Role: "user", Content: "take a screenshot"},
					{Role: "assistant", Content: []dto.ClaudeMediaMessage{
						{Type: "tool_use", Id: "toolu_1", Name: "screenshot", Input: map[string]any{}},
					}},
					{Role: "user", Content: []dto.ClaudeMediaMessage{
						{Type: "tool_result", ToolUseId: "toolu_1", Content: tt.content},
						textBlock("what do you see?"),
					}},
				},
			}, &convmeta.Values{})
			require.NoError(t, err)

			require.Len(t, got.Messages, 4)
			tool := got.Messages[2]
			assert.Equal(t, "tool", tool.Role)
			assert.Equal(t, "toolu_1", tool.ToolCallId)
			if tt.wantImages == nil && tt.wantContent[0] == '[' {
				assert.JSONEq(t, tt.wantContent, tool.StringContent())
			} else {
				assert.Equal(t, tt.wantContent, tool.StringContent())
			}

			user := got.Messages[3]
			assert.Equal(t, "user", user.Role)
			parts := user.ParseContent()
			require.Len(t, parts, len(tt.wantImages)+1)
			for i, wantURL := range tt.wantImages {
				assert.Equal(t, dto.ContentTypeImageURL, parts[i].Type)
				require.NotNil(t, parts[i].GetImageMedia())
				assert.Equal(t, wantURL, parts[i].GetImageMedia().Url)
			}
			assert.Equal(t, dto.ContentTypeText, parts[len(parts)-1].Type)
			assert.Equal(t, "what do you see?", parts[len(parts)-1].Text)
		})
	}
}

func TestClaudeMessagesRequestToOpenAIChatKeepsParallelToolResultsContiguous(t *testing.T) {
	const dataURL = "data:image/png;base64,iVBORw0KGgo="
	got, err := ClaudeMessagesRequestToOpenAIChat(context.Background(), dto.ClaudeRequest{
		Model:     "claude-test",
		MaxTokens: lo.ToPtr(uint(64)),
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "screenshot and read the note"},
			{Role: "assistant", Content: []dto.ClaudeMediaMessage{
				{Type: "text", Text: lo.ToPtr("Let me look.")},
				{Type: "tool_use", Id: "toolu_shot", Name: "screenshot", Input: map[string]any{}},
				{Type: "tool_use", Id: "toolu_read", Name: "read_file", Input: map[string]any{"path": "note.txt"}},
			}},
			{Role: "user", Content: []dto.ClaudeMediaMessage{
				{Type: "tool_result", ToolUseId: "toolu_shot", Content: []dto.ClaudeMediaMessage{{
					Type:   "image",
					Source: &dto.ClaudeMessageSource{Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="},
				}}},
				{Type: "tool_result", ToolUseId: "toolu_read", Content: "note contents"},
			}},
		},
	}, &convmeta.Values{})
	require.NoError(t, err)

	roles := make([]string, 0, len(got.Messages))
	for _, message := range got.Messages {
		roles = append(roles, message.Role)
	}
	assert.Equal(t, []string{"user", "assistant", "tool", "tool", "user"}, roles)
	assert.Len(t, got.Messages[1].ParseToolCalls(), 2)
	assert.Equal(t, "[image]", got.Messages[2].StringContent())
	assert.Equal(t, "toolu_shot", got.Messages[2].ToolCallId)
	assert.Equal(t, "note contents", got.Messages[3].StringContent())
	assert.NotContains(t, got.Messages[2].StringContent()+got.Messages[3].StringContent(), "base64")

	parts := got.Messages[4].ParseContent()
	require.Len(t, parts, 1)
	assert.Equal(t, dto.ContentTypeImageURL, parts[0].Type)
	require.NotNil(t, parts[0].GetImageMedia())
	assert.Equal(t, dataURL, parts[0].GetImageMedia().Url)
}

func TestClaudeMessagesRequestToOpenAIChatUserImageSources(t *testing.T) {
	const imageData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	got, err := ClaudeMessagesRequestToOpenAIChat(context.Background(), dto.ClaudeRequest{
		Model:     "kimi-k3.1-pro",
		MaxTokens: lo.ToPtr(uint(64)),
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: []dto.ClaudeMediaMessage{
				{Type: "image", Source: &dto.ClaudeMessageSource{Type: "url", Url: "https://example.com/shot.png"}},
				{Type: "text", Text: lo.ToPtr("what is this?")},
			}},
			{Role: "user", Content: []dto.ClaudeMediaMessage{
				{Type: "image", Source: &dto.ClaudeMessageSource{Type: "base64", MediaType: "image/png", Data: imageData}},
				{Type: "text", Text: lo.ToPtr("and this?")},
			}},
		},
	}, &convmeta.Values{})
	require.NoError(t, err)
	require.Len(t, got.Messages, 2)

	// A url-source image must reach the Chat body as its real URL, not as an
	// empty base64 data URL: the official Anthropic endpoint answers such a
	// request with 400 "unsupported image url", and an empty data URL made the
	// upstream silently ignore the image and return 200 (customer report,
	// 2026-10-26).
	urlParts := got.Messages[0].ParseContent()
	require.Len(t, urlParts, 2)
	require.NotNil(t, urlParts[0].GetImageMedia())
	assert.Equal(t, "https://example.com/shot.png", urlParts[0].GetImageMedia().Url)

	// Base64 sources keep becoming a data URL.
	base64Parts := got.Messages[1].ParseContent()
	require.Len(t, base64Parts, 2)
	require.NotNil(t, base64Parts[0].GetImageMedia())
	assert.Equal(t, "data:image/png;base64,"+imageData, base64Parts[0].GetImageMedia().Url)
}

// TestClaudeMessagesRequestToOpenAIChatDropsExplicitCacheBreakpoints pins the
// 2026-10-30 customer-visible 400: a Claude request carrying cache_control
// breakpoints was converted into an OpenAI chat payload that kept the field, and
// the Kimi aggregator upstream rejected the whole request ("'cache_control'
// (explicit cache breakpoints) is not supported ...; prompt cache is applied
// automatically"). Only the OpenRouter dialect documents the field, so only that
// dialect may receive it.
func TestClaudeMessagesRequestToOpenAIChatDropsExplicitCacheBreakpoints(t *testing.T) {
	breakpoint := json.RawMessage(`{"type":"ephemeral"}`)
	maxTokens := uint(64)
	claudeReq := dto.ClaudeRequest{
		Model:     "kimi-k3.1-pro",
		MaxTokens: &maxTokens,
		Messages: []dto.ClaudeMessage{
			{
				Role: "user",
				Content: []dto.ClaudeMediaMessage{
					{Type: "text", Text: lo.ToPtr("cached prefix"), CacheControl: breakpoint},
					{Type: "text", Text: lo.ToPtr("question")},
				},
			},
		},
	}

	convert := func(openRouter bool) string {
		req, err := ClaudeMessagesRequestToOpenAIChat(context.Background(), claudeReq, &convmeta.Values{
			OriginModelName:   "kimi-k3.1-pro",
			UpstreamModelName: "kimi-k3.1-pro",
			Options:           &convmeta.Options{OpenRouterDialect: openRouter},
		})
		require.NoError(t, err)
		require.Len(t, req.Messages, 1)
		encoded, err := json.Marshal(req.Messages[0].Content)
		require.NoError(t, err)
		return string(encoded)
	}

	plain := convert(false)
	require.NotContains(t, plain, "cache_control", "a plain OpenAI upstream rejects the field; the request must not carry it")
	require.Contains(t, plain, "cached prefix", "the text itself still travels")
	require.Contains(t, plain, "question")

	openRouter := convert(true)
	require.Contains(t, openRouter, "cache_control", "the OpenRouter dialect documents and accepts the breakpoint")
}
