package service

import (
	"fmt"
	"testing"

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

func TestEstimatePromptTokensViaEndpointReadsTotalTokens(t *testing.T) {
	t.Setenv(VideoEstimateAPIKeyEnv, "test-key")
	withEstimatePost(t, func(cfg VideoEstimateConfig, body []byte) (int, []byte, error) {
		if cfg.APIKey != "test-key" {
			t.Errorf("credential must reach the request, got %q", cfg.APIKey)
		}
		return 200, []byte(`{"code":0,"data":{"total_tokens":12789},"status":true}`), nil
	})
	total, ok, err := EstimatePromptTokensViaEndpoint("kimi-k3", []any{map[string]any{"role": "user"}})
	if err != nil || !ok {
		t.Fatalf("expected a usable total, got ok=%v err=%v", ok, err)
	}
	if total != 12789 {
		t.Fatalf("total: got %d, want 12789", total)
	}
}

func TestEstimatePromptTokensViaEndpointReportsFallbackForBadAnswers(t *testing.T) {
	t.Setenv(VideoEstimateAPIKeyEnv, "test-key")
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
			total, ok, err := EstimatePromptTokensViaEndpoint("kimi-k3", []any{map[string]any{"role": "user"}})
			if ok {
				t.Fatalf("a %s must not report a usable total (got %d)", tc.name, total)
			}
			if err == nil {
				t.Fatalf("a %s must surface an error for logging", tc.name)
			}
		})
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
	for _, ok := range []string{"", "estimate", "  estimate  "} {
		settings := &dto.ChannelOtherSettings{VideoUsageMode: ok}
		if err := settings.ValidateVideoUsageMode(); err != nil {
			t.Errorf("mode %q must validate: %v", ok, err)
		}
	}
	if !(&dto.ChannelOtherSettings{VideoUsageMode: "estimate"}).EstimatesVideoUsage() {
		t.Fatal("estimate must enable the feature")
	}
	if (&dto.ChannelOtherSettings{}).EstimatesVideoUsage() {
		t.Fatal("empty must leave the upstream values untouched")
	}
	for _, bad := range []string{"true", "estiamte", "on"} {
		settings := &dto.ChannelOtherSettings{VideoUsageMode: bad}
		if err := settings.ValidateVideoUsageMode(); err == nil {
			t.Errorf("mode %q must be rejected at save time", bad)
		}
	}
}
