package service

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAppendStreamStatusRedactsCyberPolicyErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	MarkOpsCyberPolicy(c, CyberPolicyMark{Message: "blocked"})

	info := &relaycommon.RelayInfo{
		IsStream:           true,
		StreamStatus:       relaycommon.NewStreamStatus(),
		StreamFinishReason: "length",
	}
	secret := "API key provided: sk-live-stream-secret"
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, errors.New(secret))
	info.StreamStatus.RecordError(secret)

	other := map[string]interface{}{}
	appendStreamStatus(c, info, other)

	streamStatus, ok := other["stream_status"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "length", streamStatus["finish_reason"])
	require.NotContains(t, streamStatus["end_error"], "sk-live-stream-secret")
	errors, ok := streamStatus["errors"].([]string)
	require.True(t, ok)
	require.Len(t, errors, 1)
	require.NotContains(t, errors[0], "sk-live-stream-secret")
}

func TestGenerateTextOtherInfoIncludesOllamaPromptCacheObservation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(string(constant.ContextKeyOllamaPromptCache), map[string]interface{}{
		"outcome":        "hit_estimated",
		"partition_hash": strings.Repeat("a", 64),
	})
	info := &relaycommon.RelayInfo{
		StartTime:         time.Now(),
		FirstResponseTime: time.Now(),
		ChannelMeta:       &relaycommon.ChannelMeta{},
	}
	other := GenerateTextOtherInfo(c, info, 1, 1, 1, 10, 1, 1, 1)
	adminInfo, ok := other["admin_info"].(map[string]interface{})
	require.True(t, ok)
	_, ok = adminInfo["ollama_prompt_cache"]
	require.True(t, ok)
}

func TestAppendStreamStatusLeavesOrdinaryErrorsUnchanged(t *testing.T) {
	info := &relaycommon.RelayInfo{
		IsStream:     true,
		StreamStatus: relaycommon.NewStreamStatus(),
	}
	message := "ordinary upstream error: " + strings.Repeat("x", 8)
	info.StreamStatus.RecordError(message)

	other := map[string]interface{}{}
	appendStreamStatus(nil, info, other)

	streamStatus, ok := other["stream_status"].(map[string]interface{})
	require.True(t, ok)
	errors, ok := streamStatus["errors"].([]string)
	require.True(t, ok)
	require.Equal(t, []string{message}, errors)
}
