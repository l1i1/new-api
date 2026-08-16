package service

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentReconciliationHandlerRecordsAggregateResult(t *testing.T) {
	truncate(t)
	t.Setenv("PAYMENT_RECONCILIATION_PENDING_AGE_MINUTES", "30")

	now := common.GetTimestamp()
	topUp := &model.TopUp{
		UserId:          7,
		Amount:          3,
		Money:           3,
		TradeNo:         "pending-reconciliation-test",
		PaymentMethod:   model.PaymentProviderWaffoPancake,
		PaymentProvider: model.PaymentProviderWaffoPancake,
		CreateTime:      now - int64(time.Hour.Seconds()),
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, topUp.Insert())

	task, err := model.CreateSystemTask(paymentReconciliationTaskType, nil, nil)
	require.NoError(t, err)
	claimedTask, claimed, err := model.ClaimSystemTask(task.ID, paymentReconciliationTaskType, "reconciliation-runner", now+60)
	require.NoError(t, err)
	require.True(t, claimed)

	paymentReconciliationHandler{}.Run(context.Background(), claimedTask, "reconciliation-runner")

	completed, err := model.GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	assert.Equal(t, model.SystemTaskStatusSucceeded, completed.Status)

	result := PaymentReconciliationScanResult{}
	require.NoError(t, common.UnmarshalJsonStr(completed.Result, &result))
	assert.Equal(t, int64(1), result.PendingCount)
	require.Len(t, result.Providers, 1)
	assert.Equal(t, model.PaymentProviderWaffoPancake, result.Providers[0].PaymentProvider)
	assert.Equal(t, int64(1), result.Providers[0].PendingCount)
}

func TestPaymentReconciliationHandlerIncludesPendingSubscriptions(t *testing.T) {
	truncate(t)
	t.Setenv("PAYMENT_RECONCILIATION_PENDING_AGE_MINUTES", "30")

	now := common.GetTimestamp()
	require.NoError(t, (&model.TopUp{
		UserId: 8, Amount: 4, Money: 4, TradeNo: "pending-wallet-reconciliation-test",
		PaymentMethod: model.PaymentProviderWaffoPancake, PaymentProvider: model.PaymentProviderWaffoPancake,
		CreateTime: now - int64(time.Hour.Seconds()), Status: common.TopUpStatusPending,
	}).Insert())
	require.NoError(t, (&model.SubscriptionOrder{
		UserId: 8, PlanId: 1, Money: 12, TradeNo: "pending-subscription-reconciliation-test",
		PaymentMethod: model.PaymentProviderWaffoPancake, PaymentProvider: model.PaymentProviderWaffoPancake,
		CreateTime: now - int64(2*time.Hour.Seconds()), Status: common.TopUpStatusPending,
	}).Insert())

	task, err := model.CreateSystemTask(paymentReconciliationTaskType, nil, nil)
	require.NoError(t, err)
	claimedTask, claimed, err := model.ClaimSystemTask(task.ID, paymentReconciliationTaskType, "reconciliation-runner", now+60)
	require.NoError(t, err)
	require.True(t, claimed)

	paymentReconciliationHandler{}.Run(context.Background(), claimedTask, "reconciliation-runner")
	completed, err := model.GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	assert.Equal(t, model.SystemTaskStatusSucceeded, completed.Status)

	result := PaymentReconciliationScanResult{}
	require.NoError(t, common.UnmarshalJsonStr(completed.Result, &result))
	require.Len(t, result.Providers, 1)
	assert.Equal(t, int64(2), result.PendingCount)
	assert.Equal(t, int64(2), result.Providers[0].PendingCount)
	assert.Equal(t, now-2*int64(time.Hour.Seconds()), result.Providers[0].OldestCreateTime)
}

func TestPaymentReconciliationHandlerConfiguration(t *testing.T) {
	handler := paymentReconciliationHandler{}
	t.Setenv("PAYMENT_RECONCILIATION_SCAN_ENABLED", "false")
	t.Setenv("PAYMENT_RECONCILIATION_SCAN_INTERVAL_MINUTES", "7")

	assert.False(t, handler.Enabled())
	assert.Equal(t, 7*time.Minute, handler.Interval())
}
