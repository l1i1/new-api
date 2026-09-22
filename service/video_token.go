package service

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/tidwall/gjson"
)

// Kimi vision-family video token model.
//
// Calibrated 2026-09-21 against api.moonshot.cn with fifteen ffmpeg variants of
// one clip (see docs/cdp/mainland-k3-video-capability-20260921.md §5). The
// official endpoint answers /v1/tokenizers/estimate-token-count exactly; this
// local model is the offline fallback and is accurate to ±0.5 % on mid and high
// resolutions, -4..-8.5 % at the smallest (640x346) size.
//
// The rules the calibration pinned, all three counter-intuitive:
//   - the source frame rate is ignored; duration is always priced at 30 fps
//     (a 15 fps source is not discounted, a 60 fps source is not surcharged);
//   - there is a 15-frame floor (a one-frame clip still bills 15 frames);
//   - the total is capped at videoTokenCap tokens, which the native-resolution
//     samples reach at roughly 5.5 s.
const (
	videoFramesPerSecond = 30
	videoMinFrames       = 15
	videoTokenCap        = 42320
	// videoPerFrameTokenCap bounds the per-frame cost at roughly 3.2 MP; the
	// calibration cannot separate this from the total cap (every sample above
	// that size was already total-capped), so it only makes extrapolation
	// conservative.
	videoPerFrameTokenCap = 282
	// videoPixelsPerToken is the linear term: roughly 87.5 tokens per megapixel.
	videoPixelsPerToken = 11400
)

// VideoMetadata is what the token model needs from a container.
type VideoMetadata struct {
	Width           int
	Height          int
	DurationSeconds float64
}

// valid reports whether the metadata is usable for pricing.
func (m VideoMetadata) valid() bool {
	return m.Width > 0 && m.Height > 0 && m.DurationSeconds > 0 &&
		!math.IsNaN(m.DurationSeconds) && !math.IsInf(m.DurationSeconds, 0)
}

// EstimateVideoTokens applies the calibrated model to one decoded video.
func EstimateVideoTokens(meta VideoMetadata) int {
	if !meta.valid() {
		return 0
	}
	// Bound the float-to-int conversion before converting. A huge finite
	// duration can still exceed the platform int range even though the final
	// model is capped.
	frames := videoTokenCap
	maxUncappedDuration := float64(videoTokenCap) / float64(videoFramesPerSecond)
	if meta.DurationSeconds < maxUncappedDuration {
		frames = int(meta.DurationSeconds * videoFramesPerSecond)
	}
	if frames < videoMinFrames {
		frames = videoMinFrames
	}
	// Avoid width*height overflow by proving the product is below the only
	// threshold that matters before multiplying it.
	perFrame := videoPerFrameTokenCap
	maxPixelsAtCap := videoPixelsPerToken * videoPerFrameTokenCap
	if meta.Width <= maxPixelsAtCap/meta.Height {
		perFrame = meta.Width * meta.Height / videoPixelsPerToken
	}
	if perFrame <= 0 {
		// Sub-pixel-per-token sizes would price as zero; bill the floor instead
		// so a tiny clip is never free.
		perFrame = 1
	}
	tokens := frames * perFrame
	if tokens > videoTokenCap {
		tokens = videoTokenCap
	}
	return tokens
}

// ---------------------------------------------------------------------------
// Minimal ISO base media file format reader (mp4/mov/m4v)
// ---------------------------------------------------------------------------

// isoBmffBoxHeader is one box's size and type.
type isoBmffBox struct {
	Type string
	// Payload is the box body, excluding the size and type fields.
	Payload []byte
}

// readBoxes walks the boxes in data. It stops at the first malformed box and
// returns what it read, so a truncated container degrades instead of failing.
func readBoxes(data []byte) []isoBmffBox {
	var boxes []isoBmffBox
	for offset := 0; offset+8 <= len(data); {
		size := int64(binary.BigEndian.Uint32(data[offset:]))
		boxType := string(data[offset+4 : offset+8])
		headerSize := int64(8)
		if size == 1 {
			if offset+16 > len(data) {
				break
			}
			size = int64(binary.BigEndian.Uint64(data[offset+8:]))
			headerSize = 16
		} else if size == 0 {
			size = int64(len(data) - offset)
		}
		if size < headerSize || size > int64(len(data)-offset) {
			break
		}
		boxes = append(boxes, isoBmffBox{
			Type:    boxType,
			Payload: data[offset+int(headerSize) : offset+int(size)],
		})
		offset += int(size)
	}
	return boxes
}

func findBox(boxes []isoBmffBox, boxType string) (isoBmffBox, bool) {
	for _, box := range boxes {
		if box.Type == boxType {
			return box, true
		}
	}
	return isoBmffBox{}, false
}

// parseMvhd reads the movie header's timescale and duration.
func parseMvhd(payload []byte) (timescale uint32, duration uint64, ok bool) {
	if len(payload) < 4 {
		return 0, 0, false
	}
	switch payload[0] {
	case 0:
		if len(payload) < 20 {
			return 0, 0, false
		}
		timescale = binary.BigEndian.Uint32(payload[12:])
		duration = uint64(binary.BigEndian.Uint32(payload[16:]))
	case 1:
		if len(payload) < 32 {
			return 0, 0, false
		}
		timescale = binary.BigEndian.Uint32(payload[20:])
		duration = binary.BigEndian.Uint64(payload[24:])
	default:
		return 0, 0, false
	}
	return timescale, duration, timescale > 0
}

// parseTkhd reads the track header's display dimensions (16.16 fixed point).
func parseTkhd(payload []byte) (width, height int, ok bool) {
	if len(payload) < 4 {
		return 0, 0, false
	}
	// Both versions place width and height last, after a 36-byte matrix.
	var widthOffset int
	switch payload[0] {
	case 0:
		widthOffset = 76
	case 1:
		widthOffset = 88
	default:
		return 0, 0, false
	}
	if len(payload) < widthOffset+8 {
		return 0, 0, false
	}
	width = int(binary.BigEndian.Uint32(payload[widthOffset:]) >> 16)
	height = int(binary.BigEndian.Uint32(payload[widthOffset+4:]) >> 16)
	return width, height, width > 0 && height > 0
}

// DecodeVideoMetadata extracts the dimensions and duration from an ISO base
// media container. It reads the movie header for duration and the first track
// header with dimensions, which covers mp4/mov as produced by every browser,
// ffmpeg and phone recorder. Unknown containers return ok=false and the caller
// falls back.
func DecodeVideoMetadata(data []byte) (VideoMetadata, bool) {
	boxes := readBoxes(data)
	moov, found := findBox(boxes, "moov")
	if !found {
		return VideoMetadata{}, false
	}

	var meta VideoMetadata
	moovBoxes := readBoxes(moov.Payload)
	if mvhd, ok := findBox(moovBoxes, "mvhd"); ok {
		if timescale, duration, ok := parseMvhd(mvhd.Payload); ok {
			meta.DurationSeconds = float64(duration) / float64(timescale)
		}
	}
	for _, box := range moovBoxes {
		if box.Type != "trak" {
			continue
		}
		trakBoxes := readBoxes(box.Payload)
		tkhd, ok := findBox(trakBoxes, "tkhd")
		if !ok {
			continue
		}
		width, height, ok := parseTkhd(tkhd.Payload)
		if !ok {
			continue
		}
		meta.Width, meta.Height = width, height
		break
	}
	if !meta.valid() {
		return VideoMetadata{}, false
	}
	return meta, true
}

// CountVideoTokensForMeta sums the locally priced video parts of one request.
// It is separate from CountRequestToken because settlement needs the video
// contribution on its own: the upstream still prices text correctly and only
// the media part goes missing, so the corrected prompt count is
// upstreamPrompt + this value. A file that cannot be priced is skipped (the
// caller keeps the upstream number) rather than failing the request.
func CountVideoTokensForMeta(meta *types.TokenCountMeta) int {
	if meta == nil {
		return 0
	}
	// Ensure the media files were materialized and typed; GetTokenCountMeta
	// already tags video parts, so only URL-sourced entries can be untyped here.
	for _, file := range meta.Files {
		if file == nil || file.Source == nil {
			continue
		}
		if file.FileType == types.FileTypeVideo {
			continue
		}
		if file.Source.IsURL() {
			if cached, err := LoadFileSource(nil, file.Source, "video_token_meta"); err == nil {
				detected := DetectFileType(cached.MimeType)
				if detected == types.FileTypeVideo {
					file.FileType = detected
				}
			}
		}
	}
	total := 0
	for _, file := range meta.Files {
		if file == nil || file.FileType != types.FileTypeVideo || file.Source == nil {
			continue
		}
		if token, err := CountVideoToken(file.Source); err == nil && token > 0 {
			total = saturatingTokenTotal(total, token)
		}
	}
	return total
}

// RequestBytesCarryVideo reports whether a raw JSON request body contains a
// video part. Channel selection runs ahead of relay validation, so the routing
// decision is made from the body rather than from a parsed request.
//
// The scan looks for the content-part object that carries a video_url, rather
// than for the bare string anywhere in the payload: a prompt that merely
// mentions "video_url" must not be treated as a video request, or it would be
// routed away from every channel that cannot read video. A part that names the
// type without a url carries no media and is not a video request, matching what
// the parser yields for pricing and settlement; the Gemini shape is matched on
// the same terms, against the mime type the DTO prices it by.
func RequestBytesCarryVideo(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	return openAIBodyCarriesVideo(body) || geminiBodyCarriesVideo(body)
}

// openAIBodyCarriesVideo matches the chat-completions shape.
func openAIBodyCarriesVideo(body []byte) bool {
	result := gjson.GetBytes(body, "messages")
	if !result.IsArray() {
		return false
	}
	for _, message := range result.Array() {
		content := message.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, part := range content.Array() {
			if part.Get("type").String() != dto.ContentTypeVideoUrl {
				continue
			}
			if part.Get("video_url").Exists() {
				return true
			}
		}
	}
	return false
}

// geminiBodyCarriesVideo matches the Gemini-native shape, where media arrives
// as an inline part typed by its mime string rather than as a named content
// part. Without this, a Gemini video request was routed with no capability
// narrowing at all — measured: it was offered to a channel that silently drops
// video before landing on one that reads it — and its video part was never
// priced, so an estimate-mode channel billed the upstream's text-only count.
//
// Both spellings the DTO accepts are accepted here, and the part must carry
// data: a mime type alone describes nothing to price.
func geminiBodyCarriesVideo(body []byte) bool {
	contents := gjson.GetBytes(body, "contents")
	if !contents.IsArray() {
		return false
	}
	for _, content := range contents.Array() {
		parts := content.Get("parts")
		if !parts.IsArray() {
			continue
		}
		for _, part := range parts.Array() {
			inline := part.Get("inlineData")
			if !inline.Exists() {
				inline = part.Get("inline_data")
			}
			if !inline.Exists() {
				continue
			}
			mime := inline.Get("mimeType").String()
			if mime == "" {
				mime = inline.Get("mime_type").String()
			}
			if !strings.HasPrefix(mime, "video/") {
				continue
			}
			if inline.Get("data").String() != "" {
				return true
			}
		}
	}
	return false
}

// VideoUsageLooksCounted reports whether a prompt count (from an upstream or
// from a tokenizer endpoint) can have included the media, given the locally
// priced video part. The measured failure reports the text-only count — 27 or
// 96 tokens against a 42 320-token clip, under 1 % of the video price — while
// any count that saw the media is at least the video price itself. Requiring
// half the video price sits far from both sides, so a partially-counting
// source is left alone rather than double-charged or under-charged.
func VideoUsageLooksCounted(promptTokens, videoTokens int) bool {
	if promptTokens < 0 || videoTokens <= 0 {
		return false
	}
	threshold := videoTokens / 2
	if videoTokens%2 != 0 {
		threshold++
	}
	return promptTokens >= threshold
}

// videoPromptTotalSawMedia decides whether the tokenizer endpoint's total may
// supersede the upstream report.
//
// With a local video price the yardstick is that price, exactly as
// VideoUsageLooksCounted uses it. Without one — a container the local model
// cannot read, so nothing was priced locally — the only yardstick left is the
// report itself: estimate mode exists because the report counted text alone,
// so an endpoint total above it saw at least something of the media. That
// branch is deliberately weaker than the priced one; it is reachable only for
// containers the local model does not parse, and the alternative is to keep
// billing a full video as if it were text (measured: 26 tokens for a 4 s clip
// the endpoint priced at 1027).
func videoPromptTotalSawMedia(total, videoTokens, reportedTokens int) bool {
	if total <= 0 {
		return false
	}
	if videoTokens > 0 {
		return VideoUsageLooksCounted(total, videoTokens)
	}
	return total > reportedTokens
}

// correctedVideoBillingUsage returns a copy of usage carrying the video-aware
// prompt count, or ok=false when no correction applies. The input is never
// mutated: callers keep the upstream-reported number for logging and audit.
//
// The decision reads the settings of the channel that actually served the
// request (info.ChannelMeta is built per attempt and is populated by the time
// settlement runs), never a channel that was merely tried first, so a
// cross-channel retry cannot bill one channel's correction against another's
// report.
func correctedVideoBillingUsage(info *relaycommon.RelayInfo, usage *dto.Usage) (*dto.Usage, bool) {
	if info == nil || info.ChannelMeta == nil || !info.ChannelOtherSettings.EstimatesVideoUsage() {
		return nil, false
	}
	corrected := dto.Usage{}
	if usage != nil {
		corrected = *usage
	}
	reportedTokens := usagePromptTokens(usage)
	videoTokens := info.GetVideoTokens()
	switch total, ok := videoPromptTotalForSettlement(info); {
	case ok && videoPromptTotalSawMedia(total, videoTokens, reportedTokens):
		// Authoritative: the tokenizer priced text and media together. The
		// media-aware guard applies here too, so an endpoint that saw no video
		// cannot replace the report with a text-only number.
		corrected.PromptTokens = total
	default:
		if videoTokens <= 0 || VideoUsageLooksCounted(reportedTokens, videoTokens) {
			return nil, false
		}
		// The upstream's text count is trustworthy; only the media is missing.
		corrected.PromptTokens = saturatingTokenTotal(corrected.PromptTokens, videoTokens)
	}
	corrected.TotalTokens = saturatingTokenTotal(corrected.PromptTokens, corrected.CompletionTokens)
	// Build the payload from a copy that does not yet carry a BillingUsage, so
	// the constructor clones the corrected counts rather than re-entering.
	payload := corrected
	payload.BillingUsage = nil
	corrected.BillingUsage = dto.NewEstimatedOpenAIChatBillingUsage(&payload)
	return &corrected, true
}

// videoPromptTotalForSettlement reads the prefetched endpoint answer for this
// request. The read never blocks: a cold entry means the prefetch is still in
// flight, was never started, or failed, and the caller falls back to the
// locally priced video part.
func videoPromptTotalForSettlement(info *relaycommon.RelayInfo) (int, bool) {
	if info.Request == nil {
		return 0, false
	}
	return VideoPromptTotalForRequest(info.OriginModelName, info.Request)
}

// CountVideoToken prices one video file part on the official Kimi vision
// family. It mirrors getImageToken: the bytes come from the shared file
// service, so a URL source is fetched once and cached like an image would be.
func CountVideoToken(source types.FileSource) (int, error) {
	if source == nil {
		return 0, fmt.Errorf("video source is nil")
	}
	cachedData, err := LoadFileSource(nil, source, "video_token")
	if err != nil {
		return 0, fmt.Errorf("failed to load video source: %w", err)
	}
	base64Data, err := cachedData.GetBase64Data()
	if err != nil {
		return 0, fmt.Errorf("failed to read video data: %w", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return 0, fmt.Errorf("failed to decode video base64: %w", err)
	}
	meta, ok := DecodeVideoMetadata(decoded)
	if !ok {
		return 0, fmt.Errorf("unsupported or unparsable video container")
	}
	return EstimateVideoTokens(meta), nil
}
