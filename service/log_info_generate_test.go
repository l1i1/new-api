package service

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAppendStreamStatusRedactsCyberPolicyErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	MarkOpsCyberPolicy(c, CyberPolicyMark{Message: "blocked"})

	info := &relaycommon.RelayInfo{
		IsStream:     true,
		StreamStatus: relaycommon.NewStreamStatus(),
	}
	secret := "API key provided: sk-live-stream-secret"
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, errors.New(secret))
	info.StreamStatus.RecordError(secret)

	other := map[string]interface{}{}
	appendStreamStatus(c, info, other)

	streamStatus, ok := other["stream_status"].(map[string]interface{})
	require.True(t, ok)
	require.NotContains(t, streamStatus["end_error"], "sk-live-stream-secret")
	errors, ok := streamStatus["errors"].([]string)
	require.True(t, ok)
	require.Len(t, errors, 1)
	require.NotContains(t, errors[0], "sk-live-stream-secret")
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
