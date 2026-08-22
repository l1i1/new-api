package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenaiHandlerFormatsCyberPolicyForClaudeClients(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"cyber_policy","message":"blocked"},"usage":{"prompt_tokens":3,"completion_tokens":1}}`)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		RelayFormat: types.RelayFormatClaude,
	}

	usage, err := OpenaiHandler(c, info, resp)
	require.NotNil(t, err)
	require.Equal(t, types.ErrorCode("cyber_policy"), err.GetErrorCode())
	require.Equal(t, 3, usage.PromptTokens)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"type":"error"`)
	require.Contains(t, recorder.Body.String(), `"type":"cyber_policy"`)
	require.NotContains(t, recorder.Body.String(), `"choices"`)
}

func TestOpenaiHandlerFormatsCyberPolicyForGeminiClients(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"cyber_policy","message":"blocked"}}`)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		RelayFormat: types.RelayFormatGemini,
	}

	_, err := OpenaiHandler(c, info, resp)
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"status":"CYBER_POLICY"`)
	require.Contains(t, recorder.Body.String(), `"code":400`)
}
