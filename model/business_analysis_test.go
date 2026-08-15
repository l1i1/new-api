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
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBusinessAnalysisConsumeQuotaAggregatesAllPeriodsInOneQuery(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE logs (type integer, created_at integer, quota integer)").Error)
	require.NoError(t, db.Exec("INSERT INTO logs (type, created_at, quota) VALUES (?, ?, ?), (?, ?, ?), (?, ?, ?)",
		LogTypeConsume, 10, 100,
		LogTypeConsume, 20, 200,
		LogTypeManage, 10, 999,
	).Error)

	previousLogDB := LOG_DB
	LOG_DB = db
	t.Cleanup(func() { LOG_DB = previousLogDB })

	quotas, err := sumBusinessConsumeQuotaByPeriods([]businessPeriod{
		{Start: 0, End: 15},
		{Start: 15, End: 25},
	})
	require.NoError(t, err)
	assert.Equal(t, []int64{100, 200}, quotas)
}

func TestBusinessAnalysisPeriodsUseShanghaiCalendar(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC).Unix()
	daily := buildBusinessDailyPeriods(now, 2)
	weekly := buildBusinessWeeklyPeriods(now, 1)

	require.Len(t, daily, 2)
	assert.Equal(t, "2026-07-30", daily[0].Label)
	assert.Equal(t, "2026-07-31", daily[1].Label)
	assert.Equal(t, "2026-07-27 ~ 2026-08-02", weekly[0].Label)
	assert.Equal(t, daily[1].End, startOfBusinessShanghaiDay(now)+businessReportSecondsPerDay)
}

func TestBusinessAnalysisCompletedTopupAndProviderConversion(t *testing.T) {
	assert.True(t, businessTopUpCompleted(TopUp{Status: "PAID"}))
	assert.True(t, businessTopUpCompleted(TopUp{CompleteTime: 10}))
	assert.False(t, businessTopUpCompleted(TopUp{Status: "pending"}))

	assert.EqualValues(t, 123, businessCreditedQuota(TopUp{
		PaymentProvider: "creem",
		Amount:          123,
	}, 500_000))
	assert.EqualValues(t, 1_000_000, businessCreditedQuota(TopUp{Money: 14, PaymentCurrency: PaymentCurrencyUSD}, 500_000))
	assert.EqualValues(t, 7_000_000, businessCreditedQuota(TopUp{Money: 14, PaymentCurrency: PaymentCurrencyCNY}, 500_000))
	assert.EqualValues(t, 1_000_000, businessCreditedQuota(TopUp{Amount: 2}, 500_000))
}

func TestBusinessAnalysisTopUpCurrencyConversion(t *testing.T) {
	assert.Equal(t, 14.0, businessTopUpCNY(TopUp{Money: 14, PaymentCurrency: PaymentCurrencyCNY}))
	assert.Equal(t, 98.0, businessTopUpCNY(TopUp{Money: 14, PaymentCurrency: PaymentCurrencyUSD}))
	assert.Equal(t, 14.0, businessTopUpCNY(TopUp{Money: 14, PaymentProvider: PaymentProviderEpay}))
}

func TestBusinessAnalysisParsesNonRechargeLogs(t *testing.T) {
	const quotaPerUnit = 500_000.0
	assert.EqualValues(t, 500_000, businessQuotaFromLogContent("新用户注册赠送 ¥7额度", quotaPerUnit))
	assert.EqualValues(t, 500_000, businessQuotaFromLogContent("用户签到获得 ¥7.00额度", quotaPerUnit))
	assert.EqualValues(t, 142857, businessQuotaOverrideDelta("Overrode user quota from ¥7额度 to ¥9额度", quotaPerUnit))
	assert.EqualValues(t, -142857, businessQuotaOverrideDelta("Overrode user quota from ¥9额度 to ¥7额度", quotaPerUnit))
	assert.EqualValues(t, 0, businessQuotaFromLogContent("新用户注册赠送 7 quota", quotaPerUnit))
}

func TestBusinessAnalysisInventoryKeepsNegativeAndDisabledBalancesSeparate(t *testing.T) {
	users := []User{
		{Id: 1, Username: "enabled", Status: common.UserStatusEnabled, Quota: 100, AffQuota: 50},
		{Id: 2, Username: "negative", Status: common.UserStatusEnabled, Quota: -20},
		{Id: 3, Username: "disabled", Status: common.UserStatusDisabled, Quota: 80},
		{Id: 4, Username: "deleted", Status: common.UserStatusEnabled, Quota: 70, DeletedAt: gorm.DeletedAt{Valid: true}},
	}

	result := buildBusinessQuotaInventory(users)
	assert.Equal(t, 4, result.Users.Total)
	assert.Equal(t, 2, result.Users.Enabled)
	assert.Equal(t, int64(150), result.ConsumableEnabledVisible)
	assert.Equal(t, int64(130), result.NetEnabledVisible)
	assert.Equal(t, int64(150), result.DisabledOrDeletedPositive)
	assert.Equal(t, int64(-20), result.NegativeEnabledVisible)
	assert.Len(t, result.Top20, 1)
}

func TestBusinessAnalysisQuotaOriginSeparatesCompletedTopups(t *testing.T) {
	users := []User{
		{Id: 1, Username: "paid", Status: common.UserStatusEnabled, Quota: 100, AffQuota: 5},
		{Id: 2, Username: "grant", Status: common.UserStatusEnabled, Quota: 80, AffQuota: 10},
		{Id: 3, Username: "empty", Status: common.UserStatusEnabled},
	}
	result := buildBusinessQuotaOrigin(users, map[int]struct{}{1: {}})

	assert.Equal(t, 3, result.EnabledUsers)
	assert.Equal(t, 2, result.PositiveQuotaEnabledUsers)
	assert.Equal(t, int64(180), result.PositiveQuotaEnabledTotal)
	assert.Equal(t, 1, result.PositiveQuotaWithTopupUsers)
	assert.Equal(t, int64(100), result.PositiveQuotaWithTopupTotal)
	assert.Equal(t, 1, result.PositiveQuotaNoTopupUsers)
	assert.Equal(t, int64(80), result.PositiveQuotaNoTopupTotal)
	assert.Equal(t, 2, result.EnabledPositiveAffUsers)
	assert.Equal(t, int64(15), result.EnabledPositiveAffTotal)
}
