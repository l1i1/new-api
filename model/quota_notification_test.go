package model

import (
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupQuotaNotificationTest(t *testing.T) {
	t.Helper()
	previousDB := DB
	previousMainType := common.MainDatabaseType()
	dsn := "file:quota_notification_" + strconv.FormatInt(time.Now().UnixNano(), 10) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	DB = db
	require.NoError(t, db.AutoMigrate(&User{}, &UserSubscription{}))
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousMainType)
	})
}

func setQuotaNotificationUserQuota(t *testing.T, userID int, quota int) {
	t.Helper()
	require.NoError(t, DB.Model(&User{}).Where("id = ?", userID).Update("quota", quota).Error)
}

func TestClaimQuotaWarningOnlyOnceUntilBalanceRecovers(t *testing.T) {
	setupQuotaNotificationTest(t)
	user := &User{Username: "quota-notification-user", AffCode: "quota-notification-user", Password: "unused", Status: common.UserStatusEnabled, Quota: 150}
	require.NoError(t, DB.Create(user).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("quota_warning_sent", nil).Error)
	setQuotaNotificationUserQuota(t, user.Id, 90)
	claimed, err := ClaimQuotaWarning(user.Id, false, 0, 100, 150, 90)
	require.NoError(t, err)
	require.True(t, claimed)

	setQuotaNotificationUserQuota(t, user.Id, 80)
	claimed, err = ClaimQuotaWarning(user.Id, false, 0, 100, 90, 80)
	require.NoError(t, err)
	require.False(t, claimed)

	// A top-up can restore the balance without any request observing the
	// recovered value; the next threshold crossing must still notify once.
	setQuotaNotificationUserQuota(t, user.Id, 90)
	claimed, err = ClaimQuotaWarning(user.Id, false, 0, 100, 200, 90)
	require.NoError(t, err)
	require.True(t, claimed)

	setQuotaNotificationUserQuota(t, user.Id, 120)
	claimed, err = ClaimQuotaWarning(user.Id, false, 0, 100, 80, 120)
	require.NoError(t, err)
	require.False(t, claimed)

	setQuotaNotificationUserQuota(t, user.Id, 90)
	claimed, err = ClaimQuotaWarning(user.Id, false, 0, 100, 120, 90)
	require.NoError(t, err)
	require.True(t, claimed)
}

func TestClaimQuotaWarningDoesNotDuplicateConcurrentSnapshot(t *testing.T) {
	setupQuotaNotificationTest(t)
	user := &User{Username: "quota-notification-concurrent-user", AffCode: "quota-notification-concurrent-user", Password: "unused", Status: common.UserStatusEnabled, Quota: 150}
	require.NoError(t, DB.Create(user).Error)

	setQuotaNotificationUserQuota(t, user.Id, 90)
	claimed, err := ClaimQuotaWarning(user.Id, false, 0, 100, 150, 90)
	require.NoError(t, err)
	require.True(t, claimed)

	// The second request retained the same pre-request snapshot, but another
	// debit has already lowered the locked database balance further.
	setQuotaNotificationUserQuota(t, user.Id, 30)
	claimed, err = ClaimQuotaWarning(user.Id, false, 0, 100, 150, 130)
	require.NoError(t, err)
	require.False(t, claimed)

	setQuotaNotificationUserQuota(t, user.Id, 80)
	claimed, err = ClaimQuotaWarning(user.Id, false, 0, 100, 90, 80)
	require.NoError(t, err)
	require.False(t, claimed)
}

func TestClaimQuotaWarningSeparatesSubscriptionState(t *testing.T) {
	setupQuotaNotificationTest(t)
	user := &User{Username: "subscription-quota-notification-user", AffCode: "subscription-quota-notification-user", Password: "unused", Status: common.UserStatusEnabled, Quota: 150}
	require.NoError(t, DB.Create(user).Error)
	subscription := &UserSubscription{UserId: user.Id, AmountTotal: 150, AmountUsed: 60, Status: "active"}
	require.NoError(t, DB.Create(subscription).Error)

	setQuotaNotificationUserQuota(t, user.Id, 90)
	claimed, err := ClaimQuotaWarning(user.Id, false, 0, 100, 150, 90)
	require.NoError(t, err)
	require.True(t, claimed)

	claimed, err = ClaimQuotaWarning(user.Id, true, subscription.Id, 100, 150, 90)
	require.NoError(t, err)
	require.True(t, claimed)

	setQuotaNotificationUserQuota(t, user.Id, 80)
	claimed, err = ClaimQuotaWarning(user.Id, false, 0, 100, 90, 80)
	require.NoError(t, err)
	require.False(t, claimed)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", subscription.Id).Update("amount_used", 70).Error)
	claimed, err = ClaimQuotaWarning(user.Id, true, subscription.Id, 100, 90, 80)
	require.NoError(t, err)
	require.False(t, claimed)
}

func TestClaimQuotaWarningSeparatesSubscriptions(t *testing.T) {
	setupQuotaNotificationTest(t)
	user := &User{Username: "subscription-quota-notification-isolation", AffCode: "subscription-quota-notification-isolation", Password: "unused", Status: common.UserStatusEnabled, Quota: 1000}
	require.NoError(t, DB.Create(user).Error)
	subscription1 := &UserSubscription{UserId: user.Id, AmountTotal: 150, AmountUsed: 60, Status: "active"}
	subscription2 := &UserSubscription{UserId: user.Id, AmountTotal: 150, AmountUsed: 60, Status: "active"}
	require.NoError(t, DB.Create(subscription1).Error)
	require.NoError(t, DB.Create(subscription2).Error)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id IN ?", []int{subscription1.Id, subscription2.Id}).Update("quota_warning_sent", nil).Error)

	claimed, err := ClaimQuotaWarning(user.Id, true, subscription1.Id, 100, 150, 90)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = ClaimQuotaWarning(user.Id, true, subscription2.Id, 100, 150, 90)
	require.NoError(t, err)
	require.True(t, claimed)
}
