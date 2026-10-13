package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// garbageCounts are the 2026-10-13 upstream values; no client may see them.
var garbageCounts = []string{"1388347719831046", "562949953421056", "1951297673252102", "322313980642816"}

// TestOpenaiHandlerZeroesGarbageUsageInClientBody pins the customer-visible
// contract for a non-stream completion: the body is forwarded verbatim, so the
// sanitizer has to rewrite the bytes — while billing keeps the upstream counts,
// because the settlement gate needs them to record the anomaly and zero the
// charge.
func TestOpenaiHandlerZeroesGarbageUsageInClientBody(t *testing.T) {
	body := `{"id":"chatcmpl_g1","object":"chat.completion","created":1710000000,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1388347719831046,"completion_tokens":562949953421056,"total_tokens":1951297673252102,"prompt_tokens_details":{"cached_tokens":322313980642816}}}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
	}

	usage, apiErr := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	})
	if apiErr != nil {
		t.Fatalf("OpenaiHandler: %v", apiErr)
	}

	client := recorder.Body.String()
	for _, raw := range garbageCounts {
		if strings.Contains(client, raw) {
			t.Errorf("client body still carries %s: %s", raw, client)
		}
	}
	if !strings.Contains(client, `"content":"ok"`) {
		t.Errorf("client body lost its content: %s", client)
	}
	var payload struct {
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal([]byte(client), &payload); err != nil {
		t.Fatalf("client body is not JSON: %v (%s)", err, client)
	}
	if got, ok := payload.Usage["prompt_tokens"].(float64); !ok || got != 0 {
		t.Errorf("client prompt_tokens = %v, want 0", payload.Usage["prompt_tokens"])
	}
	if usage == nil || usage.PromptTokens != 1388347719831046 {
		t.Fatalf("billing input was sanitized: %+v", usage)
	}
}

// TestOaiStreamHandlerZeroesGarbageUsageInClientStream is the streaming half of
// the same contract: the usage event the client receives must carry zeros, and
// must not smuggle the unmodelled upstream cache-write count.
func TestOaiStreamHandlerZeroesGarbageUsageInClientStream(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	streamBody := strings.Join([]string{
		`data: {"id":"chat_s1","object":"chat.completion.chunk","created":1710000000,"model":"test-model","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
		`data: {"id":"chat_s1","object":"chat.completion.chunk","created":1710000000,"model":"test-model","choices":[],"usage":{"prompt_tokens":1388347719831046,"completion_tokens":562949953421056,"total_tokens":1951297673252102,"prompt_cache_write_tokens":999999999999}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
		IsStream:           true,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		ClientIncludeUsage: true,
		DisablePing:        true,
	}

	usage, apiErr := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(streamBody)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})
	if apiErr != nil {
		t.Fatalf("OaiStreamHandler: %v", apiErr)
	}

	client := recorder.Body.String()
	for _, raw := range append(append([]string{}, garbageCounts...), "999999999999") {
		if strings.Contains(client, raw) {
			t.Errorf("client stream still carries %s: %s", raw, client)
		}
	}
	if !strings.Contains(client, `"content":"hello"`) {
		t.Errorf("client stream lost its content: %s", client)
	}
	if !strings.Contains(client, `"prompt_tokens":0`) {
		t.Errorf("client stream carries no zeroed usage event: %s", client)
	}
	if usage == nil || usage.PromptTokens != 1388347719831046 {
		t.Fatalf("billing input was sanitized: %+v", usage)
	}
}

// TestOpenaiHandlerKeepsPlausibleUsage is the regression guard for ordinary
// traffic: nothing about a healthy response may change.
func TestOpenaiHandlerKeepsPlausibleUsage(t *testing.T) {
	body := `{"id":"chatcmpl_ok","object":"chat.completion","created":1710000000,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":19610,"completion_tokens":200,"total_tokens":19810,"prompt_tokens_details":{"cached_tokens":9728}}}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
	}

	if _, apiErr := OpenaiHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}); apiErr != nil {
		t.Fatalf("OpenaiHandler: %v", apiErr)
	}

	client := recorder.Body.String()
	for _, want := range []string{`"prompt_tokens":19610`, `"completion_tokens":200`, `"cached_tokens":9728`} {
		if !strings.Contains(client, want) {
			t.Errorf("plausible client body is missing %s: %s", want, client)
		}
	}
}

// garbageStreamUsage mirrors the 2026-10-13 upstream answer: counts far beyond
// any real context, with an extra key this fork does not model.
func garbageStreamUsage() *dto.Usage {
	return &dto.Usage{
		PromptTokens:     1388347719831046,
		CompletionTokens: 562949953421056,
		TotalTokens:      1951297673252102,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 322313980642816,
		},
	}
}

// The client event must not keep any raw count, including the keys the usage
// shape does not model: a merge would forward those beside our zeros.
func TestPatchStreamUsageDataReplacesGarbageUsage(t *testing.T) {
	data := `{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":1388347719831046,"completion_tokens":562949953421056,"total_tokens":1951297673252102,"prompt_cache_write_tokens":999999999999,"billing_usage":{"source":"openai"}}}`
	patched, err := patchStreamUsageData(data, garbageStreamUsage())
	if err != nil {
		t.Fatalf("patchStreamUsageData: %v", err)
	}
	for _, raw := range []string{"1388347719831046", "562949953421056", "1951297673252102", "322313980642816", "999999999999"} {
		if bytes.Contains([]byte(patched), []byte(raw)) {
			t.Errorf("raw count %s reached the client event: %s", raw, patched)
		}
	}
	if bytes.Contains([]byte(patched), []byte("billing_usage")) {
		t.Errorf("internal billing extension reached the client event: %s", patched)
	}
	var payload struct {
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal([]byte(patched), &payload); err != nil {
		t.Fatalf("patched event is not JSON: %v (%s)", err, patched)
	}
	if got, ok := payload.Usage["prompt_tokens"].(float64); !ok || got != 0 {
		t.Errorf("prompt_tokens = %v, want 0", payload.Usage["prompt_tokens"])
	}
}

// Plausible usage keeps the merge behaviour: upstream keys the shape does not
// carry (here prompt_cache_write_tokens) are preserved.
func TestPatchStreamUsageDataKeepsPlausibleUpstreamKeys(t *testing.T) {
	data := `{"id":"chatcmpl-2","choices":[],"usage":{"prompt_tokens":19610,"completion_tokens":200,"prompt_cache_write_tokens":128}}`
	usage := &dto.Usage{
		PromptTokens:     19610,
		CompletionTokens: 200,
		TotalTokens:      19810,
	}
	patched, err := patchStreamUsageData(data, usage)
	if err != nil {
		t.Fatalf("patchStreamUsageData: %v", err)
	}
	if !bytes.Contains([]byte(patched), []byte(`"prompt_cache_write_tokens":128`)) {
		t.Errorf("upstream key dropped for a plausible usage: %s", patched)
	}
	if !bytes.Contains([]byte(patched), []byte(`"prompt_tokens":19610`)) {
		t.Errorf("plausible count rewritten: %s", patched)
	}
}

// The K3 official usage shape is rebuilt from the passed usage object plus the
// upstream prompt_cache_write_tokens; both must go to zero together.
func TestKimiK3UsageJSONZeroesGarbageUsageAndCacheWrite(t *testing.T) {
	encoded, err := kimiK3UsageJSON(garbageStreamUsage(), 999999999999)
	if err != nil {
		t.Fatalf("kimiK3UsageJSON: %v", err)
	}
	for _, raw := range []string{"1388347719831046", "562949953421056", "1951297673252102", "999999999999"} {
		if bytes.Contains(encoded, []byte(raw)) {
			t.Errorf("raw count %s reached the K3 usage view: %s", raw, encoded)
		}
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("K3 usage is not JSON: %v (%s)", err, encoded)
	}
	if got := payload["prompt_tokens"].(float64); got != 0 {
		t.Errorf("prompt_tokens = %v, want 0", got)
	}
	if !bytes.Contains(encoded, []byte(`"cache_write_tokens":0`)) {
		t.Errorf("cache_write_tokens not zeroed: %s", encoded)
	}
}
