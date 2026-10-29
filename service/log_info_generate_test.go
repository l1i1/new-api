package service

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
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

	other := model.NewLogOther()
	appendStreamStatus(c, info, other)

	streamStatus, ok := other.Snapshot()["stream_status"].(map[string]interface{})
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
	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]interface{})
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

	other := model.NewLogOther()
	appendStreamStatus(nil, info, other)

	streamStatus, ok := other.Snapshot()["stream_status"].(map[string]interface{})
	require.True(t, ok)
	errors, ok := streamStatus["errors"].([]string)
	require.True(t, ok)
	require.Equal(t, []string{message}, errors)
}

// TestAppendRelayLogAdminInfoCarriesStreamCutDiagnostics keeps the promise that
// makes an unrepaired stream cut explainable from /api/log/: the failure row has
// to carry the budget and configuration the repair path saw. Without it the only
// record was a container log line (2026-10-29: a 986s cut could not be explained
// after the fact).
func TestAppendRelayLogAdminInfoCarriesStreamCutDiagnostics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	info := &relaycommon.RelayInfo{
		StreamCutDiagnostics: &relaycommon.StreamCutDiagnostics{
			FinishReason:           "error",
			ReasoningChars:         48000,
			ClientMaxTokens:        32000,
			CompletionTokens:       32000,
			BudgetLeft:             0,
			ContinuationConfigured: true,
			AbnormalConfigured:     true,
		},
	}
	other := model.NewLogOther()
	AppendRelayLogAdminInfo(c, info, other)

	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]interface{})
	require.True(t, ok)
	cut, ok := adminInfo["stream_cut"].(*relaycommon.StreamCutDiagnostics)
	require.True(t, ok, "an undelivered cut must reach the failure row")
	assert.Equal(t, "error", cut.FinishReason)
	assert.Equal(t, 0, cut.BudgetLeft)

	// The wire shape is the operator-facing contract, so pin the JSON keys too.
	encoded, err := other.MarshalJSON()
	require.NoError(t, err)
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	var admin map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(decoded["admin_info"], &admin))
	var cutJSON map[string]any
	require.NoError(t, json.Unmarshal(admin["stream_cut"], &cutJSON))
	assert.Equal(t, "error", cutJSON["finish_reason"])
	assert.EqualValues(t, 0, cutJSON["budget_left"])
	assert.EqualValues(t, 48000, cutJSON["reasoning_chars"])
	assert.Equal(t, true, cutJSON["continuation_configured"])
	assert.NotContains(t, cutJSON, "repair_attempted", "a repair that never ran must not be advertised")

	// A request that did not cut must not grow the marker.
	other2 := model.NewLogOther()
	AppendRelayLogAdminInfo(c, &relaycommon.RelayInfo{}, other2)
	adminInfo2, _ := other2.Snapshot()["admin_info"].(map[string]interface{})
	_, ok = adminInfo2["stream_cut"]
	assert.False(t, ok)
}
