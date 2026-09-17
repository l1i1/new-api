package controller

import (
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

const (
	partnerMaxInviterIDs = 100
	partnerMaxUserIDs    = 1000
	partnerMaxPageSize   = 500
	partnerContentLimit  = 20000
)

// shanghaiMonthRange resolves "2026-09" to the half-open unix range of that
// East-8 natural month, matching the business-analysis calendar.
func shanghaiMonthRange(month string) (int64, int64, bool) {
	parsed, err := time.Parse("2006-01", strings.TrimSpace(month))
	if err != nil {
		return 0, 0, false
	}
	zone := time.FixedZone("CST", 8*60*60)
	start := time.Date(parsed.Year(), parsed.Month(), 1, 0, 0, 0, 0, zone).Unix()
	return start, time.Date(parsed.Year(), parsed.Month()+1, 1, 0, 0, 0, 0, zone).Unix(), true
}

func writePartnerError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"code": code, "message": message})
}

func requirePartnerBypass(c *gin.Context) bool {
	if !middleware.PartnerBypassActive(c) {
		writePartnerError(c, http.StatusUnauthorized, "partner_unauthorized", "partner signature is invalid")
		return false
	}
	return true
}

// PartnerHealth is the service probe for partner-console sync clients.
func PartnerHealth(c *gin.Context) {
	if !requirePartnerBypass(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":             true,
		"quota_per_unit": common.QuotaPerUnit,
		"server_time":    time.Now().Unix(),
	})
}

// PartnerUsers returns invitees attributed to the given inviters.
func PartnerUsers(c *gin.Context) {
	if !requirePartnerBypass(c) {
		return
	}
	var body struct {
		InviterIDs []int `json:"inviter_ids"`
		Page       int   `json:"page"`
		PageSize   int   `json:"page_size"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writePartnerError(c, http.StatusBadRequest, "partner_invalid", "request body is invalid")
		return
	}
	if len(body.InviterIDs) == 0 || len(body.InviterIDs) > partnerMaxInviterIDs {
		writePartnerError(c, http.StatusBadRequest, "partner_invalid", "inviter_ids must hold 1-100 ids")
		return
	}
	if body.Page <= 0 {
		body.Page = 1
	}
	if body.PageSize <= 0 || body.PageSize > partnerMaxPageSize {
		body.PageSize = 100
	}
	invitees, total, err := model.GetPartnerInvitees(body.InviterIDs, body.Page, body.PageSize)
	if err != nil {
		writePartnerError(c, http.StatusServiceUnavailable, "partner_unavailable", "failed to load invitees")
		return
	}
	users := make([]gin.H, 0, len(invitees))
	for _, invitee := range invitees {
		users = append(users, gin.H{
			"id":               invitee.ID,
			"masked_id":        maskedPartnerUserID(invitee.ID),
			"registered_at":    invitee.RegisteredAt,
			"commission_until": invitee.CommissionUntil,
			"inviter_id":       invitee.InviterID,
			"status":           invitee.Status,
		})
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "users": users})
}

// PartnerConsumption returns the monthly consumption four-piece per user.
func PartnerConsumption(c *gin.Context) {
	if !requirePartnerBypass(c) {
		return
	}
	var body struct {
		UserIDs []int  `json:"user_ids"`
		Month   string `json:"month"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writePartnerError(c, http.StatusBadRequest, "partner_invalid", "request body is invalid")
		return
	}
	if len(body.UserIDs) == 0 || len(body.UserIDs) > partnerMaxUserIDs {
		writePartnerError(c, http.StatusBadRequest, "partner_invalid", "user_ids must hold 1-1000 ids")
		return
	}
	start, end, ok := shanghaiMonthRange(body.Month)
	if !ok {
		writePartnerError(c, http.StatusBadRequest, "partner_invalid", "month must be YYYY-MM")
		return
	}
	rows, err := model.GetPartnerConsumption(body.UserIDs, start, end)
	if err != nil {
		writePartnerError(c, http.StatusServiceUnavailable, "partner_unavailable", "failed to load consumption")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"quota_per_unit": common.QuotaPerUnit,
		"month_start":    start,
		"month_end":      end,
		"rows":           rows,
	})
}

// PartnerInviteRewardOffsets returns applied 20% rewards per inviter for
// settlement deduction during the transition period.
func PartnerInviteRewardOffsets(c *gin.Context) {
	if !requirePartnerBypass(c) {
		return
	}
	var body struct {
		InviterIDs []int `json:"inviter_ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writePartnerError(c, http.StatusBadRequest, "partner_invalid", "request body is invalid")
		return
	}
	if len(body.InviterIDs) == 0 || len(body.InviterIDs) > partnerMaxInviterIDs {
		writePartnerError(c, http.StatusBadRequest, "partner_invalid", "inviter_ids must hold 1-100 ids")
		return
	}
	offsets, err := model.GetPartnerInviteRewardOffsets(body.InviterIDs)
	if err != nil {
		writePartnerError(c, http.StatusServiceUnavailable, "partner_unavailable", "failed to load reward offsets")
		return
	}
	rows := make([]gin.H, 0, len(offsets))
	for inviterID, reward := range offsets {
		rows = append(rows, gin.H{"inviter_id": inviterID, "applied_reward_quota": reward})
	}
	c.JSON(http.StatusOK, gin.H{"rows": rows})
}

// PartnerLedger returns one user's replayable topup/consume/refund events in
// a time window for exact FIFO commission computation on the console side.
func PartnerLedger(c *gin.Context) {
	if !requirePartnerBypass(c) {
		return
	}
	var body struct {
		UserID   int   `json:"user_id"`
		Since    int64 `json:"since"`
		Until    int64 `json:"until"`
		Page     int   `json:"page"`
		PageSize int   `json:"page_size"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writePartnerError(c, http.StatusBadRequest, "partner_invalid", "request body is invalid")
		return
	}
	if body.UserID <= 0 || body.Until <= body.Since {
		writePartnerError(c, http.StatusBadRequest, "partner_invalid", "user_id and a valid [since, until) range are required")
		return
	}
	if body.Page <= 0 {
		body.Page = 1
	}
	if body.PageSize <= 0 || body.PageSize > 5000 {
		body.PageSize = 5000
	}
	events, err := model.GetPartnerLedgerEvents(body.UserID, body.Since, body.Until, body.Page, body.PageSize)
	if err != nil {
		writePartnerError(c, http.StatusServiceUnavailable, "partner_unavailable", "failed to load ledger events")
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": events})
}

// PartnerConfig writes white-label display copy for a partner and merges
// inviter membership. Content-only calls keep the old behavior; passing
// inviter_user_ids additionally upserts membership (creating the entry when
// absent). Membership removal is not exposed: unattribution stays a manual,
// audited operator action.
func PartnerConfig(c *gin.Context) {
	if !requirePartnerBypass(c) {
		return
	}
	var body struct {
		PartnerID      string `json:"partner_id"`
		Contact        string `json:"contact"`
		Notice         string `json:"notice"`
		InviterUserIDs []int  `json:"inviter_user_ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writePartnerError(c, http.StatusBadRequest, "partner_invalid", "request body is invalid")
		return
	}
	body.PartnerID = strings.TrimSpace(body.PartnerID)
	if body.PartnerID == "" || len(body.Contact) > partnerContentLimit || len(body.Notice) > partnerContentLimit {
		writePartnerError(c, http.StatusBadRequest, "partner_invalid", "partner_id is required and contents are limited to 20000 chars")
		return
	}
	if len(body.InviterUserIDs) > partnerMaxInviterIDs {
		writePartnerError(c, http.StatusBadRequest, "partner_invalid", "inviter_user_ids holds at most 100 ids")
		return
	}
	if len(body.InviterUserIDs) > 0 {
		if _, err := operation_setting.UpsertPartnerMembers(body.PartnerID, body.InviterUserIDs, model.UpdateOption); err != nil {
			writePartnerError(c, http.StatusBadRequest, "partner_invalid", err.Error())
			return
		}
	} else if _, found := operation_setting.FindPartner(body.PartnerID); !found {
		writePartnerError(c, http.StatusForbidden, "partner_forbidden", "unknown partner")
		return
	}
	version, err := operation_setting.UpdatePartnerContent(body.PartnerID, body.Contact, body.Notice, model.UpdateOption)
	if err != nil {
		writePartnerError(c, http.StatusForbidden, "partner_forbidden", "unknown partner")
		return
	}
	c.JSON(http.StatusOK, gin.H{"partner_id": body.PartnerID, "version": version})
}

// resolvePartnerNotice returns the partner notice copy for white-labeled
// users whose commission window is still open, or "" for everyone else.
func resolvePartnerNotice(c *gin.Context) string {
	id := c.GetInt("id")
	if id <= 0 {
		return ""
	}
	user, err := model.GetSelfUserById(id)
	if err != nil {
		return ""
	}
	whiteLabel := user.GetSetting().WhiteLabel
	if whiteLabel == nil || !whiteLabel.HideReferral || whiteLabel.PartnerID == "" {
		return ""
	}
	if whiteLabel.Until > 0 && time.Now().Unix() > whiteLabel.Until {
		return ""
	}
	entry, found := operation_setting.FindPartner(whiteLabel.PartnerID)
	if !found {
		return ""
	}
	return entry.Notice
}

func maskedPartnerUserID(id int) string {
	digit := id % 10
	if digit < 0 {
		digit = -digit
	}
	return "u***" + string(rune('0'+digit))
}

// resolveTopupContact returns the partner contact copy for white-labeled
// users whose commission window is still open, falling back to the global
// topup_contact for everyone else. Only the display value is overridden;
// the global configuration is never mutated here.
func resolveTopupContact(c *gin.Context) string {
	fallback := operation_setting.GetPaymentSetting().TopupContact
	id := c.GetInt("id")
	if id <= 0 {
		return fallback
	}
	user, err := model.GetSelfUserById(id)
	if err != nil {
		return fallback
	}
	whiteLabel := user.GetSetting().WhiteLabel
	if whiteLabel == nil || !whiteLabel.HideReferral || whiteLabel.PartnerID == "" {
		return fallback
	}
	if whiteLabel.Until > 0 && time.Now().Unix() > whiteLabel.Until {
		return fallback
	}
	entry, found := operation_setting.FindPartner(whiteLabel.PartnerID)
	if !found || strings.TrimSpace(entry.Contact) == "" {
		return fallback
	}
	return entry.Contact
}
