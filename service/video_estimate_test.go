package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// withEstimatePost swaps the seam for the duration of one test.
func withEstimatePost(t *testing.T, fn func(cfg VideoEstimateConfig, body []byte) (int, []byte, error)) {
	t.Helper()
	original := videoEstimatePost
	videoEstimatePost = fn
	t.Cleanup(func() { videoEstimatePost = original })
	ResetVideoEstimateCacheForTest()
}

func TestLoadVideoEstimateConfigIsInertWithoutAKey(t *testing.T) {
	t.Setenv(VideoEstimateAPIKeyEnv, "")
	if _, ok := LoadVideoEstimateConfig(); ok {
		t.Fatal("no key configured must mean no endpoint")
	}
	t.Setenv(VideoEstimateAPIKeyEnv, "  test-key  ")
	t.Setenv(VideoEstimateBaseURLEnv, "")
	cfg, ok := LoadVideoEstimateConfig()
	if !ok {
		t.Fatal("a configured key must be usable")
	}
	if cfg.APIKey != "test-key" {
		t.Fatalf("key must be trimmed, got %q", cfg.APIKey)
	}
	if cfg.BaseURL != DefaultVideoEstimateBaseURL {
		t.Fatalf("base URL must default to the documented endpoint, got %q", cfg.BaseURL)
	}
	t.Setenv(VideoEstimateBaseURLEnv, "https://example.test/v1/")
	cfg, _ = LoadVideoEstimateConfig()
	if cfg.BaseURL != "https://example.test/v1" {
		t.Fatalf("trailing slash must be trimmed, got %q", cfg.BaseURL)
	}
}

// videoRequest builds the chat request the prefetch path is given: one message
// carrying a video part, which is what makes it eligible at all.
func videoRequest() *dto.GeneralOpenAIRequest {
	content := []dto.MediaContent{
		{Type: dto.ContentTypeText, Text: "what happens in this clip?"},
		{Type: dto.ContentTypeVideoUrl, VideoUrl: &dto.MessageVideoUrl{Url: "ms://file-abc"}},
	}
	return &dto.GeneralOpenAIRequest{
		Model:    "kimi-k3",
		Messages: []dto.Message{{Role: "user", Content: content}},
	}
}

func TestEstimateRequestBodySerializesWhatTheEndpointPrices(t *testing.T) {
	cfg, ok := LoadVideoEstimateConfig()
	if ok {
		t.Fatal("no key configured must mean no endpoint")
	}
	t.Setenv(VideoEstimateAPIKeyEnv, "test-key")
	cfg, ok = LoadVideoEstimateConfig()
	if !ok {
		t.Fatal("a configured key must be usable")
	}
	if cfg.APIKey != "test-key" {
		t.Fatalf("key must be trimmed, got %q", cfg.APIKey)
	}

	body, cacheKey, ok := estimateRequestBody("kimi-k3", videoRequest(), cfg.BaseURL)
	if !ok {
		t.Fatal("a chat request with a video part must serialize")
	}
	if cacheKey == "" {
		t.Fatal("a video content hash must key the cache")
	}
	// The body goes out as the client sent it, so the endpoint prices exactly
	// what the upstream was asked to read.
	var sent videoEstimateRequest
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("the body must be the endpoint's JSON shape: %v", err)
	}
	if sent.Model != "kimi-k3" {
		t.Fatalf("model: got %q", sent.Model)
	}
	if !strings.Contains(string(body), "ms://file-abc") {
		t.Fatal("the video part must reach the endpoint")
	}

	// Requests that carry no video, and non-chat shapes, are not estimate work.
	if _, _, ok := estimateRequestBody("kimi-k3", &dto.GeneralOpenAIRequest{Model: "kimi-k3"}, cfg.BaseURL); ok {
		t.Fatal("a request without video must not be serialized for the endpoint")
	}
	if _, _, ok := estimateRequestBody("", videoRequest(), cfg.BaseURL); ok {
		t.Fatal("an unknown model must not be priced")
	}
}

func TestEstimateTotalViaEndpointReadsTotalTokens(t *testing.T) {
	withEstimatePost(t, func(cfg VideoEstimateConfig, body []byte) (int, []byte, error) {
		if cfg.APIKey != "test-key" {
			t.Errorf("credential must reach the request, got %q", cfg.APIKey)
		}
		return 200, []byte(`{"code":0,"data":{"total_tokens":12789},"status":true}`), nil
	})
	total, ok, err := estimateTotalViaEndpoint(VideoEstimateConfig{APIKey: "test-key"}, []byte(`{}`))
	if err != nil || !ok {
		t.Fatalf("expected a usable total, got ok=%v err=%v", ok, err)
	}
	if total != 12789 {
		t.Fatalf("total: got %d, want 12789", total)
	}
}

func TestEstimateTotalViaEndpointReportsFallbackForBadAnswers(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		payload string
	}{
		{"transport error", 0, ""},
		{"http error with envelope", 400, `{"error":{"message":"bad request"}}`},
		{"non-JSON body", 200, `<html>nope</html>`},
		{"zero total", 200, `{"data":{"total_tokens":0}}`},
		{"missing data", 200, `{"code":0,"status":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withEstimatePost(t, func(VideoEstimateConfig, []byte) (int, []byte, error) {
				if tc.name == "transport error" {
					return 0, nil, fmt.Errorf("dial failed")
				}
				return tc.status, []byte(tc.payload), nil
			})
			total, ok, err := estimateTotalViaEndpoint(VideoEstimateConfig{APIKey: "test-key"}, []byte(`{}`))
			if ok {
				t.Fatalf("a %s must not report a usable total (got %d)", tc.name, total)
			}
			if err == nil {
				t.Fatalf("a %s must surface an error for logging", tc.name)
			}
		})
	}
}

// TestPrefetchWarmsTheSettlementLookup is the whole point of the prefetch: it
// runs alongside the upstream request and settlement then reads its answer
// without doing I/O. The lookup must also miss cleanly before the answer lands,
// because settlement falls back to the locally priced video part in that case.
func TestPrefetchWarmsTheSettlementLookup(t *testing.T) {
	t.Setenv(VideoEstimateAPIKeyEnv, "test-key")

	request := videoRequest()
	if _, hit := VideoPromptTotalForRequest("kimi-k3", request); hit {
		t.Fatal("nothing may be cached before the prefetch runs")
	}

	answered := make(chan struct{})
	withEstimatePost(t, func(VideoEstimateConfig, []byte) (int, []byte, error) {
		close(answered)
		return 200, []byte(`{"data":{"total_tokens":42416}}`), nil
	})

	// The prefetch must return promptly rather than waiting for the endpoint,
	// which is what keeps the call off the first-token path.
	PrefetchVideoPromptTotal("kimi-k3", request)
	select {
	case <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("the prefetch never reached the endpoint")
	}

	// The answer lands asynchronously, so poll briefly for the cache write.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if total, hit := VideoPromptTotalForRequest("kimi-k3", request); hit {
			if total != 42416 {
				t.Fatalf("cached total: got %d, want 42416", total)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the prefetched total never became readable")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPrefetchIsInertWithoutConfiguration pins the default state: no endpoint
// configured means no goroutine and no outbound call, so billing falls back to
// the local container model.
func TestPrefetchIsInertWithoutConfiguration(t *testing.T) {
	t.Setenv(VideoEstimateAPIKeyEnv, "")
	called := false
	withEstimatePost(t, func(VideoEstimateConfig, []byte) (int, []byte, error) {
		called = true
		return 200, []byte(`{"data":{"total_tokens":1}}`), nil
	})
	PrefetchVideoPromptTotal("kimi-k3", videoRequest())
	time.Sleep(80 * time.Millisecond)
	if called {
		t.Fatal("an unconfigured endpoint must not be called")
	}
}

// TestVideoUsageLooksCountedFixatesTheThreshold checks the guard that decides
// whether a reported prompt count could have included the media.
func TestVideoUsageLooksCountedFixatesTheThreshold(t *testing.T) {
	const video = 42320
	// The measured failure: text-only counts against a 42k-token clip.
	for _, reported := range []int{0, 27, 96, 153} {
		if VideoUsageLooksCounted(reported, video) {
			t.Errorf("reported %d must not count as media-aware", reported)
		}
	}
	// Any source that priced the media reports at least the video price (the
	// local model adds text, the endpoint totals both).
	for _, reported := range []int{video / 2, video, video + 96, 42416} {
		if !VideoUsageLooksCounted(reported, video) {
			t.Errorf("reported %d must count as media-aware", reported)
		}
	}
}

// TestVideoEstimateFallbackKeepsLocalPrice is the behaviour that matters when
// an endpoint is configured but answers with a media-blind number: the local
// price must still be what settlement uses.
func TestVideoEstimateFallbackKeepsLocalPrice(t *testing.T) {
	const local = 42320
	// A media-blind endpoint answer (text-only) fails the guard, so the caller
	// keeps the local value.
	if VideoUsageLooksCounted(96, local) {
		t.Fatal("a media-blind endpoint answer must not be accepted")
	}
	// An endpoint that saw the media passes the guard and supersedes the local
	// price, because it prices text and media together.
	if !VideoUsageLooksCounted(42416, local) {
		t.Fatal("an endpoint total must supersede the local price")
	}
}

// TestEstimatedBillingUsageMarksTheLogPath checks that the corrected usage is
// flagged as estimated, which is what routes it to the estimated billing path
// in the consume log.
func TestEstimatedBillingUsageMarksTheLogPath(t *testing.T) {
	usage := &dto.Usage{PromptTokens: 42416, CompletionTokens: 12, TotalTokens: 42428}
	marked := dto.NewEstimatedOpenAIChatBillingUsage(usage)
	if marked == nil {
		t.Fatal("expected a billing usage")
	}
	if !marked.Estimated {
		t.Fatal("the correction must be marked estimated")
	}
	if marked.Source != dto.BillingUsageSourceOAIChat || marked.Semantic != dto.BillingUsageSemanticOpenAI {
		t.Fatalf("dialect must stay OpenAI: source=%q semantic=%q", marked.Source, marked.Semantic)
	}
	if marked.OpenAIUsage == nil || marked.OpenAIUsage.PromptTokens != 42416 {
		t.Fatalf("payload must carry the corrected prompt count: %+v", marked.OpenAIUsage)
	}
	// An all-zero usage must not become a billing usage, or it would take
	// precedence at settlement and zero out a real count.
	if zero := dto.NewEstimatedOpenAIChatBillingUsage(&dto.Usage{}); zero != nil {
		t.Fatal("a zero usage must not produce a billing usage")
	}
}

func TestVideoUsageModeValidation(t *testing.T) {
	// Empty is always valid: it leaves the upstream-reported values untouched.
	if err := (&dto.ChannelOtherSettings{}).ValidateVideoUsageMode(); err != nil {
		t.Errorf("empty mode must validate: %v", err)
	}
	// A usage mode requires the video capability on the same channel, because a
	// video request is never routed to a channel that has not declared it — a
	// mode alone would be dead configuration that reads as if it worked.
	for _, mode := range []string{"estimate", "  estimate  "} {
		settings := &dto.ChannelOtherSettings{SupportsVideo: true, VideoUsageMode: mode}
		if err := settings.ValidateVideoUsageMode(); err != nil {
			t.Errorf("mode %q with the capability must validate: %v", mode, err)
		}
		modeOnly := &dto.ChannelOtherSettings{VideoUsageMode: mode}
		if err := modeOnly.ValidateVideoUsageMode(); err == nil {
			t.Errorf("mode %q without the capability must be rejected", mode)
		}
	}
	if !(&dto.ChannelOtherSettings{VideoUsageMode: "estimate"}).EstimatesVideoUsage() {
		t.Fatal("estimate must enable the feature")
	}
	if (&dto.ChannelOtherSettings{}).EstimatesVideoUsage() {
		t.Fatal("empty must leave the upstream values untouched")
	}
	for _, bad := range []string{"true", "estiamte", "on"} {
		settings := &dto.ChannelOtherSettings{SupportsVideo: true, VideoUsageMode: bad}
		if err := settings.ValidateVideoUsageMode(); err == nil {
			t.Errorf("mode %q must be rejected at save time", bad)
		}
	}
}

// TestEstimateCacheKeyCoversTextAndModel is a regression test for a billing
// bug: the cache key originally hashed only the video bytes while the endpoint
// prices text and media together, so a longer prompt reusing the same clip hit
// the shorter request's entry and was billed the shorter total (silent
// under-billing). The key must change whenever anything priced changes.
func TestEstimateCacheKeyCoversTextAndModel(t *testing.T) {
	video := []dto.MediaContent{{Type: dto.ContentTypeVideoUrl, VideoUrl: &dto.MessageVideoUrl{Url: "ms://same-clip"}}}
	withText := func(text string) *dto.GeneralOpenAIRequest {
		content := append([]dto.MediaContent{{Type: dto.ContentTypeText, Text: text}}, video...)
		return &dto.GeneralOpenAIRequest{
			Model:    "kimi-k3",
			Messages: []dto.Message{{Role: "user", Content: content}},
		}
	}
	keyOf := func(model string, request *dto.GeneralOpenAIRequest) string {
		_, key, ok := estimateRequestBody(model, request, DefaultVideoEstimateBaseURL)
		if !ok {
			t.Fatalf("request must be serializable")
		}
		return key
	}

	shortKey := keyOf("kimi-k3", withText("hi"))
	longKey := keyOf("kimi-k3", withText("a much longer prompt that costs thousands of tokens"))
	if shortKey == longKey {
		t.Fatal("different text must not share a cache key (it would bill the longer prompt the shorter total)")
	}

	// A different model prices differently too, so it must not reuse the entry.
	if keyOf("kimi-k3", withText("hi")) == keyOf("kimi-k2", withText("hi")) {
		t.Fatal("a different model must not share a cache key")
	}

	// The same payload must be stable, or the prefetch and settlement lookups
	// would never meet and every request would pay the endpoint call.
	if keyOf("kimi-k3", withText("hi")) != shortKey {
		t.Fatal("an identical payload must produce an identical key")
	}
	// Likewise, identical video with identical text must agree across separate
	// request objects (the prefetch and settlement build different instances).
	if keyOf("kimi-k3", withText("hi")) != keyOf("kimi-k3", withText("hi")) {
		t.Fatal("separate but identical requests must share a key")
	}

	// The endpoint is live configurable, so its identity is part of the key:
	// otherwise a replaced tokenizer would keep serving the old one's counts for
	// every payload already seen.
	_, otherEndpointKey, ok := estimateRequestBody("kimi-k3", withText("hi"), "https://other.test/v1")
	if !ok {
		t.Fatal("request must be serializable")
	}
	if otherEndpointKey == shortKey {
		t.Fatal("a different endpoint must not share a cache key")
	}
}

// TestLoadVideoEstimateConfigPrefersTheAdminSetting pins the precedence the
// console relies on: the stored setting wins, the environment is the bootstrap
// and the fallback, and clearing the setting hands control back to the
// environment rather than leaving the endpoint unconfigured.
func TestLoadVideoEstimateConfigPrefersTheAdminSetting(t *testing.T) {
	setting := operation_setting.GetVideoEstimateSetting()
	originalBase, originalKey := setting.BaseURL, setting.APIKey
	t.Cleanup(func() {
		setting.BaseURL, setting.APIKey = originalBase, originalKey
	})

	t.Setenv(VideoEstimateAPIKeyEnv, "env-key")
	t.Setenv(VideoEstimateBaseURLEnv, "https://env.test/v1")

	setting.BaseURL, setting.APIKey = "", ""
	cfg, ok := LoadVideoEstimateConfig()
	if !ok || cfg.APIKey != "env-key" || cfg.BaseURL != "https://env.test/v1" {
		t.Fatalf("with no stored setting the environment must be used, got %+v ok=%v", cfg, ok)
	}

	// A stored key alone must not lose the environment's base URL.
	setting.APIKey = "  stored-key  "
	cfg, ok = LoadVideoEstimateConfig()
	if !ok || cfg.APIKey != "stored-key" || cfg.BaseURL != "https://env.test/v1" {
		t.Fatalf("a stored key must be trimmed and keep the environment base URL, got %+v ok=%v", cfg, ok)
	}

	setting.BaseURL = "https://stored.test/v1/"
	cfg, ok = LoadVideoEstimateConfig()
	if !ok || cfg.APIKey != "stored-key" || cfg.BaseURL != "https://stored.test/v1" {
		t.Fatalf("the stored setting must win and be trimmed, got %+v ok=%v", cfg, ok)
	}

	// Each field falls back on its own, so a half-configured console still works:
	// clearing the stored key hands the credential back to the environment while
	// the stored base URL stays in effect.
	setting.APIKey = ""
	cfg, ok = LoadVideoEstimateConfig()
	if !ok || cfg.APIKey != "env-key" || cfg.BaseURL != "https://stored.test/v1" {
		t.Fatalf("clearing the stored key must fall back per field, got %+v ok=%v", cfg, ok)
	}
	setting.BaseURL = ""
	cfg, ok = LoadVideoEstimateConfig()
	if !ok || cfg.APIKey != "env-key" || cfg.BaseURL != "https://env.test/v1" {
		t.Fatalf("clearing both stored fields must return to the environment, got %+v ok=%v", cfg, ok)
	}
}

// TestSettlementLookupUsesTheOriginalModelId pins the call-site contract of the
// estimate cache. The prefetch runs in PrepareRequestBilling, which is before
// the handler runs ModelMappedHelper, so RelayInfo.UpstreamModelName is still
// empty there and may hold a mapped name later; the original model id is the
// only id both ends can key on. Without this, an answer that was fetched and
// cached can never be read back and the endpoint precision is silently lost.
func TestSettlementLookupUsesTheOriginalModelId(t *testing.T) {
	ResetVideoEstimateCacheForTest()
	// The settlement side resolves the endpoint itself, so the fixture has to
	// pin one: the lookup reads the cache only while an endpoint is configured,
	// and it keys on the resolved endpoint's URL.
	t.Setenv(VideoEstimateAPIKeyEnv, "test-key")
	t.Setenv(VideoEstimateBaseURLEnv, "https://estimate.test/v1")

	request := videoRequest()
	cfg, configured := LoadVideoEstimateConfig()
	if !configured {
		t.Fatal("the fixture must configure an endpoint")
	}
	_, cacheKey, ok := estimateRequestBody("kimi-k3", request, cfg.BaseURL)
	if !ok {
		t.Fatal("the fixture must be estimable")
	}
	storeVideoEstimate(cacheKey, 42416)

	prefetched := &relaycommon.RelayInfo{
		Request:         request,
		OriginModelName: "kimi-k3",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3-mapped"},
	}
	total, hit := videoPromptTotalForSettlement(prefetched)
	if !hit || total != 42416 {
		t.Fatalf("settlement must read the prefetched answer by the original model id: hit=%v total=%d", hit, total)
	}

	// The mapped name alone must not fabricate a hit: it is not the key the
	// prefetch wrote, so the caller has to fall back to the local price.
	mappedOnly := &relaycommon.RelayInfo{
		Request:     request,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3-mapped"},
	}
	if _, hit := videoPromptTotalForSettlement(mappedOnly); hit {
		t.Fatal("a mapped upstream name alone must not read someone else's cache entry")
	}
}
