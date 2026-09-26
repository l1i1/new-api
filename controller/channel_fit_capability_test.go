package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupFitCapabilityEndpoint(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousMemoryCache := common.MemoryCacheEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ChannelFitCapability{}))
	// A mark must belong to an existing channel.
	require.NoError(t, db.Create(&model.Channel{
		Id: 7, Name: "ch-7", Status: common.ChannelStatusEnabled, Group: "default", Models: "kimi-k3", Key: "sk",
	}).Error)
	model.DB = db
	model.LOG_DB = nil
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.MemoryCacheEnabled = previousMemoryCache
		model.ResetFitCapabilityIndexForTest()
	})
}

func putCapability(t *testing.T, body string, role int) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/fit-capability", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("role", role)

	PutChannelFitCapability(c)

	payload := map[string]any{}
	if recorder.Body.Len() > 0 {
		_ = common.Unmarshal(recorder.Body.Bytes(), &payload)
	}
	return recorder, payload
}

func capabilityBody(expectedRevision any) string {
	revision := "null"
	if expectedRevision != nil {
		revision = fmt.Sprintf("%v", expectedRevision)
	}
	return fmt.Sprintf(`{
		"channel_id": 7,
		"family": "kimi-k3",
		"model": "kimi-k3",
		"behavior": "tools.dynamic_names",
		"supported": true,
		"source": "suite",
		"suite": "cdp-k3",
		"cases": "30/30",
		"rounds": 3,
		"at": 1700000000,
		"expected_revision": %s
	}`, revision)
}

// TestPutFitCapabilityRequiresExpectedRevision is the guard against blind
// overwrites: a write that cannot say which revision it replaces is rejected
// before it reaches the database.
func TestPutFitCapabilityRequiresExpectedRevision(t *testing.T) {
	setupFitCapabilityEndpoint(t)

	recorder, payload := putCapability(t, capabilityBody(nil), common.RoleRootUser)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, payload["message"], "expected_revision")
}

func TestPutFitCapabilityCASLifecycle(t *testing.T) {
	setupFitCapabilityEndpoint(t)

	recorder, payload := putCapability(t, capabilityBody(0), common.RoleRootUser)
	require.Equal(t, http.StatusOK, recorder.Code, "body: %s", recorder.Body.String())
	assert.Equal(t, true, payload["success"])

	stored, found, err := model.GetChannelFitCapability(7, "kimi-k3", "kimi-k3", "tools.dynamic_names")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(1), stored.Revision)

	recorder, _ = putCapability(t, capabilityBody(1), common.RoleRootUser)
	require.Equal(t, http.StatusOK, recorder.Code)

	// A stale revision must be a real 409. The shared ApiError helper returns
	// HTTP 200 for everything, so a 200 here would make callers read a rejected
	// write as success.
	recorder, payload = putCapability(t, capabilityBody(1), common.RoleRootUser)
	require.Equal(t, http.StatusConflict, recorder.Code, "body: %s", recorder.Body.String())
	require.NotNil(t, payload["current"], "the conflict must return the stored row so the caller can retry")

	stored, _, err = model.GetChannelFitCapability(7, "kimi-k3", "kimi-k3", "tools.dynamic_names")
	require.NoError(t, err)
	require.Equal(t, int64(2), stored.Revision, "a rejected write must not bump the revision")
}

func TestPutFitCapabilityForceNeedsTheForcePermission(t *testing.T) {
	setupFitCapabilityEndpoint(t)

	// An operator mark first.
	manual := `{
		"channel_id": 7, "family": "kimi-k3", "model": "kimi-k3",
		"behavior": "usage.thinking_counting", "supported": true,
		"source": "manual", "at": 1700000000, "expected_revision": 0
	}`
	recorder, _ := putCapability(t, manual, common.RoleRootUser)
	require.Equal(t, http.StatusOK, recorder.Code)

	suite := `{
		"channel_id": 7, "family": "kimi-k3", "model": "kimi-k3",
		"behavior": "usage.thinking_counting", "supported": true, "source": "suite",
		"at": 1700000000, "expected_revision": 1, "force": true
	}`

	// A caller without the force action is refused before anything is written.
	recorder, payload := putCapability(t, suite, common.RoleCommonUser)
	require.Equal(t, http.StatusForbidden, recorder.Code, "body: %s", recorder.Body.String())
	assert.Contains(t, payload["message"], "capability.force")

	stored, _, err := model.GetChannelFitCapability(7, "kimi-k3", "kimi-k3", "usage.thinking_counting")
	require.NoError(t, err)
	require.Equal(t, "manual", stored.Source)
	require.Equal(t, int64(1), stored.Revision)

	// Without force, a suite write cannot replace a live manual mark either.
	noForce := strings.Replace(suite, `"force": true`, `"force": false`, 1)
	recorder, _ = putCapability(t, noForce, common.RoleRootUser)
	require.Equal(t, http.StatusConflict, recorder.Code)

	// With the permission the override lands and is recorded as forced.
	recorder, _ = putCapability(t, suite, common.RoleRootUser)
	require.Equal(t, http.StatusOK, recorder.Code)
	stored, _, err = model.GetChannelFitCapability(7, "kimi-k3", "kimi-k3", "usage.thinking_counting")
	require.NoError(t, err)
	require.Equal(t, "suite", stored.Source)
	require.True(t, stored.Force)
	require.Equal(t, int64(2), stored.Revision)
}

// TestPutFitCapabilityAuditFailureDoesNotFailTheWrite pins the cross-database
// reality: the audit table may live in a different database, so the capability
// write must survive an audit failure instead of being rolled back by it.
func TestPutFitCapabilityAuditFailureDoesNotFailTheWrite(t *testing.T) {
	setupFitCapabilityEndpoint(t)

	logDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := logDB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	model.LOG_DB = logDB

	recorder, _ := putCapability(t, capabilityBody(0), common.RoleRootUser)
	require.Equal(t, http.StatusOK, recorder.Code, "an unavailable audit database must not fail the capability write")

	stored, found, err := model.GetChannelFitCapability(7, "kimi-k3", "kimi-k3", "tools.dynamic_names")
	require.NoError(t, err)
	require.True(t, found, "the capability write must still be committed")
	require.Equal(t, int64(1), stored.Revision)
}

func TestPutFitCapabilityRejectsInvalidInput(t *testing.T) {
	setupFitCapabilityEndpoint(t)

	for name, body := range map[string]string{
		"unknown source":    strings.Replace(capabilityBody(0), `"source": "suite"`, `"source": "guesswork"`, 1),
		"missing channel":   strings.Replace(capabilityBody(0), `"channel_id": 7`, `"channel_id": 0`, 1),
		"missing behavior":  strings.Replace(capabilityBody(0), `"behavior": "tools.dynamic_names"`, `"behavior": ""`, 1),
		"negative revision": strings.Replace(capabilityBody(0), `"expected_revision": 0`, `"expected_revision": -3`, 1),
		"malformed json":    `{"channel_id":`,
	} {
		t.Run(name, func(t *testing.T) {
			recorder, _ := putCapability(t, body, common.RoleRootUser)
			require.Equal(t, http.StatusBadRequest, recorder.Code, "body: %s", recorder.Body.String())
		})
	}
}

func TestGetChannelFitCapabilitiesReportsState(t *testing.T) {
	setupFitCapabilityEndpoint(t)

	now := common.GetTimestamp()
	_, err := model.ApplyChannelFitCapability(model.FitCapabilityWrite{
		ChannelId: 7, Family: "kimi-k3", Model: "kimi-k3", Behavior: "tools.dynamic_names",
		Supported: true, Source: model.FitCapabilitySourceSuite, At: now, ExpectedRevision: 0,
	}, now)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/fit-capability?channel_id=7", nil)

	GetChannelFitCapabilities(c)

	require.Equal(t, http.StatusOK, recorder.Code, "body: %s", recorder.Body.String())
	payload := map[string]any{}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	rows, ok := payload["data"].([]any)
	require.True(t, ok)
	require.Len(t, rows, 1)
	first, ok := rows[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, model.FitCapabilitySuiteFresh, first["state"])

	missing := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(missing)
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/fit-capability", nil)
	GetChannelFitCapabilities(c2)
	assert.Equal(t, http.StatusBadRequest, missing.Code)
}

func postReport(t *testing.T, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/fit-capability/report", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("role", common.RoleRootUser)

	PostFitCapabilityReport(c)

	payload := map[string]any{}
	if recorder.Body.Len() > 0 {
		_ = common.Unmarshal(recorder.Body.Bytes(), &payload)
	}
	return recorder, payload
}

// installLivePolicy puts a real policy snapshot in place and returns the report
// binding a suite would have measured against.
func installLivePolicy(t *testing.T) (version int, hash string) {
	t.Helper()
	snapshot, err := fitpolicy.CompileJSON(mustBuiltinPolicyJSONForController(t))
	require.NoError(t, err)
	fitpolicy.Install(snapshot)
	t.Cleanup(func() { fitpolicy.Install(nil) })
	return snapshot.Version(), snapshot.Hash()
}

func mustBuiltinPolicyJSONForController(t *testing.T) []byte {
	t.Helper()
	encoded, err := common.Marshal(fitpolicy.BuiltinPolicy())
	require.NoError(t, err)
	return encoded
}

func reportBody(version int, hash, behavior string, channelID int, supported bool) string {
	return fmt.Sprintf(`{
		"report_id": "report-1", "run_id": "run-1", "suite": "cdp-k3",
		"policy_version": %d, "policy_hash": %q, "baseline_hash": "",
		"generated_at": %d, "rounds": 3,
		"results": [
			{"channel_id": %d, "family": "kimi-k3", "model": "kimi-k3",
			 "behavior": %q, "supported": %v, "cases": "30/30"}
		]
	}`, version, hash, common.GetTimestamp(), channelID, behavior, supported)
}

// TestPostFitCapabilityReportRequiresALivePolicy covers the binding requirement:
// with no policy installed a mark has no defined meaning, so the report is
// refused instead of recorded.
func TestPostFitCapabilityReportRequiresALivePolicy(t *testing.T) {
	setupFitCapabilityEndpoint(t)
	fitpolicy.Install(nil)

	recorder, payload := postReport(t, reportBody(1, "whatever", "tools.dynamic_names", 7, true))
	require.Equal(t, http.StatusConflict, recorder.Code, "body: %s", recorder.Body.String())
	assert.Contains(t, payload["message"], "no fit policy is installed")
}

// TestPostFitCapabilityReportRejectsStaleBinding is the whole-report gate: a run
// measured against older rules must not mark channels against the current ones.
func TestPostFitCapabilityReportRejectsStaleBinding(t *testing.T) {
	setupFitCapabilityEndpoint(t)
	version, hash := installLivePolicy(t)

	recorder, payload := postReport(t, reportBody(version, hash+"-stale", "tools.dynamic_names", 7, true))
	require.Equal(t, http.StatusConflict, recorder.Code, "body: %s", recorder.Body.String())
	assert.Contains(t, payload["message"], "policy hash")

	rows, err := model.ListChannelFitCapabilities(7)
	require.NoError(t, err)
	assert.Empty(t, rows, "a rejected report must write nothing")
}

func TestPostFitCapabilityReportAppliesAndRefreshesTheIndex(t *testing.T) {
	setupFitCapabilityEndpoint(t)
	version, hash := installLivePolicy(t)

	recorder, payload := postReport(t, reportBody(version, hash, "tools.dynamic_names", 7, true))
	require.Equal(t, http.StatusOK, recorder.Code, "body: %s", recorder.Body.String())
	assert.Equal(t, true, payload["success"])

	summary, ok := payload["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(1), summary["applied"])

	stored, found, err := model.GetChannelFitCapability(7, "kimi-k3", "kimi-k3", "tools.dynamic_names")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(1), stored.Revision)
	assert.Equal(t, "cdp-k3", stored.Suite)
	assert.Equal(t, "report-1", stored.ReportId)

	// The endpoint refreshes the index with the channel cache, so the mark is
	// immediately usable by selection on this node.
	assert.True(t, model.ChannelSatisfiesFitMarks(7, model.FitMarkRequirement{
		Model: "kimi-k3",
		Marks: []string{"tools.dynamic_names"},
	}))
}

// TestPostFitCapabilityReportKeepsOperatorMarks is the sticky rule seen from the
// endpoint: the applier holds only capability.write, so a report cannot replace a
// live manual mark, and the run is still reported as partially applied.
func TestPostFitCapabilityReportKeepsOperatorMarks(t *testing.T) {
	setupFitCapabilityEndpoint(t)

	manual := `{
		"channel_id": 7, "family": "kimi-k3", "model": "kimi-k3",
		"behavior": "usage.thinking_counting", "supported": true,
		"source": "manual", "at": 1700000000, "expected_revision": 0
	}`
	recorder, _ := putCapability(t, manual, common.RoleRootUser)
	require.Equal(t, http.StatusOK, recorder.Code)

	version, hash := installLivePolicy(t)
	recorder, payload := postReport(t, reportBody(version, hash, "usage.thinking_counting", 7, false))
	require.Equal(t, http.StatusOK, recorder.Code, "body: %s", recorder.Body.String())

	summary, ok := payload["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(0), summary["applied"])
	assert.Equal(t, float64(1), summary["conflicts"])

	stored, _, err := model.GetChannelFitCapability(7, "kimi-k3", "kimi-k3", "usage.thinking_counting")
	require.NoError(t, err)
	assert.Equal(t, "manual", stored.Source)
	assert.True(t, stored.Supported, "the operator's value must survive a report that disagrees")
	assert.False(t, stored.Force)
}

func TestPostFitCapabilityReportRejectsMalformedShape(t *testing.T) {
	setupFitCapabilityEndpoint(t)
	version, hash := installLivePolicy(t)

	// An unparseable body is a bad request; a well-formed report whose contents
	// or binding are wrong is a conflict.
	t.Run("malformed json", func(t *testing.T) {
		recorder, _ := postReport(t, `{"report_id":`)
		require.Equal(t, http.StatusBadRequest, recorder.Code, "body: %s", recorder.Body.String())
	})

	for name, body := range map[string]string{
		"unregistered family": strings.Replace(reportBody(version, hash, "tools.dynamic_names", 7, true), `"family": "kimi-k3"`, `"family": "nope"`, 1),
		"malformed behavior":  reportBody(version, hash, "tools dynamic names", 7, true),
		"missing channel":     reportBody(version, hash, "tools.dynamic_names", 0, true),
	} {
		t.Run(name, func(t *testing.T) {
			recorder, _ := postReport(t, body)
			require.Equal(t, http.StatusConflict, recorder.Code, "body: %s", recorder.Body.String())
		})
	}
}

// TestPostFitCapabilityReportBindsTheBaseline covers the baseline half of the
// report binding, which is a policy field: a report measured against a different
// official reference must not mark channels.
func TestPostFitCapabilityReportBindsTheBaseline(t *testing.T) {
	setupFitCapabilityEndpoint(t)

	policy := fitpolicy.BuiltinPolicy()
	policy.Baseline = "baseline-a"
	encoded, err := common.Marshal(policy)
	require.NoError(t, err)
	snapshot, err := fitpolicy.CompileJSON(encoded)
	require.NoError(t, err)
	fitpolicy.Install(snapshot)
	t.Cleanup(func() { fitpolicy.Install(nil) })

	now := common.GetTimestamp()
	body := fmt.Sprintf(`{
		"report_id": "report-1", "run_id": "run-1", "suite": "cdp-k3",
		"policy_version": %d, "policy_hash": %q, "baseline_hash": "baseline-b",
		"generated_at": %d, "rounds": 3,
		"results": [{"channel_id": 7, "family": "kimi-k3", "model": "kimi-k3",
			"behavior": "tools.dynamic_names", "supported": true, "cases": "30/30"}]
	}`, snapshot.Version(), snapshot.Hash(), now)

	recorder, payload := postReport(t, body)
	require.Equal(t, http.StatusConflict, recorder.Code, "body: %s", recorder.Body.String())
	assert.Contains(t, payload["message"], "baseline")

	rows, err := model.ListChannelFitCapabilities(7)
	require.NoError(t, err)
	assert.Empty(t, rows)

	// The matching baseline is accepted, so the check is a binding rather than a
	// blanket refusal.
	matching := strings.Replace(body, `"baseline_hash": "baseline-b"`, `"baseline_hash": "baseline-a"`, 1)
	recorder, _ = postReport(t, matching)
	require.Equal(t, http.StatusOK, recorder.Code, "body: %s", recorder.Body.String())
}
