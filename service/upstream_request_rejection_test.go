package service

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

// TestIsUpstreamRequestRejectionMatchesLiveContextOverflowTexts pins the exact
// messages recorded on the production channels for one over-length request
// (2026-09-21, deepseek-v4-pro, five channels in a single retry chain) plus the
// remaining wordings the same log window produced. A drift in any upstream must
// be caught here rather than resurface as a five-channel failover in the error
// log.
func TestIsUpstreamRequestRejectionMatchesLiveContextOverflowTexts(t *testing.T) {
	messages := []string{
		"The prompt is too long: 1270711, model maximum context length: 1048571",
		"The prompt is too long: 1270974, model maximum context length: 1048576 (ref: c0dcb7fa-724d-4301-b7e3-82bcf352b926)",
		"This model's maximum context length is 1048576 tokens. However, you requested 1272079 tokens (1272079 in the messages, 0 in the completion). Please reduce the length of the messages or completion.",
		"This endpoint's maximum context length is 1048576 tokens. However, you requested 1272079 tokens",
		"The request is invalid: This model's maximum context length is 1048576 tokens.",
		`{"error":{"message":"This model's maximum context length is 1048576 tokens. However, you requested 1272079 tokens","type":"AI_APICallError","param":{"isRetryable":false}}}`,
		"Input length 1270721 exceeds the maximum allowed input length of 1048576",
		"Input length 1066563 exceeds the maximum length 1048566. Request id: 0a1b2c3d",
		"<400> InvalidParameter: Range of input length should be [1, 1048576]",
		"Error from provider (Console Go): Upstream request failed: [400] The input (1112787 tokens) is longer than the model's context length (1048576 tokens).",
		"Input token exceed the limit (request id: 2026091505571927208808c955d568TfYQmTMr)",
	}

	for _, message := range messages {
		err := types.NewOpenAIError(errors.New(message), types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest)
		require.True(t, IsUpstreamRequestRejection(err), message)
	}
}

func TestIsUpstreamRequestRejectionIgnoresChannelAndTransientFailures(t *testing.T) {
	// A channel-capability 400 stays retryable: another channel can serve it.
	require.False(t, IsUpstreamRequestRejection(types.NewErrorWithStatusCode(
		errors.New(`unsupported ollama response format type "text"`),
		types.ErrorCodeChannelUnsupportedFeature,
		http.StatusBadRequest,
	)))

	// Parameter rejections outside the context window are exactly what the
	// force-retry rule exists for: another channel's range or capability differs.
	for _, message := range []string{
		"upstream rejected this channel request",
		`{"error":{"message":"Invalid max_tokens value, the valid range of max_tokens is [1, 393216]","type":"invalid_request_error"}}`,
		"Error from provider (Console Go): Upstream request failed: [400] Model only supports text input; received unsupported content type 'image_url'.",
	} {
		require.False(t, IsUpstreamRequestRejection(types.NewOpenAIError(
			errors.New(message),
			types.ErrorCodeBadResponseStatusCode,
			http.StatusBadRequest,
		)), message)
	}

	// The same wording behind a 5xx may be a transient upstream fault, so the
	// retry stays available.
	require.False(t, IsUpstreamRequestRejection(types.NewOpenAIError(
		errors.New("This model's maximum context length is 1048576 tokens."),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusInternalServerError,
	)))

	require.False(t, IsUpstreamRequestRejection(nil))
}
