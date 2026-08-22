package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestExtractContentModerationInputUsesLatestUserTurn(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"old"},{"role":"assistant","content":"answer"},{"role":"user","content":[{"type":"text","text":"latest"},{"type":"text","text":"part"}]}]}`)
	got := ExtractContentModerationInput(nil, body, ContentModerationProtocolOpenAIChat)
	require.Equal(t, "latest part", got.Text)

	gemini := []byte(`{"contents":[{"role":"model","parts":[{"text":"answer"}]},{"parts":[{"text":"latest gemini"}]}]}`)
	require.Equal(t, "latest gemini", ExtractContentModerationInput(nil, gemini, ContentModerationProtocolGemini).Text)

	anthropic := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>internal</system-reminder>"},{"type":"text","text":"visible"}]}]}`)
	require.Equal(t, "visible", ExtractContentModerationInput(nil, anthropic, ContentModerationProtocolAnthropic).Text)
}

func TestExtractContentModerationTextUsesTypedRequests(t *testing.T) {
	var openAI dto.GeneralOpenAIRequest
	require.NoError(t, json.Unmarshal([]byte(`{"messages":[{"role":"user","content":"old"},{"role":"assistant","content":"answer"},{"role":"user","content":[{"type":"text","text":"latest"},{"type":"text","text":" part"}]}]}`), &openAI))
	require.Equal(t, "latest part", ExtractContentModerationText(&openAI, ContentModerationProtocolOpenAIChat))

	var claude dto.ClaudeRequest
	require.NoError(t, json.Unmarshal([]byte(`{"messages":[{"role":"user","content":"old"},{"role":"assistant","content":"answer"},{"role":"user","content":[{"type":"text","text":"<system-reminder>internal</system-reminder>"},{"type":"text","text":"visible"}]}]}`), &claude))
	require.Equal(t, "visible", ExtractContentModerationText(&claude, ContentModerationProtocolAnthropic))

	var gemini dto.GeminiChatRequest
	require.NoError(t, json.Unmarshal([]byte(`{"contents":[{"role":"model","parts":[{"text":"answer"}]},{"role":"user","parts":[{"text":"latest"},{"text":" gemini"}]}]}`), &gemini))
	require.Equal(t, "latest gemini", ExtractContentModerationText(&gemini, ContentModerationProtocolGemini))

	responses := &dto.OpenAIResponsesRequest{Input: json.RawMessage(`[{"role":"user","content":[{"type":"input_text","text":"old"}]},{"role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"role":"user","content":[{"type":"input_text","text":"latest responses"}]}]`)}
	require.Equal(t, "latest responses", ExtractContentModerationText(responses, ContentModerationProtocolOpenAIResponses))
}

func TestExtractContentModerationInputSupportsConversationImages(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		body     string
		text     string
		images   []string
	}{
		{
			name:     "openai chat url",
			protocol: ContentModerationProtocolOpenAIChat,
			body:     `{"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://img.test/chat.png"}}]}]}`,
			text:     "look",
			images:   []string{"https://img.test/chat.png"},
		},
		{
			name:     "openai chat data url",
			protocol: ContentModerationProtocolOpenAIChat,
			body:     `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":"data:image/png;base64,Y2hhdA=="}]}]}`,
			images:   []string{"data:image/png;base64,Y2hhdA=="},
		},
		{
			name:     "responses url and data url",
			protocol: ContentModerationProtocolOpenAIResponses,
			body:     `{"input":[{"role":"user","content":[{"type":"input_text","text":"inspect"},{"type":"input_image","image_url":"https://img.test/responses.png"},{"type":"input_image","image_url":{"url":"data:image/png;base64,cmVzcG9uc2Vz"}}]}]}`,
			text:     "inspect",
			images:   []string{"https://img.test/responses.png", "data:image/png;base64,cmVzcG9uc2Vz"},
		},
		{
			name:     "anthropic base64 and url",
			protocol: ContentModerationProtocolAnthropic,
			body:     `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YW50aHJvcGlj"}},{"type":"image","source":{"type":"url","url":"https://img.test/anthropic.png"}}]}]}`,
			images:   []string{"data:image/png;base64,YW50aHJvcGlj", "https://img.test/anthropic.png"},
		},
		{
			name:     "gemini camel case",
			protocol: ContentModerationProtocolGemini,
			body:     `{"contents":[{"role":"user","parts":[{"text":"gemini"},{"inlineData":{"mimeType":"image/png","data":"Z2VtaW5p"}},{"fileData":{"mimeType":"image/jpeg","fileUri":"https://img.test/gemini.jpg"}}]}]}`,
			text:     "gemini",
			images:   []string{"data:image/png;base64,Z2VtaW5p", "https://img.test/gemini.jpg"},
		},
		{
			name:     "gemini snake case",
			protocol: ContentModerationProtocolGemini,
			body:     `{"contents":[{"role":"user","parts":[{"inline_data":{"mime_type":"image/png","data":"c25ha2U="}},{"file_data":{"mime_type":"image/png","file_uri":"https://img.test/snake.png"}}]}]}`,
			images:   []string{"data:image/png;base64,c25ha2U=", "https://img.test/snake.png"},
		},
		{
			name:     "gemini non image file",
			protocol: ContentModerationProtocolGemini,
			body:     `{"contents":[{"role":"user","parts":[{"fileData":{"mimeType":"video/mp4","fileUri":"https://img.test/video.mp4"}}]}]}`,
		},
		{
			name:     "unsupported image source",
			protocol: ContentModerationProtocolOpenAIChat,
			body:     `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"file:///private/image.png"}},{"type":"image_url","image_url":{"url":"data:text/plain;base64,dGV4dA=="}}]}]}`,
		},
		{
			name:     "duplicate image",
			protocol: ContentModerationProtocolOpenAIChat,
			body:     `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://img.test/duplicate.png"}},{"type":"image_url","image_url":{"url":"https://img.test/duplicate.png"}}]}]}`,
			images:   []string{"https://img.test/duplicate.png"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ExtractContentModerationInput(nil, []byte(test.body), test.protocol)
			require.Equal(t, test.text, got.Text)
			if test.images == nil {
				require.Empty(t, got.Images)
			} else {
				require.Equal(t, test.images, got.Images)
			}
		})
	}
}

func TestExtractContentModerationInputDoesNotReauditHistoricalOrToolTurns(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		body     string
	}{
		{
			name:     "assistant is latest chat turn",
			protocol: ContentModerationProtocolOpenAIChat,
			body:     `{"messages":[{"role":"user","content":"historical danger"},{"role":"assistant","content":"answer"}]}`,
		},
		{
			name:     "anthropic tool result",
			protocol: ContentModerationProtocolAnthropic,
			body:     `{"messages":[{"role":"user","content":"historical danger"},{"role":"assistant","content":[{"type":"tool_use","name":"lookup"}]},{"role":"user","content":[{"type":"tool_result","content":"untrusted tool output"}]}]}`,
		},
		{
			name:     "responses function output",
			protocol: ContentModerationProtocolOpenAIResponses,
			body:     `{"input":[{"role":"user","content":[{"type":"input_text","text":"historical danger"}]},{"type":"function_call_output","output":"untrusted tool output"}]}`,
		},
		{
			name:     "responses unknown final item",
			protocol: ContentModerationProtocolOpenAIResponses,
			body:     `{"input":[{"role":"user","content":[{"type":"input_text","text":"historical danger"}]},{"id":"opaque-history-item"}]}`,
		},
		{
			name:     "responses unroled message",
			protocol: ContentModerationProtocolOpenAIResponses,
			body:     `{"input":[{"role":"user","content":[{"type":"input_text","text":"historical danger"}]},{"type":"message","content":[{"type":"input_text","text":"ambiguous"}]}]}`,
		},
		{
			name:     "gemini function response",
			protocol: ContentModerationProtocolGemini,
			body:     `{"contents":[{"role":"user","parts":[{"text":"historical danger"}]},{"role":"model","parts":[{"functionCall":{"name":"lookup"}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"value":"untrusted tool output"}}}]}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ExtractContentModerationInput(nil, []byte(test.body), test.protocol)
			require.True(t, got.isEmpty(), "%+v", got)
		})
	}
}

func TestExtractContentModerationInputSupportsFlatResponsesParts(t *testing.T) {
	body := []byte(`{"input":[{"type":"input_text","text":"inspect"},{"type":"input_image","image_url":"https://img.test/responses.png"}]}`)
	got := ExtractContentModerationInput(nil, body, ContentModerationProtocolOpenAIResponses)
	require.Equal(t, "inspect", got.Text)
	require.Equal(t, []string{"https://img.test/responses.png"}, got.Images)
}

func TestExtractContentModerationInputRejectsInvalidImages(t *testing.T) {
	tests := []struct {
		name   string
		image  string
		status int
	}{
		{name: "unsupported source", image: "file:///private/image.png", status: http.StatusBadRequest},
		{name: "unsupported data type", image: "data:image/svg+xml;base64,PHN2Zz4=", status: http.StatusBadRequest},
		{name: "invalid base64", image: "data:image/png;base64,***", status: http.StatusBadRequest},
		{name: "oversized url", image: "https://img.test/" + strings.Repeat("a", contentModerationMaxImageURLBytes), status: http.StatusRequestEntityTooLarge},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, err := common.Marshal(map[string]any{
				"messages": []any{map[string]any{
					"role":    "user",
					"content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": test.image}}},
				}},
			})
			require.NoError(t, err)
			got := ExtractContentModerationInput(nil, body, ContentModerationProtocolOpenAIChat)
			require.NotNil(t, got.ValidationError)
			require.Equal(t, test.status, got.ValidationError.StatusCode)
			require.NotEmpty(t, got.ValidationError.Message)
			require.NotContains(t, got.ValidationError.Message, test.image)
			require.Empty(t, got.Images)
		})
	}
}

func TestExtractContentModerationInputRejectsOversizedImageData(t *testing.T) {
	encoded := strings.Repeat("A", base64.StdEncoding.EncodedLen(contentModerationMaxImageBytes)+4)
	got := ContentModerationInput{Images: []string{"data:image/png;base64," + encoded}}
	got.normalize()
	require.NotNil(t, got.ValidationError)
	require.Equal(t, http.StatusRequestEntityTooLarge, got.ValidationError.StatusCode)
	require.Equal(t, "Image exceeds the 20 MB content moderation limit", got.ValidationError.Message)
}

func TestExtractContentModerationInputRejectsTooManyDistinctImages(t *testing.T) {
	content := make([]any, 0, contentModerationMaxCandidateImages+1)
	for index := range contentModerationMaxCandidateImages + 1 {
		content = append(content, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": fmt.Sprintf("https://img.test/%d.png", index)},
		})
	}
	body, err := common.Marshal(map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": content}},
	})
	require.NoError(t, err)

	got := ExtractContentModerationInput(nil, body, ContentModerationProtocolOpenAIChat)
	require.NotNil(t, got.ValidationError)
	require.Equal(t, http.StatusRequestEntityTooLarge, got.ValidationError.StatusCode)
	require.Equal(t, "Too many images for content moderation", got.ValidationError.Message)
}

func TestCheckContentModerationPreBlockRejectsInvalidImageWithoutProviderCall(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	providerCalls := 0
	contentModerationHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		providerCalls++
		return nil, errors.New("provider must not be called")
	})}
	withContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"https://moderation.example.test","sample_rate":1,"all_groups":true,"all_models":true}`)

	decision, err := CheckContentModeration(context.Background(), ContentModerationRequest{
		Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat,
		Images: []string{"data:image/svg+xml;base64,PHN2Zz4="},
	})
	require.NoError(t, err)
	require.True(t, decision.Checked)
	require.True(t, decision.Blocked)
	require.Equal(t, http.StatusBadRequest, decision.StatusCode)
	require.Equal(t, 0, providerCalls)
}

func TestExtractContentModerationContentSupportsTypedConversationImages(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		body     string
		text     string
		images   []string
	}{
		{
			name:     "openai chat",
			protocol: ContentModerationProtocolOpenAIChat,
			body:     `{"messages":[{"role":"user","content":[{"type":"text","text":"chat"},{"type":"image_url","image_url":{"url":"https://img.test/chat.png"}}]}]}`,
			text:     "chat",
			images:   []string{"https://img.test/chat.png"},
		},
		{
			name:     "responses",
			protocol: ContentModerationProtocolOpenAIResponses,
			body:     `{"input":[{"role":"user","content":[{"type":"input_text","text":"responses"},{"type":"input_image","image_url":{"url":"https://img.test/responses.png"}}]}]}`,
			text:     "responses",
			images:   []string{"https://img.test/responses.png"},
		},
		{
			name:     "anthropic",
			protocol: ContentModerationProtocolAnthropic,
			body:     `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YW50aHJvcGlj"}}]}]}`,
			images:   []string{"data:image/png;base64,YW50aHJvcGlj"},
		},
		{
			name:     "gemini snake file",
			protocol: ContentModerationProtocolGemini,
			body:     `{"contents":[{"role":"user","parts":[{"file_data":{"mime_type":"image/png","file_uri":"https://img.test/gemini.png"}}]}]}`,
			images:   []string{"https://img.test/gemini.png"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := decodeContentModerationTypedRequest(t, test.protocol, test.body)
			got := ExtractContentModerationContent(request, test.protocol)
			require.Equal(t, test.text, got.Text)
			require.Equal(t, test.images, got.Images)
		})
	}
}

func TestExtractContentModerationContentSupportsResponsesCompaction(t *testing.T) {
	request := &dto.OpenAIResponsesCompactionRequest{}
	require.NoError(t, common.Unmarshal([]byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"compact"},{"type":"input_image","image_url":"https://img.test/compact.png"}]}]}`), request))

	got := ExtractContentModerationContent(request, ContentModerationProtocolOpenAIResponses)
	require.Equal(t, "compact", got.Text)
	require.Equal(t, []string{"https://img.test/compact.png"}, got.Images)
}

func TestContentModerationAffinitySamplingIsStable(t *testing.T) {
	config := defaultContentModerationConfig()
	config.Enabled = true
	config.SampleRate = 0.5
	input := ContentModerationRequest{Group: "default", Model: "gpt-test", AffinityCacheIdentity: "stable-conversation"}
	first := shouldModerateContent(config, input, ContentModerationInput{Text: "first body"})
	input.RequestID = "different-request"
	require.Equal(t, first, shouldModerateContent(config, input, ContentModerationInput{Text: "different body"}))
}

func decodeContentModerationTypedRequest(t *testing.T, protocol string, body string) dto.Request {
	t.Helper()
	var request dto.Request
	switch protocol {
	case ContentModerationProtocolOpenAIChat:
		request = &dto.GeneralOpenAIRequest{}
	case ContentModerationProtocolOpenAIResponses:
		request = &dto.OpenAIResponsesRequest{}
	case ContentModerationProtocolAnthropic:
		request = &dto.ClaudeRequest{}
	case ContentModerationProtocolGemini:
		request = &dto.GeminiChatRequest{}
	default:
		t.Fatalf("unsupported protocol %q", protocol)
	}
	require.NoError(t, common.Unmarshal([]byte(body), request))
	return request
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestContentModerationPolicyFingerprintInvalidatesAffinityKey(t *testing.T) {
	input := ContentModerationRequest{UserID: 1, Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat, AffinityRuleName: "rule", AffinityCacheIdentity: "conversation", AffinityTTLSeconds: 300, AffinityChannelID: 1}
	first := defaultContentModerationConfig()
	second := first
	second.SampleRate = 0.5
	require.NotEqual(t, contentModerationAffinityCacheKey(input, first), contentModerationAffinityCacheKey(input, second))
}

func TestCachedFlaggedDecisionCannotBeBypassedBySampling(t *testing.T) {
	withContentModerationOption(t, `{"enabled":true,"mode":"pre_block","sample_rate":0.000000001,"all_groups":true,"all_models":true}`)
	config := GetContentModerationConfig()
	input := ContentModerationRequest{UserID: 1, Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat, RequestID: "sample-miss", Text: "new request", AffinityRuleName: "rule", AffinityCacheIdentity: "cached-conversation", AffinityTTLSeconds: 300, AffinityChannelID: 1}
	key := contentModerationAffinityCacheKey(input, config)
	require.NoError(t, getContentModerationAffinityCache().SetWithTTL(key, contentModerationAffinityCacheEntry{Flagged: true, Category: "sexual", Score: 0.9, SideEffects: true}, time.Minute))
	t.Cleanup(func() { _, _ = getContentModerationAffinityCache().DeleteMany([]string{key}) })

	decision, err := CheckContentModeration(context.Background(), input)
	require.NoError(t, err)
	require.True(t, decision.Cached)
	require.True(t, decision.Blocked)
}

func TestNormalizeContentModerationConfigRejectsOversizedViolationWindow(t *testing.T) {
	config := defaultContentModerationConfig()
	config.ViolationWindowHours = contentModerationMaxViolationWindowHours + 1
	_, err := NormalizeContentModerationConfig(config)
	require.ErrorContains(t, err, "violation_window_hours")
}

func TestNormalizeContentModerationConfigMergesPartialThresholdsWithDefaults(t *testing.T) {
	config := defaultContentModerationConfig()
	config.Thresholds = map[string]float64{"sexual": 0.8}
	normalized, err := NormalizeContentModerationConfig(config)
	require.NoError(t, err)
	require.Equal(t, 0.8, normalized.Thresholds["sexual"])
	require.Equal(t, defaultContentModerationThresholds["violence"], normalized.Thresholds["violence"])
	require.Equal(t, defaultContentModerationThresholds["harassment"], normalized.Thresholds["harassment"])
}

func TestNormalizeContentModerationConfigTreatsRecordLogsAsLegacyAlias(t *testing.T) {
	config := defaultContentModerationConfig()
	config.RecordLogs = true
	normalized, err := NormalizeContentModerationConfig(config)
	require.NoError(t, err)
	require.True(t, normalized.RecordNonHits)
	require.False(t, normalized.RecordLogs)
}

func TestContentModerationEnabledReturnsStoredConfigErrors(t *testing.T) {
	withContentModerationOption(t, `{invalid`)
	enabled, err := ContentModerationEnabled()
	require.False(t, enabled)
	require.ErrorContains(t, err, "invalid content moderation config")
}

func TestContentModerationConfigRefreshesFromDatabaseAcrossNodes(t *testing.T) {
	require.NotNil(t, model.DB)
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
	localRaw := `{"enabled":false,"mode":"observe","sample_rate":1,"all_groups":true,"all_models":true}`
	remoteRaw := `{"enabled":true,"mode":"pre_block","sample_rate":1,"all_groups":true,"all_models":true}`
	withContentModerationOption(t, localRaw)

	var previous model.Option
	previousErr := model.DB.First(&previous, "key = ?", ContentModerationOptionKey).Error
	require.True(t, previousErr == nil || errors.Is(previousErr, gorm.ErrRecordNotFound))
	t.Cleanup(func() {
		if previousErr == nil {
			_ = model.DB.Save(&previous).Error
		} else {
			_ = model.DB.Delete(&model.Option{}, "key = ?", ContentModerationOptionKey).Error
		}
		contentModerationConfigCacheMu.Lock()
		contentModerationConfigCacheRaw = ""
		contentModerationConfigCacheValue = ContentModerationConfig{}
		contentModerationConfigCacheAt = time.Time{}
		contentModerationConfigCacheMu.Unlock()
	})

	contentModerationConfigCacheMu.Lock()
	contentModerationConfigCacheRaw = ""
	contentModerationConfigCacheValue = ContentModerationConfig{}
	contentModerationConfigCacheAt = time.Time{}
	contentModerationConfigCacheMu.Unlock()
	enabled, err := ContentModerationEnabled()
	require.NoError(t, err)
	require.False(t, enabled)

	require.NoError(t, model.DB.Save(&model.Option{Key: ContentModerationOptionKey, Value: remoteRaw}).Error)
	contentModerationConfigCacheMu.Lock()
	contentModerationConfigCacheAt = time.Now().Add(-2 * contentModerationConfigRefreshInterval)
	contentModerationConfigCacheMu.Unlock()

	enabled, err = ContentModerationEnabled()
	require.NoError(t, err)
	require.True(t, enabled)
	common.OptionMapRWMutex.RLock()
	require.Equal(t, remoteRaw, common.OptionMap[ContentModerationOptionKey])
	common.OptionMapRWMutex.RUnlock()
}

func TestContentModerationConfigRefreshKeepsLastValidPolicyWhenDatabaseValueIsInvalid(t *testing.T) {
	require.NotNil(t, model.DB)
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
	validRaw := `{"enabled":true,"mode":"pre_block","sample_rate":1,"all_groups":true,"all_models":true}`
	withContentModerationOption(t, validRaw)

	var previous model.Option
	previousErr := model.DB.First(&previous, "key = ?", ContentModerationOptionKey).Error
	require.True(t, previousErr == nil || errors.Is(previousErr, gorm.ErrRecordNotFound))
	t.Cleanup(func() {
		if previousErr == nil {
			_ = model.DB.Save(&previous).Error
		} else {
			_ = model.DB.Delete(&model.Option{}, "key = ?", ContentModerationOptionKey).Error
		}
		contentModerationConfigCacheMu.Lock()
		contentModerationConfigCacheRaw = ""
		contentModerationConfigCacheValue = ContentModerationConfig{}
		contentModerationConfigCacheAt = time.Time{}
		contentModerationConfigCacheMu.Unlock()
	})

	enabled, err := ContentModerationEnabled()
	require.NoError(t, err)
	require.True(t, enabled)
	require.NoError(t, model.DB.Save(&model.Option{Key: ContentModerationOptionKey, Value: `{invalid`}).Error)
	contentModerationConfigCacheMu.Lock()
	contentModerationConfigCacheAt = time.Now().Add(-2 * contentModerationConfigRefreshInterval)
	contentModerationConfigCacheMu.Unlock()

	for range 2 {
		enabled, err = ContentModerationEnabled()
		require.NoError(t, err)
		require.True(t, enabled)
	}
	common.OptionMapRWMutex.RLock()
	require.Equal(t, validRaw, common.OptionMap[ContentModerationOptionKey])
	common.OptionMapRWMutex.RUnlock()
}

func TestEvaluateContentModerationScoresUsesConfiguredThresholdAndHighestHit(t *testing.T) {
	flagged, category, score := EvaluateContentModerationScores(
		map[string]float64{"self-harm": 0.7, "sexual": 0.9},
		map[string]float64{"self-harm": 0.65, "sexual": 0.95},
	)
	require.True(t, flagged)
	require.Equal(t, "self-harm", category)
	require.Equal(t, 0.7, score)
}

func TestCheckContentModerationPreBlockUsesOpenAICompatibleContract(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	var gotAuthorization string
	var gotPath string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		var data []byte
		data, _ = io.ReadAll(r.Body)
		gotBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"flagged":true,"category_scores":{"self-harm":0.9}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","model":"omni-moderation-latest","api_key":"test-key","sample_rate":1,"all_groups":true,"all_models":true,"block_status":451}`)

	decision, err := CheckContentModeration(context.Background(), ContentModerationRequest{
		UserID: 1, Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat,
		RequestPath: "/v1/chat/completions", RequestID: "req-1", Body: []byte(`{"messages":[{"role":"user","content":"danger"}]}`),
	})
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Equal(t, 451, decision.StatusCode)
	require.Equal(t, "Bearer test-key", gotAuthorization)
	require.Equal(t, "/v1/moderations", gotPath)
	require.True(t, strings.Contains(gotBody, `"model":"omni-moderation-latest"`))
	require.True(t, strings.Contains(gotBody, `"input":"danger"`))
}

func TestCheckContentModerationUsesMultimodalContract(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	selectedImages := map[string]bool{
		"https://img.test/first.png":     true,
		"data:image/png;base64,c2Vjb25k": true,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string          `json:"model"`
			Input json.RawMessage `json:"input"`
		}
		require.NoError(t, common.DecodeJson(r.Body, &payload))
		require.Equal(t, "omni-moderation-latest", payload.Model)

		var parts []moderationAPIInputPart
		require.NoError(t, common.Unmarshal(payload.Input, &parts))
		require.Len(t, parts, 3)
		require.Equal(t, "text", parts[0].Type)
		require.Equal(t, contentModerationMaxInputRunes, len([]rune(parts[0].Text)))
		require.Equal(t, "text", parts[1].Type)
		require.Equal(t, "tail", strings.TrimSpace(parts[1].Text))
		require.Equal(t, "image_url", parts[2].Type)
		require.NotNil(t, parts[2].ImageURL)
		require.True(t, selectedImages[parts[2].ImageURL.URL])
		_, _ = w.Write([]byte(`{"results":[{"flagged":true,"category_scores":{"violence":0.99}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","model":"omni-moderation-latest","sample_rate":1,"all_groups":true,"all_models":true}`)

	decision, err := CheckContentModeration(context.Background(), ContentModerationRequest{
		UserID: 1, Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat,
		Text: strings.Repeat("a", contentModerationMaxInputRunes) + " tail",
		Images: []string{
			"https://img.test/first.png",
			"data:image/png;base64,c2Vjb25k",
			"https://img.test/first.png",
		},
	})
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Equal(t, "violence", decision.Category)
}

func TestCheckContentModerationSupportsImageOnlyInput(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	image := "data:image/png;base64,aW1hZ2Utb25seQ=="
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Input []moderationAPIInputPart `json:"input"`
		}
		require.NoError(t, common.DecodeJson(r.Body, &payload))
		require.Len(t, payload.Input, 1)
		require.Equal(t, "image_url", payload.Input[0].Type)
		require.Equal(t, image, payload.Input[0].ImageURL.URL)
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"observe","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true}`)

	decision, err := CheckContentModeration(context.Background(), ContentModerationRequest{
		Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat, Images: []string{image},
	})
	require.NoError(t, err)
	require.True(t, decision.Checked)
	require.False(t, decision.Flagged)
}

func TestCheckContentModerationDoesNotCacheInvalidMultimodalResultCount(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}},{"flagged":false,"category_scores":{"sexual":0.02}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true}`)

	config := GetContentModerationConfig()
	input := ContentModerationRequest{
		UserID: 1, Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat,
		Images: []string{"https://img.test/cardinality.png"}, AffinityRuleName: "rule", AffinityCacheIdentity: "multimodal-cardinality",
		AffinityTTLSeconds: 300, AffinityChannelID: 1,
	}
	key := contentModerationAffinityCacheKey(input, config)
	t.Cleanup(func() { _, _ = getContentModerationAffinityCache().DeleteMany([]string{key}) })

	for range 2 {
		decision, err := CheckContentModeration(context.Background(), input)
		require.NoError(t, err)
		require.Contains(t, decision.Error, "does not match input count")
		require.False(t, decision.Cached)
		require.False(t, decision.Blocked)
	}
	require.Equal(t, 2, requestCount)
}

func TestCheckContentModerationImageFailureDoesNotExposeImageReference(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true}`)

	image := "https://private.example.test/sensitive.png"
	decision, err := CheckContentModeration(context.Background(), ContentModerationRequest{
		Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat, Images: []string{image},
	})
	require.NoError(t, err)
	require.Contains(t, decision.Error, "status 400")
	require.NotContains(t, decision.Error, image)
	require.False(t, decision.Blocked)
}

func TestCheckContentModerationImageTimeoutIsRetryable(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	requestCount := 0
	contentModerationHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	withContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"https://moderation.example.test","timeout_ms":1,"retry_count":0,"sample_rate":1,"all_groups":true,"all_models":true,"key_cooldown_ms":100}`)

	config := GetContentModerationConfig()
	input := ContentModerationRequest{
		UserID: 1, Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat,
		Images: []string{"https://img.test/timeout.png"}, AffinityRuleName: "rule", AffinityCacheIdentity: "image-timeout",
		AffinityTTLSeconds: 300, AffinityChannelID: 1,
	}
	key := contentModerationAffinityCacheKey(input, config)
	t.Cleanup(func() { _, _ = getContentModerationAffinityCache().DeleteMany([]string{key}) })

	for attempt := range 2 {
		decision, err := CheckContentModeration(context.Background(), input)
		require.NoError(t, err)
		require.Contains(t, decision.Error, "moderation request failed")
		require.False(t, decision.Cached)
		require.False(t, decision.Blocked)
		if attempt == 0 {
			time.Sleep(125 * time.Millisecond)
		}
	}
	require.Equal(t, 2, requestCount)
}

func TestCheckContentModerationDoesNotPersistImageReference(t *testing.T) {
	require.NotNil(t, model.DB)
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.ContentModerationUserState{}))
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"observe","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true,"record_non_hits":true}`)

	requestID := "image-reference-redaction"
	image := "https://private.example.test/sensitive-image.png"
	t.Cleanup(func() {
		_ = model.DB.Where("request_id = ?", requestID).Delete(&model.ContentModerationLog{}).Error
	})
	decision, err := CheckContentModeration(context.Background(), ContentModerationRequest{
		UserID: 987662, Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat,
		RequestID: requestID, Images: []string{image},
	})
	require.NoError(t, err)
	require.NotZero(t, decision.LogID)

	entry, err := model.GetContentModerationLog(decision.LogID)
	require.NoError(t, err)
	require.Empty(t, entry.Excerpt)
	require.Len(t, entry.ExcerptHash, sha256.Size*2)
	serialized, err := common.Marshal(entry)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), image)
}

func TestCheckContentModerationRotatesKeysAfterTransientFailure(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	requestCount := 0
	var authorization []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		authorization = append(authorization, r.Header.Get("Authorization"))
		if requestCount == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"observe","base_url":"`+server.URL+`","api_key":"first-key\nsecond-key","sample_rate":1,"all_groups":true,"all_models":true,"retry_count":1}`)

	decision, err := CheckContentModeration(context.Background(), ContentModerationRequest{
		Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat,
		RequestID: "req-rotate", Body: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	})
	require.NoError(t, err)
	require.False(t, decision.Flagged)
	require.Equal(t, 2, requestCount)
	require.Equal(t, []string{"Bearer first-key", "Bearer second-key"}, authorization)
}

func TestCallModerationAvoidsCooledDownKey(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()
	resetContentModerationCapacityState()
	defer resetContentModerationCapacityState()

	var authorization []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		authorization = append(authorization, request.Header.Get("Authorization"))
		if request.Header.Get("Authorization") == "Bearer first-key" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()

	config := defaultContentModerationConfig()
	config.Enabled = true
	config.BaseURL = server.URL
	config.APIKey = "first-key\nsecond-key"
	config.RetryCount = 1
	config.KeyCooldownMS = 1000
	content := ContentModerationInput{Text: "cooldown-probe"}
	for contentModerationProviderCredentialStart(content, 2) != 0 {
		content.Text += "x"
	}

	_, _, err := callModeration(context.Background(), config, content)
	require.NoError(t, err)
	config.RetryCount = 0
	_, _, err = callModeration(context.Background(), config, content)
	require.NoError(t, err)
	require.Equal(t, []string{
		"Bearer first-key",
		"Bearer second-key",
		"Bearer second-key",
	}, authorization)
}

func TestCallModerationReportsCapacityExhaustionAfterTransientFailure(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()
	resetContentModerationCapacityState()
	defer resetContentModerationCapacityState()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()

	config := defaultContentModerationConfig()
	config.BaseURL = server.URL
	config.APIKey = "first-key\nsecond-key"
	config.RetryCount = 1
	config.QueueWaitMS = 20
	credentials := contentModerationProviderCredentials(config)
	require.True(t, tryAcquireLocalContentModerationProviderSlot(credentials[1].Fingerprint, 1))
	defer releaseLocalContentModerationProviderSlot(credentials[1].Fingerprint)
	content := ContentModerationInput{Text: "capacity-after-failure"}
	for contentModerationProviderCredentialStart(content, 2) != 0 {
		content.Text += "x"
	}

	_, _, err := callModeration(context.Background(), config, content)
	require.ErrorIs(t, err, ErrContentModerationCapacity)
}

func TestCheckContentModerationLimitsLocalConcurrencyPerKey(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	var active atomic.Int32
	var maximum atomic.Int32
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		active.Add(-1)
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"observe","base_url":"`+server.URL+`","api_key":"one-key","sample_rate":1,"all_groups":true,"all_models":true,"max_in_flight_per_key":1,"queue_wait_ms":1000}`)

	const requests = 6
	start := make(chan struct{})
	results := make(chan *ContentModerationDecision, requests)
	var waitGroup sync.WaitGroup
	for index := range requests {
		index := index
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			decision, err := CheckContentModeration(context.Background(), ContentModerationRequest{
				UserID: 750000 + index, Group: "default", Model: "model-a",
				Protocol: ContentModerationProtocolOpenAIChat, Text: fmt.Sprintf("probe-%d", index),
			})
			require.NoError(t, err)
			results <- decision
		}()
	}
	close(start)
	waitGroup.Wait()
	close(results)
	for decision := range results {
		require.True(t, decision.Checked)
		require.False(t, decision.Overloaded)
	}
	require.EqualValues(t, requests, calls.Load())
	require.EqualValues(t, 1, maximum.Load())
}

func TestCheckContentModerationUsesAvailableCapacityAcrossKeys(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	var mutex sync.Mutex
	activeByKey := map[string]int{}
	maximumByKey := map[string]int{}
	globalActive := 0
	globalMaximum := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		key := request.Header.Get("Authorization")
		mutex.Lock()
		activeByKey[key]++
		if activeByKey[key] > maximumByKey[key] {
			maximumByKey[key] = activeByKey[key]
		}
		globalActive++
		if globalActive > globalMaximum {
			globalMaximum = globalActive
		}
		mutex.Unlock()

		time.Sleep(60 * time.Millisecond)

		mutex.Lock()
		activeByKey[key]--
		globalActive--
		mutex.Unlock()
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"observe","base_url":"`+server.URL+`","api_key":"first-key\nsecond-key","sample_rate":1,"all_groups":true,"all_models":true,"max_in_flight_per_key":1,"queue_wait_ms":1000}`)

	start := make(chan struct{})
	var waitGroup sync.WaitGroup
	for index := range 4 {
		index := index
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			decision, err := CheckContentModeration(context.Background(), ContentModerationRequest{
				UserID: 751000 + index, Group: "default", Model: "model-a",
				Protocol: ContentModerationProtocolOpenAIChat, Text: fmt.Sprintf("parallel-%d", index),
			})
			require.NoError(t, err)
			require.False(t, decision.Overloaded)
		}()
	}
	close(start)
	waitGroup.Wait()

	mutex.Lock()
	defer mutex.Unlock()
	require.Equal(t, 1, maximumByKey["Bearer first-key"])
	require.Equal(t, 1, maximumByKey["Bearer second-key"])
	require.Equal(t, 2, globalMaximum)
}

func TestCheckContentModerationObservePersistsCapacitySkip(t *testing.T) {
	require.NotNil(t, model.DB)
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.ContentModerationUserState{}))
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"observe","base_url":"`+server.URL+`","api_key":"one-key","sample_rate":1,"all_groups":true,"all_models":true,"max_in_flight_per_key":1,"queue_wait_ms":20}`)

	const firstUserID = 752001
	const secondUserID = 752002
	t.Cleanup(func() {
		_ = model.DB.Where("user_id IN ?", []int{firstUserID, secondUserID}).Delete(&model.ContentModerationLog{}).Error
	})
	firstResult := make(chan *ContentModerationDecision, 1)
	go func() {
		decision, _ := CheckContentModeration(context.Background(), ContentModerationRequest{
			UserID: firstUserID, Group: "default", Model: "model-a",
			Protocol: ContentModerationProtocolOpenAIChat, Text: "hold",
		})
		firstResult <- decision
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first moderation request did not start")
	}
	defer func() {
		close(release)
		require.NotNil(t, <-firstResult)
	}()

	decision, err := CheckContentModeration(context.Background(), ContentModerationRequest{
		UserID: secondUserID, Group: "default", Model: "model-a", RequestID: "observe-capacity-skip",
		Protocol: ContentModerationProtocolOpenAIChat, Text: "overflow",
	})
	require.NoError(t, err)
	require.True(t, decision.Checked)
	require.True(t, decision.Overloaded)
	require.False(t, decision.Blocked)
	require.Contains(t, decision.Error, ErrContentModerationCapacity.Error())
	require.NotZero(t, decision.LogID)
	entry, err := model.GetContentModerationLog(decision.LogID)
	require.NoError(t, err)
	require.Equal(t, "skipped_capacity", entry.Action)
	require.False(t, entry.Flagged)
	require.False(t, entry.Blocked)

}

func TestCheckContentModerationPreBlockFailsOpenWhenCapacityIsExhausted(t *testing.T) {
	require.NotNil(t, model.DB)
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.ContentModerationUserState{}))
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","api_key":"one-key","sample_rate":1,"all_groups":true,"all_models":true,"max_in_flight_per_key":1,"queue_wait_ms":20,"overload_status":429}`)

	firstResult := make(chan *ContentModerationDecision, 1)
	go func() {
		decision, _ := CheckContentModeration(context.Background(), ContentModerationRequest{
			UserID: 753001, Group: "default", Model: "model-a",
			Protocol: ContentModerationProtocolOpenAIChat, Text: "hold",
		})
		firstResult <- decision
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first moderation request did not start")
	}
	defer func() {
		close(release)
		require.NotNil(t, <-firstResult)
	}()

	decision, err := CheckContentModeration(context.Background(), ContentModerationRequest{
		UserID: 753002, Group: "default", Model: "model-a",
		Protocol: ContentModerationProtocolOpenAIChat, Text: "overflow",
	})
	require.NoError(t, err)
	require.True(t, decision.Overloaded)
	require.False(t, decision.Blocked)
	require.Zero(t, decision.StatusCode)
	require.Empty(t, decision.Message)

}

func TestCheckContentModerationPreBlockFailsOpenWhenProviderIsUnavailable(t *testing.T) {
	require.NotNil(t, model.DB)
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.ContentModerationUserState{}))
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","api_key":"first-key\nsecond-key","sample_rate":1,"all_groups":true,"all_models":true,"retry_count":1,"queue_wait_ms":20}`)

	requestID := "provider-unavailable-pre-block"
	t.Cleanup(func() {
		_ = model.DB.Where("request_id = ?", requestID).Delete(&model.ContentModerationLog{}).Error
	})

	decision, err := CheckContentModeration(context.Background(), ContentModerationRequest{
		UserID: 752003, Group: "default", Model: "model-a", RequestID: requestID,
		Protocol: ContentModerationProtocolOpenAIChat, Text: "provider outage",
	})
	require.NoError(t, err)
	require.True(t, decision.Checked)
	require.False(t, decision.Flagged)
	require.False(t, decision.Blocked)
	require.False(t, decision.Overloaded)
	require.Contains(t, decision.Error, "moderation API returned status 503")
}

func TestContentModerationProviderSlotUsesRedisFingerprintLease(t *testing.T) {
	server := withContentModerationRedis(t)
	config := defaultContentModerationConfig()
	config.MaxInFlightPerKey = 1
	credential := contentModerationProviderCredential{
		APIKey:      "super-secret-key",
		Fingerprint: contentModerationProviderKeyFingerprint(config, "super-secret-key"),
	}

	first, acquired, degraded := tryAcquireContentModerationProviderSlot(context.Background(), config, credential)
	require.True(t, acquired)
	require.False(t, degraded)
	keys := server.Keys()
	require.Len(t, keys, 1)
	require.Contains(t, keys[0], credential.Fingerprint)
	require.NotContains(t, keys[0], credential.APIKey)

	resetContentModerationCapacityState()
	_, acquired, degraded = tryAcquireContentModerationProviderSlot(context.Background(), config, credential)
	require.False(t, acquired)
	require.False(t, degraded)

	server.FastForward(contentModerationProviderLeaseTTL(config) + time.Millisecond)
	third, acquired, degraded := tryAcquireContentModerationProviderSlot(context.Background(), config, credential)
	require.True(t, acquired)
	require.False(t, degraded)
	third.release()
	require.Empty(t, server.Keys())
	first.Local = false

	markContentModerationProviderKeyCooldown(config, credential.Fingerprint)
	keys = server.Keys()
	require.Len(t, keys, 1)
	require.Contains(t, keys[0], credential.Fingerprint)
	require.NotContains(t, keys[0], credential.APIKey)
	resetContentModerationCapacityState()
	coolingDown, degraded := contentModerationProviderKeyCoolingDown(context.Background(), credential.Fingerprint)
	require.True(t, coolingDown)
	require.False(t, degraded)
}

func TestContentModerationProviderSlotLargeLimitKeepsRedisWorkBounded(t *testing.T) {
	server := withContentModerationRedis(t)
	config := defaultContentModerationConfig()
	config.MaxInFlightPerKey = 1_000_000_000
	credential := contentModerationProviderCredential{
		Fingerprint: contentModerationProviderKeyFingerprint(config, "large-limit-key"),
	}

	first, acquired, degraded := tryAcquireContentModerationProviderSlot(context.Background(), config, credential)
	require.True(t, acquired)
	require.False(t, degraded)
	resetContentModerationCapacityState()

	commandsBefore := server.Server().TotalCommands()
	second, acquired, degraded := tryAcquireContentModerationProviderSlot(context.Background(), config, credential)
	commandsAfter := server.Server().TotalCommands()
	require.True(t, acquired)
	require.False(t, degraded)
	require.LessOrEqual(t, commandsAfter-commandsBefore, 10)
	second.release()
	first.release()
}

func TestContentModerationProviderSlotFallsBackToLocalWhenRedisFails(t *testing.T) {
	server := withContentModerationRedis(t)
	server.SetError("capacity-redis-unavailable")

	config := defaultContentModerationConfig()
	credential := contentModerationProviderCredential{
		Fingerprint: contentModerationProviderKeyFingerprint(config, "fallback-key"),
	}

	first, acquired, degraded := tryAcquireContentModerationProviderSlot(context.Background(), config, credential)
	require.True(t, acquired)
	require.True(t, degraded)

	_, acquired, degraded = tryAcquireContentModerationProviderSlot(context.Background(), config, credential)
	require.False(t, acquired)
	require.False(t, degraded)
	first.release()
}

func TestNormalizeContentModerationConfigValidatesCapacitySettings(t *testing.T) {
	config := defaultContentModerationConfig()
	require.Equal(t, 1, config.MaxInFlightPerKey)
	require.Equal(t, 200, config.QueueWaitMS)
	require.Equal(t, http.StatusServiceUnavailable, config.OverloadStatus)
	require.Equal(t, 5000, config.KeyCooldownMS)

	config.OverloadStatus = http.StatusForbidden
	_, err := NormalizeContentModerationConfig(config)
	require.EqualError(t, err, "overload_status must be 429 or 503")

	config = defaultContentModerationConfig()
	config.MaxInFlightPerKey = 128
	normalized, err := NormalizeContentModerationConfig(config)
	require.NoError(t, err)
	require.Equal(t, 128, normalized.MaxInFlightPerKey)
}

func TestCheckContentModerationDeduplicatesConcurrentShortAllowAcrossModels(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"observe","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true}`)

	base := ContentModerationRequest{
		UserID: 741001, Group: "default", Protocol: ContentModerationProtocolOpenAIChat,
		Text: "hi",
	}
	config := GetContentModerationConfig()
	content := ContentModerationInput{Text: base.Text}
	key := contentModerationAllowCacheKey(base, config, content)
	require.NotEmpty(t, key)
	t.Cleanup(func() { _, _ = getContentModerationAllowCache().DeleteMany([]string{key}) })

	start := make(chan struct{})
	results := make(chan *ContentModerationDecision, 2)
	var waitGroup sync.WaitGroup
	for _, modelName := range []string{"model-a", "model-b"} {
		modelName := modelName
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			input := base
			input.Model = modelName
			decision, err := CheckContentModeration(context.Background(), input)
			require.NoError(t, err)
			results <- decision
		}()
	}
	close(start)
	waitGroup.Wait()
	close(results)
	for decision := range results {
		require.True(t, decision.Checked)
		require.False(t, decision.Flagged)
	}
	require.EqualValues(t, 1, requestCount.Load())

	third := base
	third.Model = "model-c"
	decision, err := CheckContentModeration(context.Background(), third)
	require.NoError(t, err)
	require.True(t, decision.Cached)
	require.EqualValues(t, 1, requestCount.Load())
}

func TestCheckContentModerationShortAllowDedupKeepsUserGroupAndTextIsolation(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"observe","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true}`)

	requests := []ContentModerationRequest{
		{UserID: 741101, Group: "default", Model: "model-a", Protocol: ContentModerationProtocolOpenAIChat, Text: "hi"},
		{UserID: 741102, Group: "default", Model: "model-b", Protocol: ContentModerationProtocolOpenAIChat, Text: "hi"},
		{UserID: 741101, Group: "vip", Model: "model-c", Protocol: ContentModerationProtocolOpenAIChat, Text: "hi"},
		{UserID: 741101, Group: "default", Model: "model-d", Protocol: ContentModerationProtocolOpenAIChat, Text: "hello"},
		{UserID: 741101, Group: "default", Model: "model-e", Protocol: ContentModerationProtocolOpenAIChat, Text: "hi"},
	}
	config := GetContentModerationConfig()
	keys := make([]string, 0, len(requests))
	for _, input := range requests {
		keys = append(keys, contentModerationAllowCacheKey(input, config, ContentModerationInput{Text: input.Text}))
	}
	t.Cleanup(func() { _, _ = getContentModerationAllowCache().DeleteMany(keys) })

	wantCalls := []int32{1, 2, 3, 4, 4}
	for index, input := range requests {
		decision, err := CheckContentModeration(context.Background(), input)
		require.NoError(t, err)
		require.True(t, decision.Checked)
		require.EqualValues(t, wantCalls[index], requestCount.Load())
	}
}

func TestCheckContentModerationDoesNotDeduplicateFlaggedShortTextAcrossModels(t *testing.T) {
	require.NotNil(t, model.DB)
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.ContentModerationUserState{}))
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		_, _ = w.Write([]byte(`{"results":[{"flagged":true,"category_scores":{"sexual":0.9}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"observe","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true}`)

	const userID = 741201
	t.Cleanup(func() { _ = model.DB.Where("user_id = ?", userID).Delete(&model.ContentModerationLog{}).Error })
	for _, modelName := range []string{"model-a", "model-b"} {
		decision, err := CheckContentModeration(context.Background(), ContentModerationRequest{
			UserID: userID, Group: "default", Model: modelName,
			Protocol: ContentModerationProtocolOpenAIChat, Text: "bad",
		})
		require.NoError(t, err)
		require.True(t, decision.Flagged)
	}
	require.EqualValues(t, 2, requestCount.Load())
}

func TestCheckContentModerationAllowDedupEligibilityExcludesLongTextAndImages(t *testing.T) {
	config := defaultContentModerationConfig()
	config.Enabled = true
	base := ContentModerationRequest{UserID: 741301, Group: "default", Protocol: ContentModerationProtocolOpenAIChat}

	longText := ContentModerationInput{Text: strings.Repeat("a", contentModerationAllowDedupMaxRunes+1)}
	require.Empty(t, contentModerationAllowCacheKey(base, config, longText))

	imageInput := ContentModerationInput{Text: "hi", Images: []string{"https://img.test/probe.png"}}
	require.Empty(t, contentModerationAllowCacheKey(base, config, imageInput))

	shortText := ContentModerationInput{Text: "hi"}
	require.NotEmpty(t, contentModerationAllowCacheKey(base, config, shortText))
}

func TestCheckContentModerationDoesNotCacheEmptyAPIResults(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true}`)

	config := GetContentModerationConfig()
	input := ContentModerationRequest{
		UserID: 1, Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat,
		Text: "danger", AffinityRuleName: "rule", AffinityCacheIdentity: "empty-results",
		AffinityTTLSeconds: 300, AffinityChannelID: 1,
	}
	key := contentModerationAffinityCacheKey(input, config)
	t.Cleanup(func() { _, _ = getContentModerationAffinityCache().DeleteMany([]string{key}) })

	first, err := CheckContentModeration(context.Background(), input)
	require.NoError(t, err)
	require.Contains(t, first.Error, "response has no results")
	require.False(t, first.Blocked)

	second, err := CheckContentModeration(context.Background(), input)
	require.NoError(t, err)
	require.False(t, second.Cached)
	require.Equal(t, 2, requestCount)
}

func TestCheckContentModerationDoesNotDeduplicateResponseWithoutCategoryScores(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"categories":{"sexual":false}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"observe","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true}`)

	base := ContentModerationRequest{
		UserID: 741401, Group: "default", Protocol: ContentModerationProtocolOpenAIChat,
		Text: "hi",
	}
	config := GetContentModerationConfig()
	key := contentModerationAllowCacheKey(base, config, ContentModerationInput{Text: base.Text})
	require.NotEmpty(t, key)
	t.Cleanup(func() { _, _ = getContentModerationAllowCache().DeleteMany([]string{key}) })

	for _, modelName := range []string{"model-a", "model-b"} {
		input := base
		input.Model = modelName
		decision, err := CheckContentModeration(context.Background(), input)
		require.NoError(t, err)
		require.True(t, decision.Checked)
		require.False(t, decision.Flagged)
		require.False(t, decision.Cached)
	}
	require.Equal(t, 2, requestCount)
}

func TestCheckContentModerationDoesNotDeduplicateRawProviderFlag(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		_, _ = w.Write([]byte(`{"results":[{"flagged":true,"category_scores":{"sexual":0.9}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"observe","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true,"thresholds":{"sexual":0.95}}`)

	base := ContentModerationRequest{
		UserID: 741402, Group: "default", Protocol: ContentModerationProtocolOpenAIChat,
		Text: "hi",
	}
	config := GetContentModerationConfig()
	key := contentModerationAllowCacheKey(base, config, ContentModerationInput{Text: base.Text})
	require.NotEmpty(t, key)
	t.Cleanup(func() { _, _ = getContentModerationAllowCache().DeleteMany([]string{key}) })

	for _, modelName := range []string{"model-a", "model-b"} {
		input := base
		input.Model = modelName
		decision, err := CheckContentModeration(context.Background(), input)
		require.NoError(t, err)
		require.True(t, decision.Checked)
		require.False(t, decision.Flagged)
		require.False(t, decision.Cached)
	}
	require.Equal(t, 2, requestCount)
}

func TestCheckContentModerationAuditsLongLatestTurnInOneRequest(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var payload struct {
			Input []string `json:"input"`
		}
		require.NoError(t, common.DecodeJson(r.Body, &payload))
		require.Len(t, payload.Input, 2)
		require.Equal(t, contentModerationMaxInputRunes, len([]rune(payload.Input[0])))
		require.Contains(t, payload.Input[1], "danger-at-the-end")
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}},{"flagged":true,"category_scores":{"sexual":0.91}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true}`)

	decision, err := CheckContentModeration(context.Background(), ContentModerationRequest{
		UserID: 1, Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat,
		Text: strings.Repeat("a", contentModerationMaxInputRunes) + " danger-at-the-end",
	})
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Equal(t, "sexual", decision.Category)
	require.Equal(t, 0.91, decision.Score)
	require.Equal(t, 1, requestCount)
}

func TestCheckContentModerationDoesNotCachePartialResultsForLongInput(t *testing.T) {
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var payload struct {
			Input []string `json:"input"`
		}
		require.NoError(t, common.DecodeJson(r.Body, &payload))
		require.Len(t, payload.Input, 2)
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true}`)

	config := GetContentModerationConfig()
	input := ContentModerationRequest{
		UserID: 1, Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat,
		Text:             strings.Repeat("a", contentModerationMaxInputRunes) + " danger-at-the-end",
		AffinityRuleName: "rule", AffinityCacheIdentity: "partial-results",
		AffinityTTLSeconds: 300, AffinityChannelID: 1,
	}
	key := contentModerationAffinityCacheKey(input, config)
	t.Cleanup(func() { _, _ = getContentModerationAffinityCache().DeleteMany([]string{key}) })

	for range 2 {
		decision, err := CheckContentModeration(context.Background(), input)
		require.NoError(t, err)
		require.Contains(t, decision.Error, "does not match input count")
		require.False(t, decision.Blocked)
		require.False(t, decision.Cached)
	}
	require.Equal(t, 2, requestCount)
}

func TestCheckContentModerationUsesChannelAffinityCachePerConversationAndChannel(t *testing.T) {
	require.NotNil(t, model.DB)
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.ContentModerationUserState{}))
	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			_, _ = w.Write([]byte(`{"results":[{"flagged":true,"category_scores":{"sexual":0.9}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","api_key":"test-key","sample_rate":1,"all_groups":true,"all_models":true}`)

	base := ContentModerationRequest{
		UserID:                 1,
		Group:                  "default",
		Model:                  "gpt-test",
		Protocol:               ContentModerationProtocolOpenAIChat,
		RequestID:              "affinity-first",
		AffinityRuleName:       "responses-trace",
		AffinityKeyFingerprint: "conversation-fingerprint",
		AffinityTTLSeconds:     300,
		AffinityChannelID:      101,
		Body:                   []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"first"},{"type":"image_url","image_url":{"url":"https://img.test/first.png"}}]}]}`),
	}
	config := GetContentModerationConfig()
	cacheKeys := []string{contentModerationAffinityCacheKey(base, config)}
	t.Cleanup(func() {
		if cache := contentModerationAffinityCache; cache != nil {
			_, _ = cache.DeleteMany(cacheKeys)
		}
	})
	first, err := CheckContentModeration(context.Background(), base)
	require.NoError(t, err)
	require.True(t, first.Flagged)
	require.True(t, first.Blocked)

	secondInput := base
	secondInput.RequestID = "affinity-second"
	secondInput.Body = []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"second"},{"type":"image_url","image_url":{"url":"https://img.test/second.png"}}]}]}`)
	second, err := CheckContentModeration(context.Background(), secondInput)
	require.NoError(t, err)
	require.True(t, second.Cached)
	require.True(t, second.Flagged)
	require.True(t, second.Blocked)
	require.Equal(t, 1, requestCount)

	thirdInput := secondInput
	thirdInput.RequestID = "affinity-other-channel"
	thirdInput.AffinityChannelID = 202
	cacheKeys = append(cacheKeys, contentModerationAffinityCacheKey(thirdInput, config))
	third, err := CheckContentModeration(context.Background(), thirdInput)
	require.NoError(t, err)
	require.False(t, third.Cached)
	require.False(t, third.Flagged)
	require.Equal(t, 2, requestCount)
}

func TestContentModerationAffinityLeaseAllowsOnlyOneOwner(t *testing.T) {
	withContentModerationRedis(t)
	config := defaultContentModerationConfig()
	config.Enabled = true
	config.TimeoutMS = 100
	input := ContentModerationRequest{UserID: 1, Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat, AffinityRuleName: "rule", AffinityCacheIdentity: "lease-success", AffinityTTLSeconds: 300, AffinityChannelID: 1}
	key := contentModerationAffinityCacheKey(input, config)
	t.Cleanup(func() { _, _ = getContentModerationAffinityCache().DeleteMany([]string{key}) })

	var checks atomic.Int32
	check := func() *ContentModerationDecision {
		checks.Add(1)
		time.Sleep(50 * time.Millisecond)
		decision := ContentModerationDecision{Checked: true}
		cacheContentModerationDecision(input, config, decision, true)
		return &decision
	}
	results := make(chan *ContentModerationDecision, 2)
	var waitGroup sync.WaitGroup
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			results <- runContentModerationWithAffinityLease(context.Background(), input, config, key, check)
		}()
	}
	waitGroup.Wait()
	close(results)
	for decision := range results {
		require.True(t, decision.Checked)
	}
	require.EqualValues(t, 1, checks.Load())
}

func TestContentModerationAllowLeaseAllowsOnlyOneOwner(t *testing.T) {
	withContentModerationRedis(t)
	config := defaultContentModerationConfig()
	config.Enabled = true
	config.TimeoutMS = 100
	key := "allow-lease-test"
	t.Cleanup(func() { _, _ = getContentModerationAllowCache().DeleteMany([]string{key}) })

	var checks atomic.Int32
	check := func() *ContentModerationDecision {
		checks.Add(1)
		time.Sleep(50 * time.Millisecond)
		cacheContentModerationAllowDecision(key)
		return &ContentModerationDecision{Checked: true}
	}
	results := make(chan *ContentModerationDecision, 2)
	var waitGroup sync.WaitGroup
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			results <- runContentModerationWithAllowLease(context.Background(), config, key, check)
		}()
	}
	waitGroup.Wait()
	close(results)
	for decision := range results {
		require.True(t, decision.Checked)
	}
	require.EqualValues(t, 1, checks.Load())
}

func TestContentModerationAffinityLeaseRenewsDuringSlowOwner(t *testing.T) {
	withContentModerationRedis(t)
	config := defaultContentModerationConfig()
	config.Enabled = true
	config.TimeoutMS = 100
	input := ContentModerationRequest{UserID: 1, Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat, AffinityRuleName: "rule", AffinityCacheIdentity: "lease-renewal", AffinityTTLSeconds: 300, AffinityChannelID: 1}
	key := contentModerationAffinityCacheKey(input, config)
	t.Cleanup(func() { _, _ = getContentModerationAffinityCache().DeleteMany([]string{key}) })

	var checks atomic.Int32
	check := func() *ContentModerationDecision {
		checks.Add(1)
		time.Sleep(contentModerationLeaseTTL(config) + 500*time.Millisecond)
		decision := ContentModerationDecision{Checked: true}
		cacheContentModerationDecision(input, config, decision, true)
		return &decision
	}
	results := make(chan *ContentModerationDecision, 2)
	var waitGroup sync.WaitGroup
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			results <- runContentModerationWithAffinityLease(context.Background(), input, config, key, check)
		}()
	}
	waitGroup.Wait()
	close(results)
	for decision := range results {
		require.True(t, decision.Checked)
	}
	require.EqualValues(t, 1, checks.Load())
}

func TestContentModerationAffinityLeaseRetriesAfterOwnerFailure(t *testing.T) {
	withContentModerationRedis(t)
	config := defaultContentModerationConfig()
	config.Enabled = true
	config.TimeoutMS = 100
	input := ContentModerationRequest{UserID: 1, Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat, AffinityRuleName: "rule", AffinityCacheIdentity: "lease-failure", AffinityTTLSeconds: 300, AffinityChannelID: 1}
	key := contentModerationAffinityCacheKey(input, config)
	t.Cleanup(func() { _, _ = getContentModerationAffinityCache().DeleteMany([]string{key}) })

	var checks atomic.Int32
	check := func() *ContentModerationDecision {
		attempt := checks.Add(1)
		if attempt == 1 {
			time.Sleep(30 * time.Millisecond)
			return &ContentModerationDecision{Checked: true, Error: "owner failed"}
		}
		decision := ContentModerationDecision{Checked: true}
		cacheContentModerationDecision(input, config, decision, true)
		return &decision
	}
	results := make(chan *ContentModerationDecision, 2)
	var waitGroup sync.WaitGroup
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			results <- runContentModerationWithAffinityLease(context.Background(), input, config, key, check)
		}()
	}
	waitGroup.Wait()
	close(results)
	require.EqualValues(t, 2, checks.Load())
	var successful int
	for decision := range results {
		if decision.Error == "" {
			successful++
		}
	}
	require.Equal(t, 1, successful)
}

func TestContentModerationDoesNotCacheWhenLogPersistenceFails(t *testing.T) {
	previousClient := contentModerationHTTPClient
	previousDB := model.DB
	defer func() { contentModerationHTTPClient = previousClient; model.DB = previousDB }()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		_, _ = w.Write([]byte(`{"results":[{"flagged":true,"category_scores":{"sexual":0.9}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true}`)
	config := GetContentModerationConfig()
	input := ContentModerationRequest{UserID: 1, Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat, Text: "danger", AffinityRuleName: "rule", AffinityCacheIdentity: "log-failure", AffinityTTLSeconds: 300, AffinityChannelID: 1}
	key := contentModerationAffinityCacheKey(input, config)
	t.Cleanup(func() { _, _ = getContentModerationAffinityCache().DeleteMany([]string{key}) })
	model.DB = nil

	first, err := CheckContentModeration(context.Background(), input)
	require.NoError(t, err)
	require.True(t, first.Blocked)
	second, err := CheckContentModeration(context.Background(), input)
	require.NoError(t, err)
	require.True(t, second.Blocked)
	require.False(t, second.Cached)
	require.Equal(t, 2, requestCount)
}

func TestContentModerationCacheHitRetriesIncompleteSideEffects(t *testing.T) {
	require.NotNil(t, model.DB)
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.ContentModerationUserState{}, &model.User{}))
	const userID = 987654
	_ = model.DB.Unscoped().Delete(&model.User{}, userID).Error
	t.Cleanup(func() { _ = model.DB.Unscoped().Delete(&model.User{}, userID).Error })

	previousClient := contentModerationHTTPClient
	defer func() { contentModerationHTTPClient = previousClient }()
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		_, _ = w.Write([]byte(`{"results":[{"flagged":true,"category_scores":{"sexual":0.9}}]}`))
	}))
	defer server.Close()
	contentModerationHTTPClient = server.Client()
	withContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true,"email_on_hit":true}`)
	config := GetContentModerationConfig()
	input := ContentModerationRequest{UserID: userID, Group: "default", Model: "gpt-test", Protocol: ContentModerationProtocolOpenAIChat, Text: "danger", AffinityRuleName: "rule", AffinityCacheIdentity: "side-effect-retry", AffinityTTLSeconds: 300, AffinityChannelID: 1}
	key := contentModerationAffinityCacheKey(input, config)
	t.Cleanup(func() { _, _ = getContentModerationAffinityCache().DeleteMany([]string{key}) })

	first, err := CheckContentModeration(context.Background(), input)
	require.NoError(t, err)
	require.True(t, first.Blocked)
	entry, found, err := getContentModerationAffinityCache().Get(key)
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, entry.SideEffects)

	require.NoError(t, model.DB.Create(&model.User{Id: userID, Username: "moderation-retry", Password: "unused-password", Status: common.UserStatusEnabled, Role: common.RoleCommonUser}).Error)
	second, err := CheckContentModeration(context.Background(), input)
	require.NoError(t, err)
	require.True(t, second.Cached)
	require.Equal(t, 1, requestCount)
	entry, found, err = getContentModerationAffinityCache().Get(key)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, entry.SideEffects)
}

func TestContentModerationEmailSideEffectIsAsyncAndClaimedOnce(t *testing.T) {
	require.NoError(t, i18n.Init())
	require.NotNil(t, model.DB)
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.ContentModerationUserState{}, &model.User{}))
	const userID = 987655
	_ = model.DB.Unscoped().Delete(&model.User{}, userID).Error
	t.Cleanup(func() {
		_ = model.DB.Where("user_id = ?", userID).Delete(&model.ContentModerationLog{}).Error
		_ = model.DB.Unscoped().Delete(&model.User{}, userID).Error
	})
	require.NoError(t, model.DB.Create(&model.User{Id: userID, Username: "moderation-email", Password: "unused-password", Email: "moderation@example.com", Status: common.UserStatusEnabled, Role: common.RoleCommonUser}).Error)
	entry := &model.ContentModerationLog{UserID: userID, Flagged: true, Category: "sexual", Score: 0.9, CreatedAt: time.Now().Unix()}
	require.NoError(t, model.CreateContentModerationLog(entry))

	previousSender := contentModerationEmailSender
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var sends atomic.Int32
	contentModerationEmailSender = func(ctx context.Context, _ string, _ dto.Notify, _ int) error {
		sends.Add(1)
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.Cleanup(func() { contentModerationEmailSender = previousSender })

	config := defaultContentModerationConfig()
	config.EmailOnHit = true
	input := ContentModerationRequest{UserID: userID}
	startedAt := time.Now()
	result := applyContentModerationSideEffects(input, config, entry)
	require.False(t, result)
	require.Less(t, time.Since(startedAt), time.Second)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("email worker did not start")
	}

	var waitGroup sync.WaitGroup
	for range 8 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			entryCopy := *entry
			_ = applyContentModerationSideEffects(input, config, &entryCopy)
		}()
	}
	waitGroup.Wait()
	require.EqualValues(t, 1, sends.Load())

	close(release)
	require.Eventually(t, func() bool {
		latest, err := model.GetContentModerationLog(entry.ID)
		return err == nil && latest.EmailSent && !latest.EmailSending
	}, time.Second, 10*time.Millisecond)
	require.EqualValues(t, 1, sends.Load())
}

func TestContentModerationEmailFailureReleasesClaimForRetry(t *testing.T) {
	require.NoError(t, i18n.Init())
	require.NotNil(t, model.DB)
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.ContentModerationUserState{}, &model.User{}))
	const userID = 987656
	_ = model.DB.Unscoped().Delete(&model.User{}, userID).Error
	t.Cleanup(func() {
		_ = model.DB.Where("user_id = ?", userID).Delete(&model.ContentModerationLog{}).Error
		_ = model.DB.Unscoped().Delete(&model.User{}, userID).Error
	})
	require.NoError(t, model.DB.Create(&model.User{Id: userID, Username: "moderation-retry-email", Password: "unused-password", Email: "moderation@example.com", Status: common.UserStatusEnabled, Role: common.RoleCommonUser}).Error)
	entry := &model.ContentModerationLog{UserID: userID, Flagged: true, Category: "sexual", Score: 0.9, CreatedAt: time.Now().Unix()}
	require.NoError(t, model.CreateContentModerationLog(entry))

	previousSender := contentModerationEmailSender
	var sends atomic.Int32
	contentModerationEmailSender = func(context.Context, string, dto.Notify, int) error {
		if sends.Add(1) == 1 {
			return &common.EmailDeliveryError{Err: errors.New("smtp unavailable"), RetrySafe: true}
		}
		return nil
	}
	t.Cleanup(func() { contentModerationEmailSender = previousSender })

	config := defaultContentModerationConfig()
	config.EmailOnHit = true
	input := ContentModerationRequest{UserID: userID}
	require.False(t, applyContentModerationSideEffects(input, config, entry))
	require.Eventually(t, func() bool {
		latest, err := model.GetContentModerationLog(entry.ID)
		return err == nil && !latest.EmailSent && !latest.EmailSending
	}, time.Second, 10*time.Millisecond)
	require.False(t, applyContentModerationSideEffects(input, config, entry))
	require.Eventually(t, func() bool {
		latest, err := model.GetContentModerationLog(entry.ID)
		return err == nil && latest.EmailSent
	}, time.Second, 10*time.Millisecond)
	require.EqualValues(t, 2, sends.Load())
}

func TestContentModerationEmailAmbiguousFailureKeepsClaim(t *testing.T) {
	require.NoError(t, i18n.Init())
	require.NotNil(t, model.DB)
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.ContentModerationUserState{}, &model.User{}))
	const userID = 987658
	_ = model.DB.Unscoped().Delete(&model.User{}, userID).Error
	t.Cleanup(func() {
		_ = model.DB.Where("user_id = ?", userID).Delete(&model.ContentModerationLog{}).Error
		_ = model.DB.Unscoped().Delete(&model.User{}, userID).Error
	})
	require.NoError(t, model.DB.Create(&model.User{Id: userID, Username: "moderation-ambiguous-email", Password: "unused-password", Email: "moderation@example.com", Status: common.UserStatusEnabled, Role: common.RoleCommonUser}).Error)
	entry := &model.ContentModerationLog{UserID: userID, Flagged: true, Category: "sexual", Score: 0.9, CreatedAt: time.Now().Unix()}
	require.NoError(t, model.CreateContentModerationLog(entry))

	previousSender := contentModerationEmailSender
	var sends atomic.Int32
	contentModerationEmailSender = func(context.Context, string, dto.Notify, int) error {
		sends.Add(1)
		return &common.EmailDeliveryError{Err: errors.New("delivery confirmation lost"), RetrySafe: false}
	}
	t.Cleanup(func() { contentModerationEmailSender = previousSender })

	config := defaultContentModerationConfig()
	config.EmailOnHit = true
	input := ContentModerationRequest{UserID: userID}
	require.False(t, applyContentModerationSideEffects(input, config, entry))
	require.Eventually(t, func() bool {
		latest, err := model.GetContentModerationLog(entry.ID)
		return err == nil && latest.EmailSending && !latest.EmailSent
	}, time.Second, 10*time.Millisecond)
	require.False(t, applyContentModerationSideEffects(input, config, entry))
	time.Sleep(50 * time.Millisecond)
	require.EqualValues(t, 1, sends.Load())
}

func TestContentModerationAutoBanIsIdempotent(t *testing.T) {
	require.NotNil(t, model.DB)
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.ContentModerationUserState{}, &model.User{}, &model.UserSession{}))
	const userID = 987657
	_ = model.DB.Unscoped().Delete(&model.User{}, userID).Error
	t.Cleanup(func() {
		_ = model.DB.Where("user_id = ?", userID).Delete(&model.ContentModerationLog{}).Error
		_ = model.DB.Unscoped().Delete(&model.User{}, userID).Error
	})
	require.NoError(t, model.DB.Create(&model.User{Id: userID, Username: "moderation-ban", Password: "unused-password", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AuthVersion: 1}).Error)
	for range 2 {
		require.NoError(t, model.CreateContentModerationLog(&model.ContentModerationLog{UserID: userID, Flagged: true, CreatedAt: time.Now().Unix()}))
	}

	config := defaultContentModerationConfig()
	config.AutoBanEnabled = true
	config.BanThreshold = 2
	input := ContentModerationRequest{UserID: userID}
	entry := &model.ContentModerationLog{UserID: userID, Flagged: true}
	require.True(t, applyContentModerationSideEffects(input, config, entry))
	require.True(t, applyContentModerationSideEffects(input, config, entry))

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.Equal(t, common.UserStatusDisabled, user.Status)
	require.EqualValues(t, 2, user.AuthVersion)
}

func TestContentModerationAutoBanPublishesDisabledAuthCache(t *testing.T) {
	require.NotNil(t, model.DB)
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.ContentModerationUserState{}, &model.User{}, &model.UserSession{}, &model.Token{}))
	withContentModerationRedis(t)
	const userID = 987660
	_ = model.DB.Unscoped().Delete(&model.User{}, userID).Error
	t.Cleanup(func() {
		_ = model.DB.Where("user_id = ?", userID).Delete(&model.ContentModerationLog{}).Error
		_ = model.DB.Unscoped().Delete(&model.User{}, userID).Error
	})
	require.NoError(t, model.DB.Create(&model.User{Id: userID, Username: "moderation-cache-ban", Password: "unused-password", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AuthVersion: 1}).Error)
	cachedBefore, err := model.GetUserCache(userID)
	require.NoError(t, err)
	require.Equal(t, common.UserStatusEnabled, cachedBefore.Status)
	require.EqualValues(t, 1, cachedBefore.AuthVersion)

	for range 2 {
		require.NoError(t, model.CreateContentModerationLog(&model.ContentModerationLog{UserID: userID, Flagged: true, CreatedAt: time.Now().Unix()}))
	}
	config := defaultContentModerationConfig()
	config.AutoBanEnabled = true
	config.BanThreshold = 2
	require.True(t, applyContentModerationSideEffects(
		ContentModerationRequest{UserID: userID},
		config,
		&model.ContentModerationLog{UserID: userID, Flagged: true},
	))

	cachedAfter, err := model.GetUserCache(userID)
	require.NoError(t, err)
	require.Equal(t, common.UserStatusDisabled, cachedAfter.Status)
	require.EqualValues(t, 2, cachedAfter.AuthVersion)
}

func withContentModerationRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	resetContentModerationCapacityState()
	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RedisEnabled = true
	common.RDB = client
	contentModerationAffinityCacheOnce = sync.Once{}
	contentModerationAffinityCache = nil
	contentModerationAllowCacheOnce = sync.Once{}
	contentModerationAllowCache = nil
	t.Cleanup(func() {
		resetContentModerationCapacityState()
		_ = client.Close()
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
		contentModerationAffinityCacheOnce = sync.Once{}
		contentModerationAffinityCache = nil
		contentModerationAllowCacheOnce = sync.Once{}
		contentModerationAllowCache = nil
	})
	return server
}

func withContentModerationOption(t *testing.T, value string) {
	t.Helper()
	resetContentModerationCapacityState()
	common.OptionMapRWMutex.Lock()
	previous := common.OptionMap
	common.OptionMap = map[string]string{ContentModerationOptionKey: value}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		resetContentModerationCapacityState()
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previous
		common.OptionMapRWMutex.Unlock()
	})
}
