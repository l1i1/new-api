package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertReconciliationTopUp(t *testing.T, tradeNo string, provider string, status string, createTime int64) {
	t.Helper()
	topUp := &TopUp{
		UserId:          1,
		Amount:          1,
		Money:           1,
		TradeNo:         tradeNo,
		PaymentMethod:   provider,
		PaymentProvider: provider,
		CreateTime:      createTime,
		Status:          status,
	}
	require.NoError(t, topUp.Insert())
}

func TestGetPendingTopUpProviderSummaries(t *testing.T) {
	truncateTables(t)

	const cutoff = int64(10_000)
	insertReconciliationTopUp(t, "old-epay-1", PaymentProviderEpay, common.TopUpStatusPending, cutoff-200)
	insertReconciliationTopUp(t, "old-epay-2", PaymentProviderEpay, common.TopUpStatusPending, cutoff-100)
	insertReconciliationTopUp(t, "old-waffo", PaymentProviderWaffoPancake, common.TopUpStatusPending, cutoff-50)
	insertReconciliationTopUp(t, "recent-epay", PaymentProviderEpay, common.TopUpStatusPending, cutoff+1)
	insertReconciliationTopUp(t, "completed-epay", PaymentProviderEpay, common.TopUpStatusSuccess, cutoff-300)
	insertReconciliationTopUp(t, "balance", PaymentProviderBalance, common.TopUpStatusPending, cutoff-400)

	summaries, err := GetPendingTopUpProviderSummaries(cutoff)
	require.NoError(t, err)
	require.Len(t, summaries, 2)

	assert.Equal(t, PendingTopUpProviderSummary{
		PaymentProvider:  PaymentProviderEpay,
		PendingCount:     2,
		OldestCreateTime: cutoff - 200,
	}, summaries[0])
	assert.Equal(t, PendingTopUpProviderSummary{
		PaymentProvider:  PaymentProviderWaffoPancake,
		PendingCount:     1,
		OldestCreateTime: cutoff - 50,
	}, summaries[1])
}

func TestGetPendingTopUpProviderSummariesRejectsInvalidCutoff(t *testing.T) {
	_, err := GetPendingTopUpProviderSummaries(0)
	require.Error(t, err)
}
