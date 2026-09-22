package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/bytedance/gopkg/util/gopool"
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
	// VideoEstimateBaseURLEnv / VideoEstimateAPIKeyEnv are the deployment-level
	// configuration. The administrator setting (video_estimate_setting.*) takes
	// precedence when it is set, so an operator can rotate the key from the
	// console without a release; these remain as the bootstrap and as the way to
	// configure a fleet that never opens that page.
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

// LoadVideoEstimateConfig resolves the endpoint, preferring the administrator
// setting over the environment. ok is false when no key is configured in either
// place, which is the default state and means "use the local model".
func LoadVideoEstimateConfig() (VideoEstimateConfig, bool) {
	key := operation_setting.TrimmedVideoEstimateAPIKey()
	if key == "" {
		key = strings.TrimSpace(os.Getenv(VideoEstimateAPIKeyEnv))
	}
	if key == "" {
		return VideoEstimateConfig{}, false
	}
	base := operation_setting.TrimmedVideoEstimateBaseURL()
	if base == "" {
		base = strings.TrimSpace(os.Getenv(VideoEstimateBaseURLEnv))
	}
	if base == "" {
		base = DefaultVideoEstimateBaseURL
	}
	return VideoEstimateConfig{BaseURL: strings.TrimRight(base, "/"), APIKey: key}, true
}

// PrefetchVideoPromptTotal asks the configured tokenizer endpoint for this
// request's full prompt count (text and media together) and caches the answer.
// It returns immediately: the call carries the whole clip, so it runs alongside
// the upstream request and settlement reads the cache entry once it is warm.
// Nothing here fails a request — an endpoint that is unconfigured, refuses the
// request or times out simply leaves the cache cold, and settlement bills the
// locally priced video part instead.
func PrefetchVideoPromptTotal(model string, request dto.Request) {
	cfg, configured := LoadVideoEstimateConfig()
	if !configured {
		return
	}
	body, cacheKey, ok := estimateRequestBody(model, request, cfg.BaseURL)
	if !ok {
		return
	}
	if _, hit := lookupVideoEstimate(cacheKey); hit {
		return
	}
	gopool.Go(func() {
		total, ok, err := estimateTotalViaEndpoint(cfg, body)
		if err != nil || !ok {
			return
		}
		storeVideoEstimate(cacheKey, total)
	})
}

// VideoPromptTotalForRequest returns the cached endpoint total for this
// request, if the prefetch has already answered. It never performs I/O, so the
// settlement path can call it without delaying the client. model must be the
// same upstream model id the prefetch used, because the key covers the whole
// priced payload; a different id misses and falls back to the local price.
func VideoPromptTotalForRequest(model string, request dto.Request) (int, bool) {
	cfg, configured := LoadVideoEstimateConfig()
	if !configured {
		// Unconfigured between the prefetch and settlement: fall back to the
		// locally priced part rather than serving an answer the operator has
		// just turned off.
		return 0, false
	}
	_, cacheKey, ok := estimateRequestBody(model, request, cfg.BaseURL)
	if !ok {
		return 0, false
	}
	return lookupVideoEstimate(cacheKey)
}

// estimateRequestBody serializes what the endpoint should price, plus the cache
// key. Serializing here (rather than inside the prefetch goroutine) keeps the
// request DTO on one goroutine: converters mutate it while the upstream request
// is in flight.
//
// The key covers the whole payload — model, text and media — because that is
// what the endpoint prices and the answer replaces the reported prompt count.
// Keying on the video alone would hand a longer prompt the shorter one's total,
// silently under-billing every reuse of the same clip.
func estimateRequestBody(model string, request dto.Request, endpoint string) ([]byte, string, bool) {
	messages, _, ok := estimateMessagesFor(request)
	if !ok {
		return nil, "", false
	}
	if model == "" {
		return nil, "", false
	}
	body, err := common.Marshal(videoEstimateRequest{Model: model, Messages: messages})
	if err != nil {
		return nil, "", false
	}
	return body, estimateCacheKey(endpoint, body), true
}

// estimateCacheKey derives the cache key from the exact bytes sent to the
// endpoint, so an equal key means an equal request and therefore an equal
// answer. estimateMessagesFor's video hash is deliberately not used for this:
// it identifies the media, not the priced payload.
//
// The endpoint is part of the key because the answer is that tokenizer's count
// of this payload. The endpoint is live configurable, so without this a
// corrected or replaced tokenizer would keep serving the previous one's numbers
// for every payload already seen, until the process restarted.
func estimateCacheKey(endpoint string, body []byte) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, endpoint)
	_, _ = hash.Write(body)
	return hex.EncodeToString(hash.Sum(nil))[:32]
}

// estimateMessagesFor extracts the message array an estimate endpoint expects
// from a request, plus a key identifying its video content. Requests that are
// not chat shapes, or that carry no video, report ok=false so the caller falls
// back without an outbound call.
func estimateMessagesFor(request dto.Request) (any, string, bool) {
	chat, ok := request.(*dto.GeneralOpenAIRequest)
	if !ok || chat == nil || len(chat.Messages) == 0 {
		return nil, "", false
	}
	messages := chat.Messages

	hash := sha256.New()
	sawVideo := false
	for _, message := range messages {
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
	return messages, hex.EncodeToString(hash.Sum(nil))[:32], true
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
	Data *struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"data"`
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

// estimateTotalViaEndpoint posts an already-serialized estimate body and reads
// the total back. Split out so the prefetch goroutine touches no request state.
func estimateTotalViaEndpoint(cfg VideoEstimateConfig, body []byte) (int, bool, error) {
	status, payload, err := videoEstimatePost(cfg, body)
	if err != nil {
		return 0, false, fmt.Errorf("estimate request failed: %w", err)
	}
	var parsed videoEstimateResponse
	if err := common.Unmarshal(payload, &parsed); err != nil {
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
