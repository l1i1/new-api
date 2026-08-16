package model

import (
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PaymentGatewayProviderEvent is a compact, non-payload event ledger used by
// legacy callback drain paths. It prevents a replayed provider event from
// running the legacy credit transaction twice without retaining raw payloads.
type PaymentGatewayProviderEvent struct {
	Id              int    `json:"id"`
	Provider        string `json:"provider" gorm:"type:varchar(64);not null;uniqueIndex:idx_legacy_provider_event,priority:1"`
	Environment     string `json:"environment" gorm:"type:varchar(16);not null;uniqueIndex:idx_legacy_provider_event,priority:2"`
	ProviderEventID string `json:"provider_event_id" gorm:"type:varchar(255);not null;uniqueIndex:idx_legacy_provider_event,priority:3"`
	MerchantOrderID string `json:"merchant_order_id" gorm:"type:varchar(255);not null;index"`
	PayloadHash     string `json:"payload_hash" gorm:"type:char(64);not null"`
	Status          string `json:"status" gorm:"type:varchar(32);not null"`
	CreatedAt       int64  `json:"created_at" gorm:"bigint;not null"`
}

// RecordLegacyProviderEvent claims an event ID. A duplicate claim is safe and
// returns duplicate=true so the callback can acknowledge without crediting.
func RecordLegacyProviderEvent(provider, environment, eventID, merchantOrderID, payloadHash string, now int64) (duplicate bool, err error) {
	if DB == nil {
		return false, ErrPaymentGatewaySettlementRetryable
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	environment = strings.ToLower(strings.TrimSpace(environment))
	eventID = strings.TrimSpace(eventID)
	merchantOrderID = strings.TrimSpace(merchantOrderID)
	if provider == "" || environment == "" || eventID == "" || merchantOrderID == "" || strings.TrimSpace(payloadHash) == "" {
		return false, ErrPaymentGatewaySettlementInvalid
	}
	record := PaymentGatewayProviderEvent{Provider: provider, Environment: environment, ProviderEventID: eventID, MerchantOrderID: merchantOrderID, PayloadHash: payloadHash, Status: "processing", CreatedAt: now}
	result := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 1 {
		return false, nil
	}
	var existing PaymentGatewayProviderEvent
	if err := DB.Where("provider = ? AND environment = ? AND provider_event_id = ?", provider, environment, eventID).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, ErrPaymentGatewaySettlementRetryable
		}
		return false, err
	}
	if existing.PayloadHash != payloadHash || existing.MerchantOrderID != merchantOrderID {
		return false, ErrPaymentGatewaySettlementConflict
	}
	if existing.Status == "completed" {
		return true, nil
	}
	if existing.Status == "processing" {
		return false, ErrPaymentGatewaySettlementRetryable
	}
	if err := DB.Model(&existing).Update("status", "processing").Error; err != nil {
		return false, err
	}
	return false, nil
}

func CompleteLegacyProviderEvent(provider, environment, eventID string) error {
	return DB.Model(&PaymentGatewayProviderEvent{}).
		Where("provider = ? AND environment = ? AND provider_event_id = ?", strings.ToLower(strings.TrimSpace(provider)), strings.ToLower(strings.TrimSpace(environment)), strings.TrimSpace(eventID)).
		Update("status", "completed").Error
}

func FailLegacyProviderEvent(provider, environment, eventID string) error {
	return DB.Model(&PaymentGatewayProviderEvent{}).
		Where("provider = ? AND environment = ? AND provider_event_id = ?", strings.ToLower(strings.TrimSpace(provider)), strings.ToLower(strings.TrimSpace(environment)), strings.TrimSpace(eventID)).
		Update("status", "failed").Error
}
