package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsOfficialQuotaExhaustedFailureMatchesLiveWordings pins the 2026-10-30
// official 429 wordings that mean "this channel's capacity is spent" rather
// than "the engine is down".
func TestIsOfficialQuotaExhaustedFailureMatchesLiveWordings(t *testing.T) {
	for _, message := range []string{
		"Your credit balance is running low for account <org-37af7c6850d74943a694e6c173b7c73d>, please top up",
		"You exceeded your current token quota: <org-a38b31ed1aa74d9f82ff79d78025d41c> 11841805, please upgrade",
		`{"error":{"message":"insufficient_quota","type":"insufficient_quota"}}`,
		"Your account org-4b483f98ff734090b8b15bcc2c0af213 / proj-51c8096bc7784c54 TPM rate limit reached",
	} {
		require.True(t, IsOfficialQuotaExhaustedFailure(message), message)
	}
	require.False(t, IsOfficialQuotaExhaustedFailure("upstream stream ended without a terminal finish_reason"))
	require.False(t, IsOfficialQuotaExhaustedFailure(""))
}

// TestPinnedQuotaExhausted429RelaxesThePin pins the availability contract: the
// official channel reports its quota is spent, so the remaining attempts may use
// the low-thinking fallback pool instead of dying as a client-visible 429. The
// controller still prefers rotating to a sibling credential first, so the pin is
// only given up, not skipped.
func TestPinnedQuotaExhausted429RelaxesThePin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
	err := types.NewOpenAIError(
		errors.New("Your credit balance is running low for account <org-37af7c6850d74943a694e6c173b7c73d>, please top up"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusTooManyRequests,
	)
	decision := DecideRelayRetry(c, err, 3)
	assert.Equal(t, "retry", decision.Action)
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyOfficialPinRelaxed),
		"the pin must step aside so the fallback pool can serve and its rejections are not read as official verdicts")
}

// TestPinnedVerdictDoesNotRelaxOnQuotaWords pins the other direction: a verdict
// (a request-level rejection) keeps the pin even if its text happens to mention
// quota, because failing over cannot produce a different answer.
func TestPinnedVerdictDoesNotRelaxOnQuotaWords(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
	err := types.NewOpenAIError(
		errors.New("insufficient_quota"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadRequest,
	)
	_ = DecideRelayRetry(c, err, 3)
	assert.False(t, common.GetContextKeyBool(c, constant.ContextKeyOfficialPinRelaxed))
}
