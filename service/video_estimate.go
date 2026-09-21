package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// Official token estimation for video billing.
//
// A channel marked ChannelOtherSettings.VideoUsageMode == "estimate" reports a
// prompt count that ignores its video payload (measured: a 3.3 MB clip billed as
// 27 prompt tokens while the upstream charged for the real video tokens). The
// local container model in video_token.go prices such requests offline; this
// client prefers the provider's own tokenizer when an operator has configured
// one, because that answer is authoritative (measured within 0.08 % of the real
// prompt count) and costs nothing to call.
//
// It is deliberately opt-in and inert by default: the endpoint needs a key with
// a direct relationship to the tokenizer's owner, and sending a user's video to
// a second supplier for estimation is a data-flow decision an operator must
// make explicitly. Unconfigured or on any failure the caller keeps the local
// estimate, so billing never silently falls back to the untrustworthy number.

const (
	// VideoEstimateBaseURLEnv / VideoEstimateAPIKeyEnv configure the endpoint.
	// The key is read from the environment so it never lands in the database or
	// in a tracked file.
	VideoEstimateBaseURLEnv = "VIDEO_ESTIMATE_BASE_URL"
	VideoEstimateAPIKeyEnv  = "VIDEO_ESTIMATE_API_KEY"
	// DefaultVideoEstimateBaseURL is Moonshot's documented estimate endpoint,
	// the only implementation observed to accept a video part.
	DefaultVideoEstimateBaseURL = "https://api.moonshot.cn/v1"

	videoEstimateTimeout = 20 * time.Second
)

// VideoEstimateConfig is a resolved endpoint plus credential.
type VideoEstimateConfig struct {
	BaseURL string
	APIKey  string
}

// LoadVideoEstimateConfig reads the endpoint from the environment. ok is false
// when no key is configured, which is the default state and means "use the
// local model".
func LoadVideoEstimateConfig() (VideoEstimateConfig, bool) {
	key := strings.TrimSpace(os.Getenv(VideoEstimateAPIKeyEnv))
	if key == "" {
		return VideoEstimateConfig{}, false
	}
	base := strings.TrimSpace(os.Getenv(VideoEstimateBaseURLEnv))
	if base == "" {
		base = DefaultVideoEstimateBaseURL
	}
	return VideoEstimateConfig{BaseURL: strings.TrimRight(base, "/"), APIKey: key}, true
}

// VideoPromptTotalFromEndpoint asks the configured tokenizer endpoint for this
// request's full prompt count (text and media together). It returns ok=false —
// never an error to the caller — when nothing is configured, the request cannot
// be re-serialized, the endpoint fails, or the answer arrives after the
// timeout, because the caller always has the local container model to fall back
// on.
//
// Every answer is cached by the request's video content, so a repeated clip
// costs one call. The cache exists because a call carries the whole video: the
// measured latency was ~2.8 s for a 3.3 MB clip, and billing should not pay
// that per request.
func VideoPromptTotalFromEndpoint(model string, request dto.Request) (int, bool) {
	messages, cacheKey, ok := estimateMessagesFor(request)
	if !ok {
		return 0, false
	}
	if cached, hit := lookupVideoEstimate(cacheKey); hit {
		return cached, true
	}
	total, ok, err := EstimatePromptTokensViaEndpoint(model, messages)
	if err != nil || !ok {
		return 0, false
	}
	storeVideoEstimate(cacheKey, total)
	return total, true
}

// estimateMessagesFor extracts the message array an estimate endpoint expects
// from a chat-completions request, plus a cache key derived from the video
// bytes. Requests that are not chat shapes, or that carry no video, report
// ok=false so the caller falls back without an outbound call.
func estimateMessagesFor(request dto.Request) (any, string, bool) {
	chat, ok := request.(*dto.GeneralOpenAIRequest)
	if !ok || chat == nil || len(chat.Messages) == 0 {
		return nil, "", false
	}
	hash := sha256.New()
	sawVideo := false
	for _, message := range chat.Messages {
		if message.Content == nil {
			continue
		}
		for _, part := range message.ParseContent() {
			if part.Type != dto.ContentTypeVideoUrl {
				continue
			}
			source := part.ToFileSource()
			if source == nil {
				continue
			}
			sawVideo = true
			_, _ = io.WriteString(hash, source.GetIdentifier())
		}
	}
	if !sawVideo {
		return nil, "", false
	}
	// The message array goes out as the client sent it, so the endpoint prices
	// exactly what the upstream was asked to read.
	return chat.Messages, hex.EncodeToString(hash.Sum(nil))[:32], true
}

// videoEstimateCache holds endpoint answers keyed by video content. It is
// bounded so a stream of distinct clips cannot grow it without limit.
const videoEstimateCacheLimit = 512

var (
	videoEstimateMu    sync.Mutex
	videoEstimateCache = make(map[string]int)
)

func lookupVideoEstimate(key string) (int, bool) {
	if key == "" {
		return 0, false
	}
	videoEstimateMu.Lock()
	defer videoEstimateMu.Unlock()
	total, ok := videoEstimateCache[key]
	return total, ok
}

func storeVideoEstimate(key string, total int) {
	if key == "" || total <= 0 {
		return
	}
	videoEstimateMu.Lock()
	defer videoEstimateMu.Unlock()
	if len(videoEstimateCache) >= videoEstimateCacheLimit {
		// Drop the whole table rather than track recency: the entries are cheap
		// to recompute and a plain reset keeps the lock hold constant.
		videoEstimateCache = make(map[string]int)
	}
	videoEstimateCache[key] = total
}

// ResetVideoEstimateCacheForTest clears the cache so tests are independent.
func ResetVideoEstimateCacheForTest() {
	videoEstimateMu.Lock()
	defer videoEstimateMu.Unlock()
	videoEstimateCache = make(map[string]int)
}

// videoEstimateRequest is the documented body: the model plus the message array
// (which may contain a video_url part).
type videoEstimateRequest struct {
	Model    string `json:"model"`
	Messages any    `json:"messages"`
}

type videoEstimateResponse struct {
	Data  *struct{ TotalTokens int `json:"total_tokens"` } `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// videoEstimatePost is the seam tests replace; production always posts.
var videoEstimatePost = postVideoEstimate

func postVideoEstimate(cfg VideoEstimateConfig, body []byte) (int, []byte, error) {
	client := &http.Client{Timeout: videoEstimateTimeout}
	request, err := http.NewRequest(http.MethodPost, cfg.BaseURL+"/tokenizers/estimate-token-count", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	// Bound the read: an estimation response is a few hundred bytes, and a
	// misconfigured base URL must not stream an unbounded body into memory.
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	if err != nil {
		return response.StatusCode, nil, err
	}
	return response.StatusCode, payload, nil
}

// EstimatePromptTokensViaEndpoint asks the configured endpoint for this
// request's prompt count. It returns ok=false (with no error) when nothing is
// configured, so callers treat "not set up" as a normal fallback rather than a
// failure.
func EstimatePromptTokensViaEndpoint(model string, messages any) (int, bool, error) {
	cfg, configured := LoadVideoEstimateConfig()
	if !configured {
		return 0, false, nil
	}
	if model == "" || messages == nil {
		return 0, false, nil
	}
	body, err := json.Marshal(videoEstimateRequest{Model: model, Messages: messages})
	if err != nil {
		return 0, false, fmt.Errorf("marshal estimate request: %w", err)
	}
	status, payload, err := videoEstimatePost(cfg, body)
	if err != nil {
		return 0, false, fmt.Errorf("estimate request failed: %w", err)
	}
	var parsed videoEstimateResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return 0, false, fmt.Errorf("estimate response is not JSON (status %d)", status)
	}
	if status != http.StatusOK || parsed.Data == nil {
		message := "no data"
		if parsed.Error != nil && parsed.Error.Message != "" {
			message = parsed.Error.Message
		}
		return 0, false, fmt.Errorf("estimate endpoint rejected the request (status %d): %s", status, message)
	}
	total := parsed.Data.TotalTokens
	if total <= 0 {
		return 0, false, fmt.Errorf("estimate endpoint returned %d tokens", total)
	}
	return total, true, nil
}
