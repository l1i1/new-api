package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A dated snapshot id passes the official-fit Validate gate only when the exact
// spelling is published for the request's routing group. An unpublished
// spelling has no ability to route through and no channel model_mapping to
// rewrite it, so it must keep the official unknown-model answer instead of
// leaking the platform's internal "cannot prove candidate exhaustion" error.
func TestMarkV4OfficialPinFromDistributorDatedSnapshotNeedsPublication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupVideoAffinityDB(t)

	channel := &model.Channel{
		Name:   "ds-snapshot",
		Key:    "sk-snapshot",
		Status: common.ChannelStatusEnabled,
		Type:   constant.ChannelTypeDeepSeek,
		Models: "deepseek-v4.1-flash,deepseek-v4.1-flash-1017",
		Group:  "DeepSeek-O",
	}
	require.NoError(t, model.DB.Create(channel).Error)
	for _, modelName := range []string{"deepseek-v4.1-flash", "deepseek-v4.1-flash-1017"} {
		require.NoError(t, model.DB.Create(&model.Ability{
			Group:     "DeepSeek-O",
			Model:     modelName,
			ChannelId: channel.Id,
			Enabled:   true,
		}).Error)
	}

	newContext := func(modelName string) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body, err := json.Marshal(map[string]any{
			"model":                 modelName,
			"messages":              []map[string]string{{"role": "user", "content": "hi"}},
			"max_completion_tokens": 16,
		})
		require.NoError(t, err)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBuffer(body))
		c.Request.Header.Set("Content-Type", "application/json")
		common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
		common.SetContextKey(c, constant.ContextKeyTokenGroup, "DeepSeek-O")
		common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{
			OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
				"deepseek-v4": {Validate: true, Route: true},
			}},
		})
		return c, w
	}

	t.Run("published dated snapshot passes", func(t *testing.T) {
		c, _ := newContext("deepseek-v4.1-flash-1017")
		markV4OfficialPinFromDistributor(c)
		assert.False(t, c.IsAborted())
	})

	t.Run("unpublished dated snapshot keeps the official 400", func(t *testing.T) {
		c, w := newContext("deepseek-v4.1-flash-1018")
		markV4OfficialPinFromDistributor(c)
		assert.True(t, c.IsAborted())
		assert.Equal(t, http.StatusBadRequest, w.Code)
		var payload struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		assert.Equal(t, "The supported API model names are deepseek-flash, deepseek-v4-pro, but you passed deepseek-v4.1-flash-1018.", payload.Error.Message)
	})

	t.Run("id outside the dated rule keeps the official 400", func(t *testing.T) {
		c, _ := newContext("deepseek-v4.1-flash-vision-exp")
		markV4OfficialPinFromDistributor(c)
		assert.True(t, c.IsAborted())
	})
}

// The dated-snapshot rule is family-agnostic, so the publish requirement guards
// the kimi-k3 and glm-5.3 families too: an unpublished dated kimi id keeps the
// Moonshot 404, while a published one passes the gate.
func TestMarkV4OfficialPinFromDistributorDatedSnapshotKimi(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupVideoAffinityDB(t)

	channel := &model.Channel{
		Name:   "k3-snapshot",
		Key:    "sk-k3",
		Status: common.ChannelStatusEnabled,
		Type:   constant.ChannelTypeMoonshot,
		Models: "kimi-k3,kimi-k3-20250101",
		Group:  "DeepSeek-O",
	}
	require.NoError(t, model.DB.Create(channel).Error)
	for _, modelName := range []string{"kimi-k3", "kimi-k3-20250101"} {
		require.NoError(t, model.DB.Create(&model.Ability{
			Group:     "DeepSeek-O",
			Model:     modelName,
			ChannelId: channel.Id,
			Enabled:   true,
		}).Error)
	}

	newContext := func(modelName string) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body, err := json.Marshal(map[string]any{
			"model":                 modelName,
			"messages":              []map[string]string{{"role": "user", "content": "hi"}},
			"max_completion_tokens": 16,
		})
		require.NoError(t, err)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBuffer(body))
		c.Request.Header.Set("Content-Type", "application/json")
		common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
		common.SetContextKey(c, constant.ContextKeyTokenGroup, "DeepSeek-O")
		common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{
			OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
				"kimi-k3": {Validate: true, Route: true},
			}},
		})
		return c, w
	}

	t.Run("published dated kimi snapshot passes", func(t *testing.T) {
		c, _ := newContext("kimi-k3-20250101")
		markV4OfficialPinFromDistributor(c)
		assert.False(t, c.IsAborted())
	})

	t.Run("unpublished dated kimi snapshot keeps the Moonshot 404", func(t *testing.T) {
		c, w := newContext("kimi-k3-20250102")
		markV4OfficialPinFromDistributor(c)
		assert.True(t, c.IsAborted())
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}
