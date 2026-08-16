package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

const (
	paymentReconciliationTaskType                 = "payment_reconciliation_scan"
	paymentReconciliationDefaultIntervalMinutes   = 15
	paymentReconciliationDefaultPendingAgeMinutes = 30
)

type paymentReconciliationHandler struct{}

type PaymentReconciliationScanResult struct {
	CutoffTimestamp int64                                 `json:"cutoff_timestamp"`
	PendingCount    int64                                 `json:"pending_count"`
	Providers       []model.PendingPaymentProviderSummary `json:"providers"`
}

func (paymentReconciliationHandler) Type() string { return paymentReconciliationTaskType }

func (paymentReconciliationHandler) Enabled() bool {
	return common.GetEnvOrDefaultBool("PAYMENT_RECONCILIATION_SCAN_ENABLED", true)
}

func (paymentReconciliationHandler) Interval() time.Duration {
	minutes := common.GetEnvOrDefault(
		"PAYMENT_RECONCILIATION_SCAN_INTERVAL_MINUTES",
		paymentReconciliationDefaultIntervalMinutes,
	)
	if minutes < 1 {
		minutes = paymentReconciliationDefaultIntervalMinutes
	}
	return time.Duration(minutes) * time.Minute
}

func (paymentReconciliationHandler) NewPayload() any { return nil }

func (paymentReconciliationHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	ageMinutes := common.GetEnvOrDefault(
		"PAYMENT_RECONCILIATION_PENDING_AGE_MINUTES",
		paymentReconciliationDefaultPendingAgeMinutes,
	)
	if ageMinutes < 1 {
		ageMinutes = paymentReconciliationDefaultPendingAgeMinutes
	}
	cutoff := common.GetTimestamp() - int64((time.Duration(ageMinutes) * time.Minute).Seconds())

	summaries, err := model.GetPendingPaymentProviderSummaries(cutoff)
	if err != nil {
		failSystemTask(task, runnerID, err)
		return
	}

	result := PaymentReconciliationScanResult{
		CutoffTimestamp: cutoff,
		Providers:       summaries,
	}
	providerCounts := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		result.PendingCount += summary.PendingCount
		provider := summary.PaymentProvider
		if provider == "" {
			provider = "unknown"
		}
		providerCounts = append(providerCounts, fmt.Sprintf("%s=%d", provider, summary.PendingCount))
	}

	if result.PendingCount > 0 {
		logger.LogWarn(ctx, fmt.Sprintf(
			"payment reconciliation scan found overdue pending payments: total=%d pending_age_minutes=%d providers=%s",
			result.PendingCount,
			ageMinutes,
			strings.Join(providerCounts, ","),
		))
	}

	if err := model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, result, ""); err != nil {
		logSystemTaskLockError(ctx, task, err)
	}
}

func init() {
	RegisterSystemTaskHandler(paymentReconciliationHandler{})
}
