package logger

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestLogDebugMasksAndBoundsPayload(t *testing.T) {
	originalDebug := common.DebugEnabled
	common.DebugEnabled = true
	t.Cleanup(func() { common.DebugEnabled = originalDebug })

	var logs bytes.Buffer
	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logs
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
	})

	secret := "sk-debug-log-secret-123456"
	LogDebug(context.Background(), "request body: %s", `{"content":"`+secret+`"}`+strings.Repeat("x", common.LocalLogContentLimit))

	require.NotContains(t, logs.String(), secret)
	require.Contains(t, logs.String(), "[truncated")
}
