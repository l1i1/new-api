package middleware

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetModelFromJSONBodyMarksVideoRequests pins the wiring between body
// parsing and the routing flag: channel selection reads the flag to keep
// media-blind upstreams out of the candidate set, so a video request must set it
// during the same pass that extracts the model. Text and image requests must
// leave it unset, or ordinary traffic would be restricted to video channels.
func TestGetModelFromJSONBodyMarksVideoRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "video request",
			body: `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"ms://abc"}}]}]}`,
			want: true,
		},
		{
			name: "text request",
			body: `{"model":"kimi-k3","messages":[{"role":"user","content":"1+1=?"}]}`,
			want: false,
		},
		{
			name: "image request",
			body: `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://e.test/a.png"}}]}]}`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")

			modelRequest, shouldSelect, err := getModelRequest(c)
			require.NoError(t, err)
			require.True(t, shouldSelect)
			require.Equal(t, "kimi-k3", modelRequest.Model)

			assert.Equal(t, tc.want, common.GetContextKeyBool(c, constant.ContextKeyVideoRequest))
		})
	}
}
