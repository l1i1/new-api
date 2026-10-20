package helper

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/officialfit"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

// deepSeekV4NonImageRejectionFragments are account-level verdicts that must
// never be rewritten into a download-failure text: the upstream is talking
// about the credential, not about the image, and telling the caller otherwise
// would hide a real provisioning failure.
var deepSeekV4NonImageRejectionFragments = []string{
	"insufficient balance",
	"insufficient quota",
	"余额",
	"quota",
	"unauthorized",
	"invalid token",
	"invalid api key",
	"api key",
	"rate limit",
	"too many requests",
	"account",
}

// FitDeepSeekV4ImageDownloadError normalizes an aggregation backend's rejection
// of a remote-image request into the official DeepSeek V4 download-failure
// text, so an official-fit client sees the contract it compares against.
//
// Background (live-probed 2026-10-19, CDP consistency suite DS-131): the
// official endpoint fetches a remote image itself and, when the fetch fails,
// answers 400 with
//
//	.messages[<i>].image[<j>]: Failed to download image from <url> (request_id: <uuid>)
//
// The aggregation pool never fetches the image at relay time, so the same body
// comes back as the backend's own provider error — a 502 "Model provider request
// failed", a JSON business error asking the caller to replace the image, or a
// generic 400. That is a wording/status divergence, not a different verdict on
// the request: the official contract says "this image cannot be downloaded", so
// rewriting the message (and normalizing the status to the official 400) keeps
// the client-visible bytes identical while staying truthful about the failure.
//
// Deliberately NOT rewritten: auth/quota/rate-limit rejections (the fragments
// above), errors that already carry the official text, and every relay mode
// except chat completions.
func FitDeepSeekV4ImageDownloadError(c *gin.Context, statusCode int, message string) (string, bool) {
	if c == nil {
		return "", false
	}
	switch statusCode {
	case http.StatusBadRequest, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		// Provider-class failures. A 429/401/402/403 is an account or client
		// verdict and keeps its own text.
	default:
		return "", false
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "", false
	}
	// Already official (either the direct text or the marker set the local
	// validation surface produces): nothing to normalize.
	if IsStrictFitValidationMessage(message) || strings.Contains(message, deepSeekV4ImageDownloadFailedText) {
		return "", false
	}
	lowered := strings.ToLower(message)
	for _, fragment := range deepSeekV4NonImageRejectionFragments {
		if strings.Contains(lowered, fragment) {
			return "", false
		}
	}
	info, ok := common.GetContextKeyType[*relaycommon.RelayInfo](c, constant.ContextKeyRelayInfoPtr)
	if !ok || info == nil || info.RelayMode != relayconstant.RelayModeChatCompletions {
		return "", false
	}
	if officialfit.FamilyOf(info.OriginModelName) != officialfit.FamilyDeepSeekV4 {
		return "", false
	}
	request, ok := info.Request.(*dto.GeneralOpenAIRequest)
	if !ok || request == nil {
		return "", false
	}
	messageIndex, imageIndex, imageURL, found := firstRemoteImage(request.Messages)
	if !found {
		return "", false
	}
	return fmt.Sprintf(
		".messages[%d].image[%d]: Failed to download image from %s (request_id: %s)",
		messageIndex, imageIndex, imageURL, uuid.New().String(),
	), true
}

// firstRemoteImage finds the first remote image part and reports its position
// in the official texts' terms: the message index and the image index within
// that message's image array.
func firstRemoteImage(messages []dto.Message) (messageIndex int, imageIndex int, imageURL string, found bool) {
	for i := range messages {
		imageIndex = 0
		for _, part := range messages[i].ParseContent() {
			if !strings.EqualFold(strings.TrimSpace(part.Type), "image_url") {
				continue
			}
			media := part.GetImageMedia()
			if media == nil {
				imageIndex++
				continue
			}
			if media.IsRemoteImage() {
				return i, imageIndex, media.Url, true
			}
			imageIndex++
		}
	}
	return 0, 0, "", false
}
