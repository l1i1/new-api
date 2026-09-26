package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFitPolicyContext builds a distributor context with the Route dimension on
// for every family, so the legacy predicate decides the pin.
func newFitPolicyContext(t *testing.T, body string) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{
		OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"*": {Route: true},
		}},
	})
	return c
}

func pinFor(t *testing.T, body string) bool {
	t.Helper()
	c := newFitPolicyContext(t, body)
	markV4OfficialPinFromDistributor(c)
	return common.GetContextKeyBool(c, constant.ContextKeyV4OfficialPin)
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
	divergent := []byte(`{
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
	}`)
	snapshot, err := fitpolicy.CompileJSON(divergent)
	require.NoError(t, err)
	fitpolicy.Install(snapshot)

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

func mustBuiltinPolicyJSON(t *testing.T) []byte {
	t.Helper()
	encoded, err := common.Marshal(fitpolicy.BuiltinPolicy())
	require.NoError(t, err)
	return encoded
}
