package relay

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newOllamaValidationContext(t *testing.T, path string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOllama)
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "llama3")
	return c
}

func TestOllamaTextValidationReturnsClientError(t *testing.T) {
	c := newOllamaValidationContext(t, "/v1/completions")
	request := &dto.GeneralOpenAIRequest{Model: "llama3", Prompt: []any{"first", "second"}}
	apiErr := TextHelper(c, &relaycommon.RelayInfo{
		Request:         request,
		OriginModelName: "llama3",
		RelayMode:       relayconstant.RelayModeCompletions,
		RequestURLPath:  "/v1/completions",
	})

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.True(t, types.IsSkipRetryError(apiErr))
}

func TestOllamaEmbeddingValidationReturnsClientError(t *testing.T) {
	c := newOllamaValidationContext(t, "/v1/embeddings")
	request := &dto.EmbeddingRequest{Model: "embed-model", Input: []any{"valid", 42}}
	apiErr := EmbeddingHelper(c, &relaycommon.RelayInfo{
		Request:         request,
		OriginModelName: "embed-model",
		RelayMode:       relayconstant.RelayModeEmbeddings,
		RequestURLPath:  "/v1/embeddings",
	})

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.True(t, types.IsSkipRetryError(apiErr))
}

func TestOllamaRerankPassThroughStillReturnsClientError(t *testing.T) {
	var upstreamCalled atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		upstreamCalled.Store(true)
	}))
	defer upstream.Close()

	c := newOllamaValidationContext(t, "/v1/rerank")
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/rerank", bytes.NewBufferString(`{"model":"llama3","query":"q","documents":["d"]}`))
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
	common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: true})
	t.Cleanup(func() { common.CleanupBodyStorage(c) })

	apiErr := RerankHelper(c, &relaycommon.RelayInfo{
		Request:        &dto.RerankRequest{Model: "llama3", Query: "q", Documents: []any{"d"}},
		RelayMode:      relayconstant.RelayModeRerank,
		RequestURLPath: "/v1/rerank",
	})

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.False(t, upstreamCalled.Load())
}
