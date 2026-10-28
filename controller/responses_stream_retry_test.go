package controller

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// A Responses stream that fails before producing any output used to commit the
// response on response.created, which made the relay stop with
// response_committed and answer the client with the failed channel's error. The
// lifecycle events are now buffered, so this attempt stays retryable: the relay
// must exclude the failing channel and serve the request from the next one.
func TestResponsesStreamPreOutputFailureRetriesAnotherChannel(t *testing.T) {
	fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(*websocket.Conn, *http.Request) {})

	oldRetries := common.RetryTimes
	common.RetryTimes = 1
	t.Cleanup(func() { common.RetryTimes = oldRetries })

	var attempts atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if attempts.Add(1) == 1 {
			// Failing channel: lifecycle events followed by an in-band overload,
			// exactly the shape observed from the overloaded official channel.
			fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"code\":\"server_is_overloaded\",\"message\":\"The engine is currently overloaded, please try again later\"}}\n\n")
			return
		}
		// Serving channel after failover.
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_2\",\"status\":\"in_progress\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n")
	}))
	t.Cleanup(upstream.Close)

	mapping := `{"ws-billing":"gpt-4o"}`
	// The fixture channel answers first: give it the higher priority tier.
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).Updates(map[string]any{
		"base_url": upstream.URL,
		"priority": 10,
	}).Error)
	retryChannel := &model.Channel{
		Name: "responses-retry-target", Key: "retry-key", Status: common.ChannelStatusEnabled,
		Type: constant.ChannelTypeOpenAI, Group: "default", Models: "ws-billing", BaseURL: &upstream.URL,
		ModelMapping: &mapping,
	}
	retryChannel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	retryChannel.SetOtherSettings(dto.ChannelOtherSettings{AllowServiceTier: true})
	require.NoError(t, model.DB.Create(retryChannel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{ChannelId: retryChannel.Id, Model: "ws-billing", Group: "default", Enabled: true}).Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("channel_id = ?", retryChannel.Id).Delete(&model.Ability{}).Error)
		require.NoError(t, model.DB.Delete(retryChannel).Error)
	})
	model.InitChannelCache()

	request, err := http.NewRequest(http.MethodPost, fixture.gatewayURL+"/v1/responses",
		strings.NewReader(`{"model":"ws-billing","input":"hi","stream":true,"max_output_tokens":16}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer sk-"+fixture.token.Key)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, int64(2), attempts.Load(), "the failed attempt must be retried on the second channel")
	require.Contains(t, string(body), "response.completed")
	require.NotContains(t, string(body), "currently overloaded")

	fixture.closeAndWait(t)
}

// A replay registered by an earlier attempt must not survive into the next one.
// If the second attempt fails before its stream handler (HTTP JSON error, DoRequest
// failure, selection/billing error), the client must see that attempt's HTTP
// failure, not the first channel's held-back SSE stream.
func TestResponsesStreamStaleReplayDoesNotMaskLaterAttemptFailure(t *testing.T) {
	fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(*websocket.Conn, *http.Request) {})

	oldRetries := common.RetryTimes
	common.RetryTimes = 1
	t.Cleanup(func() { common.RetryTimes = oldRetries })

	var attempts atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			// First channel registers a replay, then fails in-band before output.
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"code\":\"server_is_overloaded\",\"message\":\"The engine is currently overloaded, please try again later\"}}\n\n")
			return
		}
		// Second channel rejects with an HTTP 4xx JSON error before any stream.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"invalid image input","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(upstream.Close)

	mapping := `{"ws-billing":"gpt-4o"}`
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).Updates(map[string]any{
		"base_url": upstream.URL,
		"priority": 10,
	}).Error)
	retryChannel := &model.Channel{
		Name: "responses-retry-rejected", Key: "retry-key", Status: common.ChannelStatusEnabled,
		Type: constant.ChannelTypeOpenAI, Group: "default", Models: "ws-billing", BaseURL: &upstream.URL,
		ModelMapping: &mapping,
	}
	retryChannel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	retryChannel.SetOtherSettings(dto.ChannelOtherSettings{AllowServiceTier: true})
	require.NoError(t, model.DB.Create(retryChannel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{ChannelId: retryChannel.Id, Model: "ws-billing", Group: "default", Enabled: true}).Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("channel_id = ?", retryChannel.Id).Delete(&model.Ability{}).Error)
		require.NoError(t, model.DB.Delete(retryChannel).Error)
	})
	model.InitChannelCache()

	request, err := http.NewRequest(http.MethodPost, fixture.gatewayURL+"/v1/responses",
		strings.NewReader(`{"model":"ws-billing","input":"hi","stream":true,"max_output_tokens":16}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer sk-"+fixture.token.Key)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	require.Equal(t, http.StatusBadRequest, response.StatusCode,
		"the final attempt's HTTP failure must not be masked by the stale replay")
	require.Contains(t, string(body), "invalid image input")
	require.NotContains(t, string(body), "currently overloaded")
	require.Equal(t, int64(2), attempts.Load())

	fixture.closeAndWait(t)
}

// With keep-alive pings already on the wire and no channel left to retry, the
// client must still receive a protocol terminal event instead of a bare 200 with
// only ping comments. The ping-only commit is classified as uncommitted so a
// retry window exists, and the relay replays the synthetic response.failed when
// it gives up.
func TestResponsesStreamKeepAliveOnlyNoRetryReplaysSyntheticFailure(t *testing.T) {
	fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(*websocket.Conn, *http.Request) {})

	oldRetries := common.RetryTimes
	common.RetryTimes = 0
	t.Cleanup(func() { common.RetryTimes = oldRetries })
	generalSetting := operation_setting.GetGeneralSetting()
	oldPingEnabled, oldPingSeconds := generalSetting.PingIntervalEnabled, generalSetting.PingIntervalSeconds
	generalSetting.PingIntervalEnabled, generalSetting.PingIntervalSeconds = true, 1
	t.Cleanup(func() {
		generalSetting.PingIntervalEnabled, generalSetting.PingIntervalSeconds = oldPingEnabled, oldPingSeconds
	})

	// Upstream sends no SSE event at all: only the gateway's pings reach the
	// client, then the stream ends without response.completed.
	fixture.httpUpstream = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-time.After(2500 * time.Millisecond)
	}
	model.InitChannelCache()

	request, err := http.NewRequest(http.MethodPost, fixture.gatewayURL+"/v1/responses",
		strings.NewReader(`{"model":"ws-billing","input":"hi","stream":true}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer sk-"+fixture.token.Key)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Contains(t, string(body), ": PING", "the retry window kept the stream alive with pings")
	require.Contains(t, string(body), "response.failed", "giving up must still terminate the stream")
	require.Contains(t, string(body), "data: [DONE]")

	fixture.closeAndWait(t)
}
