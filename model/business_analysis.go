/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package model

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

const (
	businessReportTimezoneOffsetSeconds = int64(8 * 60 * 60)
	businessReportSecondsPerDay         = int64(24 * 60 * 60)
	businessReportCNYPerUSD             = 7.0
)

var (
	businessQuotaPattern    = regexp.MustCompile(`¥\s*([0-9]+(?:\.[0-9]+)?)\s*额度`)
	businessOverridePattern = regexp.MustCompile(`from ¥\s*(-?[0-9]+(?:\.[0-9]+)?)\s*额度 to ¥\s*(-?[0-9]+(?:\.[0-9]+)?)\s*额度`)
)

// BusinessAnalysisReport is the read-only aggregate consumed by the admin dashboard.
// The report deliberately keeps quota values in their native unit so that the
// UI can show an auditable CNY/USD reference without losing precision.
type BusinessAnalysisReport struct {
	GeneratedAt  string                 `json:"generated_at"`
	QuotaPerUnit float64                `json:"quota_per_unit"`
	CNYPerUSD    float64                `json:"cny_per_usd"`
	Inventory    BusinessQuotaInventory `json:"inventory"`
	QuotaOrigin  BusinessQuotaOrigin    `json:"quota_origin"`
	Daily        []BusinessFlowBucket   `json:"daily"`
	Weekly       []BusinessFlowBucket   `json:"weekly"`
	Totals       BusinessFlowTotals     `json:"totals"`
	FlowStart    int64                  `json:"flow_start_timestamp"`
	FlowEnd      int64                  `json:"flow_end_timestamp"`
}

type BusinessQuotaInventory struct {
	Users                      BusinessUserCounts    `json:"users"`
	ConsumableEnabledVisible   int64                 `json:"consumable_enabled_visible"`
	ConsumableEnabledQuotaOnly int64                 `json:"consumable_enabled_quota_only"`
	ConsumableEnabledAffOnly   int64                 `json:"consumable_enabled_aff_only"`
	NetEnabledVisible          int64                 `json:"net_enabled_visible"`
	DisabledOrDeletedPositive  int64                 `json:"disabled_or_deleted_positive_visible"`
	AllUsersPositiveVisible    int64                 `json:"all_users_positive_visible"`
	AllUsersNetVisible         int64                 `json:"all_users_net_visible"`
	PositiveEnabledUserCount   int                   `json:"positive_enabled_user_count"`
	PositiveDisabledUserCount  int                   `json:"positive_disabled_user_count"`
	NegativeEnabledUserCount   int                   `json:"negative_enabled_user_count"`
	NegativeEnabledVisible     int64                 `json:"negative_enabled_visible"`
	Concentration              BusinessConcentration `json:"concentration"`
	Top20                      []BusinessBalanceRow  `json:"top20"`
}

type BusinessUserCounts struct {
	Total             int `json:"total"`
	Enabled           int `json:"enabled"`
	DisabledOrDeleted int `json:"disabled_or_deleted"`
}

type BusinessConcentration struct {
	Top1  float64 `json:"top1"`
	Top5  float64 `json:"top5"`
	Top20 float64 `json:"top20"`
}

type BusinessBalanceRow struct {
	ID           int    `json:"id"`
	Username     string `json:"username"`
	Quota        int64  `json:"quota"`
	AffQuota     int64  `json:"aff_quota"`
	Visible      int64  `json:"visible"`
	UsedQuota    int64  `json:"used_quota"`
	RequestCount int64  `json:"request_count"`
}

type BusinessQuotaOrigin struct {
	Options                     BusinessQuotaOriginOptions `json:"options"`
	EnabledUsers                int                        `json:"enabled_users"`
	PositiveQuotaEnabledUsers   int                        `json:"positive_quota_enabled_users"`
	PositiveQuotaEnabledTotal   int64                      `json:"positive_quota_enabled_total"`
	PositiveQuotaNoTopupUsers   int                        `json:"positive_quota_no_topup_users"`
	PositiveQuotaNoTopupTotal   int64                      `json:"positive_quota_no_topup_total"`
	PositiveQuotaWithTopupUsers int                        `json:"positive_quota_with_topup_users"`
	PositiveQuotaWithTopupTotal int64                      `json:"positive_quota_with_topup_total"`
	EnabledPositiveAffUsers     int                        `json:"enabled_positive_aff_users"`
	EnabledPositiveAffTotal     int64                      `json:"enabled_positive_aff_total"`
	TopNoTopup                  []BusinessBalanceRow       `json:"top_no_topup"`
}

type BusinessQuotaOriginOptions struct {
	QuotaForNewUser int64 `json:"quota_for_new_user"`
	CheckinMinQuota int64 `json:"checkin_min_quota"`
	CheckinMaxQuota int64 `json:"checkin_max_quota"`
}

type BusinessFlowBucket struct {
	Label                       string  `json:"label"`
	Start                       int64   `json:"start"`
	End                         int64   `json:"end"`
	TopupOrders                 int     `json:"topup_orders"`
	TopupUsers                  int     `json:"topup_users"`
	TopupCNY                    float64 `json:"topup_cny"`
	TopupQuota                  int64   `json:"topup_quota"`
	ConsumeQuota                int64   `json:"consume_quota"`
	SignupGrantQuota            int64   `json:"signup_grant_quota"`
	SignupGrantCount            int     `json:"signup_grant_count"`
	CheckinQuota                int64   `json:"checkin_quota"`
	CheckinCount                int     `json:"checkin_count"`
	ManualAddQuota              int64   `json:"manual_add_quota"`
	ManualAddCount              int     `json:"manual_add_count"`
	ManualOverrideIncreaseQuota int64   `json:"manual_override_increase_quota"`
	ManualOverrideIncreaseCount int     `json:"manual_override_increase_count"`
	NonRechargeIncreaseQuota    int64   `json:"non_recharge_increase_quota"`
	NetAfterConsumeQuota        int64   `json:"net_after_consume_quota"`
}

type BusinessFlowTotals struct {
	TopupCNY                    float64 `json:"topup_cny"`
	TopupQuota                  int64   `json:"topup_quota"`
	ConsumeQuota                int64   `json:"consume_quota"`
	SignupGrantQuota            int64   `json:"signup_grant_quota"`
	CheckinQuota                int64   `json:"checkin_quota"`
	ManualAddQuota              int64   `json:"manual_add_quota"`
	ManualOverrideIncreaseQuota int64   `json:"manual_override_increase_quota"`
	NonRechargeIncreaseQuota    int64   `json:"non_recharge_increase_quota"`
	NetAfterConsumeQuota        int64   `json:"net_after_consume_quota"`
}

type businessPeriod struct {
	Label string
	Start int64
	End   int64
}

type businessLogRecord struct {
	CreatedAt int64
	Content   string
}

type businessBalanceUser struct {
	row     BusinessBalanceRow
	visible int64
	enabled bool
}

var completedBusinessTopUpStatuses = []string{
	"success",
	"succeeded",
	"completed",
	"complete",
	"paid",
}

// BuildBusinessAnalysisReport reads the minimum data needed by all three
// Tokeness operations reports in one server-side request. The endpoint is
// intentionally read-only; all writes remain in the existing payment/reward
// workflows.
func BuildBusinessAnalysisReport(dailyPeriods, weeklyPeriods int, now int64) (*BusinessAnalysisReport, error) {
	if dailyPeriods <= 0 || weeklyPeriods <= 0 {
		return nil, errors.New("period counts must be positive")
	}
	if DB == nil || LOG_DB == nil {
		return nil, errors.New("database is not initialized")
	}

	quotaPerUnit := common.QuotaPerUnit
	if quotaPerUnit <= 0 || math.IsNaN(quotaPerUnit) || math.IsInf(quotaPerUnit, 0) {
		quotaPerUnit = 500_000
	}
	cnyPerUSD := operation_setting.Price
	if cnyPerUSD <= 0 || math.IsNaN(cnyPerUSD) || math.IsInf(cnyPerUSD, 0) {
		cnyPerUSD = operation_setting.USDExchangeRate
	}
	if cnyPerUSD <= 0 || math.IsNaN(cnyPerUSD) || math.IsInf(cnyPerUSD, 0) {
		cnyPerUSD = businessReportCNYPerUSD
	}
	if now <= 0 {
		now = time.Now().Unix()
	}

	daily := buildBusinessDailyPeriods(now, dailyPeriods)
	weekly := buildBusinessWeeklyPeriods(now, weeklyPeriods)
	flowStart := daily[0].Start
	if weekly[0].Start < flowStart {
		flowStart = weekly[0].Start
	}

	users, err := loadBusinessUsers()
	if err != nil {
		return nil, err
	}
	completedTopupUserIDs, err := loadCompletedBusinessTopupUserIDs()
	if err != nil {
		return nil, err
	}
	flowTopups, err := loadBusinessFlowTopups(flowStart, now)
	if err != nil {
		return nil, err
	}
	systemLogs, err := loadBusinessLogs(LogTypeSystem, flowStart, now)
	if err != nil {
		return nil, err
	}
	operationLogs, err := loadBusinessLogs(LogTypeManage, flowStart, now)
	if err != nil {
		return nil, err
	}

	dailyConsumeQuota, err := sumBusinessConsumeQuotaByPeriods(daily)
	if err != nil {
		return nil, err
	}
	weeklyConsumeQuota, err := sumBusinessConsumeQuotaByPeriods(weekly)
	if err != nil {
		return nil, err
	}
	dailyBuckets, err := buildBusinessFlowBuckets(daily, flowTopups, systemLogs, operationLogs, dailyConsumeQuota, quotaPerUnit)
	if err != nil {
		return nil, err
	}
	weeklyBuckets, err := buildBusinessFlowBuckets(weekly, flowTopups, systemLogs, operationLogs, weeklyConsumeQuota, quotaPerUnit)
	if err != nil {
		return nil, err
	}

	return &BusinessAnalysisReport{
		GeneratedAt:  time.Unix(now, 0).UTC().Format(time.RFC3339),
		QuotaPerUnit: quotaPerUnit,
		CNYPerUSD:    cnyPerUSD,
		Inventory:    buildBusinessQuotaInventory(users),
		QuotaOrigin:  buildBusinessQuotaOrigin(users, completedTopupUserIDs),
		Daily:        dailyBuckets,
		Weekly:       weeklyBuckets,
		Totals:       buildBusinessFlowTotals(dailyBuckets),
		FlowStart:    flowStart,
		FlowEnd:      now,
	}, nil
}

func loadBusinessUsers() ([]User, error) {
	var users []User
	err := DB.Unscoped().Select("id, username, status, quota, used_quota, request_count, aff_quota, deleted_at").Find(&users).Error
	return users, err
}

func loadCompletedBusinessTopupUserIDs() (map[int]struct{}, error) {
	var ids []int
	err := DB.Model(&TopUp{}).
		Where("status IN ? OR complete_time > 0", completedBusinessTopUpStatuses).
		Distinct("user_id").Pluck("user_id", &ids).Error
	if err != nil {
		return nil, err
	}
	result := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		result[id] = struct{}{}
	}
	return result, nil
}

func loadBusinessFlowTopups(start, end int64) ([]TopUp, error) {
	var topups []TopUp
	query := DB.Model(&TopUp{}).
		Select("id, user_id, amount, money, payment_method, payment_provider, payment_currency, credited_quota, create_time, complete_time, status").
		Where("(status IN ? OR complete_time > 0)", completedBusinessTopUpStatuses).
		Where("(complete_time >= ? AND complete_time < ?) OR (complete_time = 0 AND create_time >= ? AND create_time < ?)", start, end, start, end)
	err := query.Find(&topups).Error
	return topups, err
}

func loadBusinessLogs(logType int, start, end int64) ([]businessLogRecord, error) {
	var logs []businessLogRecord
	err := LOG_DB.Model(&Log{}).
		Select("created_at, content").
		Where("type = ? AND created_at >= ? AND created_at < ?", logType, start, end).
		Find(&logs).Error
	return logs, err
}

func buildBusinessQuotaInventory(users []User) BusinessQuotaInventory {
	rows := make([]businessBalanceUser, 0, len(users))
	result := BusinessQuotaInventory{
		Users: BusinessUserCounts{Total: len(users)},
		Top20: []BusinessBalanceRow{},
	}
	for _, user := range users {
		quota := int64(user.Quota)
		affQuota := int64(user.AffQuota)
		visible := quota + affQuota
		enabled := user.Status == common.UserStatusEnabled && !user.DeletedAt.Valid
		row := BusinessBalanceRow{
			ID:           user.Id,
			Username:     user.Username,
			Quota:        quota,
			AffQuota:     affQuota,
			Visible:      visible,
			UsedQuota:    int64(user.UsedQuota),
			RequestCount: int64(user.RequestCount),
		}
		rows = append(rows, businessBalanceUser{row: row, visible: visible, enabled: enabled})
		if enabled {
			result.Users.Enabled++
		} else {
			result.Users.DisabledOrDeleted++
		}
	}

	for _, item := range rows {
		if item.enabled {
			result.ConsumableEnabledVisible += maxInt64(item.visible, 0)
			result.ConsumableEnabledQuotaOnly += maxInt64(item.row.Quota, 0)
			result.ConsumableEnabledAffOnly += maxInt64(item.row.AffQuota, 0)
			result.NetEnabledVisible += item.visible
			if item.visible > 0 {
				result.PositiveEnabledUserCount++
			} else if item.visible < 0 {
				result.NegativeEnabledUserCount++
				result.NegativeEnabledVisible += item.visible
			}
		} else if item.visible > 0 {
			result.DisabledOrDeletedPositive += item.visible
			result.PositiveDisabledUserCount++
		}
		result.AllUsersPositiveVisible += maxInt64(item.visible, 0)
		result.AllUsersNetVisible += item.visible
	}

	positiveEnabled := make([]businessBalanceUser, 0)
	for _, item := range rows {
		if item.enabled && item.visible > 0 {
			positiveEnabled = append(positiveEnabled, item)
		}
	}
	sort.SliceStable(positiveEnabled, func(i, j int) bool {
		return positiveEnabled[i].visible > positiveEnabled[j].visible
	})
	for _, item := range positiveEnabled[:minInt(len(positiveEnabled), 20)] {
		result.Top20 = append(result.Top20, item.row)
	}
	top1 := int64(0)
	if len(positiveEnabled) > 0 {
		top1 = positiveEnabled[0].visible
	}
	result.Concentration = BusinessConcentration{
		Top1:  safeBusinessShare(top1, result.ConsumableEnabledVisible),
		Top5:  safeBusinessShare(sumBalanceUsers(positiveEnabled, 5), result.ConsumableEnabledVisible),
		Top20: safeBusinessShare(sumBalanceUsers(positiveEnabled, 20), result.ConsumableEnabledVisible),
	}
	return result
}

func buildBusinessQuotaOrigin(users []User, completedTopupUserIDs map[int]struct{}) BusinessQuotaOrigin {
	checkinMin, checkinMax := operation_setting.GetCheckinQuotaRange()
	result := BusinessQuotaOrigin{
		Options: BusinessQuotaOriginOptions{
			QuotaForNewUser: int64(common.QuotaForNewUser),
			CheckinMinQuota: int64(checkinMin),
			CheckinMaxQuota: int64(checkinMax),
		},
		TopNoTopup: []BusinessBalanceRow{},
	}
	noTopup := make([]BusinessBalanceRow, 0)
	for _, user := range users {
		if user.Status != common.UserStatusEnabled || user.DeletedAt.Valid {
			continue
		}
		result.EnabledUsers++
		quota := int64(user.Quota)
		affQuota := int64(user.AffQuota)
		if quota > 0 {
			result.PositiveQuotaEnabledUsers++
			result.PositiveQuotaEnabledTotal += quota
			row := BusinessBalanceRow{
				ID:           user.Id,
				Username:     user.Username,
				Quota:        quota,
				AffQuota:     affQuota,
				Visible:      quota + affQuota,
				UsedQuota:    int64(user.UsedQuota),
				RequestCount: int64(user.RequestCount),
			}
			if _, ok := completedTopupUserIDs[user.Id]; ok {
				result.PositiveQuotaWithTopupUsers++
				result.PositiveQuotaWithTopupTotal += quota
			} else {
				result.PositiveQuotaNoTopupUsers++
				result.PositiveQuotaNoTopupTotal += quota
				noTopup = append(noTopup, row)
			}
		}
		if affQuota > 0 {
			result.EnabledPositiveAffUsers++
			result.EnabledPositiveAffTotal += affQuota
		}
	}
	sort.SliceStable(noTopup, func(i, j int) bool { return noTopup[i].Quota > noTopup[j].Quota })
	result.TopNoTopup = append(result.TopNoTopup, noTopup[:minInt(len(noTopup), 20)]...)
	return result
}

func buildBusinessDailyPeriods(now int64, count int) []businessPeriod {
	end := startOfBusinessShanghaiDay(now) + businessReportSecondsPerDay
	periods := make([]businessPeriod, 0, count)
	for i := count - 1; i >= 0; i-- {
		start := end - int64(i+1)*businessReportSecondsPerDay
		periodEnd := start + businessReportSecondsPerDay
		periods = append(periods, businessPeriod{Label: businessShanghaiDate(start), Start: start, End: periodEnd})
	}
	return periods
}

func buildBusinessWeeklyPeriods(now int64, count int) []businessPeriod {
	weekStart := startOfBusinessShanghaiWeek(now)
	periods := make([]businessPeriod, 0, count)
	for i := count - 1; i >= 0; i-- {
		start := weekStart - int64(i)*7*businessReportSecondsPerDay
		end := start + 7*businessReportSecondsPerDay
		periods = append(periods, businessPeriod{
			Label: businessShanghaiDate(start) + " ~ " + businessShanghaiDate(end-1),
			Start: start,
			End:   end,
		})
	}
	return periods
}

func startOfBusinessShanghaiDay(timestamp int64) int64 {
	shifted := timestamp + businessReportTimezoneOffsetSeconds
	return shifted/businessReportSecondsPerDay*businessReportSecondsPerDay - businessReportTimezoneOffsetSeconds
}

func startOfBusinessShanghaiWeek(timestamp int64) int64 {
	shifted := timestamp + businessReportTimezoneOffsetSeconds
	dayIndex := shifted / businessReportSecondsPerDay
	dayOfWeek := (dayIndex + 3) % 7
	return (dayIndex-dayOfWeek)*businessReportSecondsPerDay - businessReportTimezoneOffsetSeconds
}

func businessShanghaiDate(timestamp int64) string {
	return time.Unix(timestamp+businessReportTimezoneOffsetSeconds, 0).UTC().Format("2006-01-02")
}

func buildBusinessFlowBuckets(periods []businessPeriod, topups []TopUp, systemLogs, operationLogs []businessLogRecord, consumeQuota []int64, quotaPerUnit float64) ([]BusinessFlowBucket, error) {
	buckets := make([]BusinessFlowBucket, len(periods))
	topupUsers := make([]map[int]struct{}, len(periods))
	for index, period := range periods {
		buckets[index] = BusinessFlowBucket{Label: period.Label, Start: period.Start, End: period.End}
		topupUsers[index] = make(map[int]struct{})
	}
	for _, topup := range topups {
		timestamp := topup.CompleteTime
		if timestamp <= 0 {
			timestamp = topup.CreateTime
		}
		index := businessPeriodIndex(periods, timestamp)
		if index < 0 {
			continue
		}
		bucket := &buckets[index]
		bucket.TopupOrders++
		bucket.TopupCNY += businessTopUpCNY(topup)
		bucket.TopupQuota += businessCreditedQuota(topup, quotaPerUnit)
		topupUsers[index][topup.UserId] = struct{}{}
	}
	for index := range buckets {
		buckets[index].TopupUsers = len(topupUsers[index])
	}
	for _, log := range systemLogs {
		index := businessPeriodIndex(periods, log.CreatedAt)
		if index < 0 {
			continue
		}
		quota := businessQuotaFromLogContent(log.Content, quotaPerUnit)
		if strings.Contains(log.Content, "新用户注册赠送") {
			buckets[index].SignupGrantCount++
			buckets[index].SignupGrantQuota += quota
		} else if strings.Contains(log.Content, "用户签到") {
			buckets[index].CheckinCount++
			buckets[index].CheckinQuota += quota
		}
	}
	for _, log := range operationLogs {
		index := businessPeriodIndex(periods, log.CreatedAt)
		if index < 0 {
			continue
		}
		if strings.Contains(strings.ToLower(log.Content), "increased user quota by") {
			buckets[index].ManualAddCount++
			buckets[index].ManualAddQuota += businessQuotaFromLogContent(log.Content, quotaPerUnit)
		} else if strings.Contains(strings.ToLower(log.Content), "overrode user quota from") {
			delta := businessQuotaOverrideDelta(log.Content, quotaPerUnit)
			if delta > 0 {
				buckets[index].ManualOverrideIncreaseCount++
				buckets[index].ManualOverrideIncreaseQuota += delta
			}
		}
	}
	for index := range periods {
		bucket := &buckets[index]
		if index < len(consumeQuota) {
			bucket.ConsumeQuota = consumeQuota[index]
		}
		bucket.NonRechargeIncreaseQuota = bucket.SignupGrantQuota + bucket.CheckinQuota + bucket.ManualAddQuota + bucket.ManualOverrideIncreaseQuota
		bucket.NetAfterConsumeQuota = bucket.TopupQuota + bucket.NonRechargeIncreaseQuota - bucket.ConsumeQuota
	}
	return buckets, nil
}

// sumBusinessConsumeQuotaByPeriods folds all period sums into one SQL scan. The
// previous implementation issued one aggregate query per day/week, which made
// the admin page wait on 22 sequential scans before rendering any data.
func sumBusinessConsumeQuotaByPeriods(periods []businessPeriod) ([]int64, error) {
	if len(periods) == 0 {
		return []int64{}, nil
	}
	selectParts := make([]string, 0, len(periods))
	args := make([]any, 0, len(periods)*2+3)
	minStart, maxEnd := periods[0].Start, periods[0].End
	for index, period := range periods {
		selectParts = append(selectParts, fmt.Sprintf(
			"COALESCE(SUM(CASE WHEN created_at >= ? AND created_at < ? THEN quota ELSE 0 END), 0) AS period_%d",
			index,
		))
		args = append(args, period.Start, period.End)
		if period.Start < minStart {
			minStart = period.Start
		}
		if period.End > maxEnd {
			maxEnd = period.End
		}
	}
	args = append(args, LogTypeConsume, minStart, maxEnd)
	query := fmt.Sprintf(
		"SELECT %s FROM logs WHERE type = ? AND created_at >= ? AND created_at < ?",
		strings.Join(selectParts, ", "),
	)
	rows, err := LOG_DB.Raw(query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return make([]int64, len(periods)), nil
	}
	values := make([]sql.NullInt64, len(periods))
	destinations := make([]any, len(values))
	for index := range values {
		destinations[index] = &values[index]
	}
	if err := rows.Scan(destinations...); err != nil {
		return nil, err
	}
	quotas := make([]int64, len(values))
	for index, value := range values {
		if value.Valid {
			quotas[index] = value.Int64
		}
	}
	return quotas, rows.Err()
}

func buildBusinessFlowTotals(buckets []BusinessFlowBucket) BusinessFlowTotals {
	var totals BusinessFlowTotals
	for _, bucket := range buckets {
		totals.TopupCNY += bucket.TopupCNY
		totals.TopupQuota += bucket.TopupQuota
		totals.ConsumeQuota += bucket.ConsumeQuota
		totals.SignupGrantQuota += bucket.SignupGrantQuota
		totals.CheckinQuota += bucket.CheckinQuota
		totals.ManualAddQuota += bucket.ManualAddQuota
		totals.ManualOverrideIncreaseQuota += bucket.ManualOverrideIncreaseQuota
		totals.NonRechargeIncreaseQuota += bucket.NonRechargeIncreaseQuota
		totals.NetAfterConsumeQuota += bucket.NetAfterConsumeQuota
	}
	return totals
}

func businessPeriodIndex(periods []businessPeriod, timestamp int64) int {
	for index, period := range periods {
		if timestamp >= period.Start && timestamp < period.End {
			return index
		}
	}
	return -1
}

func businessTopUpCompleted(topup TopUp) bool {
	status := strings.ToLower(strings.TrimSpace(topup.Status))
	for _, completedStatus := range completedBusinessTopUpStatuses {
		if status == completedStatus {
			return true
		}
	}
	return topup.CompleteTime > 0
}

func businessCreditedQuota(topup TopUp, quotaPerUnit float64) int64 {
	if topup.CreditedQuota > 0 {
		return int64(topup.CreditedQuota)
	}
	provider := strings.ToLower(strings.TrimSpace(topup.PaymentProvider))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(topup.PaymentMethod))
	}
	if provider == "creem" {
		return nonNegativeRoundedQuota(float64(topup.Amount))
	}
	if topup.Money > 0 {
		if businessTopUpCurrency(topup) == PaymentCurrencyCNY {
			if topup.Amount > 0 {
				return nonNegativeRoundedQuota(float64(topup.Amount) * quotaPerUnit)
			}
			return nonNegativeRoundedQuota(topup.Money * quotaPerUnit)
		}
		return nonNegativeRoundedQuota(topup.Money / businessReportCNYPerUSD * quotaPerUnit)
	}
	return nonNegativeRoundedQuota(float64(topup.Amount) * quotaPerUnit)
}

func businessTopUpCurrency(topup TopUp) string {
	currency := strings.ToUpper(strings.TrimSpace(topup.PaymentCurrency))
	if currency != "" {
		return currency
	}
	provider := strings.ToLower(strings.TrimSpace(topup.PaymentProvider))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(topup.PaymentMethod))
	}
	if provider == PaymentProviderEpay {
		return PaymentCurrencyCNY
	}
	return PaymentCurrencyUSD
}

func businessTopUpCNY(topup TopUp) float64 {
	if topup.Money <= 0 {
		return 0
	}
	if businessTopUpCurrency(topup) == PaymentCurrencyUSD {
		return topup.Money * businessReportCNYPerUSD
	}
	return topup.Money
}

func businessQuotaFromLogContent(content string, quotaPerUnit float64) int64 {
	match := businessQuotaPattern.FindStringSubmatch(content)
	if len(match) != 2 {
		return 0
	}
	value, ok := parseBusinessFloat(match[1])
	if !ok || value <= 0 {
		return 0
	}
	return nonNegativeRoundedQuota(value / businessReportCNYPerUSD * quotaPerUnit)
}

func businessQuotaOverrideDelta(content string, quotaPerUnit float64) int64 {
	match := businessOverridePattern.FindStringSubmatch(content)
	if len(match) != 3 {
		return 0
	}
	from, fromOK := parseBusinessFloat(match[1])
	to, toOK := parseBusinessFloat(match[2])
	if !fromOK || !toOK {
		return 0
	}
	delta := (to - from) / businessReportCNYPerUSD * quotaPerUnit
	return signedRoundedQuota(delta)
}

func parseBusinessFloat(value string) (float64, bool) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
}

func nonNegativeRoundedQuota(value float64) int64 {
	if value <= 0 || math.IsNaN(value) {
		return 0
	}
	return int64(common.QuotaRound(value))
}

func signedRoundedQuota(value float64) int64 {
	if math.IsNaN(value) {
		return 0
	}
	return int64(common.QuotaRound(value))
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func sumBalanceUsers(users []businessBalanceUser, limit int) int64 {
	var total int64
	for _, user := range users[:minInt(len(users), limit)] {
		total += user.visible
	}
	return total
}

func safeBusinessShare(numerator, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}
