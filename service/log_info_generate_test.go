package service

import (
	"encoding/json"
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

// TestAppendStreamStatusMarksClosedAfterTerminal keeps the tail close countable
// from /api/log/ alone: when the provider sent a terminal finish_reason and then
// kept the stream open, the success row has to say the relay closed it, not the
// transport (2026-10-29 neurvibe ch48; without the marker the only record would
// be a container log line that rotates away).
func TestAppendStreamStatusMarksClosedAfterTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	info := &relaycommon.RelayInfo{
		IsStream:           true,
		StreamStatus:       relaycommon.NewStreamStatus(),
		StreamFinishReason: "tool_calls",
	}
	info.StreamStatus.MarkTerminalFinishSeen()
	info.StreamStatus.MarkClosedAfterTerminal()
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)

	other := model.NewLogOther()
	appendStreamStatus(c, info, other)

	streamStatus, ok := other.Snapshot()["stream_status"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "ok", streamStatus["status"])
	assert.Equal(t, "done", streamStatus["end_reason"])
	assert.Equal(t, true, streamStatus["closed_after_terminal"])

	// An ordinary stream must not carry the marker.
	plain := &relaycommon.RelayInfo{
		IsStream:           true,
		StreamStatus:       relaycommon.NewStreamStatus(),
		StreamFinishReason: "stop",
	}
	plain.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	plainOther := model.NewLogOther()
	appendStreamStatus(c, plain, plainOther)
	plainStatus, ok := plainOther.Snapshot()["stream_status"].(map[string]interface{})
	require.True(t, ok)
	assert.NotContains(t, plainStatus, "closed_after_terminal")
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

// adminInfoFromJSON renders a LogOther through its wire format and returns the
// admin_info object as generic JSON values. The console reads these exact keys,
// so the tests assert on the marshalled shape rather than on Go structs.
func adminInfoFromJSON(t *testing.T, other *model.LogOther) map[string]any {
	t.Helper()
	encoded, err := other.MarshalJSON()
	require.NoError(t, err)
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	adminRaw, ok := decoded["admin_info"]
	require.True(t, ok, "admin_info must be present")
	var admin map[string]any
	require.NoError(t, json.Unmarshal(adminRaw, &admin))
	return admin
}

// TestAppendRelayLogAdminInfoWritesNodeBodyBytesAndContinuation pins the
// observability fields the /usage-logs/common page reads: which node served the
// request, the three payload sizes, and the compact continuation summary. All
// three come from data already in memory (no body reads during log writing).
func TestAppendRelayLogAdminInfoWritesNodeBodyBytesAndContinuation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldNodeName := common.NodeName
	common.NodeName = "i-2ze0obstest"
	t.Cleanup(func() { common.NodeName = oldNodeName })

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}]}`)
	storage, err := common.CreateBodyStorage(body)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })
	c.Set(common.KeyBodyStorage, storage)
	written, err := c.Writer.WriteString("response-body")
	require.NoError(t, err)
	require.Equal(t, len("response-body"), written)

	info := &relaycommon.RelayInfo{
		UpstreamRequestBytes: int64(len(body) + 16),
		ChatContinuationAudit: &relaycommon.ChatContinuationAudit{
			Modes:                 []string{"length"},
			Rounds:                2,
			MaxSteps:              2,
			Finish:                "stop",
			ExtraCompletionTokens: 4096,
		},
	}
	other := model.NewLogOther()
	AppendRelayLogAdminInfo(c, info, other)

	admin := adminInfoFromJSON(t, other)
	assert.Equal(t, "i-2ze0obstest", admin["node_name"])

	bodyBytes, ok := admin["body_bytes"].(map[string]any)
	require.True(t, ok, "known sizes must be recorded")
	assert.Len(t, bodyBytes, 3)
	assert.EqualValues(t, len(body), bodyBytes["request"], "cached (decompressed) client body size")
	assert.EqualValues(t, len(body)+16, bodyBytes["upstream_request"], "last upstream attempt body size")
	assert.EqualValues(t, len("response-body"), bodyBytes["response"], "bytes written to the client")

	continuation, ok := admin["continuation"].(map[string]any)
	require.True(t, ok, "a repaired response must carry its continuation audit")
	assert.Len(t, continuation, 5, "exactly the contract keys, nothing else")
	assert.Equal(t, []any{"length"}, continuation["modes"])
	assert.EqualValues(t, 2, continuation["rounds"])
	assert.EqualValues(t, 2, continuation["max_steps"])
	assert.Equal(t, "stop", continuation["finish"])
	assert.EqualValues(t, 4096, continuation["extra_completion_tokens"])
	assert.NotContains(t, continuation, "mode", "the legacy single-mode key must not be emitted")
	assert.NotContains(t, continuation, "content_chars")
}

// TestAppendRelayLogAdminInfoBodyBytesFallbackAndOmission pins the cheap/safe
// half of the body_bytes contract: ContentLength is the fallback when no cached
// storage exists (the size readings never create one), each 0/unknown size is
// omitted individually, and an ordinary request does not grow an empty object.
func TestAppendRelayLogAdminInfoBodyBytesFallbackAndOmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := `{"model":"kimi-k3"}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	require.NotZero(t, c.Request.ContentLength)

	other := model.NewLogOther()
	AppendRelayLogAdminInfo(c, &relaycommon.RelayInfo{UpstreamRequestBytes: 777}, other)
	admin := adminInfoFromJSON(t, other)
	bodyBytes, ok := admin["body_bytes"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, len(body), bodyBytes["request"], "ContentLength fallback when nothing was buffered")
	assert.EqualValues(t, 777, bodyBytes["upstream_request"])
	assert.NotContains(t, bodyBytes, "response", "nothing written means unknown, not zero")

	// No payload information at all: omit the object entirely.
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	other2 := model.NewLogOther()
	AppendRelayLogAdminInfo(c2, &relaycommon.RelayInfo{}, other2)
	admin2 := adminInfoFromJSON(t, other2)
	assert.NotContains(t, admin2, "body_bytes")
	assert.NotContains(t, admin2, "continuation")
}

// TestAppendRelayLogAdminInfoCarriesContinuationAudit pins the consume-log side
// of a repaired response: the merged continuation reaches admin_info.continuation
// with the contract shape, and an ordinary (or zero-round) response stays clean.
func TestAppendRelayLogAdminInfoCarriesContinuationAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	info := &relaycommon.RelayInfo{
		ChatContinuationAudit: &relaycommon.ChatContinuationAudit{
			Modes:                 []string{"length"},
			Rounds:                2,
			MaxSteps:              3,
			Finish:                "stop",
			ExtraCompletionTokens: 4096,
		},
		ClosedAsToolCalls: true,
	}
	other := model.NewLogOther()
	AppendRelayLogAdminInfo(c, info, other)

	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]interface{})
	require.True(t, ok)
	audit, ok := adminInfo["continuation"].(*relaycommon.ChatContinuationAudit)
	require.True(t, ok, "a repaired response must carry its continuation audit")
	assert.Equal(t, []string{"length"}, audit.Modes)
	assert.Equal(t, 2, audit.Rounds)
	assert.Equal(t, 3, audit.MaxSteps)
	assert.Equal(t, "stop", audit.Finish)
	assert.Equal(t, 4096, audit.ExtraCompletionTokens)
	closed, ok := adminInfo["closed_as_tool_calls"].(bool)
	require.True(t, ok, "a closed cut must carry the closure marker")
	assert.True(t, closed)

	// An ordinary response stays clean: no continuation marker is written.
	plain := &relaycommon.RelayInfo{}
	otherPlain := model.NewLogOther()
	AppendRelayLogAdminInfo(c, plain, otherPlain)
	adminPlain, ok := otherPlain.Snapshot()["admin_info"].(map[string]interface{})
	require.True(t, ok)
	_, hasAudit := adminPlain["continuation"]
	_, hasClosed := adminPlain["closed_as_tool_calls"]
	assert.False(t, hasAudit)
	assert.False(t, hasClosed)

	// A non-nil audit with zero rounds must not be advertised either.
	zero := &relaycommon.RelayInfo{ChatContinuationAudit: &relaycommon.ChatContinuationAudit{}}
	otherZero := model.NewLogOther()
	AppendRelayLogAdminInfo(c, zero, otherZero)
	adminZero, ok := otherZero.Snapshot()["admin_info"].(map[string]interface{})
	require.True(t, ok)
	_, hasZeroAudit := adminZero["continuation"]
	assert.False(t, hasZeroAudit)
}

// TestAppendRelayLogAdminInfoIsNilSafe keeps log generation panic-free on the
// paths that have no gin context: the context-free fields are still recorded,
// and nothing reads a nil request/writer.
func TestAppendRelayLogAdminInfoIsNilSafe(t *testing.T) {
	other := model.NewLogOther()
	AppendRelayLogAdminInfo(nil, &relaycommon.RelayInfo{
		ChatContinuationAudit: &relaycommon.ChatContinuationAudit{
			Modes:    []string{"missing"},
			Rounds:   1,
			MaxSteps: 1,
			Finish:   "stop",
		},
	}, other)
	admin := adminInfoFromJSON(t, other)
	require.Contains(t, admin, "continuation")
	assert.NotContains(t, admin, "body_bytes")
	assert.NotContains(t, admin, "use_channel")

	// No context, no relay info, no log object at all: no panic.
	AppendRelayLogAdminInfo(nil, nil, nil)
	AppendRelayLogAdminInfo(nil, nil, model.NewLogOther())
}
