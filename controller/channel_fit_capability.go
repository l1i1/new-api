package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

// Channel fit capability endpoint.
//
// This is the only write path for behaviour-level capability marks. It is kept
// separate from the generic channel update endpoints because a capability write
// is a compare-and-swap against a revision, while UpdateChannel saves the whole
// channel row — letting the latter touch capability state would mean an
// unrelated edit could silently reset measurements.
//
// The route is registered outside the /api/channel group, which applies
// AdminAuth to everything under it: a controlled suite applier must be able to
// hold capability.write without holding general administrator rights.

type fitCapabilityRequest struct {
	ChannelId     int    `json:"channel_id"`
	Family        string `json:"family"`
	Model         string `json:"model"`
	Behavior      string `json:"behavior"`
	Supported     bool   `json:"supported"`
	Source        string `json:"source"`
	Suite         string `json:"suite"`
	Cases         string `json:"cases"`
	Rounds        int    `json:"rounds"`
	At            int64  `json:"at"`
	ExpiresAt     int64  `json:"expires_at"`
	PolicyVersion int    `json:"policy_version"`
	PolicyHash    string `json:"policy_hash"`
	BaselineHash  string `json:"baseline_hash"`
	ReportId      string `json:"report_id"`
	RunId         string `json:"run_id"`
	Force         bool   `json:"force"`
	// ExpectedRevision is a pointer so a missing field can be rejected. A write
	// that does not state which revision it replaces is a blind overwrite, which
	// is exactly what the CAS exists to prevent.
	ExpectedRevision *int64 `json:"expected_revision"`
}

// PutChannelFitCapability writes one capability mark under compare-and-swap.
func PutChannelFitCapability(c *gin.Context) {
	var request fitCapabilityRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid request body"})
		return
	}
	if request.ExpectedRevision == nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "expected_revision is required"})
		return
	}
	if request.Force && !authz.Can(c.GetInt("id"), c.GetInt("role"), authz.ChannelCapabilityForce) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "force requires the capability.force permission"})
		return
	}

	before, _, err := model.GetChannelFitCapability(request.ChannelId, request.Family, request.Model, request.Behavior)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to read the current capability"})
		return
	}

	write := model.FitCapabilityWrite{
		ChannelId:        request.ChannelId,
		Family:           request.Family,
		Model:            request.Model,
		Behavior:         request.Behavior,
		Supported:        request.Supported,
		Source:           request.Source,
		Suite:            request.Suite,
		Cases:            request.Cases,
		Rounds:           request.Rounds,
		At:               request.At,
		ExpiresAt:        request.ExpiresAt,
		PolicyVersion:    request.PolicyVersion,
		PolicyHash:       request.PolicyHash,
		BaselineHash:     request.BaselineHash,
		ReportId:         request.ReportId,
		RunId:            request.RunId,
		Force:            request.Force,
		ExpectedRevision: *request.ExpectedRevision,
	}
	row, err := model.ApplyChannelFitCapability(write, common.GetTimestamp())
	if err != nil {
		var conflict *model.FitCapabilityConflictError
		if errors.As(err, &conflict) {
			// common.ApiError returns HTTP 200 for every error, so a conflict has
			// to be sent explicitly or the caller would read it as success.
			c.JSON(http.StatusConflict, gin.H{
				"success": false,
				"message": conflict.Error(),
				"current": conflict.Current,
			})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}

	// Ride the existing channel-cache notification so every node picks the mark
	// up through the path already proven for channel changes.
	model.InitChannelCacheAndNotify()

	recordFitCapabilityAudit(c, before, row, request.Force, *request.ExpectedRevision)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": row})
}

// GetChannelFitCapabilities lists the marks of one channel.
func GetChannelFitCapabilities(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Query("channel_id"))
	if err != nil || channelID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "channel_id must be a positive integer"})
		return
	}
	rows, err := model.ListChannelFitCapabilities(channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to list capabilities"})
		return
	}
	now := common.GetTimestamp()
	type markView struct {
		model.ChannelFitCapability
		State string `json:"state"`
	}
	views := make([]markView, 0, len(rows))
	for i := range rows {
		views = append(views, markView{
			ChannelFitCapability: rows[i],
			State:                model.FitCapabilityState(&rows[i], now, model.DefaultFitCapabilityStaleAfterDays, "", ""),
		})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": views})
}

// recordFitCapabilityAudit writes the audit trail for a capability change.
//
// The capability row is already committed by the time this runs, and the audit
// storage may be a different database entirely (LOG_DB can point at a dedicated
// log database or ClickHouse). The two are therefore NOT atomic: the audit is
// best-effort and its failure is logged by RecordAuditLog, never allowed to roll
// back or fail the capability write. Only identifiers and behaviour names are
// recorded — never a request body, credential or upstream response.
func recordFitCapabilityAudit(c *gin.Context, before, after *model.ChannelFitCapability, force bool, expectedRevision int64) {
	if after == nil {
		return
	}
	params := model.AuditFields{
		"channel_id":   after.ChannelId,
		"family":       after.Family,
		"model":        after.Model,
		"behavior":     after.Behavior,
		"supported":    after.Supported,
		"source":       after.Source,
		"suite":        after.Suite,
		"run_id":       after.RunId,
		"report_id":    after.ReportId,
		"force":        force,
		"expires_at":   after.ExpiresAt,
		"new_revision": after.Revision,
	}
	// The revision the caller claimed is part of the record: it is what makes a
	// later "was this an overwrite or a race?" question answerable from the audit
	// alone, and it is available even when no row existed yet.
	params["expected_revision"] = expectedRevision
	if before != nil {
		params["before_supported"] = before.Supported
		params["before_source"] = before.Source
		params["before_revision"] = before.Revision
	}
	model.RecordAuditLog(c, model.AuditLog{
		Category: "channel",
		Action:   "channel.fit_capability.write",
		Success:  true,
		Content:  "channel fit capability write",
		Other: model.AuditOther{
			Op: &model.AuditOperation{
				Action: "channel.fit_capability.write",
				Params: params,
			},
		},
	})
}

// PostFitCapabilityReport accepts one suite run's results.
//
// The report is validated before anything is written: its shape is bounded and
// it must be bound to the policy and baseline currently in force. A report
// measured against older rules describes behaviour under requirements that no
// longer exist, so it is rejected as a whole (409) rather than applied with a
// warning -- silently marking channels against a stale rule set is exactly the
// failure the binding exists to prevent.
func PostFitCapabilityReport(c *gin.Context) {
	var report fitpolicy.SuiteReport
	if err := c.ShouldBindJSON(&report); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid report body"})
		return
	}
	fitpolicy.NormalizeSuiteReport(&report)

	snapshot := fitpolicy.Current()
	if snapshot == nil {
		// Nothing to bind to: with no policy installed a mark has no defined
		// meaning, and accepting it would leave the fleet with marks that no
		// rule can interpret.
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "no fit policy is installed; the report cannot be bound"})
		return
	}
	binding := fitpolicy.ReportBinding{
		PolicyVersion: snapshot.Version(),
		PolicyHash:    snapshot.Hash(),
		BaselineHash:  snapshot.Baseline(),
	}
	if err := fitpolicy.ValidateSuiteReport(&report, binding); err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": err.Error()})
		return
	}

	now := common.GetTimestamp()
	summary := model.ApplyFitCapabilityReport(report, now)
	if summary.Applied > 0 {
		model.InitChannelCacheAndNotify()
	}
	recordFitCapabilityReportAudit(c, &report, summary)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": summary})
}

// recordFitCapabilityReportAudit records the run, not the individual rows: the
// per-row trail is written by the single-mark endpoint, and a report can carry
// thousands of results. Counts and identifiers only.
func recordFitCapabilityReportAudit(c *gin.Context, report *fitpolicy.SuiteReport, summary model.FitCapabilityReportSummary) {
	if report == nil {
		return
	}
	model.RecordAuditLog(c, model.AuditLog{
		Category: "channel",
		Action:   "channel.fit_capability.report",
		Success:  summary.Failed == 0,
		Content:  "channel fit capability report applied",
		Other: model.AuditOther{
			Op: &model.AuditOperation{
				Action: "channel.fit_capability.report",
				Params: model.AuditFields{
					"report_id":      report.ReportID,
					"run_id":         report.RunID,
					"suite":          report.Suite,
					"policy_version": report.PolicyVersion,
					"results":        len(report.Results),
					"applied":        summary.Applied,
					"conflicts":      summary.Conflicts,
					"failed":         summary.Failed,
				},
			},
		},
	})
}
