package dto

import (
	"testing"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/require"
)

// TestParseContentAcceptsObjectFormVideoUrl pins the fix for a silently dropped
// media part. Moonshot's documented video channel sends the url as an object
// ({"url":"ms://<file_id>"}) and accepts inline base64 the same way; Aliyun
// Bailian sends a bare string. Accepting only the string form left the part
// invisible to token pricing and to the estimate endpoint, which then priced
// and counted a video request as if it were text.
func TestParseContentAcceptsObjectFormVideoUrl(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // expected resolved url
	}{
		{
			name: "object form with ms file id (Moonshot documented)",
			body: `{"messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"ms://file-abc"}}]}]}`,
			want: "ms://file-abc",
		},
		{
			name: "object form with remote url",
			body: `{"messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://example.test/v.mp4"}}]}]}`,
			want: "https://example.test/v.mp4",
		},
		{
			name: "string form (Aliyun Bailian)",
			body: `{"messages":[{"role":"user","content":[{"type":"video_url","video_url":"https://example.test/v.mp4"}]}]}`,
			want: "https://example.test/v.mp4",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var request GeneralOpenAIRequest
			require.NoError(t, kitutil.Unmarshal([]byte(tc.body), &request))
			require.Len(t, request.Messages, 1)

			parts := request.Messages[0].ParseContent()
			require.Len(t, parts, 1, "the video part must survive parsing")
			require.Equal(t, ContentTypeVideoUrl, parts[0].Type)

			video := parts[0].GetVideoUrl()
			require.NotNil(t, video, "the url must be readable")
			require.Equal(t, tc.want, video.Url)
			// The part must be recognizable as media, or pricing skips it.
			require.NotNil(t, parts[0].ToFileSource())
		})
	}
}

// TestParseContentSkipsVideoPartWithoutUrl pins that an unusable part is not
// reported as a readable media part: it carries no url for the upstream to
// fetch, so downstream must not treat it as a video request.
func TestParseContentSkipsVideoPartWithoutUrl(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"video_url","video_url":{}}]}]}`
	var request GeneralOpenAIRequest
	require.NoError(t, kitutil.Unmarshal([]byte(body), &request))

	for _, part := range request.Messages[0].ParseContent() {
		require.NotEqual(t, ContentTypeVideoUrl, part.Type, "a part with no url must not be reported as video")
	}
}
