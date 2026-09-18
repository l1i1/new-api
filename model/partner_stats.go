package model

import (
	"github.com/QuantumNous/new-api/common"
)

// PartnerInvitee is the per-user attribution row served to partner-console.
type PartnerInvitee struct {
	ID              int   `json:"id"`
	InviterID       int   `json:"inviter_id"`
	RegisteredAt    int64 `json:"registered_at"`
	CommissionUntil int64 `json:"commission_until"`
	Status          int   `json:"status"`
}

// PartnerConsumptionRow is one user's monthly consumption four-piece set.
type PartnerConsumptionRow struct {
	UserID             int   `json:"user_id"`
	ConsumeQuota       int64 `json:"consume_quota"`
	OfficialExcluded   int64 `json:"official_excluded_quota"`
	RefundQuota        int64 `json:"refund_quota"`
	TopupCreditedQuota int64 `json:"topup_credited_quota"`
}

// officialGroupSuffix matches the contractual no-profit official groups.
const officialGroupSuffix = "-Official"

// GetPartnerInvitees returns users attributed to the given inviters.
// inviterIDs is capped by the caller; page is 1-based.
func GetPartnerInvitees(inviterIDs []int, page, pageSize int) ([]PartnerInvitee, int64, error) {
	if DB == nil {
		return nil, 0, ErrPaymentGatewaySettlementRetryable
	}
	if len(inviterIDs) == 0 || page <= 0 || pageSize <= 0 || pageSize > 500 {
		return nil, 0, ErrPaymentGatewaySettlementInvalid
	}
	var total int64
	if err := DB.Model(&User{}).Where("inviter_id IN ?", inviterIDs).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var users []User
	if err := DB.Select("id", "inviter_id", "created_at", "status").
		Where("inviter_id IN ?", inviterIDs).
		Order("id ASC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&users).Error; err != nil {
		return nil, 0, err
	}
	invitees := make([]PartnerInvitee, 0, len(users))
	for _, user := range users {
		invitees = append(invitees, PartnerInvitee{
			ID:              user.Id,
			InviterID:       user.InviterId,
			RegisteredAt:    user.CreatedAt,
			CommissionUntil: newWhiteLabelConfig("", user.CreatedAt).Until,
			Status:          user.Status,
		})
	}
	return invitees, total, nil
}

// GetPartnerConsumption aggregates one month of consumption per user.
// start/end are unix seconds, half-open [start, end). LOG_DB is honored so
// log-database deployments keep working.
func GetPartnerConsumption(userIDs []int, start, end int64) ([]PartnerConsumptionRow, error) {
	if len(userIDs) == 0 || end <= start {
		return nil, ErrPaymentGatewaySettlementInvalid
	}
	db := LOG_DB
	if db == nil {
		db = DB
	}
	if db == nil {
		return nil, ErrPaymentGatewaySettlementRetryable
	}
	type consumeRow struct {
		UserID  int    `gorm:"column:user_id"`
		Quota   int64  `gorm:"column:quota"`
		Group   string `gorm:"column:group"`
		LogType int    `gorm:"column:type"`
	}
	var rows []consumeRow
	// group/type are reserved words: backticks for SQLite/MySQL, double
	// quotes for PostgreSQL (see model/channel.go for the same pattern).
	quote := func(column string) string {
		if common.UsingLogDatabase(common.DatabaseTypePostgreSQL) {
			return `"` + column + `"`
		}
		return "`" + column + "`"
	}
	if err := db.Table("logs").Select("user_id, quota, "+quote("group")+", "+quote("type")).
		Where("user_id IN ?", userIDs).
		Where("created_at >= ? AND created_at < ?", start, end).
		Where(quote("type")+" IN ?", []int{LogTypeConsume, LogTypeRefund}).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	byUser := make(map[int]*PartnerConsumptionRow, len(userIDs))
	for _, id := range userIDs {
		byUser[id] = &PartnerConsumptionRow{UserID: id}
	}
	for _, row := range rows {
		target, ok := byUser[row.UserID]
		if !ok {
			continue
		}
		switch row.LogType {
		case LogTypeConsume:
			if len(row.Group) >= len(officialGroupSuffix) && row.Group[len(row.Group)-len(officialGroupSuffix):] == officialGroupSuffix {
				target.OfficialExcluded += int64(row.Quota)
			} else {
				target.ConsumeQuota += int64(row.Quota)
			}
		case LogTypeRefund:
			target.RefundQuota += int64(row.Quota)
		}
	}
	var credited []struct {
		UserID   int
		Credited int64
	}
	if DB != nil {
		if err := DB.Table("top_ups").Select("user_id, COALESCE(SUM(credited_quota), 0) AS credited").
			Where("user_id IN ?", userIDs).
			Where("status = ?", common.TopUpStatusSuccess).
			Group("user_id").
			Find(&credited).Error; err != nil {
			return nil, err
		}
		for _, row := range credited {
			if target, ok := byUser[row.UserID]; ok {
				target.TopupCreditedQuota = row.Credited
			}
		}
	}
	result := make([]PartnerConsumptionRow, 0, len(byUser))
	for _, id := range userIDs {
		result = append(result, *byUser[id])
	}
	return result, nil
}

// GetPartnerInviteRewardOffsets sums applied 20% first-top-up quota rewards
// per inviter for settlement deduction during the transition period.
func GetPartnerInviteRewardOffsets(inviterIDs []int) (map[int]int64, error) {
	if DB == nil {
		return nil, ErrPaymentGatewaySettlementRetryable
	}
	if len(inviterIDs) == 0 {
		return map[int]int64{}, nil
	}
	var rows []struct {
		InviterID int
		Reward    int64
	}
	if err := DB.Table("invite_top_up_rewards").
		Select("inviter_id, COALESCE(SUM(reward_quota), 0) AS reward").
		Where("inviter_id IN ?", inviterIDs).
		Where("status = ?", InviteTopUpRewardStatusApplied).
		Group("inviter_id").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	offsets := make(map[int]int64, len(rows))
	for _, row := range rows {
		offsets[row.InviterID] = row.Reward
	}
	return offsets, nil
}

// PartnerLedgerEvent is one FIFO-replayable entry: a successful top-up
// credit or a consume/refund quota movement. Excluded marks official-group
// consumption: it spends balance but never accrues commission, so the console
// drains it from the FIFO lots without counting it.
type PartnerLedgerEvent struct {
	Kind      string `json:"kind"` // "topup" | "consume" | "refund"
	CreatedAt int64  `json:"created_at"`
	Quota     int64  `json:"quota"`
	Excluded  bool   `json:"excluded,omitempty"`
}

// GetPartnerLedgerEvents returns one user's replayable events in time order
// for exact FIFO commission computation. page is 1-based, capped at 5000.
func GetPartnerLedgerEvents(userID int, start, end int64, page, pageSize int) ([]PartnerLedgerEvent, error) {
	if userID <= 0 || end <= start || page <= 0 || pageSize <= 0 || pageSize > 5000 {
		return nil, ErrPaymentGatewaySettlementInvalid
	}
	logDB := LOG_DB
	if logDB == nil {
		logDB = DB
	}
	if logDB == nil || DB == nil {
		return nil, ErrPaymentGatewaySettlementRetryable
	}
	type consumeRow struct {
		Quota     int64  `gorm:"column:quota"`
		Group     string `gorm:"column:group"`
		LogType   int    `gorm:"column:type"`
		CreatedAt int64  `gorm:"column:created_at"`
	}
	quote := func(column string) string {
		if common.UsingLogDatabase(common.DatabaseTypePostgreSQL) {
			return `"` + column + `"`
		}
		return "`" + column + "`"
	}
	var logRows []consumeRow
	if err := logDB.Table("logs").Select("quota, "+quote("group")+", "+quote("type")+" AS type, created_at").
		Where("user_id = ?", userID).
		Where("created_at >= ? AND created_at < ?", start, end).
		Where(quote("type")+" IN ?", []int{LogTypeConsume, LogTypeRefund}).
		Order("created_at ASC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&logRows).Error; err != nil {
		return nil, err
	}
	var topupRows []struct {
		Credited    int64 `gorm:"column:credited"`
		CompletedAt int64 `gorm:"column:completed_at"`
	}
	if err := DB.Table("top_ups").Select("credited_quota AS credited, complete_time AS completed_at").
		Where("user_id = ?", userID).
		Where("status = ?", common.TopUpStatusSuccess).
		Where("complete_time >= ? AND complete_time < ?", start, end).
		Where("credited_quota > 0").
		Order("complete_time ASC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&topupRows).Error; err != nil {
		return nil, err
	}
	events := make([]PartnerLedgerEvent, 0, len(logRows)+len(topupRows))
	for _, row := range logRows {
		switch row.LogType {
		case LogTypeConsume:
			official := len(row.Group) >= len(officialGroupSuffix) && row.Group[len(row.Group)-len(officialGroupSuffix):] == officialGroupSuffix
			events = append(events, PartnerLedgerEvent{Kind: "consume", CreatedAt: row.CreatedAt, Quota: int64(row.Quota), Excluded: official})
		case LogTypeRefund:
			events = append(events, PartnerLedgerEvent{Kind: "refund", CreatedAt: row.CreatedAt, Quota: int64(row.Quota)})
		}
	}
	for _, row := range topupRows {
		events = append(events, PartnerLedgerEvent{Kind: "topup", CreatedAt: row.CompletedAt, Quota: row.Credited})
	}
	// Time order with top-ups before same-timestamp consumption, matching the
	// console replay expectation.
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && (events[j].CreatedAt < events[j-1].CreatedAt ||
			(events[j].CreatedAt == events[j-1].CreatedAt && events[j].Kind == "topup" && events[j-1].Kind != "topup")); j-- {
			events[j], events[j-1] = events[j-1], events[j]
		}
	}
	return events, nil
}
