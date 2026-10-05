package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
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
		{types.RelayFormatOpenAIImage, service.ContentModerationProtocolOpenAIImage},
	}
	for _, test := range tests {
		t.Run(string(test.format), func(t *testing.T) {
			require.Equal(t, test.protocol, ContentModerationProtocolForRelayFormat(test.format))
		})
	}

	for _, format := range []types.RelayFormat{
		types.RelayFormatOpenAIAlphaSearch,
		types.RelayFormatOpenAIAudio,
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

func TestCheckRelayContentModerationSkipsForGroupPolicyExemption(t *testing.T) {
	withControllerContentModerationOption(t, `{"enabled":true,"mode":"pre_block"}`)
	body := &unreadableRequestBody{}
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	common.SetContextKey(context, constant.ContextKeyGroupAccessPolicy, model.GroupAccessPolicySnapshot{
		GroupName:                 "default",
		ContentModerationDisabled: true,
	})

	decision := checkRelayContentModeration(context, types.RelayFormatOpenAI, &relaycommon.RelayInfo{
		UserId: 1, OriginModelName: "gpt-test",
	})
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
	var moderationCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		moderationCalls.Add(1)
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
	require.False(t, decision.Blocked)

	require.Eventually(t, func() bool {
		return moderationCalls.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
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
			withControllerContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","sample_rate":1,"all_groups":true,"all_models":true}`)

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

func TestCheckRelayContentModerationAuditsImageGenerationPrompt(t *testing.T) {
	var audited string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Input json.RawMessage `json:"input"`
		}
		require.NoError(t, common.DecodeJson(request.Body, &payload))
		audited = string(payload.Input)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"results":[{"flagged":true,"category_scores":{"sexual":0.97}}]}`))
	}))
	defer server.Close()
	withControllerContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","api_key":"test-key","sample_rate":1,"all_groups":true,"all_models":true,"block_status":451}`)

	// The image request is typed, so its prompt is audited without another
	// BodyStorage read.
	body := &unreadableRequestBody{}
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", body)
	decision := checkRelayContentModeration(context, types.RelayFormatOpenAIImage, &relaycommon.RelayInfo{
		UserId: 1, OriginModelName: "gpt-image-2", RequestId: "image-prompt-test",
		Request: &dto.ImageRequest{Model: "gpt-image-2", Prompt: "flagged image prompt"},
	})

	require.NotNil(t, decision)
	require.True(t, decision.Checked)
	require.True(t, decision.Blocked)
	require.Equal(t, http.StatusUnavailableForLegalReasons, decision.StatusCode)
	require.Contains(t, audited, "flagged image prompt")
	require.Zero(t, body.reads)
}

func TestGenerationContentModerationProtocolForRequestScopesGenerationRoutes(t *testing.T) {
	tests := []struct {
		path     string
		protocol string
	}{
		{"/v1/images/generations", service.ContentModerationProtocolOpenAIImage},
		{"/v1/images/edits", service.ContentModerationProtocolOpenAIImage},
		{"/v1/edits", service.ContentModerationProtocolOpenAIImage},
		{"/pg/images/generations", service.ContentModerationProtocolOpenAIImage},
		{"/v1/videos", service.ContentModerationProtocolOpenAIVideo},
		{"/v1/video/generations", service.ContentModerationProtocolOpenAIVideo},
		{"/kling/v1/videos/text2video", service.ContentModerationProtocolOpenAIVideo},
		{"/suno/submit/music", service.ContentModerationProtocolTask},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{"prompt":"x"}`))
			require.Equal(t, test.protocol, generationContentModerationProtocolForRequest(context))
		})
	}

	// A pinned task-plugin endpoint states its own protocol, which wins over
	// the path even when the bridge rewrites it.
	pinnedProtocols := []struct {
		pinned   string
		protocol string
	}{
		{pluginruntime.ProtocolOpenAIImage, service.ContentModerationProtocolOpenAIImage},
		{pluginruntime.ProtocolOpenAIVideo, service.ContentModerationProtocolOpenAIVideo},
		// The Responses bridge submits through the task path but carries a
		// Responses body, so it must keep the Responses extractor.
		{pluginruntime.ProtocolOpenAIResponses, service.ContentModerationProtocolOpenAIResponses},
	}
	for _, test := range pinnedProtocols {
		t.Run("pinned "+test.pinned, func(t *testing.T) {
			pinned, _ := gin.CreateTestContext(httptest.NewRecorder())
			pinned.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"x"}`))
			pinned.Set(pluginruntime.ContextKeyPinnedEndpoint, pluginruntime.PinnedEndpoint{Protocol: test.pinned})
			require.Equal(t, test.protocol, generationContentModerationProtocolForRequest(pinned))
		})
	}
}

func TestCheckGenerationContentModerationAuditsPinnedResponsesInput(t *testing.T) {
	var audited string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Input json.RawMessage `json:"input"`
		}
		require.NoError(t, common.DecodeJson(request.Body, &payload))
		audited = string(payload.Input)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"results":[{"flagged":true,"category_scores":{"illicit":0.99}}]}`))
	}))
	defer server.Close()
	withControllerContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","api_key":"test-key","sample_rate":1,"all_groups":true,"all_models":true,"block_status":451}`)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"gpt-test","input":"flagged responses prompt"}`))
	context.Set(pluginruntime.ContextKeyPinnedEndpoint, pluginruntime.PinnedEndpoint{Protocol: pluginruntime.ProtocolOpenAIResponses})
	context.Set(common.RequestIdKey, "pinned-responses-test")

	decision := checkGenerationContentModeration(context, &relaycommon.RelayInfo{
		UserId: 1, OriginModelName: "gpt-test", RequestId: "pinned-responses-test",
	}, generationContentModerationProtocolForRequest(context))
	common.CleanupBodyStorage(context)

	require.NotNil(t, decision)
	require.True(t, decision.Blocked)
	require.Contains(t, audited, "flagged responses prompt")
}

func TestCheckGenerationContentModerationAuditsMultipartTaskPrompt(t *testing.T) {
	var audited string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Input json.RawMessage `json:"input"`
		}
		require.NoError(t, common.DecodeJson(request.Body, &payload))
		audited = string(payload.Input)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"results":[{"flagged":true,"category_scores":{"sexual":0.99}}]}`))
	}))
	defer server.Close()
	withControllerContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","api_key":"test-key","sample_rate":1,"all_groups":true,"all_models":true,"block_status":451}`)

	// POST /v1/videos accepts multipart/form-data as a declared body kind; the
	// prompt lives in a form field, which the JSON extractor cannot see.
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	require.NoError(t, writer.WriteField("model", "grok-imagine-video"))
	require.NoError(t, writer.WriteField("prompt", "flagged multipart prompt"))
	require.NoError(t, writer.Close())

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(form.Bytes()))
	context.Request.Header.Set("Content-Type", writer.FormDataContentType())
	common.SetContextKey(context, constant.ContextKeyOriginalModel, "grok-imagine-video")
	context.Set(common.RequestIdKey, "multipart-prompt-test")

	decision := checkGenerationContentModeration(context, &relaycommon.RelayInfo{
		UserId: 1, OriginModelName: "grok-imagine-video", RequestId: "multipart-prompt-test",
	}, generationContentModerationProtocolForRequest(context))
	common.CleanupBodyStorage(context)

	require.NotNil(t, decision)
	require.True(t, decision.Blocked)
	require.Contains(t, audited, "flagged multipart prompt")
}

func TestCheckGenerationContentModerationAuditsTaskPrompt(t *testing.T) {
	var audited string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Input json.RawMessage `json:"input"`
		}
		require.NoError(t, common.DecodeJson(request.Body, &payload))
		audited = string(payload.Input)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"results":[{"flagged":true,"category_scores":{"violence":0.99}}]}`))
	}))
	defer server.Close()
	withControllerContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","api_key":"test-key","sample_rate":1,"all_groups":true,"all_models":true,"block_status":451}`)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/videos",
		strings.NewReader(`{"model":"grok-imagine-video","prompt":"flagged video prompt"}`))
	common.SetContextKey(context, constant.ContextKeyOriginalModel, "grok-imagine-video")
	context.Set(common.RequestIdKey, "task-prompt-test")

	decision := checkGenerationContentModeration(context, &relaycommon.RelayInfo{
		UserId: 1, OriginModelName: "grok-imagine-video", RequestId: "task-prompt-test",
	}, generationContentModerationProtocolForRequest(context))
	common.CleanupBodyStorage(context)

	require.NotNil(t, decision)
	require.True(t, decision.Blocked)
	require.Equal(t, http.StatusUnavailableForLegalReasons, decision.StatusCode)
	require.Contains(t, audited, "flagged video prompt")
}

func TestExecuteTaskSubmissionModerationPreBlockStopsBeforeChannelSelection(t *testing.T) {
	moderationCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		moderationCalls++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"results":[{"flagged":true,"category_scores":{"sexual":0.99}}]}`))
	}))
	defer server.Close()
	withControllerContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","api_key":"test-key","sample_rate":1,"all_groups":true,"all_models":true,"block_status":451}`)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/suno/submit/music",
		strings.NewReader(`{"prompt":"flagged music prompt"}`))
	context.Set(common.RequestIdKey, "task-block-test")

	outcome, taskErr := executeTaskSubmission(context, &relaycommon.RelayInfo{
		UserId: 1, OriginModelName: "suno-v5", RequestId: "task-block-test",
	})
	common.CleanupBodyStorage(context)

	require.Nil(t, outcome)
	require.NotNil(t, taskErr)
	require.True(t, taskErr.LocalError)
	require.Equal(t, http.StatusUnavailableForLegalReasons, taskErr.StatusCode)
	require.Equal(t, 1, moderationCalls)
}

func TestExecuteTaskSubmissionPassesAllowedGenerationPromptToChannelSelection(t *testing.T) {
	moderationCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		moderationCalls++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"results":[{"flagged":false,"category_scores":{"sexual":0.01}}]}`))
	}))
	defer server.Close()
	withControllerContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","api_key":"test-key","sample_rate":1,"all_groups":true,"all_models":true,"block_status":451}`)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/suno/submit/music",
		strings.NewReader(`{"prompt":"a calm melody"}`))
	context.Set(common.RequestIdKey, "task-allow-test")
	info, err := relaycommon.GenRelayInfo(context, types.RelayFormatTask, nil, nil)
	require.NoError(t, err)

	outcome, taskErr := executeTaskSubmission(context, info)
	common.CleanupBodyStorage(context)

	// This test has no database, so channel selection is what fails; the point
	// is that an allowed prompt is not turned into a content-policy block.
	require.Nil(t, outcome)
	require.NotNil(t, taskErr)
	require.NotEqual(t, "content_policy_violation", taskErr.Code)
	require.Equal(t, 1, moderationCalls)
}

func TestRelayImageContentModerationPreBlockStopsBeforeChannelSelection(t *testing.T) {
	moderationCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		moderationCalls++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"results":[{"flagged":true,"category_scores":{"sexual":0.98}}]}`))
	}))
	defer server.Close()
	withControllerContentModerationOption(t, `{"enabled":true,"mode":"pre_block","base_url":"`+server.URL+`","api_key":"test-key","sample_rate":1,"all_groups":true,"all_models":true,"block_status":451}`)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"model":"gpt-image-2","prompt":"flagged image prompt"}`))
	context.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(context, constant.ContextKeyOriginalModel, "gpt-image-2")
	context.Set(common.RequestIdKey, "relay-image-pre-block-test")

	Relay(context, types.RelayFormatOpenAIImage)
	common.CleanupBodyStorage(context)

	require.Equal(t, http.StatusUnavailableForLegalReasons, recorder.Code)
	require.Equal(t, 1, moderationCalls)
	require.Contains(t, recorder.Body.String(), "content_policy_violation")
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

func TestRelayContentModerationPreBlockFailsOpenWhenCapacityIsExhausted(t *testing.T) {
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

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"overflow"}]}`))
	context.Request.Header.Set("Content-Type", "application/json")
	decision := checkRelayContentModeration(context, types.RelayFormatOpenAI, &relaycommon.RelayInfo{
		UserId: 1, OriginModelName: "gpt-test", RequestId: "relay-capacity-overload",
	})
	common.CleanupBodyStorage(context)

	require.NotNil(t, decision)
	require.True(t, decision.Overloaded)
	require.False(t, decision.Blocked)

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

func TestResetContentModerationUserViolationsRequiresExistingUser(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.ContentModerationLog{}, &model.ContentModerationUserState{}))

	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })

	const userID = 987661
	require.NoError(t, db.Create(&model.User{
		Id: userID, Username: "moderation-reset", Password: "unused-password",
		Status: common.UserStatusEnabled, Role: common.RoleCommonUser,
	}).Error)
	require.NoError(t, db.Create(&model.ContentModerationLog{
		UserID: userID, Flagged: true, CreatedAt: time.Now().Unix(),
	}).Error)

	callReset := func(id string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Params = gin.Params{{Key: "id", Value: id}}
		ResetContentModerationUserViolations(context)
		return recorder
	}

	require.Equal(t, http.StatusNotFound, callReset("404").Code)
	require.Equal(t, http.StatusBadRequest, callReset("invalid").Code)
	require.Equal(t, http.StatusOK, callReset(strconv.Itoa(userID)).Code)

	var state model.ContentModerationUserState
	require.NoError(t, db.First(&state, "user_id = ?", userID).Error)
	require.NotZero(t, state.ViolationResetAfterID)

	count, err := model.CountFlaggedContentModerationByUserSince(userID, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 0, count)
}

func TestGetContentModerationLogsReturnsExplicitPaginationMetadata(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ContentModerationLog{}))

	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })

	require.NoError(t, db.Create(&model.ContentModerationLog{
		UserID: 987662, RequestID: "first-request", CapacityReason: "local_slots_full", CreatedAt: time.Now().Add(-time.Minute).Unix(),
	}).Error)
	require.NoError(t, db.Create(&model.ContentModerationLog{
		UserID: 987662, RequestID: "second-request", CreatedAt: time.Now().Unix(),
	}).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/content-moderation/logs?offset=1&limit=1", nil)

	GetContentModerationLogs(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success  bool                         `json:"success"`
		Data     []model.ContentModerationLog `json:"data"`
		Total    int64                        `json:"total"`
		Offset   int                          `json:"offset"`
		Limit    int                          `json:"limit"`
		Page     int                          `json:"page"`
		PageSize int                          `json:"page_size"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.EqualValues(t, 2, response.Total)
	require.Equal(t, 1, response.Offset)
	require.Equal(t, 1, response.Limit)
	require.Equal(t, 2, response.Page)
	require.Equal(t, 1, response.PageSize)
	require.Len(t, response.Data, 1)
	require.Equal(t, "first-request", response.Data[0].RequestID)
	require.Equal(t, "local_slots_full", response.Data[0].CapacityReason)

	recorder = httptest.NewRecorder()
	context, _ = gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/content-moderation/logs?p=-1", nil)

	GetContentModerationLogs(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, 0, response.Offset)
	require.Equal(t, 1, response.Page)
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
