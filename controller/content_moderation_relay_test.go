package controller

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestContentModerationProtocolForRelayFormatScopesConversationFormats(t *testing.T) {
	tests := []struct {
		format   types.RelayFormat
		protocol string
	}{
		{types.RelayFormatOpenAI, service.ContentModerationProtocolOpenAIChat},
		{types.RelayFormatOpenAIResponses, service.ContentModerationProtocolOpenAIResponses},
		{types.RelayFormatOpenAIResponsesCompaction, service.ContentModerationProtocolOpenAIResponses},
		{types.RelayFormatClaude, service.ContentModerationProtocolAnthropic},
		{types.RelayFormatGemini, service.ContentModerationProtocolGemini},
	}
	for _, test := range tests {
		t.Run(string(test.format), func(t *testing.T) {
			require.Equal(t, test.protocol, ContentModerationProtocolForRelayFormat(test.format))
		})
	}

	for _, format := range []types.RelayFormat{
		types.RelayFormatOpenAIAlphaSearch,
		types.RelayFormatOpenAIAudio,
		types.RelayFormatOpenAIImage,
		types.RelayFormatOpenAIRealtime,
		types.RelayFormatRerank,
		types.RelayFormatEmbedding,
	} {
		require.Empty(t, ContentModerationProtocolForRelayFormat(format), format)
	}
}

func TestCheckRelayContentModerationDoesNotReadDisabledBody(t *testing.T) {
	withControllerContentModerationOption(t, `{"enabled":false}`)
	body := &unreadableRequestBody{}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request

	decision := checkRelayContentModeration(context, types.RelayFormatOpenAI, &relaycommon.RelayInfo{})
	require.Nil(t, decision)
	require.Zero(t, body.reads)
}

func TestCheckRelayContentModerationSkipsModerationRelayEndpoint(t *testing.T) {
	withControllerContentModerationOption(t, `{"enabled":true}`)
	body := &unreadableRequestBody{}
	request := httptest.NewRequest(http.MethodPost, "/v1/moderations", body)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request

	decision := checkRelayContentModeration(context, types.RelayFormatOpenAI, &relaycommon.RelayInfo{})
	require.Nil(t, decision)
	require.Zero(t, body.reads)
}

func TestCheckRelayContentModerationObserveDoesNotBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"results":[{"flagged":true,"category_scores":{"sexual":0.9}}]}`))
	}))
	defer server.Close()
	withControllerContentModerationOption(t, `{"enabled":true,"mode":"observe","base_url":"`+server.URL+`","api_key":"test-key","sample_rate":1,"all_groups":true,"all_models":true}`)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"danger"}]}`))
	info := &relaycommon.RelayInfo{UserId: 1, OriginModelName: "gpt-test", RequestId: "observe-test"}
	decision := checkRelayContentModeration(context, types.RelayFormatOpenAI, info)
	common.CleanupBodyStorage(context)

	require.NotNil(t, decision)
	require.True(t, decision.Flagged)
	require.False(t, decision.Blocked)
}

func TestCheckRelayContentModerationUsesTypedImagesWithoutReadingBody(t *testing.T) {
	formats := []struct {
		name     string
		format   types.RelayFormat
		protocol string
		path     string
		body     string
	}{
		{
			name: "openai chat", format: types.RelayFormatOpenAI, protocol: service.ContentModerationProtocolOpenAIChat,
			path: "/v1/chat/completions", body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://img.test/chat.png"}}]}]}`,
		},
		{
			name: "responses", format: types.RelayFormatOpenAIResponses, protocol: service.ContentModerationProtocolOpenAIResponses,
			path: "/v1/responses", body: `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://img.test/responses.png"}]}]}`,
		},
		{
			name: "responses compact", format: types.RelayFormatOpenAIResponsesCompaction, protocol: service.ContentModerationProtocolOpenAIResponses,
			path: "/v1/responses/compact", body: `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://img.test/compact.png"}]}]}`,
		},
		{
			name: "anthropic", format: types.RelayFormatClaude, protocol: service.ContentModerationProtocolAnthropic,
			path: "/v1/messages", body: `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YW50aHJvcGlj"}}]}]}`,
		},
		{
			name: "gemini", format: types.RelayFormatGemini, protocol: service.ContentModerationProtocolGemini,
			path: "/v1beta/models/gemini:generateContent", body: `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"Z2VtaW5p"}}]}]}`,
		},
	}

	for _, test := range formats {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls++
				var payload struct {
					Input []struct {
						Type string `json:"type"`
					} `json:"input"`
				}
				require.NoError(t, common.DecodeJson(request.Body, &payload))
				require.Len(t, payload.Input, 1)
				require.Equal(t, "image_url", payload.Input[0].Type)
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}}]}`))
			}))
			defer server.Close()
			withControllerContentModerationOption(t, `{"enabled":true,"mode":"observe","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true}`)

			body := &unreadableRequestBody{}
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = httptest.NewRequest(http.MethodPost, test.path, body)
			typedRequest := decodeControllerContentModerationRequest(t, test.protocol, test.body)
			if test.format == types.RelayFormatOpenAIResponsesCompaction {
				typedRequest = &dto.OpenAIResponsesCompactionRequest{}
				require.NoError(t, common.Unmarshal([]byte(test.body), typedRequest))
			}
			info := &relaycommon.RelayInfo{
				UserId: 1, OriginModelName: "gpt-test", RequestId: "typed-" + test.name,
				Request: typedRequest,
			}
			decision := checkRelayContentModeration(context, test.format, info)

			require.NotNil(t, decision)
			require.True(t, decision.Checked)
			require.Equal(t, 1, calls)
			require.Zero(t, body.reads)
		})
	}
}

func TestRelayContentModerationPreBlockStopsBeforeChannelSelection(t *testing.T) {
	moderationCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		moderationCalls++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"results":[{"flagged":true,"category_scores":{"self-harm":0.9}}]}`))
	}))
	defer server.Close()
	withControllerContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","api_key":"test-key","sample_rate":1,"all_groups":true,"all_models":true,"block_status":451}`)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"danger"}]}`))
	context.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(context, constant.ContextKeyOriginalModel, "gpt-test")
	context.Set(common.RequestIdKey, "relay-pre-block-test")

	Relay(context, types.RelayFormatOpenAI)
	common.CleanupBodyStorage(context)

	require.Equal(t, http.StatusUnavailableForLegalReasons, recorder.Code)
	require.Equal(t, 1, moderationCalls)
	require.Contains(t, recorder.Body.String(), "content_policy_violation")
}

func TestRelayContentModerationPreBlockReturnsOverloadStatusWhenCapacityIsExhausted(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}}]}`))
	}))
	defer server.Close()
	withControllerContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","api_key":"test-key","sample_rate":1,"all_groups":true,"all_models":true,"max_in_flight_per_key":1,"queue_wait_ms":20,"overload_status":503}`)

	firstContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	firstContext.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hold"}]}`))
	firstInfo := &relaycommon.RelayInfo{UserId: 1, OriginModelName: "gpt-test", RequestId: "capacity-owner"}
	firstDone := make(chan *service.ContentModerationDecision, 1)
	go func() {
		firstDone <- checkRelayContentModeration(firstContext, types.RelayFormatOpenAI, firstInfo)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first moderation request did not start")
	}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"overflow"}]}`))
	context.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(context, constant.ContextKeyOriginalModel, "gpt-test")
	context.Set(common.RequestIdKey, "relay-capacity-overload")

	Relay(context, types.RelayFormatOpenAI)
	common.CleanupBodyStorage(context)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Contains(t, recorder.Body.String(), "content_moderation_overloaded")
	require.NotContains(t, recorder.Body.String(), "content_policy_violation")

	close(release)
	require.NotNil(t, <-firstDone)
	common.CleanupBodyStorage(firstContext)
}

func TestRelayContentModerationPreBlockRejectsInvalidImageWithoutProviderCall(t *testing.T) {
	moderationCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		moderationCalls++
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	withControllerContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true}`)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":[{"type":"image_url","image_url":"data:image/svg+xml;base64,PHN2Zz4="}]}]}`))
	context.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(context, constant.ContextKeyOriginalModel, "gpt-test")
	context.Set(common.RequestIdKey, "relay-invalid-image-test")

	Relay(context, types.RelayFormatOpenAI)
	common.CleanupBodyStorage(context)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, 0, moderationCalls)
	require.Contains(t, recorder.Body.String(), "content_policy_violation")
}

func TestUpdateContentModerationConfigReturnsServerErrorWithoutDatabase(t *testing.T) {
	previousDB := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = previousDB })

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/content-moderation/config", bytes.NewBufferString(`{
		"enabled": true,
		"mode": "observe",
		"base_url": "https://moderation.example.test",
		"model": "omni-moderation-latest",
		"sample_rate": 1,
		"all_groups": true,
		"all_models": true
	}`))
	context.Request.Header.Set("Content-Type", "application/json")

	UpdateContentModerationConfig(context)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Contains(t, recorder.Body.String(), "failed to persist content moderation configuration")
	require.NotContains(t, recorder.Body.String(), "database is not initialized")
}

func TestUpdateContentModerationConfigCanExplicitlyClearAPIKeys(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))

	previousDB := model.DB
	common.OptionMapRWMutex.Lock()
	previousOptionMap := common.OptionMap
	model.DB = db
	common.OptionMap = map[string]string{
		service.ContentModerationOptionKey: `{"enabled":true,"mode":"observe","base_url":"https://moderation.example.test","model":"omni-moderation-latest","api_key":"stored-key","sample_rate":1,"all_groups":true,"all_models":true}`,
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		model.DB = previousDB
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/content-moderation/config", bytes.NewBufferString(`{
		"enabled": true,
		"mode": "observe",
		"base_url": "https://moderation.example.test",
		"model": "omni-moderation-latest",
		"clear_api_keys": true,
		"sample_rate": 1,
		"all_groups": true,
		"all_models": true,
		"max_in_flight_per_key": 2,
		"queue_wait_ms": 250,
		"overload_status": 429,
		"key_cooldown_ms": 1500
	}`))
	context.Request.Header.Set("Content-Type", "application/json")

	UpdateContentModerationConfig(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	stored := service.GetContentModerationConfig()
	require.Empty(t, stored.APIKey)
	require.Empty(t, stored.APIKeys)
	require.False(t, stored.ClearAPIKeys)
	require.Equal(t, 2, stored.MaxInFlightPerKey)
	require.Equal(t, 250, stored.QueueWaitMS)
	require.Equal(t, http.StatusTooManyRequests, stored.OverloadStatus)
	require.Equal(t, 1500, stored.KeyCooldownMS)
}

func TestUnbanContentModerationUserIsIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))

	previousDB := model.DB
	previousRedisEnabled := common.RedisEnabled
	model.DB = db
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedisEnabled
	})

	user := model.User{Id: 987659, Username: "moderation-unban", Password: "unused-password", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AuthVersion: 5}
	require.NoError(t, db.Create(&user).Error)

	callUnban := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Params = gin.Params{{Key: "id", Value: "987659"}}
		UnbanContentModerationUser(context)
		return recorder
	}

	require.Equal(t, http.StatusOK, callUnban().Code)
	var reloaded model.User
	require.NoError(t, db.First(&reloaded, user.Id).Error)
	require.EqualValues(t, 5, reloaded.AuthVersion)

	require.NoError(t, db.Model(&model.User{}).Where("id = ?", user.Id).Update("status", common.UserStatusDisabled).Error)
	require.Equal(t, http.StatusOK, callUnban().Code)
	require.NoError(t, db.First(&reloaded, user.Id).Error)
	require.Equal(t, common.UserStatusEnabled, reloaded.Status)
	require.EqualValues(t, 6, reloaded.AuthVersion)
}

type unreadableRequestBody struct {
	reads int
}

func decodeControllerContentModerationRequest(t *testing.T, protocol string, body string) dto.Request {
	t.Helper()
	var request dto.Request
	switch protocol {
	case service.ContentModerationProtocolOpenAIChat:
		request = &dto.GeneralOpenAIRequest{}
	case service.ContentModerationProtocolOpenAIResponses:
		request = &dto.OpenAIResponsesRequest{}
	case service.ContentModerationProtocolAnthropic:
		request = &dto.ClaudeRequest{}
	case service.ContentModerationProtocolGemini:
		request = &dto.GeminiChatRequest{}
	default:
		t.Fatalf("unsupported protocol %q", protocol)
	}
	require.NoError(t, common.Unmarshal([]byte(body), request))
	return request
}

func (body *unreadableRequestBody) Read([]byte) (int, error) {
	body.reads++
	return 0, errors.New("request body should not be read")
}

func withControllerContentModerationOption(t *testing.T, value string) {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	previous := common.OptionMap
	current := make(map[string]string, len(previous)+1)
	for key, item := range previous {
		current[key] = item
	}
	current[service.ContentModerationOptionKey] = value
	common.OptionMap = current
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previous
		common.OptionMapRWMutex.Unlock()
	})
}
