package ollama

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A client that recomputes its output budget on every turn must keep one
// partition. ZCode sends a fresh max_output_tokens per request (remaining
// context), the OpenAI conversion maps it to num_predict, and before the
// generation-only filter every request of one conversation landed in a new
// partition, so the estimator could never hit.
func TestOllamaPromptCacheIdentityIgnoresGenerationOnlyOptions(t *testing.T) {
	base := &OllamaChatRequest{
		Model:    "deepseek-v4.1-flash",
		Messages: []OllamaChatMessage{{Role: "user", Content: "hello"}},
		Options:  map[string]any{"num_predict": 346892, "temperature": 0.6, "seed": 1, "stop": []any{"x"}},
	}
	nextTurn := *base
	nextTurn.Options = map[string]any{"num_predict": 337650, "temperature": 0.2, "seed": 99, "stop": []any{"y"}}

	baseIdentity := buildOllamaChatPromptCacheIdentity(base)
	nextIdentity := buildOllamaChatPromptCacheIdentity(&nextTurn)
	assert.Equal(t, baseIdentity.KeyMaterial, nextIdentity.KeyMaterial)

	info := infoWith(1, 10, "deepseek-v4.1-flash", dto.ChannelSettings{OllamaCacheEstimationEnabled: true}, nil)
	info.RequestHeaders = map[string]string{"x-session-id": "sess-247108a9"}
	assert.Equal(t,
		buildPromptCacheKeyWithIdentity(info, baseIdentity),
		buildPromptCacheKeyWithIdentity(info, nextIdentity),
	)

	// A prompt-affecting option and an unknown option must still isolate: an
	// unrecognized option could change the prompt too.
	withContextWindow := *base
	withContextWindow.Options = map[string]any{"num_ctx": 4096}
	assert.NotEqual(t, baseIdentity.KeyMaterial, buildOllamaChatPromptCacheIdentity(&withContextWindow).KeyMaterial)

	withUnknown := *base
	withUnknown.Options = map[string]any{"mirostat": 2}
	assert.NotEqual(t, baseIdentity.KeyMaterial, buildOllamaChatPromptCacheIdentity(&withUnknown).KeyMaterial)

	generate := &OllamaGenerateRequest{Model: "deepseek-v4.1-flash", Prompt: "hello", Options: map[string]any{"num_predict": 10}}
	generateNext := *generate
	generateNext.Options = map[string]any{"num_predict": 20}
	assert.Equal(t,
		buildOllamaGeneratePromptCacheIdentity(generate).KeyMaterial,
		buildOllamaGeneratePromptCacheIdentity(&generateNext).KeyMaterial,
	)
}

// End-to-end shape of the production miss: the same session continues its
// conversation while the per-request budget keeps changing. The second turn
// must be billed through the estimated cache path.
func TestOllamaPromptCacheEstimatorHitsWhenBudgetChangesEachTurn(t *testing.T) {
	resetPromptCache()
	gin.SetMode(gin.TestMode)
	setting := dto.ChannelSettings{OllamaCacheEstimationEnabled: true}
	info := infoWith(1, 10, "deepseek-v4.1-flash", setting, nil)
	info.RequestHeaders = map[string]string{"x-session-id": "sess-247108a9"}

	first := &dto.GeneralOpenAIRequest{
		Model:     "deepseek-v4.1-flash",
		Messages:  msgs("user", "first turn"),
		MaxTokens: common.GetPointer(uint(346892)),
	}
	firstCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	firstUsage := &dto.Usage{PromptTokens: 200}
	runOllamaPromptCacheTurn(t, firstCtx, info, first, firstUsage)
	assert.Zero(t, firstUsage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, "cold_miss", ollamaPromptCacheOutcome(t, firstCtx))

	second := &dto.GeneralOpenAIRequest{
		Model:     "deepseek-v4.1-flash",
		Messages:  msgs("user", "first turn", "assistant", "answer", "user", "second turn"),
		MaxTokens: common.GetPointer(uint(337650)),
	}
	secondCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	secondUsage := &dto.Usage{PromptTokens: 300}
	runOllamaPromptCacheTurn(t, secondCtx, info, second, secondUsage)

	assert.Equal(t, 200, secondUsage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, "hit_estimated", ollamaPromptCacheOutcome(t, secondCtx))
	require.NotNil(t, secondUsage.BillingUsage)
	assert.True(t, secondUsage.BillingUsage.Estimated)

	// A genuinely different prompt must not reuse that estimate.
	other := &dto.GeneralOpenAIRequest{
		Model:     "deepseek-v4.1-flash",
		Messages:  msgs("user", "unrelated question"),
		MaxTokens: common.GetPointer(uint(337650)),
	}
	otherCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	otherUsage := &dto.Usage{PromptTokens: 300}
	runOllamaPromptCacheTurn(t, otherCtx, info, other, otherUsage)
	assert.Zero(t, otherUsage.PromptTokensDetails.CachedTokens)
}

func runOllamaPromptCacheTurn(t *testing.T, c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest, usage *dto.Usage) {
	t.Helper()
	converted, err := openAIChatToOllamaChat(c, request)
	require.NoError(t, err)
	jsonData, err := common.Marshal(converted)
	require.NoError(t, err)
	body, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
	require.NoError(t, err)
	defer closer.Close()

	captureOllamaPromptCacheIdentity(c, info, body)
	applyOllamaPromptCacheEstimation(info, usage, c)
}

func ollamaPromptCacheOutcome(t *testing.T, c *gin.Context) string {
	t.Helper()
	value, exists := c.Get(string(constant.ContextKeyOllamaPromptCache))
	require.True(t, exists)
	observation, ok := value.(promptCacheObservation)
	require.True(t, ok)
	return observation.Outcome
}
