package middleware

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installFitPolicyJSON compiles and installs a policy, failing the test on a
// policy the shipping code would reject.
func installFitPolicyJSON(t *testing.T, policyJSON string) {
	t.Helper()
	snapshot, err := fitpolicy.CompileJSON([]byte(policyJSON))
	require.NoError(t, err)
	fitpolicy.Install(snapshot)
}

// TestFitPolicyObserverSeparatesEveryExitPath is the reason the observer exists.
//
// Every early return in applyFitPolicy is silent, so a fault and a healthy "no
// opinion" look identical from outside. This walks all six paths and asserts
// each one lands on its own counter, which is what makes a divergence log that
// stays empty interpretable: without this, an empty log cannot be told apart
// from a policy that never ran.
func TestFitPolicyObserverSeparatesEveryExitPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fitpolicy.Install(nil)
	t.Cleanup(func() { fitpolicy.Install(nil) })

	builtin := string(mustBuiltinPolicyJSON(t))

	// A disabled policy: compiled, installed, and deliberately inert.
	var disabledDoc map[string]any
	require.NoError(t, json.Unmarshal([]byte(builtin), &disabledDoc))
	disabledDoc["enabled"] = false
	disabledJSON, err := json.Marshal(disabledDoc)
	require.NoError(t, err)

	t.Run("no snapshot", func(t *testing.T) {
		fitpolicy.Install(nil)
		before := fitPolicyStats.noSnapshot.Load()
		markV4OfficialPinFromDistributor(newFitPolicyContext(t, `{"model":"kimi-k3"}`))
		assert.Greater(t, fitPolicyStats.noSnapshot.Load(), before)
	})

	t.Run("installed but disabled", func(t *testing.T) {
		installFitPolicyJSON(t, string(disabledJSON))
		before := fitPolicyStats.disabled.Load()
		markV4OfficialPinFromDistributor(newFitPolicyContext(t, `{"model":"kimi-k3"}`))
		assert.Greater(t, fitPolicyStats.disabled.Load(), before)
	})

	t.Run("out of scope", func(t *testing.T) {
		installFitPolicyJSON(t, builtin)
		before := fitPolicyStats.outOfScope.Load()
		// The playground path is the case the scope rule exists for: it ends in
		// "/chat/completions", so the shipped pin predicate's suffix test reaches
		// it, while the policy's exact-path test must refuse it. A path that fails
		// the suffix test never arrives here at all and would prove nothing.
		markV4OfficialPinFromDistributor(
			newFitPolicyRequest(t, http.MethodPost, "/pg/chat/completions", `{"model":"kimi-k3"}`))
		assert.Greater(t, fitPolicyStats.outOfScope.Load(), before)
	})

	t.Run("explicit pin", func(t *testing.T) {
		installFitPolicyJSON(t, builtin)
		c := newFitPolicyContext(t, `{"model":"kimi-k3"}`)
		c.Set("specific_channel_id", 1)
		before := fitPolicyStats.explicitPin.Load()
		markV4OfficialPinFromDistributor(c)
		assert.Greater(t, fitPolicyStats.explicitPin.Load(), before)
	})

	t.Run("no opinion", func(t *testing.T) {
		installFitPolicyJSON(t, builtin)
		// DeepSeek V4 with explicit thinking-off matches no shipped rule, so the
		// policy has nothing to say and the legacy path stays in charge.
		before := fitPolicyStats.noOpinion.Load()
		markV4OfficialPinFromDistributor(
			newFitPolicyContext(t, `{"model":"deepseek-v4-flash","thinking":{"type":"disabled"}}`))
		assert.Greater(t, fitPolicyStats.noOpinion.Load(), before)
	})

	t.Run("evaluated", func(t *testing.T) {
		installFitPolicyJSON(t, builtin)
		before := fitPolicyStats.evaluated.Load()
		markV4OfficialPinFromDistributor(
			newFitPolicyContext(t, `{"model":"deepseek-v4-flash","temperature":0.7}`))
		assert.Greater(t, fitPolicyStats.evaluated.Load(), before,
			"a request the policy has an opinion about must be counted as evaluated")
	})

	t.Run("shadowed", func(t *testing.T) {
		// Shadow decides and records but acts on nothing. With the compiled-in
		// predicates retired there is no second opinion left to disagree with, so
		// what matters is that a dry run reaches the counter and that the request
		// context stays untouched — that is what makes a policy edit reviewable
		// before it is allowed to route.
		installFitPolicyJSON(t, builtin)
		before := fitPolicyStats.shadowed.Load()
		c := newFitPolicyContext(t, `{"model":"kimi-k3","tool_choice":"required"}`)
		markV4OfficialPinFromDistributor(c)
		assert.Greater(t, fitPolicyStats.shadowed.Load(), before,
			"a shadow decision must be counted")
		_, attached := common.GetContextKeyType[fitpolicy.Requirement](c, constant.ContextKeyFitRequirement)
		assert.False(t, attached, "shadow must not attach a requirement to the request")
	})

	t.Run("live attaches the requirement", func(t *testing.T) {
		// The counterpart to the shadow case: outside shadow the very same request
		// must reach the selector, or the migration would leave nothing pinning.
		var liveDoc map[string]any
		require.NoError(t, json.Unmarshal([]byte(builtin), &liveDoc))
		liveDoc["shadow"] = false
		liveJSON, err := json.Marshal(liveDoc)
		require.NoError(t, err)
		installFitPolicyJSON(t, string(liveJSON))

		before := fitPolicyStats.evaluated.Load()
		c := newFitPolicyContext(t, `{"model":"kimi-k3","tool_choice":"required"}`)
		markV4OfficialPinFromDistributor(c)

		requirement, attached := common.GetContextKeyType[fitpolicy.Requirement](c, constant.ContextKeyFitRequirement)
		assert.Greater(t, fitPolicyStats.evaluated.Load(), before)
		require.True(t, attached, "a live policy must attach the requirement")
		assert.Contains(t, requirement.Marks, fitpolicy.BehaviorToolsChoiceSemantics,
			"the K3 tool-choice rule must require its behaviour mark")
	})
}
