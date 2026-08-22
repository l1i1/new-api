package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMarkAndGetOpsCyberPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())

	require.Nil(t, GetOpsCyberPolicy(context))
	MarkOpsCyberPolicy(context, CyberPolicyMark{
		Code:           "ignored-code",
		Message:        "  blocked  ",
		Body:           "  {\"error\":{\"code\":\"cyber_policy\"}}  ",
		UpstreamStatus: 400,
	})

	mark := GetOpsCyberPolicy(context)
	require.NotNil(t, mark)
	require.Equal(t, "cyber_policy", mark.Code)
	require.Equal(t, "blocked", mark.Message)
	require.Equal(t, `{"error":{"code":"cyber_policy"}}`, mark.Body)
	require.Equal(t, 400, mark.UpstreamStatus)
}

func TestNewOpenAICyberPolicyErrorMarksUsageAndSkipsRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	err := NewOpenAICyberPolicyError(context, []byte(`{"error":{"code":" CYBER_POLICY ","message":"blocked"}}`), http.StatusBadRequest, false, &dto.Usage{InputTokens: 12, OutputTokens: 3})
	require.NotNil(t, err)
	require.Equal(t, types.ErrorCode("cyber_policy"), err.GetErrorCode())
	require.True(t, types.IsSkipRetryError(err))
	mark := GetOpsCyberPolicy(context)
	require.NotNil(t, mark)
	require.Equal(t, 12, mark.UpstreamInTok)
	require.Equal(t, 3, mark.UpstreamOutTok)
	require.Equal(t, "blocked", mark.Message)
	require.LessOrEqual(t, len(mark.Body), 4096)
}

func TestNewOpenAICyberPolicyErrorBoundsBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	payload := `{"error":{"code":"cyber_policy","message":"blocked"},"padding":"` + strings.Repeat("x", 5000) + `"}`
	err := NewOpenAICyberPolicyError(context, []byte(payload), http.StatusBadRequest, false, nil)
	require.NotNil(t, err)
	require.Len(t, GetOpsCyberPolicy(context).Body, 4096)
}

func TestNewOpenAICyberPolicyErrorWithoutContextStillReturnsTerminalError(t *testing.T) {
	err := NewOpenAICyberPolicyError(nil, []byte(`{"error":{"code":"cyber_policy"}}`), http.StatusBadRequest, false, nil)
	require.NotNil(t, err)
	require.Equal(t, types.ErrorCode("cyber_policy"), err.GetErrorCode())
	require.True(t, types.IsSkipRetryError(err))
}

func TestNewOpenAICyberPolicyErrorClampsUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	_ = NewOpenAICyberPolicyError(context, []byte(`{"error":{"code":"cyber_policy"}}`), http.StatusBadRequest, false, &dto.Usage{
		InputTokens:  common.MaxQuota,
		OutputTokens: common.MaxQuota,
	})
	mark := GetOpsCyberPolicy(context)
	require.Equal(t, common.MaxQuota/2, mark.UpstreamInTok)
	require.Equal(t, common.MaxQuota/2, mark.UpstreamOutTok)
}

func TestMarkOpsCyberPolicyFirstWins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())

	MarkOpsCyberPolicy(context, CyberPolicyMark{Message: "first"})
	MarkOpsCyberPolicy(context, CyberPolicyMark{Message: "second"})
	require.Equal(t, "first", GetOpsCyberPolicy(context).Message)
}

func TestMarkOpsCyberPolicyNilContext(t *testing.T) {
	MarkOpsCyberPolicy(nil, CyberPolicyMark{Message: "ignored"})
	ClearOpsCyberPolicy(nil)
	require.Nil(t, GetOpsCyberPolicy(nil))
}

func TestClearOpsCyberPolicyAllowsRemark(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())

	MarkOpsCyberPolicy(context, CyberPolicyMark{Message: "first"})
	ClearOpsCyberPolicy(context)
	require.Nil(t, GetOpsCyberPolicy(context))

	MarkOpsCyberPolicy(context, CyberPolicyMark{Message: "second"})
	require.Equal(t, "second", GetOpsCyberPolicy(context).Message)
}

func TestDetectOpenAICyberPolicy(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		hit     bool
		message string
	}{
		{name: "top-level error", payload: `{"error":{"code":"cyber_policy","message":"blocked"}}`, hit: true, message: "blocked"},
		{name: "response wrapped", payload: `{"response":{"error":{"code":" CYBER_POLICY ","message":"  blocked  "}}}`, hit: true, message: "blocked"},
		{name: "nested wins over unrelated top-level code", payload: `{"error":{"code":"server_error","message":"generic"},"response":{"error":{"code":"cyber_policy","message":"nested blocked"}}}`, hit: true, message: "nested blocked"},
		{name: "case insensitive", payload: `{"error":{"code":"Cyber_Policy"}}`, hit: true},
		{name: "different policy", payload: `{"error":{"code":"content_policy"}}`},
		{name: "message only", payload: `{"error":{"message":"cyber_policy"}}`},
		{name: "empty payload", payload: ``},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hit, code, message := detectOpenAICyberPolicy([]byte(test.payload))
			require.Equal(t, test.hit, hit)
			if test.hit {
				require.Equal(t, "cyber_policy", code)
				require.Equal(t, test.message, message)
				return
			}
			require.Empty(t, code)
			require.Empty(t, message)
		})
	}
}
