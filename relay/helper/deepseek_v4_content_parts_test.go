package helper

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The official DeepSeek V4 content-part enum is internally tagged by `type` and
// admits exactly text/image_url/file. An unknown variant is a deserialization
// failure with HTTP 422 (live-probed 2026-10-21: video_url, video and audio_url
// all returned 422 with the text below), and the reported column is the position
// of the variant's last character.
//
// This is the shape a multi-modal client sends to every model — the video test a
// customer ran against deepseek-v4.1-flash — and before this gate the request
// fell through to channel selection, where the video part emptied the candidate
// set and the client received the gateway's internal
// "no candidate satisfied the official pin and capability marks" 500.
func TestDeepSeekV4ContentPartVariantMatchesOfficial(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		body     string
		wantText string
	}{
		{
			name:     "video_url part is an unknown variant at message 0",
			body:     `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"video_url","video_url":{"url":"https://example.com/a.mp4"}}]}],"max_tokens":32}`,
			wantText: "Failed to deserialize the JSON body into the target type: messages[0]: unknown variant `video_url`, expected one of `text`, `image_url`, `file` at line 1 column 117",
		},
		{
			name:     "audio_url part is an unknown variant",
			body:     `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"audio_url","audio_url":{"url":"https://example.com/a.mp3"}}]}],"max_tokens":32}`,
			wantText: "Failed to deserialize the JSON body into the target type: messages[0]: unknown variant `audio_url`, expected one of `text`, `image_url`, `file` at line 1 column 111",
		},
		{
			name:     "the path names the message index, not the part index",
			body:     `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://example.com/a.mp4"}}]}],"max_tokens":32}`,
			wantText: "Failed to deserialize the JSON body into the target type: messages[2]: unknown variant `video_url`, expected one of `text`, `image_url`, `file` at line 1 column 150",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GetAndValidateTextRequest(newDeepSeekV4FitContext(tt.body), constant.RelayModeChatCompletions)
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			assert.Equal(t, http.StatusUnprocessableEntity, apiErr.StatusCode,
				"the official endpoint answers 422, not 400, for a content-part enum failure")
			assert.Equal(t, tt.wantText, apiErr.ToOpenAIError().Message)
			assert.Equal(t, "invalid_request_error", apiErr.ToOpenAIError().Type)
			assert.Equal(t, "invalid_request_error", apiErr.ToOpenAIError().Code)
		})
	}
}

// The three admitted variants must keep the old pass-through behaviour: this gate
// exists for unknown variants only, and a false positive here would reject
// legitimate multi-modal requests.
func TestDeepSeekV4KnownContentPartTypesPassValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, body := range []string{
		`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"max_tokens":32}`,
		`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/x.png"}},{"type":"text","text":"这是什么？"}]}],"max_tokens":32}`,
		`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file-1"}}]}],"max_tokens":32}`,
		// A plain string content is the text variant, not an unknown part.
		`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":32}`,
	} {
		_, err := GetAndValidateTextRequest(newDeepSeekV4FitContext(body), constant.RelayModeChatCompletions)
		require.NoErrorf(t, err, "body: %s", body)
	}
}

// The gate belongs to the official V4 family: other families keep their own
// contract (kimi-k3 accepts video parts, for example) and must not be rejected
// here.
func TestDeepSeekV4ContentPartGateLeavesOtherFamiliesAlone(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://example.com/a.mp4"}}]}],"max_tokens":32}`
	_, err := GetAndValidateTextRequest(newDeepSeekV4FitContext(body), constant.RelayModeChatCompletions)
	require.NoError(t, err)
}
