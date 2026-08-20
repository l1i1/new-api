package service

import (
	"context"
	"encoding/json"
	"errors"
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
	require.Equal(t, "latest part", got)

	gemini := []byte(`{"contents":[{"role":"model","parts":[{"text":"answer"}]},{"parts":[{"text":"latest gemini"}]}]}`)
	require.Equal(t, "latest gemini", ExtractContentModerationInput(nil, gemini, ContentModerationProtocolGemini))

	anthropic := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>internal</system-reminder>"},{"type":"text","text":"visible"}]}]}`)
	require.Equal(t, "visible", ExtractContentModerationInput(nil, anthropic, ContentModerationProtocolAnthropic))
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

func TestContentModerationAffinitySamplingIsStable(t *testing.T) {
	config := defaultContentModerationConfig()
	config.Enabled = true
	config.SampleRate = 0.5
	input := ContentModerationRequest{Group: "default", Model: "gpt-test", AffinityCacheIdentity: "stable-conversation"}
	first := shouldModerateContent(config, input, "first body")
	input.RequestID = "different-request"
	require.Equal(t, first, shouldModerateContent(config, input, "different body"))
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
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}))
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
		Body:                   []byte(`{"messages":[{"role":"user","content":"first"}]}`),
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
	secondInput.Body = []byte(`{"messages":[{"role":"user","content":"second"}]}`)
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
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.User{}))
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
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.User{}))
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
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.User{}))
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
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.User{}))
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
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.User{}, &model.UserSession{}))
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
	require.NoError(t, model.DB.AutoMigrate(&model.ContentModerationLog{}, &model.User{}, &model.UserSession{}, &model.Token{}))
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

func withContentModerationRedis(t *testing.T) {
	t.Helper()
	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RedisEnabled = true
	common.RDB = client
	contentModerationAffinityCacheOnce = sync.Once{}
	contentModerationAffinityCache = nil
	t.Cleanup(func() {
		_ = client.Close()
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
		contentModerationAffinityCacheOnce = sync.Once{}
		contentModerationAffinityCache = nil
	})
}

func withContentModerationOption(t *testing.T, value string) {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	previous := common.OptionMap
	common.OptionMap = map[string]string{ContentModerationOptionKey: value}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previous
		common.OptionMapRWMutex.Unlock()
	})
}
