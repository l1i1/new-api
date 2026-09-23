package relayconvert

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Claude prompt caching is explicit: the upstream writes and reads cache only
// where the request carries a cache_control breakpoint. OpenAI-shaped requests
// still reach us with one when the client speaks the OpenRouter-style
// extension, so the converters must carry it across instead of dropping it —
// dropping it silently turns every turn into a full-price prefill.

func TestOpenAIChatToClaudeCarriesCacheControl(t *testing.T) {
	maxTokens := uint(64)
	body := []byte(`[
		{"type": "text", "text": "stable prefix", "cache_control": {"type": "ephemeral"}},
		{"type": "image_url", "image_url": {"url": "https://example.com/a.png"}, "cache_control": {"type": "ephemeral"}}
	]`)
	var content []any
	require.NoError(t, kitutil.Unmarshal(body, &content))

	got, err := OpenAIChatRequestToClaudeMessages(context.Background(), &convmeta.Values{}, dto.GeneralOpenAIRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.Message{
			{Role: "system", Content: "plain system prompt"},
			{Role: "user", Content: content},
		},
	})
	require.NoError(t, err)

	blocks := claudeContentBlocks(t, got.Messages[0])
	require.Len(t, blocks, 2)
	assert.Equal(t, map[string]any{"type": "ephemeral"}, decodeRawObject(t, blocks[0]["cache_control"]))
	assert.Equal(t, map[string]any{"type": "ephemeral"}, decodeRawObject(t, blocks[1]["cache_control"]))
}

func TestOpenAIChatToClaudeCarriesSystemCacheControl(t *testing.T) {
	maxTokens := uint(64)
	body := []byte(`[{"type": "text", "text": "stable system block", "cache_control": {"type": "ephemeral"}}]`)
	var system []any
	require.NoError(t, kitutil.Unmarshal(body, &system))

	got, err := OpenAIChatRequestToClaudeMessages(context.Background(), &convmeta.Values{}, dto.GeneralOpenAIRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: "hi"},
		},
	})
	require.NoError(t, err)

	systems := claudeSystemBlocks(t, got)
	require.Len(t, systems, 1)
	assert.Equal(t, map[string]any{"type": "ephemeral"}, decodeRawObject(t, systems[0]["cache_control"]))
}

// The conversion must not invent breakpoints: a request without cache_control
// stays without one, so upstream caching behavior is unchanged for the vast
// majority of traffic that never opted in.
func TestOpenAIChatToClaudeDoesNotInventCacheControl(t *testing.T) {
	maxTokens := uint(64)
	got, err := OpenAIChatRequestToClaudeMessages(context.Background(), &convmeta.Values{}, dto.GeneralOpenAIRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.Message{
			{Role: "system", Content: "plain system prompt"},
			{Role: "user", Content: "plain user text"},
		},
	})
	require.NoError(t, err)

	encoded, err := kitutil.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "cache_control")
}

func TestOpenAIResponsesToClaudeCarriesCacheControl(t *testing.T) {
	maxTokens := uint(64)
	req := &dto.OpenAIResponsesRequest{
		Model:           "claude-test",
		MaxOutputTokens: &maxTokens,
		Input: []byte(`[{"role": "user", "content": [
			{"type": "input_text", "text": "stable prefix", "cache_control": {"type": "ephemeral"}}
		]}]`),
	}

	got, err := OpenAIResponsesRequestToClaudeMessages(context.Background(), &convmeta.Values{}, req)
	require.NoError(t, err)

	blocks := claudeContentBlocks(t, got.Messages[0])
	require.Len(t, blocks, 1)
	assert.Equal(t, map[string]any{"type": "ephemeral"}, decodeRawObject(t, blocks[0]["cache_control"]))
}

// claudeContentBlocks returns one message's content as raw JSON objects.
func claudeContentBlocks(t *testing.T, message dto.ClaudeMessage) []map[string]json.RawMessage {
	t.Helper()
	encoded, err := kitutil.Marshal(message.Content)
	require.NoError(t, err)
	var blocks []map[string]json.RawMessage
	require.NoError(t, kitutil.Unmarshal(encoded, &blocks))
	return blocks
}

// claudeSystemBlocks returns the request's system blocks as raw JSON objects.
func claudeSystemBlocks(t *testing.T, request *dto.ClaudeRequest) []map[string]json.RawMessage {
	t.Helper()
	encoded, err := kitutil.Marshal(request.System)
	require.NoError(t, err)
	var blocks []map[string]json.RawMessage
	require.NoError(t, kitutil.Unmarshal(encoded, &blocks))
	return blocks
}

func decodeRawObject(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var value map[string]any
	require.NoError(t, kitutil.Unmarshal(raw, &value))
	return value
}
