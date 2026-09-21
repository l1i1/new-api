package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
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

	body, cacheKey, ok := estimateRequestBody("kimi-k3", videoRequest())
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
	if _, _, ok := estimateRequestBody("kimi-k3", &dto.GeneralOpenAIRequest{Model: "kimi-k3"}); ok {
		t.Fatal("a request without video must not be serialized for the endpoint")
	}
	if _, _, ok := estimateRequestBody("", videoRequest()); ok {
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
		_, key, ok := estimateRequestBody(model, request)
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
}
