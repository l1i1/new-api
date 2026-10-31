package model

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/nodemetrics"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecordConsumeLogCountsTokensWhenLoggingDisabled pins the ordering rule of
// the node-metrics hook: settled tokens are counted even when consume logging is
// disabled, and the log path never counts requests (only middleware.NodeMetrics
// does that, so the two counters must not double count).
func TestRecordConsumeLogCountsTokensWhenLoggingDisabled(t *testing.T) {
	previous := common.LogConsumeEnabled
	common.LogConsumeEnabled = false
	t.Cleanup(func() {
		common.LogConsumeEnabled = previous
	})

	before := nodemetrics.Snapshot(time.Now())
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	RecordConsumeLog(ctx, 1, RecordConsumeLogParams{
		ChannelId:        1,
		PromptTokens:     7,
		CompletionTokens: 5,
		ModelName:        "test-model",
		TokenName:        "test-token",
	})
	after := nodemetrics.Snapshot(time.Now())

	require.Equal(t, int64(7), after.PromptTokensTotal-before.PromptTokensTotal)
	require.Equal(t, int64(5), after.CompletionTokensTotal-before.CompletionTokensTotal)
	require.Equal(t, int64(12), after.TokensTotal-before.TokensTotal)
	assert.Zero(t, after.RequestsTotal-before.RequestsTotal, "consume-log token hook must not count requests")
}
