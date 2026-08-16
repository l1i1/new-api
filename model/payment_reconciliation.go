package model

import (
	"errors"
	"sort"

	"github.com/QuantumNous/new-api/common"
)

// PendingPaymentProviderSummary is an aggregate-only view used by the payment
// reconciliation scanner. It intentionally excludes order and user details.
type PendingPaymentProviderSummary struct {
	PaymentProvider  string `json:"payment_provider"`
	PendingCount     int64  `json:"pending_count"`
	OldestCreateTime int64  `json:"oldest_create_time"`
}

// PendingTopUpProviderSummary remains as a source-compatible alias for older
// callers; the aggregate now includes subscription orders as well.
type PendingTopUpProviderSummary = PendingPaymentProviderSummary

// GetPendingPaymentProviderSummaries covers both wallet and subscription
// orders. The gateway cutover uses the same provider boundary for both
// business types, so scanning only TopUp would hide overdue subscriptions.
func GetPendingPaymentProviderSummaries(createdAtOrBefore int64) ([]PendingPaymentProviderSummary, error) {
	if createdAtOrBefore <= 0 {
		return nil, errors.New("pending payment cutoff must be positive")
	}
	type aggregate struct {
		count  int64
		oldest int64
	}
	byProvider := make(map[string]aggregate)
	var topUps []PendingPaymentProviderSummary
	if err := DB.Model(&TopUp{}).
		Select("payment_provider, COUNT(*) AS pending_count, MIN(create_time) AS oldest_create_time").
		Where("status = ? AND create_time <= ? AND payment_provider <> ?", common.TopUpStatusPending, createdAtOrBefore, PaymentProviderBalance).
		Group("payment_provider").
		Scan(&topUps).Error; err != nil {
		return nil, err
	}
	for _, item := range topUps {
		byProvider[item.PaymentProvider] = aggregate{count: item.PendingCount, oldest: item.OldestCreateTime}
	}
	var subscriptions []PendingPaymentProviderSummary
	// Some legacy maintenance databases may predate subscription_orders. The
	// main application migration creates it; keep the scanner compatible with
	// those read-only drain databases while still scanning it whenever present.
	if DB.Migrator().HasTable(&SubscriptionOrder{}) {
		if err := DB.Model(&SubscriptionOrder{}).
			Select("payment_provider, COUNT(*) AS pending_count, MIN(create_time) AS oldest_create_time").
			Where("status = ? AND create_time <= ? AND payment_provider <> ''", common.TopUpStatusPending, createdAtOrBefore).
			Group("payment_provider").
			Scan(&subscriptions).Error; err != nil {
			return nil, err
		}
	}
	for _, item := range subscriptions {
		current := byProvider[item.PaymentProvider]
		if current.oldest == 0 || (item.OldestCreateTime > 0 && item.OldestCreateTime < current.oldest) {
			current.oldest = item.OldestCreateTime
		}
		current.count += item.PendingCount
		byProvider[item.PaymentProvider] = current
	}
	result := make([]PendingPaymentProviderSummary, 0, len(byProvider))
	for provider, item := range byProvider {
		result = append(result, PendingPaymentProviderSummary{PaymentProvider: provider, PendingCount: item.count, OldestCreateTime: item.oldest})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PaymentProvider < result[j].PaymentProvider })
	return result, nil
}

// GetPendingTopUpProviderSummaries finds external-payment top-ups that have
// remained pending beyond a caller-provided cutoff. It never changes order or
// wallet state; provider-side paid-state verification is required before any
// settlement action can be safe.
func GetPendingTopUpProviderSummaries(createdAtOrBefore int64) ([]PendingPaymentProviderSummary, error) {
	return GetPendingPaymentProviderSummaries(createdAtOrBefore)
}
