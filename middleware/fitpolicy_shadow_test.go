package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	rootdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFitPolicyRequest builds a distributor context with the Route dimension on
// for every family, so the legacy predicate decides the pin.
func newFitPolicyRequest(t *testing.T, method, path, body string) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{
		OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"*": {Route: true},
		}},
	})
	return c
}

func newFitPolicyContext(t *testing.T, body string) *gin.Context {
	t.Helper()
	return newFitPolicyRequest(t, http.MethodPost, fitPolicyScopedPath, body)
}

func pinFor(t *testing.T, body string) bool {
	t.Helper()
	c := newFitPolicyContext(t, body)
	markV4OfficialPinFromDistributor(c)
	return common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin)
}

func fitRequirementAttached(c *gin.Context) bool {
	_, attached := common.GetContextKey(c, constant.ContextKeyFitRequirement)
	return attached
}

// TestFitPolicyShadowDoesNotChangePin is the shadow-mode safety gate: installing
// a policy — even the built-in one proven equivalent — must not move a single
// pin. If this ever fails, shadow has become a behaviour change.
func TestFitPolicyShadowDoesNotChangePin(t *testing.T) {
	bodies := []string{
		`{"model":"kimi-k3","temperature":0.7}`,
		`{"model":"kimi-k3","tool_choice":"required"}`,
		`{"model":"kimi-k3","thinking":{"type":"disabled"}}`,
		`{"model":"kimi-k3","response_format":{"type":"json_object"}}`,
		`{"model":"deepseek-v4-flash","temperature":0.7}`,
		`{"model":"deepseek-v4-flash","thinking":{"type":"disabled"}}`,
		`{"model":"deepseek-v4-flash","logprobs":true,"thinking":{"type":"disabled"}}`,
		`{"model":"glm-5.3","temperature":0.7}`,
	}

	gin.SetMode(gin.TestMode)
	fitpolicy.Install(nil)
	t.Cleanup(func() { fitpolicy.Install(nil) })

	before := make(map[string]bool, len(bodies))
	for _, body := range bodies {
		before[body] = pinFor(t, body)
	}

	builtin, err := fitpolicy.CompileJSON(mustBuiltinPolicyJSON(t))
	require.NoError(t, err)
	fitpolicy.Install(builtin)

	for _, body := range bodies {
		assert.Equalf(t, before[body], pinFor(t, body), "pin changed for %s", body)
	}
}

// TestFitPolicyShadowReportsDivergenceWithoutRerouting proves the other half of
// shadow: a policy that disagrees with the shipped predicate is observed and
// counted, but the request still routes on the legacy decision.
func TestFitPolicyShadowReportsDivergenceWithoutRerouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fitpolicy.Install(nil)
	t.Cleanup(func() { fitpolicy.Install(nil); fitPolicyDivergenceCount.Store(0) })

	// A policy that demands the whole kimi-k3 family for every shape, while the
	// shipped predicate leaves a plain request unpinned.
	snapshot := mustInstallPolicy(t, divergentKimiPolicy, true)

	before := fitPolicyDivergenceCount.Load()
	body := `{"model":"kimi-k3","temperature":0.7}`

	// The legacy predicate leaves this servable shape unpinned, and shadow must
	// not promote the policy's opinion into a pin.
	assert.False(t, pinFor(t, body), "shadow must not narrow routing")
	assert.Greater(t, fitPolicyDivergenceCount.Load(), before, "shadow must count the divergence")

	// Sanity: the divergence is real, not a fixture artefact.
	requirement := snapshot.Decide("kimi-k3", true, fitpolicy.RequestView{Model: "kimi-k3"})
	assert.True(t, requirement.HasOpinion(), "the divergent policy is expected to have an opinion here")
}

// TestFitPolicyAttachesRequirementOnlyOutsideShadow is the structural half of
// the shadow guarantee: while shadow is on, no requirement reaches the context,
// so no selector can act on one even if it wanted to.
func TestFitPolicyAttachesRequirementOnlyOutsideShadow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fitpolicy.Install(nil)
	t.Cleanup(func() { fitpolicy.Install(nil) })

	body := `{"model":"kimi-k3","temperature":0.7}`

	mustInstallPolicy(t, divergentKimiPolicy, true)
	shadowContext := newFitPolicyContext(t, body)
	markV4OfficialPinFromDistributor(shadowContext)
	assert.False(t, fitRequirementAttached(shadowContext), "shadow must not attach a requirement")

	mustInstallPolicy(t, divergentKimiPolicy, false)
	liveContext := newFitPolicyContext(t, body)
	markV4OfficialPinFromDistributor(liveContext)
	assert.True(t, fitRequirementAttached(liveContext), "a non-shadow policy must attach its requirement")
}

// TestFitPolicyScopeAndPinGating pins the exact scope: only the versioned chat
// completions path is governed, and an explicit pin always wins.
func TestFitPolicyScopeAndPinGating(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fitpolicy.Install(nil)
	t.Cleanup(func() { fitpolicy.Install(nil) })

	mustInstallPolicy(t, divergentKimiPolicy, false)
	body := `{"model":"kimi-k3","temperature":0.7}`

	inScope := map[string]bool{
		fitPolicyScopedPath: true,
	}
	outOfScope := []string{
		"/pg/chat/completions", // same suffix as the scoped path; must not leak
		"/v1/completions",
		"/v1/messages",
		"/v1/responses/compact",
		"/v1/alpha/search",
		"/v1/embeddings",
		"/v1/models/gemini-pro:generateContent",
		"/v1/realtime",
	}

	for path, want := range inScope {
		c := newFitPolicyRequest(t, http.MethodPost, path, body)
		markV4OfficialPinFromDistributor(c)
		assert.Equalf(t, want, fitRequirementAttached(c), "path %s", path)
	}
	for _, path := range outOfScope {
		c := newFitPolicyRequest(t, http.MethodPost, path, body)
		markV4OfficialPinFromDistributor(c)
		assert.Falsef(t, fitRequirementAttached(c), "path %s must keep today's behaviour", path)
	}

	// A non-POST request never reaches channel selection with a body to classify.
	getContext := newFitPolicyRequest(t, http.MethodGet, fitPolicyScopedPath, body)
	markV4OfficialPinFromDistributor(getContext)
	assert.False(t, fitRequirementAttached(getContext), "a GET must not attach a requirement")

	// Explicit token pin: the request is already bound to one channel.
	tokenPinned := newFitPolicyContext(t, body)
	tokenPinned.Set("specific_channel_id", "42")
	markV4OfficialPinFromDistributor(tokenPinned)
	assert.False(t, fitRequirementAttached(tokenPinned), "an explicit token pin must short circuit the policy")

	// Explicit resolved pin (origin task / token), same rule.
	resolvedPinned := newFitPolicyContext(t, body)
	service.GetChannelConstraints(resolvedPinned).AddPin(rootdto.ChannelPin{
		ChannelId: 42,
		Source:    rootdto.PinSourceOriginTask,
	})
	markV4OfficialPinFromDistributor(resolvedPinned)
	assert.False(t, fitRequirementAttached(resolvedPinned), "a resolved explicit pin must short circuit the policy")
}

// divergentKimiPolicy demands the whole kimi-k3 family for every shape, which
// disagrees with the shipped predicate on servable shapes. It exists so the
// shadow tests can prove a disagreement is observed but never routed on.
const divergentKimiPolicy = `{
	"version": 99,
	"enabled": true,
	"shadow": true,
	"families": [
		{
			"id": "kimi-k3",
			"rules": [{"id": "always", "when": "WholeFamily()", "require": ["family.whole"]}],
			"behaviors": {"family.whole": {"class": "verdict"}}
		}
	]
}`

func mustInstallPolicy(t *testing.T, document string, shadow bool) *fitpolicy.Snapshot {
	t.Helper()
	if !shadow {
		document = replaceShadowFlag(t, document, false)
	}
	snapshot, err := fitpolicy.CompileJSON([]byte(document))
	require.NoError(t, err)
	fitpolicy.Install(snapshot)
	return snapshot
}

// replaceShadowFlag rewrites the policy's shadow field so a test can install the
// same rules in live mode without a second document.
func replaceShadowFlag(t *testing.T, document string, shadow bool) string {
	t.Helper()
	fields := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(document), &fields))
	fields["shadow"] = shadow
	updated, err := json.Marshal(fields)
	require.NoError(t, err)
	return string(updated)
}

func mustBuiltinPolicyJSON(t *testing.T) []byte {
	t.Helper()
	encoded, err := common.Marshal(fitpolicy.BuiltinPolicy())
	require.NoError(t, err)
	return encoded
}

// TestFitPolicyAllowsAffinityNarrowsTheDirectBranch covers the one fast path
// that picks its channel without going through the selector. Following the
// precedent of officialPinAllowsAffinity's test, this asserts the decision
// function directly.
func TestFitPolicyAllowsAffinityNarrowsTheDirectBranch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// The memory cache path is used so the check does not need a database.
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = oldMemoryCacheEnabled })

	// No requirement and no official family: nothing to enforce.
	plain, _ := gin.CreateTestContext(httptest.NewRecorder())
	assert.True(t, fitPolicyAllowsAffinity(plain, 999, "deepseek-v4-flash"),
		"a request the policy has no opinion about keeps its affinity binding")

	outOfFamily, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(outOfFamily, constant.ContextKeyFitRequirement, fitpolicy.Requirement{
		Family: "deepseek-v4", Model: "gpt-4o", Marks: []string{"m"},
	})
	assert.True(t, fitPolicyAllowsAffinity(outOfFamily, 999, "gpt-4o"),
		"a model outside every registered family is never constrained")

	// A requirement is present and the channel is not official-behaving: the
	// binding must be dropped so selection can apply the two-phase narrowing.
	required, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(required, constant.ContextKeyFitRequirement, fitpolicy.Requirement{
		Family: "deepseek-v4", Model: "deepseek-v4-flash", Marks: []string{"m"},
	})
	assert.False(t, fitPolicyAllowsAffinity(required, 999, "deepseek-v4-flash"),
		"an official-behaviour requirement must reject a non-official affinity binding")

	// Capability data, once it exists, narrows the binding further.
	marked, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(marked, constant.ContextKeyFitRequirement, fitpolicy.Requirement{
		Family: "deepseek-v4", Model: "gpt-4o", Marks: []string{"m"},
	})
	assert.True(t, fitPolicyAllowsAffinity(marked, 999, "gpt-4o"))
}
