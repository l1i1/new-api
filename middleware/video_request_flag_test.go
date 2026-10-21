package middleware

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestGetModelFromJSONBodyMarksVideoRequests pins the wiring between body
// parsing and the routing flag: channel selection reads the flag to keep
// media-blind upstreams out of the candidate set, so a video request must set it
// during the same pass that extracts the model. Text and image requests must
// leave it unset, or ordinary traffic would be restricted to video channels.
func TestGetModelFromJSONBodyMarksVideoRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)

	deepSeekVideo := `{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://e.test/a.mp4"}}]}]}`
	deepSeekValidate := dto.UserSetting{OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
		"deepseek-v4": {Validate: true},
	}}}

	cases := []struct {
		name  string
		body  string
		setup func(*gin.Context)
		want  bool
	}{
		{
			name: "video request",
			body: `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"ms://abc"}}]}]}`,
			want: true,
		},
		{
			// The distributor selects before the relay validation runs, and for
			// this family the validation owns the verdict: every media part is an
			// official 422. Arming the video filter here would fail selection
			// first (the 2026-10-21 customer video incident).
			name: "deepseek-v4 video with official-fit validate is left to validation",
			body: deepSeekVideo,
			setup: func(c *gin.Context) {
				common.SetContextKey(c, constant.ContextKeyUserSetting, deepSeekValidate)
			},
			want: false,
		},
		{
			name: "deepseek-v4 video without validate keeps the capability filter",
			body: deepSeekVideo,
			want: true,
		},
		{
			// GLM officially serves video, so a fit GLM user must keep the
			// capability routing even with validation on.
			name: "glm video with validation keeps the capability filter",
			body: `{"model":"glm-5.3-flash","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://e.test/a.mp4"}}]}]}`,
			setup: func(c *gin.Context) {
				common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
					"glm-5.3": {Validate: true},
				}}})
			},
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
			if tc.setup != nil {
				tc.setup(c)
			}

			modelRequest, shouldSelect, err := getModelRequest(c)
			require.NoError(t, err)
			require.True(t, shouldSelect)
			require.Equal(t, gjson.Get(tc.body, "model").String(), modelRequest.Model)

			assert.Equal(t, tc.want, common.GetContextKeyBool(c, constant.ContextKeyVideoRequest))
		})
	}
}
