package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for the fit-policy administration surface: the fleet-wide capability
// listing and the policy document view/validation. The point of these is not
// that JSON is rendered, but that the states which used to be silent — a
// document that pins nothing, a mark bound to a superseded policy, an
// unwritten option versus a cleared one — are each reported distinctly.

// swapFitPolicyOption replaces the option map with a copy carrying (or missing)
// the policy key, and restores the whole map afterwards.
func swapFitPolicyOption(t *testing.T, value *string) {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	previous := common.OptionMap
	next := make(map[string]string, len(previous)+1)
	for key, existing := range previous {
		next[key] = existing
	}
	if value == nil {
		delete(next, fitpolicy.OptionKey)
	} else {
		next[fitpolicy.OptionKey] = *value
	}
	common.OptionMap = next
	common.OptionMapRWMutex.Unlock()

	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previous
		common.OptionMapRWMutex.Unlock()
	})
}

// installFitPolicySnapshot installs a compiled document for the duration of one
// test and returns its hash.
func installFitPolicySnapshot(t *testing.T, document string) string {
	t.Helper()
	previous := fitpolicy.Current()
	snapshot, err := fitpolicy.CompileJSON([]byte(document))
	require.NoError(t, err)
	fitpolicy.Install(snapshot)
	t.Cleanup(func() { fitpolicy.Install(previous) })
	return snapshot.Hash()
}

func getFitPolicyView(t *testing.T) map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/fit-policy", nil)

	GetFitPolicy(c)

	require.Equal(t, http.StatusOK, recorder.Code, "body: %s", recorder.Body.String())
	payload := map[string]any{}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, true, payload["success"])
	data, ok := payload["data"].(map[string]any)
	require.True(t, ok)
	return data
}

func validationOf(t *testing.T, document string) map[string]any {
	t.Helper()
	validation := validateFitPolicyDocument(document)
	encoded, err := common.Marshal(validation)
	require.NoError(t, err)
	decoded := map[string]any{}
	require.NoError(t, common.Unmarshal(encoded, &decoded))
	return decoded
}

func warningCodes(t *testing.T, container map[string]any) []string {
	t.Helper()
	raw, ok := container["warnings"].([]any)
	if !ok {
		return nil
	}
	codes := make([]string, 0, len(raw))
	for _, item := range raw {
		warning, ok := item.(map[string]any)
		require.True(t, ok)
		code, ok := warning["code"].(string)
		require.True(t, ok)
		codes = append(codes, code)
	}
	return codes
}

// TestFitPolicyViewSeparatesUnwrittenFromCleared is the distinction the whole
// page turns on: an unwritten option installs the shipped default, while a
// blank one takes the layer out. Both used to look identical from outside.
func TestFitPolicyViewSeparatesUnwrittenFromCleared(t *testing.T) {
	swapFitPolicyOption(t, nil)

	view := getFitPolicyView(t)
	assert.Equal(t, FitPolicySourceDefault, view["source"])
	assert.Equal(t, false, view["option_present"])
	assert.Equal(t, "", view["document"])
	assert.Equal(t, "", view["divergence"], "the shipped default cannot diverge from itself")
	assert.Equal(t, "", view["parse_error"])
	assert.NotEmpty(t, view["default_document"])
	assert.Equal(t, view["default_document"], view["effective_document"])

	cleared := ""
	swapFitPolicyOption(t, &cleared)
	view = getFitPolicyView(t)
	assert.Equal(t, FitPolicySourceCleared, view["source"])
	assert.Equal(t, true, view["option_present"])
	assert.Equal(t, "", view["effective_document"])
	assert.Contains(t, warningCodes(t, view), FitPolicyWarningCleared)
}

// TestFitPolicyViewReportsALayerThatPinsNothing: a compilable document with
// enabled=false is legal and is the documented rollback, but it must never be
// reported as healthy.
func TestFitPolicyViewReportsALayerThatPinsNothing(t *testing.T) {
	document := `{"version":1,"enabled":false,"shadow":false,"families":[]}`
	swapFitPolicyOption(t, &document)

	view := getFitPolicyView(t)
	assert.Equal(t, FitPolicySourceDocument, view["source"])
	assert.Equal(t, "", view["parse_error"])
	codes := warningCodes(t, view)
	assert.Contains(t, codes, FitPolicyWarningDisabled)
	assert.Contains(t, codes, FitPolicyWarningNoFamily)
	assert.Contains(t, view["divergence"].(string), "enabled true→false")
	assert.Contains(t, view["divergence"].(string), "family deepseek-v4 removed")
}

// TestFitPolicyViewReportsTheLiveSnapshotApartFromTheDocument: a document that
// failed to load leaves the previous snapshot installed, so the view has to
// report both rather than implying the stored bytes are what is running.
func TestFitPolicyViewReportsTheLiveSnapshotApartFromTheDocument(t *testing.T) {
	hash := installFitPolicySnapshot(t, model.FitPolicyDefaultDocument())

	document := `{"version":1,"enabled":true,"families":[]}`
	swapFitPolicyOption(t, &document)

	view := getFitPolicyView(t)
	live, ok := view["live"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, live["installed"])
	assert.Equal(t, hash, live["hash"])
	assert.Equal(t, true, live["enabled"])
	// The stored document is not what is running: it declares no family, while
	// the installed snapshot still carries the shipped rules.
	assert.Contains(t, warningCodes(t, view), FitPolicyWarningNoFamily)
}

// TestValidateFitPolicyDocumentRejectsUncompilableRules covers both rejection
// layers: a document that is not JSON at all, and one that parses but whose
// rule expression cannot be compiled. The second is the dangerous one — it
// parses cleanly, so only the compiler catches it.
func TestValidateFitPolicyDocumentRejectsUncompilableRules(t *testing.T) {
	for name, document := range map[string]string{
		"malformed json": `{"version":1,`,
		"unknown family": `{"version":1,"enabled":true,"families":[{"id":"no-such-family","rules":[],"behaviors":{},"unknown_mark_policy":"conservative","empty_match_policy":"legacy_hard_pin_then_existing_error"}]}`,
		"unknown expression": `{"version":1,"enabled":true,"families":[{"id":"kimi-k3","rules":[{"id":"r","when":"NoSuchPredicate()","require":["tools.dynamic_names"]}],"behaviors":{"tools.dynamic_names":{"class":"verdict"}},"unknown_mark_policy":"conservative","empty_match_policy":"legacy_hard_pin_then_existing_error"}]}`,
		"unknown field":      `{"version":1,"enabled":true,"families":[],"extra":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			validation := validationOf(t, document)
			assert.Equal(t, false, validation["valid"])
			assert.NotEmpty(t, validation["error"])
			assert.Nil(t, validation["summary"])
		})
	}
}

// TestValidateFitPolicyDocumentAcceptsTheShippedDefault proves the document the
// "restore the shipped default" action writes is accepted, compiles, and
// diverges from the built-in rules by nothing.
func TestValidateFitPolicyDocumentAcceptsTheShippedDefault(t *testing.T) {
	validation := validationOf(t, model.FitPolicyDefaultDocument())
	require.Equal(t, true, validation["valid"], "error: %v", validation["error"])
	assert.Equal(t, "", validation["error"])
	assert.Equal(t, "", validation["divergence"])

	codes := warningCodes(t, validation)
	assert.NotContains(t, codes, FitPolicyWarningDisabled)
	assert.NotContains(t, codes, FitPolicyWarningShadow)
	assert.NotContains(t, codes, FitPolicyWarningNoFamily)
	assert.NotContains(t, codes, FitPolicyWarningNoRules)

	summary, ok := validation["summary"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(1), summary["version"])
	assert.Equal(t, true, summary["enabled"])
	assert.Equal(t, false, summary["shadow"])
	assert.NotEmpty(t, summary["hash"])
}

// TestValidateFitPolicyDocumentWarnsAboutAShadowOrEmptyDocument keeps the
// warnings attached to the states they describe.
func TestValidateFitPolicyDocumentWarnsAboutAShadowOrEmptyDocument(t *testing.T) {
	shadow := `{"version":1,"enabled":true,"shadow":true,"families":[{"id":"kimi-k3","rules":[{"id":"r","when":"ThinkingDisabled()","require":["tools.dynamic_names"]}],"behaviors":{"tools.dynamic_names":{"class":"verdict"}},"unknown_mark_policy":"conservative","empty_match_policy":"legacy_hard_pin_then_existing_error"}]}`
	validation := validationOf(t, shadow)
	require.Equal(t, true, validation["valid"], "error: %v", validation["error"])
	assert.Contains(t, warningCodes(t, validation), FitPolicyWarningShadow)

	ruleless := `{"version":1,"enabled":true,"shadow":false,"families":[{"id":"kimi-k3","rules":[],"behaviors":{},"unknown_mark_policy":"conservative","empty_match_policy":"legacy_hard_pin_then_existing_error"}]}`
	validation = validationOf(t, ruleless)
	require.Equal(t, true, validation["valid"], "error: %v", validation["error"])
	codes := warningCodes(t, validation)
	assert.Contains(t, codes, FitPolicyWarningNoRules)
	assert.NotContains(t, codes, FitPolicyWarningNoFamily)

	cleared := validationOf(t, "")
	require.Equal(t, true, cleared["valid"])
	assert.Contains(t, warningCodes(t, cleared), FitPolicyWarningCleared)
	assert.Nil(t, cleared["summary"])
}

func getFitCapabilityPage(t *testing.T, target string) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)

	GetChannelFitCapabilitiesPage(c)

	payload := map[string]any{}
	if recorder.Body.Len() > 0 {
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	}
	return recorder.Code, payload
}

// TestGetChannelFitCapabilitiesPagesAcrossChannels covers the reason the
// endpoint exists: the per-channel listing cannot show a mark whose channel is
// gone, nor one bound to a policy that was replaced.
func TestGetChannelFitCapabilitiesPagesAcrossChannels(t *testing.T) {
	setupFitCapabilityEndpoint(t)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 8, Name: "ch-8", Status: common.ChannelStatusEnabled, Group: "default", Models: "kimi-k3", Key: "sk",
	}).Error)

	hash := installFitPolicySnapshot(t, model.FitPolicyDefaultDocument())
	now := common.GetTimestamp()
	writeMark := func(channelId int, behavior string, supported bool, policyHash string) {
		t.Helper()
		_, err := model.ApplyChannelFitCapability(model.FitCapabilityWrite{
			ChannelId: channelId, Family: "kimi-k3", Model: "kimi-k3", Behavior: behavior,
			Supported: supported, Source: model.FitCapabilitySourceSuite, At: now,
			PolicyHash: policyHash, ExpectedRevision: 0,
		}, now)
		require.NoError(t, err)
	}
	writeMark(7, "tools.dynamic_names", true, hash)
	writeMark(7, "usage.thinking_counting", false, hash)
	writeMark(8, "tools.dynamic_names", true, "superseded-hash")

	status, payload := getFitCapabilityPage(t, "/api/fit-capability/all?p=1&page_size=2")
	require.Equal(t, http.StatusOK, status, "body: %v", payload)
	data, ok := payload["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(3), data["total"])
	assert.Equal(t, hash, data["policy_hash"])

	items, ok := data["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 2, "page_size must bound the page")
	first, ok := items[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ch-7", first["channel_name"])
	assert.Equal(t, model.FitCapabilitySuiteFresh, first["state"])
	assert.Equal(t, true, first["binding_current"])

	status, payload = getFitCapabilityPage(t, "/api/fit-capability/all?p=2&page_size=2")
	require.Equal(t, http.StatusOK, status)
	data, _ = payload["data"].(map[string]any)
	items, _ = data["items"].([]any)
	require.Len(t, items, 1)
	last, ok := items[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ch-8", last["channel_name"])
	assert.Equal(t, model.FitCapabilitySuiteStale, last["state"], "a mark bound to a superseded policy is stale, not fresh")
	assert.Equal(t, false, last["binding_current"])
}

// TestGetChannelFitCapabilitiesFiltersAndRejectsBadInput keeps the filter
// tri-state honest: "supported=false" is an explicit negative result and must
// select it, while a non-boolean is a client error rather than a silent "all".
func TestGetChannelFitCapabilitiesFiltersAndRejectsBadInput(t *testing.T) {
	setupFitCapabilityEndpoint(t)
	now := common.GetTimestamp()
	for _, supported := range []bool{true, false} {
		_, err := model.ApplyChannelFitCapability(model.FitCapabilityWrite{
			ChannelId: 7, Family: "kimi-k3", Model: "kimi-k3",
			Behavior: map[bool]string{true: "tools.dynamic_names", false: "usage.thinking_counting"}[supported],
			Supported: supported, Source: model.FitCapabilitySourceSuite, At: now, ExpectedRevision: 0,
		}, now)
		require.NoError(t, err)
	}

	status, payload := getFitCapabilityPage(t, "/api/fit-capability/all?supported=false")
	require.Equal(t, http.StatusOK, status)
	data, _ := payload["data"].(map[string]any)
	assert.Equal(t, float64(1), data["total"])
	items, _ := data["items"].([]any)
	require.Len(t, items, 1)
	unsupported, ok := items[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, unsupported["supported"])
	assert.Equal(t, model.FitCapabilitySuiteFailed, unsupported["state"])

	status, _ = getFitCapabilityPage(t, "/api/fit-capability/all?behavior=usage.thinking_counting&source=suite")
	require.Equal(t, http.StatusOK, status)

	status, _ = getFitCapabilityPage(t, "/api/fit-capability/all?supported=maybe")
	assert.Equal(t, http.StatusBadRequest, status)
}
