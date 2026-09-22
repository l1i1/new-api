package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// TestRequestBytesCarryVideo pins the body-level detection that channel
// selection runs on: it must find a real video part, and must not be fooled by
// text that merely mentions the field names, which would otherwise route an
// ordinary prompt away from every channel that cannot read video.
func TestRequestBytesCarryVideo(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "video part with url",
			body: `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"text","text":"what is this?"},{"type":"video_url","video_url":{"url":"ms://file-abc"}}]}]}`,
			want: true,
		},
		{
			// A part that names the type but carries no url has no media for the
			// upstream to read, so it is not a video request. This matches the
			// DTO-level check, which requires a resolvable source.
			name: "video part without a url carries no media",
			body: `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"video_url"}]}]}`,
			want: false,
		},
		{
			name: "text request",
			body: `{"model":"kimi-k3","messages":[{"role":"user","content":"1+1=?"}]}`,
			want: false,
		},
		{
			name: "image request",
			body: `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}]}]}`,
			want: false,
		},
		{
			// A prompt that talks about the API must stay an ordinary request.
			name: "text that mentions video_url",
			body: `{"model":"kimi-k3","messages":[{"role":"user","content":"how do I pass a video_url part and what does video_url accept?"}]}`,
			want: false,
		},
		{
			name: "text content that mentions the part in a nested string",
			body: `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"text","text":"{\"type\":\"video_url\",\"video_url\":{\"url\":\"x\"}}"}]}]}`,
			want: false,
		},
		{name: "no messages", body: `{"model":"kimi-k3","input":"hi"}`, want: false},
		{name: "empty body", body: ``, want: false},
		{name: "invalid json", body: `{"model":`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequestBytesCarryVideo([]byte(tc.body)); got != tc.want {
				t.Fatalf("RequestBytesCarryVideo = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestVideoDetectorsAgree pins the two halves of the feature together: routing
// decides from the raw body while pricing and settlement decide from the parsed
// request. A body that routing calls a video request must also produce a video
// file for pricing, or the request would be routed to a video-capable channel
// and then billed as text — and the reverse would send media to a blind one.
func TestVideoDetectorsAgree(t *testing.T) {
	// Each case pairs the body with the parsed request pricing would see, so a
	// second protocol cannot be added to the detector without a body to check it
	// against. The Gemini shape is here because it was missing from the routing
	// side: pricing already recognised it, so the two halves disagreed and a
	// Gemini video request was routed with no capability narrowing at all.
	chatBodies := []string{
		`{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"video_url","video_url":{"url":"ms://abc"}}]}]}`,
		`{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"video_url"}]}]}`,
		`{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://e.test/a.png"}}]}]}`,
		`{"model":"kimi-k3","messages":[{"role":"user","content":"plain text"}]}`,
	}
	for _, body := range chatBodies {
		var request dto.GeneralOpenAIRequest
		if err := common.Unmarshal([]byte(body), &request); err != nil {
			t.Fatalf("unmarshal %s: %v", body, err)
		}
		fromBody := RequestBytesCarryVideo([]byte(body))
		sawVideo := false
		for _, file := range request.GetTokenCountMeta().Files {
			if file != nil && file.FileType == types.FileTypeVideo {
				sawVideo = true
			}
		}
		if fromBody != sawVideo {
			t.Errorf("detectors disagree on %s: routing=%v pricing=%v", body, fromBody, sawVideo)
		}
	}

}

// TestCorrectedVideoBillingUsageUsesServingChannelSettings is the settlement
// contract: the correction applies because the channel that actually served the
// request carries the mode, and does not apply for a channel without it — the
// two must not be confused when a retry switched channels.
func TestCorrectedVideoBillingUsageUsesServingChannelSettings(t *testing.T) {
	const localVideoTokens = 42320

	servingEstimate := &relaycommon.RelayInfo{
		Request: &dto.GeneralOpenAIRequest{Model: "kimi-k3"},
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelOtherSettings: dto.ChannelOtherSettings{
				SupportsVideo:  true,
				VideoUsageMode: dto.VideoUsageModeEstimate,
			},
		},
	}
	servingEstimate.SetVideoTokens(localVideoTokens)

	// A media-blind upstream report (27 tokens against a 42k-token clip) is
	// replaced by the local price plus the text the upstream counted correctly.
	usage := &dto.Usage{PromptTokens: 27, CompletionTokens: 12, TotalTokens: 39}
	corrected, ok := correctedVideoBillingUsage(servingEstimate, usage)
	if !ok {
		t.Fatal("a serving channel with the mode must be corrected")
	}
	if corrected.PromptTokens != 27+localVideoTokens {
		t.Fatalf("prompt tokens: got %d, want %d", corrected.PromptTokens, 27+localVideoTokens)
	}
	if corrected.TotalTokens != corrected.PromptTokens+12 {
		t.Fatalf("total must follow the corrected prompt: got %d", corrected.TotalTokens)
	}
	if !corrected.BillingUsage.Estimated {
		t.Fatal("the correction must be marked estimated")
	}
	// The caller's own usage object must keep the upstream number for audit.
	if usage.PromptTokens != 27 {
		t.Fatalf("the reported usage must not be mutated: got %d", usage.PromptTokens)
	}

	// A channel that did not declare the mode leaves the report alone, even
	// though it served a request that carried video.
	servingTrusted := &relaycommon.RelayInfo{
		Request: &dto.GeneralOpenAIRequest{Model: "kimi-k3"},
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelOtherSettings: dto.ChannelOtherSettings{SupportsVideo: true},
		},
	}
	servingTrusted.SetVideoTokens(localVideoTokens)
	if _, ok := correctedVideoBillingUsage(servingTrusted, usage); ok {
		t.Fatal("a channel without the mode must keep the upstream count")
	}

	// An upstream (or endpoint) that did count the media keeps its own number.
	counting := &relaycommon.RelayInfo{
		Request: &dto.GeneralOpenAIRequest{Model: "kimi-k3"},
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelOtherSettings: dto.ChannelOtherSettings{
				SupportsVideo:  true,
				VideoUsageMode: dto.VideoUsageModeEstimate,
			},
		},
	}
	counting.SetVideoTokens(localVideoTokens)
	mediaAware := &dto.Usage{PromptTokens: 42416, CompletionTokens: 5, TotalTokens: 42421}
	if _, ok := correctedVideoBillingUsage(counting, mediaAware); ok {
		t.Fatal("a media-aware report must not be corrected (no double charge)")
	}
}

// TestCorrectedVideoBillingUsageWithoutChannelMeta pins the crash that this
// path must never repeat: the gate reads channel settings through the embedded
// ChannelMeta pointer, so a nil must be a no-op rather than a panic. (The
// pre-existing implementation dereferenced it before any handler built it.)
func TestCorrectedVideoBillingUsageWithoutChannelMeta(t *testing.T) {
	info := &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{Model: "kimi-k3"}}
	info.SetVideoTokens(42320)
	if _, ok := correctedVideoBillingUsage(info, &dto.Usage{PromptTokens: 27}); ok {
		t.Fatal("no channel metadata must mean no correction")
	}
	if _, ok := correctedVideoBillingUsage(nil, &dto.Usage{PromptTokens: 27}); ok {
		t.Fatal("a nil relay info must mean no correction")
	}
}

// TestEndpointTotalIsBilledWithoutALocalPrice is the regression for a real
// under-billing: the local container model reads mp4/mov only, and the
// endpoint call used to be gated on it having produced a price. A webm clip
// therefore bypassed the one source that could price it and was billed the
// upstream's fabricated text-only count (measured in production: 26 tokens for
// a 4 s clip the endpoint priced at 1027). The endpoint answer must be billed
// on its own, without a local yardstick.
func TestEndpointTotalIsBilledWithoutALocalPrice(t *testing.T) {
	withEstimatePost(t, func(VideoEstimateConfig, []byte) (int, []byte, error) {
		return 200, []byte(`{"data":{"total_tokens":1027}}`), nil
	})
	t.Setenv(VideoEstimateAPIKeyEnv, "test-key")

	request := videoRequest()
	// The local model could not read this container, so no local price exists
	// for the video part.
	info := &relaycommon.RelayInfo{
		Request: request,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelOtherSettings: dto.ChannelOtherSettings{
			SupportsVideo:  true,
			VideoUsageMode: dto.VideoUsageModeEstimate,
		}},
		OriginModelName: "kimi-k3",
	}
	info.SetVideoTokens(0)

	// Warm the cache the way the prefetch would, then settle.
	PrefetchVideoPromptTotal("kimi-k3", request)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, hit := VideoPromptTotalForRequest("kimi-k3", request); hit {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the endpoint answer never reached the cache")
		}
		time.Sleep(10 * time.Millisecond)
	}

	corrected, applied := correctedVideoBillingUsage(info, &dto.Usage{PromptTokens: 26, CompletionTokens: 4})
	if !applied {
		t.Fatal("an endpoint total must be billed even without a local price")
	}
	if corrected.PromptTokens != 1027 {
		t.Fatalf("prompt tokens: got %d, want the endpoint's 1027", corrected.PromptTokens)
	}
	if !corrected.BillingUsage.Estimated {
		t.Fatal("the correction must stay flagged as estimated")
	}

	// A media-blind endpoint answer (no better than the report) must not be
	// accepted on that weakened branch: it would replace one text-only count
	// with another.
	if videoPromptTotalSawMedia(26, 0, 26) {
		t.Fatal("an endpoint total equal to the report must not supersede it")
	}
	if videoPromptTotalSawMedia(0, 0, 26) {
		t.Fatal("a zero endpoint total must never supersede the report")
	}
}
