package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
)

// PendingTopUpProviderSummary is an aggregate-only view used by the payment
// reconciliation scanner. It intentionally excludes order and user details.
type PendingTopUpProviderSummary struct {
	PaymentProvider  string `json:"payment_provider"`
	PendingCount     int64  `json:"pending_count"`
	OldestCreateTime int64  `json:"oldest_create_time"`
}

// GetPendingTopUpProviderSummaries finds external-payment top-ups that have
// remained pending beyond a caller-provided cutoff. It never changes order or
// wallet state; provider-side paid-state verification is required before any
// settlement action can be safe.
func GetPendingTopUpProviderSummaries(createdAtOrBefore int64) ([]PendingTopUpProviderSummary, error) {
	if createdAtOrBefore <= 0 {
		return nil, errors.New("pending top-up cutoff must be positive")
	}

	summaries := make([]PendingTopUpProviderSummary, 0)
	err := DB.Model(&TopUp{}).
		Select("payment_provider, COUNT(*) AS pending_count, MIN(create_time) AS oldest_create_time").
		Where("status = ? AND create_time <= ? AND payment_provider <> ?", common.TopUpStatusPending, createdAtOrBefore, PaymentProviderBalance).
		Group("payment_provider").
		Order("payment_provider ASC").
		Scan(&summaries).Error
	return summaries, err
}
