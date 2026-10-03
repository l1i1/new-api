package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
		"malformed json":     `{"version":1,`,
		"unknown family":     `{"version":1,"enabled":true,"families":[{"id":"no-such-family","rules":[],"behaviors":{},"unknown_mark_policy":"conservative","empty_match_policy":"legacy_hard_pin_then_existing_error"}]}`,
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

func postFitPolicyValidate(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/fit-policy/validate", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	ValidateFitPolicyDocument(c)

	payload := map[string]any{}
	if recorder.Body.Len() > 0 {
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	}
	return recorder.Code, payload
}

func putOption(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	UpdateOption(c)

	payload := map[string]any{}
	if recorder.Body.Len() > 0 {
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	}
	return recorder.Code, payload
}

// oversizedPolicyDocument is a document just past fitpolicy's byte bound. It is
// not valid JSON on purpose: the bound has to reject on size alone, before the
// decoder turns a huge payload into a huge allocation. The leading brace keeps
// it non-blank, because a blank document is the documented disable lever and is
// accepted without ever being parsed.
func oversizedPolicyDocument() string {
	return "{" + strings.Repeat(" ", fitpolicy.MaxPolicyDocumentBytes) + "}"
}

// jsonString escapes a value the way the wire body carries it, so a document
// full of quotes and braces is still a valid request body.
func jsonString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}

// TestValidateFitPolicyDocumentRefusesAnOversizedBody is the transport half of
// the size bound: /api has no body limit at all (MAX_REQUEST_BODY_MB is mounted
// on the relay router only), so without this the endpoint would buffer — and
// then compile — whatever it was sent.
func TestValidateFitPolicyDocumentRefusesAnOversizedBody(t *testing.T) {
	overBody := `{"document":"` + strings.Repeat("a", fitPolicyValidateBodyLimit) + `"}`
	status, payload := postFitPolicyValidate(t, overBody)
	assert.Equal(t, http.StatusRequestEntityTooLarge, status, "a body over the transport limit is not a validation result")
	assert.Equal(t, false, payload["success"])

	// Under the transport limit but over the document limit the request is a
	// normal validation failure: the endpoint's contract is 200 with valid=false,
	// and the message has to name the limit so the author can shrink the rule set.
	document := oversizedPolicyDocument()
	status, payload = postFitPolicyValidate(t, `{"document":`+jsonString(t, document)+`}`)
	require.Equal(t, http.StatusOK, status, "body: %v", payload)
	data, ok := payload["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, data["valid"])
	assert.Contains(t, data["error"], "byte limit")
}

// TestUpdateOptionBoundsTheSameKeyOnItsOwnWritePath closes the bypass: the
// policy document is written through the root-only generic option endpoint, so
// bounding only /api/fit-policy/validate would leave the same key reachable with
// any body at all.
func TestUpdateOptionBoundsTheSameKeyOnItsOwnWritePath(t *testing.T) {
	overBody := `{"key":"` + fitpolicy.OptionKey + `","value":"` + strings.Repeat("a", maxOptionUpdateBodyBytes) + `"}`
	status, payload := putOption(t, overBody)
	assert.Equal(t, http.StatusRequestEntityTooLarge, status, "the option write path must bound its body too")
	assert.Equal(t, false, payload["success"])

	// Inside the transport bound the document's own limit answers instead, on the
	// endpoint's existing convention for a rejected value (200 with success=false,
	// the same shape an uncompilable document already produced).
	document := oversizedPolicyDocument()
	status, payload = putOption(t, `{"key":"`+fitpolicy.OptionKey+`","value":`+jsonString(t, document)+`}`)
	require.Equal(t, http.StatusOK, status, "body: %v", payload)
	assert.Equal(t, false, payload["success"])
	assert.Contains(t, payload["message"], "byte limit")
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
			Behavior:  map[bool]string{true: "tools.dynamic_names", false: "usage.thinking_counting"}[supported],
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

// TestGetChannelFitCapabilitiesDoesNotFlagAnOperatorMarkAsSuperseded keeps the
// listing's binding verdict identical to the state machine's.
//
// The state machine compares the policy/baseline hash only for a suite
// measurement that is supported and unexpired; an operator mark is a human
// decision and no hash invalidates it. Comparing every row made a stored
// operator mark answer binding_current=false beside state=manual_active, so the
// table showed "Operator mark" and "Measured against a superseded policy" in the
// same row.
func TestGetChannelFitCapabilitiesDoesNotFlagAnOperatorMarkAsSuperseded(t *testing.T) {
	setupFitCapabilityEndpoint(t)
	installFitPolicySnapshot(t, model.FitPolicyDefaultDocument())
	now := common.GetTimestamp()
	for _, mark := range []struct {
		behavior string
		source   string
	}{
		{behavior: "tools.dynamic_names", source: model.FitCapabilitySourceManual},
		{behavior: "usage.thinking_counting", source: model.FitCapabilitySourceSuite},
	} {
		_, err := model.ApplyChannelFitCapability(model.FitCapabilityWrite{
			ChannelId: 7, Family: "kimi-k3", Model: "kimi-k3", Behavior: mark.behavior,
			Supported: true, Source: mark.source, At: now,
			PolicyHash: "superseded-hash", ExpectedRevision: 0,
		}, now)
		require.NoError(t, err)
	}

	status, payload := getFitCapabilityPage(t, "/api/fit-capability/all")
	require.Equal(t, http.StatusOK, status, "body: %v", payload)
	data, _ := payload["data"].(map[string]any)
	items, _ := data["items"].([]any)
	require.Len(t, items, 2)

	byBehavior := map[string]map[string]any{}
	for _, item := range items {
		row, ok := item.(map[string]any)
		require.True(t, ok)
		behavior, _ := row["behavior"].(string)
		byBehavior[behavior] = row
	}

	operator := byBehavior["tools.dynamic_names"]
	require.NotNil(t, operator)
	assert.Equal(t, model.FitCapabilityManualActive, operator["state"])
	assert.Equal(t, true, operator["binding_current"],
		"an operator mark is never invalidated by a hash, so it must not carry the superseded badge")

	measured := byBehavior["usage.thinking_counting"]
	require.NotNil(t, measured)
	assert.Equal(t, model.FitCapabilitySuiteStale, measured["state"])
	assert.Equal(t, false, measured["binding_current"],
		"a suite measurement under a replaced policy is exactly the case the verdict exists for")
}

// TestGetChannelFitCapabilitiesRefusesACancelledPageContract pins the paging
//
// A negative page size used to pass through common.GetPageQuery into
// db.Limit(-1). GORM writes a LIMIT clause only for a value >= 0, so the clause
// disappeared and the "page" became the whole table — one query parameter
// cancelling the contract, on a table that grows with every suite run. The two
// fixes are pinned separately: the shared helper is bounded so no endpoint can
// reach GORM with it (common/page_info_test.go), and this endpoint — which is
// new and has exactly one caller — refuses the request instead of guessing.
func TestGetChannelFitCapabilitiesRefusesACancelledPageContract(t *testing.T) {
	setupFitCapabilityEndpoint(t)
	now := common.GetTimestamp()
	for index := range 25 {
		_, err := model.ApplyChannelFitCapability(model.FitCapabilityWrite{
			ChannelId: 7, Family: "kimi-k3", Model: "kimi-k3",
			Behavior:  fmt.Sprintf("pin.%02d", index),
			Supported: true, Source: model.FitCapabilitySourceSuite, At: now, ExpectedRevision: 0,
		}, now)
		require.NoError(t, err)
	}

	// Every one of these used to answer 200 with all 25 rows.
	for _, target := range []string{
		"/api/fit-capability/all?page_size=-1",
		"/api/fit-capability/all?page_size=0",
		"/api/fit-capability/all?page_size=abc",
		"/api/fit-capability/all?ps=-1",
		"/api/fit-capability/all?size=-1",
		"/api/fit-capability/all?p=-1",
		"/api/fit-capability/all?p=0",
		"/api/fit-capability/all?p=abc",
	} {
		status, _ := getFitCapabilityPage(t, target)
		assert.Equal(t, http.StatusBadRequest, status, "%s must be refused", target)
	}

	// The documented upper bound is a clamp, not an error, and the response
	// reports the size actually used rather than the one that was asked for.
	status, payload := getFitCapabilityPage(t, "/api/fit-capability/all?page_size=100000")
	require.Equal(t, http.StatusOK, status, "body: %v", payload)
	data, _ := payload["data"].(map[string]any)
	assert.Equal(t, float64(100), data["page_size"])
	assert.Equal(t, float64(25), data["total"])

	// An empty value means "not supplied", not "no rows" and not a client error.
	status, payload = getFitCapabilityPage(t, "/api/fit-capability/all?page_size=")
	require.Equal(t, http.StatusOK, status, "body: %v", payload)
	data, _ = payload["data"].(map[string]any)
	assert.Equal(t, float64(25), data["total"], "an empty page_size must not filter anything out")
	assert.GreaterOrEqual(t, data["page_size"], float64(1))
	assert.LessOrEqual(t, data["page_size"], float64(100))
}

// TestGetChannelFitCapabilitiesRefusesAMalformedChannelFilter is the other half
// of "invalid input is reported, not ignored": channel_id was parsed with the
// error discarded, so a typed letter silently widened the listing to every
// channel instead of narrowing it — the opposite of what the operator asked for.
func TestGetChannelFitCapabilitiesRefusesAMalformedChannelFilter(t *testing.T) {
	setupFitCapabilityEndpoint(t)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 8, Name: "ch-8", Status: common.ChannelStatusEnabled, Group: "default", Models: "kimi-k3", Key: "sk",
	}).Error)
	now := common.GetTimestamp()
	for _, channelId := range []int{7, 8} {
		_, err := model.ApplyChannelFitCapability(model.FitCapabilityWrite{
			ChannelId: channelId, Family: "kimi-k3", Model: "kimi-k3",
			Behavior:  fmt.Sprintf("pin.%d", channelId),
			Supported: true, Source: model.FitCapabilitySourceSuite, At: now, ExpectedRevision: 0,
		}, now)
		require.NoError(t, err)
	}

	for _, target := range []string{
		"/api/fit-capability/all?channel_id=abc",
		"/api/fit-capability/all?channel_id=0",
		"/api/fit-capability/all?channel_id=-3",
		"/api/fit-capability/all?channel_id=7.5",
	} {
		status, _ := getFitCapabilityPage(t, target)
		assert.Equal(t, http.StatusBadRequest, status, "%s must be refused", target)
	}

	// An empty channel_id is an absent filter, exactly like an empty supported:
	// one rule for every parameter on this endpoint.
	status, payload := getFitCapabilityPage(t, "/api/fit-capability/all?channel_id=&supported=")
	require.Equal(t, http.StatusOK, status, "body: %v", payload)
	data, _ := payload["data"].(map[string]any)
	assert.Equal(t, float64(2), data["total"], "empty values must not narrow the listing")

	status, payload = getFitCapabilityPage(t, "/api/fit-capability/all?channel_id=7")
	require.Equal(t, http.StatusOK, status, "body: %v", payload)
	data, _ = payload["data"].(map[string]any)
	assert.Equal(t, float64(1), data["total"], "a valid channel_id keeps narrowing")
}

// TestFitPolicyViewReportsAdmissionPrefixes covers the prefix-level admission
// summary the channel surfaces consume: with a measured family installed its
// model prefixes are reported as measured, the other families stay in the
// official list only, and with no snapshot at all both lists are empty — the
// "everywhere declared" state in which the allowlist is still the admission
// source everywhere.
func TestFitPolicyViewReportsAdmissionPrefixes(t *testing.T) {
	policy := fitpolicy.Policy{
		Version: 1, Enabled: true,
		Families: []fitpolicy.FamilyPolicy{{
			ID: "kimi-k3",
			Rules: []fitpolicy.Rule{{
				ID: "k3-whole-family", When: "WholeFamily()", Require: []string{fitpolicy.BehaviorFamilyWhole},
			}},
			Behaviors: map[string]fitpolicy.Behavior{
				fitpolicy.BehaviorFamilyWhole:      {Class: fitpolicy.ClassVerdict},
				fitpolicy.BehaviorThinkingCounting: {Class: fitpolicy.ClassVerdict},
			},
			AdmissionSource:  fitpolicy.AdmissionSourceMeasured,
			AdmissionBattery: []string{fitpolicy.BehaviorThinkingCounting},
		}},
	}
	document, err := common.Marshal(policy)
	require.NoError(t, err)
	installFitPolicySnapshot(t, string(document))

	view := getFitPolicyView(t)
	admission, ok := view["admission"].(map[string]any)
	require.True(t, ok)
	assert.ElementsMatch(t, []any{"kimi-k3"}, admission["measured_model_prefixes"])
	official, ok := admission["official_model_prefixes"].([]any)
	require.True(t, ok)
	assert.Contains(t, official, "kimi-k3")
	assert.Contains(t, official, "deepseek-v4")
	assert.Contains(t, official, "glm-5.3")

	// Without a snapshot every family is declared: nothing is measured.
	previous := fitpolicy.Current()
	fitpolicy.Install(nil)
	t.Cleanup(func() { fitpolicy.Install(previous) })
	view = getFitPolicyView(t)
	admission, ok = view["admission"].(map[string]any)
	require.True(t, ok)
	assert.Empty(t, admission["measured_model_prefixes"])
	assert.NotEmpty(t, admission["official_model_prefixes"])
}
