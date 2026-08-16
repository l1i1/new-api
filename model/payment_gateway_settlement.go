package model

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	PaymentGatewaySettlementCommitted  = "committed"
	PaymentGatewayBusinessWallet       = "wallet_topup"
	PaymentGatewayBusinessSubscription = "subscription"
)

var (
	ErrPaymentGatewaySettlementInvalid   = errors.New("payment gateway settlement is invalid")
	ErrPaymentGatewaySettlementConflict  = errors.New("payment gateway settlement conflicts with an existing command")
	ErrPaymentGatewaySettlementRetryable = errors.New("payment gateway settlement is retryable")
	ErrPaymentGatewaySettlementMismatch  = errors.New("payment gateway settlement does not match the local order")
)

// PaymentGatewaySettlement is the durable idempotency record for a signed
// gateway command. Both command_id and settlement_key are unique so a retry
// cannot create a second ledger operation for the same business order.
type PaymentGatewaySettlement struct {
	Id                    int    `json:"id"`
	CommandID             string `json:"command_id" gorm:"type:varchar(128);not null;uniqueIndex"`
	SettlementKey         string `json:"settlement_key" gorm:"type:varchar(384);not null;uniqueIndex"`
	MerchantOrderID       string `json:"merchant_order_id" gorm:"type:varchar(255);not null;index"`
	BusinessType          string `json:"business_type" gorm:"type:varchar(32);not null"`
	Provider              string `json:"provider" gorm:"type:varchar(64);not null;uniqueIndex:idx_gateway_provider_event,priority:1"`
	ProviderAccountID     string `json:"provider_account_id" gorm:"type:varchar(255);not null;default:''"`
	Environment           string `json:"environment" gorm:"type:varchar(16);not null;default:'';uniqueIndex:idx_gateway_provider_event,priority:2"`
	ProviderEventID       string `json:"provider_event_id" gorm:"type:varchar(255);not null;uniqueIndex:idx_gateway_provider_event,priority:3"`
	PaymentMethod         string `json:"payment_method" gorm:"type:varchar(64);not null;default:''"`
	AmountMinor           int64  `json:"amount_minor" gorm:"bigint;not null;default:0"`
	Currency              string `json:"currency" gorm:"type:varchar(8);not null;default:''"`
	ProviderOrderID       string `json:"provider_order_id" gorm:"type:varchar(255);not null;default:''"`
	ProviderTransactionID string `json:"provider_transaction_id" gorm:"type:varchar(255);not null;default:''"`
	PayloadHash           string `json:"payload_hash" gorm:"type:char(64);not null"`
	CreditedReference     string `json:"credited_reference" gorm:"type:varchar(255);not null"`
	Status                string `json:"status" gorm:"type:varchar(32);not null"`
	CreatedAt             int64  `json:"created_at" gorm:"bigint;not null"`
	CommittedAt           int64  `json:"committed_at" gorm:"bigint;not null"`
}

// PaymentGatewaySettlementCommand mirrors the gateway's signed JSON contract.
// PriceSnapshot is retained only for signature/idempotency validation; the
// New API order tables remain the source of truth for business state.
type PaymentGatewaySettlementCommand struct {
	CommandID             string         `json:"command_id"`
	OrderID               string         `json:"order_id"`
	MerchantOrderID       string         `json:"merchant_order_id"`
	BusinessType          string         `json:"business_type"`
	UserID                string         `json:"user_id"`
	AmountMinor           int64          `json:"amount_minor"`
	Currency              string         `json:"currency"`
	Provider              string         `json:"provider"`
	ProviderAccountID     string         `json:"provider_account_id"`
	Environment           string         `json:"environment"`
	ProviderEventID       string         `json:"provider_event_id"`
	ProviderOrderID       string         `json:"provider_order_id"`
	ProviderTransactionID string         `json:"provider_transaction_id"`
	PaymentMethod         string         `json:"payment_method"`
	QuotaAmount           int64          `json:"quota_amount"`
	PriceSnapshot         map[string]any `json:"price_snapshot"`
	Signature             string         `json:"signature,omitempty"`
	IssuedAt              time.Time      `json:"issued_at"`
}

type PaymentGatewaySettlementResult struct {
	Settlement        PaymentGatewaySettlement
	Duplicate         bool
	CreditedReference string
}

// BindPaymentGatewayOrderID records the canonical HotPay order ID on the
// local pending order. The binding is write-once: retries may return the same
// ID, but a different ID is a terminal mismatch and must not be exposed as a
// payable local order.
func BindPaymentGatewayOrderID(businessType, merchantOrderID, gatewayOrderID string) error {
	if DB == nil {
		return ErrPaymentGatewaySettlementRetryable
	}
	businessType = strings.TrimSpace(businessType)
	merchantOrderID = strings.TrimSpace(merchantOrderID)
	gatewayOrderID = strings.TrimSpace(gatewayOrderID)
	if merchantOrderID == "" || gatewayOrderID == "" {
		return ErrPaymentGatewaySettlementInvalid
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		switch businessType {
		case PaymentGatewayBusinessWallet:
			var topUp TopUp
			if err := lockForUpdate(tx).Where("trade_no = ?", merchantOrderID).First(&topUp).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrTopUpNotFound
				}
				return err
			}
			if topUp.PaymentGatewayOrderID != "" && topUp.PaymentGatewayOrderID != gatewayOrderID {
				return ErrPaymentGatewaySettlementMismatch
			}
			if topUp.PaymentGatewayOrderID == "" {
				topUp.PaymentGatewayOrderID = gatewayOrderID
				return tx.Save(&topUp).Error
			}
			return nil
		case PaymentGatewayBusinessSubscription:
			var order SubscriptionOrder
			if err := lockForUpdate(tx).Where("trade_no = ?", merchantOrderID).First(&order).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrSubscriptionOrderNotFound
				}
				return err
			}
			if order.PaymentGatewayOrderID != "" && order.PaymentGatewayOrderID != gatewayOrderID {
				return ErrPaymentGatewaySettlementMismatch
			}
			if order.PaymentGatewayOrderID == "" {
				order.PaymentGatewayOrderID = gatewayOrderID
				return tx.Save(&order).Error
			}
			return nil
		default:
			return ErrPaymentGatewaySettlementInvalid
		}
	})
}

func normalizeGatewayPaymentMethod(method string) string {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "wechat", "wechat_pay", "wxpay":
		return "wechat_pay"
	case "applepay", "apple_pay":
		return "apple_pay"
	case "googlepay", "google_pay":
		return "google_pay"
	default:
		return strings.ToLower(strings.TrimSpace(method))
	}
}

func snapshotString(snapshot map[string]any, key string) (string, bool) {
	value, ok := snapshot[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	return strings.TrimSpace(text), ok && strings.TrimSpace(text) != ""
}

func snapshotInt64(snapshot map[string]any, key string) (int64, bool) {
	value, ok := snapshot[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), typed == float64(int64(typed))
	case float32:
		return int64(typed), typed == float32(int64(typed))
	default:
		return 0, false
	}
}

func validateGatewaySnapshotAmount(value string, expected float64) error {
	actual, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil || actual.LessThanOrEqual(decimal.Zero) {
		return ErrPaymentGatewaySettlementMismatch
	}
	if !actual.Equal(decimal.NewFromFloat(expected).Round(2)) {
		return ErrPaymentGatewaySettlementMismatch
	}
	return nil
}

func validateGatewayWalletSnapshot(topUp *TopUp, command *PaymentGatewaySettlementCommand) error {
	if topUp == nil || command == nil || len(command.PriceSnapshot) == 0 {
		return ErrPaymentGatewaySettlementMismatch
	}
	quotaAmount, ok := snapshotInt64(command.PriceSnapshot, "quota_amount")
	if !ok || quotaAmount != int64(topUp.Amount) {
		return ErrPaymentGatewaySettlementMismatch
	}
	providerAmount, ok := snapshotString(command.PriceSnapshot, "provider_amount")
	if !ok {
		return ErrPaymentGatewaySettlementMismatch
	}
	if err := validateGatewaySnapshotAmount(providerAmount, topUp.Money); err != nil {
		return err
	}
	pricingCurrency, ok := snapshotString(command.PriceSnapshot, "pricing_currency")
	if !ok {
		pricingCurrency, ok = snapshotString(command.PriceSnapshot, "currency")
	}
	if !ok || !strings.EqualFold(pricingCurrency, topUp.PaymentCurrency) {
		return ErrPaymentGatewaySettlementMismatch
	}
	return nil
}

func validateGatewaySubscriptionSnapshot(order *SubscriptionOrder, plan *SubscriptionPlan, command *PaymentGatewaySettlementCommand) error {
	if order == nil || plan == nil || command == nil || len(command.PriceSnapshot) == 0 {
		return ErrPaymentGatewaySettlementMismatch
	}
	planID, ok := snapshotInt64(command.PriceSnapshot, "plan_id")
	if !ok || int(planID) != order.PlanId || int(planID) != plan.Id {
		return ErrPaymentGatewaySettlementMismatch
	}
	priceAmount, ok := snapshotString(command.PriceSnapshot, "price_amount")
	if !ok {
		return ErrPaymentGatewaySettlementMismatch
	}
	if err := validateGatewaySnapshotAmount(priceAmount, order.Money); err != nil {
		return err
	}
	snapshotCurrency, ok := snapshotString(command.PriceSnapshot, "currency")
	planCurrency := strings.ToUpper(strings.TrimSpace(plan.Currency))
	if planCurrency == "" {
		planCurrency = PaymentCurrencyUSD
	}
	if !ok || !strings.EqualFold(snapshotCurrency, order.PaymentCurrency) || !strings.EqualFold(snapshotCurrency, planCurrency) {
		return ErrPaymentGatewaySettlementMismatch
	}
	if productID, present := snapshotString(command.PriceSnapshot, "product_id"); present && productID != strings.TrimSpace(plan.WaffoPancakeProductId) {
		return ErrPaymentGatewaySettlementMismatch
	}
	return nil
}

func validateGatewayQuota(topUp *TopUp, command *PaymentGatewaySettlementCommand) (int64, error) {
	quotaDecimal := decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	var clamp *common.QuotaClamp
	derivedQuota, clamp := common.QuotaFromDecimalChecked(quotaDecimal)
	if clamp != nil || derivedQuota <= 0 {
		return 0, ErrPaymentGatewaySettlementInvalid
	}
	if command.QuotaAmount > 0 && command.QuotaAmount != int64(derivedQuota) {
		return 0, ErrPaymentGatewaySettlementMismatch
	}
	return int64(derivedQuota), nil
}

func PaymentGatewaySettlementPayload(command PaymentGatewaySettlementCommand) ([]byte, error) {
	return common.Marshal(struct {
		CommandID             string         `json:"command_id"`
		OrderID               string         `json:"order_id"`
		MerchantOrderID       string         `json:"merchant_order_id"`
		BusinessType          string         `json:"business_type"`
		UserID                string         `json:"user_id"`
		AmountMinor           int64          `json:"amount_minor"`
		Currency              string         `json:"currency"`
		Provider              string         `json:"provider"`
		ProviderAccountID     string         `json:"provider_account_id"`
		Environment           string         `json:"environment"`
		ProviderEventID       string         `json:"provider_event_id"`
		ProviderOrderID       string         `json:"provider_order_id"`
		ProviderTransactionID string         `json:"provider_transaction_id"`
		PaymentMethod         string         `json:"payment_method"`
		QuotaAmount           int64          `json:"quota_amount"`
		PriceSnapshot         map[string]any `json:"price_snapshot"`
		IssuedAt              time.Time      `json:"issued_at"`
	}{
		CommandID: command.CommandID, OrderID: command.OrderID, MerchantOrderID: command.MerchantOrderID,
		BusinessType: command.BusinessType, UserID: command.UserID, AmountMinor: command.AmountMinor,
		Currency: command.Currency, Provider: command.Provider, ProviderAccountID: command.ProviderAccountID, Environment: command.Environment, ProviderEventID: command.ProviderEventID, ProviderOrderID: command.ProviderOrderID, ProviderTransactionID: command.ProviderTransactionID,
		PaymentMethod: command.PaymentMethod, QuotaAmount: command.QuotaAmount, PriceSnapshot: command.PriceSnapshot,
		IssuedAt: command.IssuedAt.UTC(),
	})
}

func VerifyPaymentGatewaySettlementSignature(command PaymentGatewaySettlementCommand, secret, signature string) bool {
	secret = strings.TrimSpace(secret)
	signature = strings.TrimSpace(signature)
	if secret == "" || signature == "" {
		return false
	}
	payload, err := PaymentGatewaySettlementPayload(command)
	if err != nil {
		return false
	}
	digest := common.HmacSha256(string(payload), secret)
	if len(signature) != len(digest) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(signature)), []byte(digest)) == 1
}

func PaymentGatewaySettlementPayloadHash(command PaymentGatewaySettlementCommand) (string, error) {
	payload, err := PaymentGatewaySettlementPayload(command)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(common.Sha256Raw(payload)), nil
}

// ApplyPaymentGatewaySettlement atomically records the command and updates
// either the wallet ledger or subscription entitlement. Side effects that
// touch caches/logs happen only after the SQL transaction commits.
func ApplyPaymentGatewaySettlement(command PaymentGatewaySettlementCommand) (PaymentGatewaySettlementResult, error) {
	if DB == nil {
		return PaymentGatewaySettlementResult{}, ErrPaymentGatewaySettlementRetryable
	}
	command.CommandID = strings.TrimSpace(command.CommandID)
	command.OrderID = strings.TrimSpace(command.OrderID)
	command.MerchantOrderID = strings.TrimSpace(command.MerchantOrderID)
	command.BusinessType = strings.TrimSpace(command.BusinessType)
	command.UserID = strings.TrimSpace(command.UserID)
	command.Currency = strings.ToUpper(strings.TrimSpace(command.Currency))
	command.Provider = strings.ToLower(strings.TrimSpace(command.Provider))
	command.ProviderAccountID = strings.TrimSpace(command.ProviderAccountID)
	command.Environment = strings.ToLower(strings.TrimSpace(command.Environment))
	command.ProviderEventID = strings.TrimSpace(command.ProviderEventID)
	command.ProviderOrderID = strings.TrimSpace(command.ProviderOrderID)
	command.ProviderTransactionID = strings.TrimSpace(command.ProviderTransactionID)
	command.PaymentMethod = strings.ToLower(strings.TrimSpace(command.PaymentMethod))
	if command.CommandID == "" || command.OrderID == "" || command.MerchantOrderID == "" || command.BusinessType == "" || command.UserID == "" || command.AmountMinor <= 0 || command.Currency == "" || command.Provider == "" || command.ProviderAccountID == "" || command.ProviderEventID == "" || command.ProviderOrderID == "" || command.ProviderTransactionID == "" || command.PaymentMethod == "" || command.QuotaAmount < 0 || command.IssuedAt.IsZero() {
		return PaymentGatewaySettlementResult{}, ErrPaymentGatewaySettlementInvalid
	}
	if command.Environment != "test" && command.Environment != "prod" {
		return PaymentGatewaySettlementResult{}, ErrPaymentGatewaySettlementInvalid
	}
	if command.BusinessType != PaymentGatewayBusinessWallet && command.BusinessType != PaymentGatewayBusinessSubscription {
		return PaymentGatewaySettlementResult{}, ErrPaymentGatewaySettlementInvalid
	}
	if !validGatewayPaymentMethod(command.PaymentMethod) {
		return PaymentGatewaySettlementResult{}, ErrPaymentGatewaySettlementInvalid
	}
	userID, err := strconv.Atoi(command.UserID)
	if err != nil || userID <= 0 {
		return PaymentGatewaySettlementResult{}, ErrPaymentGatewaySettlementInvalid
	}
	payloadHash, err := PaymentGatewaySettlementPayloadHash(command)
	if err != nil {
		return PaymentGatewaySettlementResult{}, ErrPaymentGatewaySettlementInvalid
	}
	settlementKey := command.BusinessType + ":" + command.MerchantOrderID
	now := common.GetTimestamp()
	var result PaymentGatewaySettlementResult
	err = DB.Transaction(func(tx *gorm.DB) error {
		var existing PaymentGatewaySettlement
		if queryErr := lockForUpdate(tx).Where("command_id = ?", command.CommandID).First(&existing).Error; queryErr == nil {
			if existing.PayloadHash != payloadHash || existing.SettlementKey != settlementKey {
				return ErrPaymentGatewaySettlementConflict
			}
			if existing.Status != PaymentGatewaySettlementCommitted {
				return ErrPaymentGatewaySettlementRetryable
			}
			result = PaymentGatewaySettlementResult{Settlement: existing, Duplicate: true, CreditedReference: existing.CreditedReference}
			return nil
		} else if !errors.Is(queryErr, gorm.ErrRecordNotFound) {
			return queryErr
		}

		// Provider event IDs are independently unique from gateway command IDs.
		// This prevents a replayed event from being accepted under a new command
		// or merchant order.
		if queryErr := lockForUpdate(tx).
			Where("provider = ? AND environment = ? AND provider_event_id = ?", command.Provider, command.Environment, command.ProviderEventID).
			First(&existing).Error; queryErr == nil {
			if existing.PayloadHash != payloadHash || existing.SettlementKey != settlementKey || existing.Status != PaymentGatewaySettlementCommitted {
				return ErrPaymentGatewaySettlementConflict
			}
			result = PaymentGatewaySettlementResult{Settlement: existing, Duplicate: true, CreditedReference: existing.CreditedReference}
			return nil
		} else if !errors.Is(queryErr, gorm.ErrRecordNotFound) {
			return queryErr
		}

		if queryErr := lockForUpdate(tx).Where("settlement_key = ?", settlementKey).First(&existing).Error; queryErr == nil {
			if existing.PayloadHash != payloadHash || existing.Status != PaymentGatewaySettlementCommitted {
				return ErrPaymentGatewaySettlementConflict
			}
			result = PaymentGatewaySettlementResult{Settlement: existing, Duplicate: true, CreditedReference: existing.CreditedReference}
			return nil
		} else if !errors.Is(queryErr, gorm.ErrRecordNotFound) {
			return queryErr
		}

		record := PaymentGatewaySettlement{
			CommandID: command.CommandID, SettlementKey: settlementKey, MerchantOrderID: command.MerchantOrderID,
			BusinessType: command.BusinessType, Provider: command.Provider, ProviderEventID: command.ProviderEventID,
			ProviderAccountID: command.ProviderAccountID, Environment: command.Environment, PaymentMethod: command.PaymentMethod,
			AmountMinor: command.AmountMinor, Currency: command.Currency, ProviderOrderID: command.ProviderOrderID, ProviderTransactionID: command.ProviderTransactionID,
			PayloadHash: payloadHash, Status: "processing", CreatedAt: now,
		}
		createResult := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
		if createErr := createResult.Error; createErr != nil {
			return createErr
		}
		if createResult.RowsAffected == 0 {
			if queryErr := lockForUpdate(tx).Where("command_id = ?", command.CommandID).First(&existing).Error; queryErr == nil {
				if existing.PayloadHash != payloadHash || existing.SettlementKey != settlementKey {
					return ErrPaymentGatewaySettlementConflict
				}
				if existing.Status != PaymentGatewaySettlementCommitted {
					return ErrPaymentGatewaySettlementRetryable
				}
				result = PaymentGatewaySettlementResult{Settlement: existing, Duplicate: true, CreditedReference: existing.CreditedReference}
				return nil
			} else if !errors.Is(queryErr, gorm.ErrRecordNotFound) {
				return queryErr
			}
			if queryErr := lockForUpdate(tx).Where("settlement_key = ?", settlementKey).First(&existing).Error; queryErr != nil {
				return queryErr
			}
			if existing.PayloadHash != payloadHash || existing.Status != PaymentGatewaySettlementCommitted {
				return ErrPaymentGatewaySettlementConflict
			}
			result = PaymentGatewaySettlementResult{Settlement: existing, Duplicate: true, CreditedReference: existing.CreditedReference}
			return nil
		}

		creditedReference := ""
		switch command.BusinessType {
		case PaymentGatewayBusinessWallet:
			creditedReference, err = applyGatewayWalletSettlementTx(tx, &command, userID)
		case PaymentGatewayBusinessSubscription:
			creditedReference, err = applyGatewaySubscriptionSettlementTx(tx, &command, userID)
		}
		if err != nil {
			return err
		}
		record.Status = PaymentGatewaySettlementCommitted
		record.CreditedReference = creditedReference
		record.CommittedAt = common.GetTimestamp()
		if saveErr := tx.Save(&record).Error; saveErr != nil {
			return saveErr
		}
		result = PaymentGatewaySettlementResult{Settlement: record, CreditedReference: creditedReference}
		return nil
	})
	if err != nil {
		if isTerminalPaymentGatewaySettlementError(err) {
			return PaymentGatewaySettlementResult{}, err
		}
		return PaymentGatewaySettlementResult{}, fmt.Errorf("%w: %v", ErrPaymentGatewaySettlementRetryable, err)
	}
	if !result.Duplicate && command.BusinessType == PaymentGatewayBusinessWallet {
		var topUp TopUp
		if DB.Where("trade_no = ?", command.MerchantOrderID).First(&topUp).Error == nil {
			processInviteFirstTopUpRewardAfterSettlement(topUp.Id)
			if common.RedisEnabled && topUp.CreditedQuota > 0 {
				if cacheErr := cacheIncrUserQuota(userID, int64(topUp.CreditedQuota)); cacheErr != nil {
					common.SysLog("failed to increase user quota cache after gateway settlement: " + cacheErr.Error())
				}
			}
			RecordTopupLog(userID, fmt.Sprintf("Payment gateway settlement succeeded, credited quota: %s", logger.FormatQuota(topUp.CreditedQuota)), "payment_gateway", command.PaymentMethod, command.Provider)
		}
	}
	return result, nil
}

func isTerminalPaymentGatewaySettlementError(err error) bool {
	for _, terminalErr := range []error{
		ErrPaymentGatewaySettlementInvalid,
		ErrPaymentGatewaySettlementConflict,
		ErrPaymentGatewaySettlementMismatch,
		ErrPaymentMethodMismatch,
		ErrPaymentAmountInvalid,
		ErrPaymentAmountMismatch,
		ErrTopUpStatusInvalid,
		ErrSubscriptionOrderStatusInvalid,
	} {
		if errors.Is(err, terminalErr) {
			return true
		}
	}
	return false
}

func applyGatewayWalletSettlementTx(tx *gorm.DB, command *PaymentGatewaySettlementCommand, userID int) (string, error) {
	var topUp TopUp
	if err := lockForUpdate(tx).Where("trade_no = ?", command.MerchantOrderID).First(&topUp).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// A signed provider event can race the local facade's order binding;
			// leave it retryable so the gateway/provider can deliver it again.
			return "", ErrPaymentGatewaySettlementRetryable
		}
		return "", err
	}
	if topUp.UserId != userID || topUp.PaymentProvider != command.Provider {
		return "", ErrPaymentMethodMismatch
	}
	if topUp.PaymentGatewayOrderID == "" || topUp.PaymentGatewayOrderID != command.OrderID {
		return "", ErrPaymentGatewaySettlementMismatch
	}
	if topUp.PaymentProviderAccountID != command.ProviderAccountID && (topUp.PaymentProviderAccountID != "" || command.ProviderAccountID != "") {
		return "", ErrPaymentGatewaySettlementMismatch
	}
	if topUp.PaymentEnvironment != command.Environment && (topUp.PaymentEnvironment != "" || command.Environment != "") {
		return "", ErrPaymentGatewaySettlementMismatch
	}
	if normalizeGatewayPaymentMethod(topUp.PaymentMethod) != command.PaymentMethod {
		return "", ErrPaymentGatewaySettlementMismatch
	}
	if topUp.PaymentCurrency == "" || !strings.EqualFold(topUp.PaymentCurrency, command.Currency) {
		return "", ErrPaymentGatewaySettlementMismatch
	}
	if err := validateGatewayMoney(topUp.Money, command.AmountMinor); err != nil {
		return "", err
	}
	if err := validateGatewayWalletSnapshot(&topUp, command); err != nil {
		return "", err
	}
	if topUp.Status == common.TopUpStatusSuccess {
		if command.QuotaAmount > 0 && topUp.CreditedQuota != int(command.QuotaAmount) {
			return "", ErrPaymentGatewaySettlementMismatch
		}
		return fmt.Sprintf("topup:%d", topUp.Id), nil
	}
	if topUp.Status != common.TopUpStatusPending {
		return "", ErrTopUpStatusInvalid
	}
	quotaToAdd, err := validateGatewayQuota(&topUp, command)
	if err != nil {
		return "", err
	}
	if quotaToAdd <= 0 || quotaToAdd > int64(common.MaxQuota) {
		return "", ErrPaymentGatewaySettlementInvalid
	}
	var user User
	if err := lockForUpdate(tx).Where("id = ?", userID).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// The gateway order may arrive before a replicated user row is
			// available. Keep the signed event retryable instead of acknowledging
			// a provider success that could not be credited.
			return "", ErrPaymentGatewaySettlementRetryable
		}
		return "", err
	}
	if int64(user.Quota) > int64(common.MaxQuota)-quotaToAdd {
		return "", ErrPaymentGatewaySettlementInvalid
	}
	topUp.PaymentMethod = command.PaymentMethod
	topUp.Status = common.TopUpStatusSuccess
	topUp.CompleteTime = common.GetTimestamp()
	topUp.CreditedQuota = int(quotaToAdd)
	if err := prepareInviteFirstTopUpRewardTx(tx, &topUp, int(quotaToAdd)); err != nil {
		return "", err
	}
	if err := tx.Save(&topUp).Error; err != nil {
		return "", err
	}
	updated := tx.Model(&user).Where("id = ?", userID).Update("quota", gorm.Expr("quota + ?", quotaToAdd))
	if updated.Error != nil {
		return "", updated.Error
	}
	if updated.RowsAffected != 1 {
		return "", ErrPaymentGatewaySettlementMismatch
	}
	return fmt.Sprintf("topup:%d", topUp.Id), nil
}

func applyGatewaySubscriptionSettlementTx(tx *gorm.DB, command *PaymentGatewaySettlementCommand, userID int) (string, error) {
	var user User
	if err := lockForUpdate(tx).Where("id = ?", userID).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// A missing user is retryable: the gateway must not acknowledge a
			// provider success while the entitlement owner is unavailable.
			return "", ErrPaymentGatewaySettlementRetryable
		}
		return "", err
	}
	var order SubscriptionOrder
	if err := lockForUpdate(tx).Where("trade_no = ?", command.MerchantOrderID).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Treat a temporarily absent local order as retryable per the gateway
			// contract; a missing order is not evidence that payment failed.
			return "", ErrPaymentGatewaySettlementRetryable
		}
		return "", err
	}
	if order.UserId != userID || order.PaymentProvider != command.Provider || order.PaymentCurrency == "" || !strings.EqualFold(order.PaymentCurrency, command.Currency) {
		return "", ErrPaymentGatewaySettlementMismatch
	}
	if order.PaymentGatewayOrderID == "" || order.PaymentGatewayOrderID != command.OrderID {
		return "", ErrPaymentGatewaySettlementMismatch
	}
	if order.PaymentProviderAccountID != command.ProviderAccountID && (order.PaymentProviderAccountID != "" || command.ProviderAccountID != "") {
		return "", ErrPaymentGatewaySettlementMismatch
	}
	if order.PaymentEnvironment != command.Environment && (order.PaymentEnvironment != "" || command.Environment != "") {
		return "", ErrPaymentGatewaySettlementMismatch
	}
	if normalizeGatewayPaymentMethod(order.PaymentMethod) != command.PaymentMethod {
		return "", ErrPaymentGatewaySettlementMismatch
	}
	if err := validateGatewayMoney(order.Money, command.AmountMinor); err != nil {
		return "", err
	}
	if order.Status == common.TopUpStatusSuccess {
		return fmt.Sprintf("subscription:%d", order.Id), nil
	}
	if order.Status != common.TopUpStatusPending {
		return "", ErrSubscriptionOrderStatusInvalid
	}
	plan, err := getSubscriptionPlanByIdTx(tx, order.PlanId)
	if err != nil {
		return "", err
	}
	if err := validateGatewaySubscriptionSnapshot(&order, plan, command); err != nil {
		return "", err
	}
	if _, err := CreateUserSubscriptionFromPlanTx(tx, userID, plan, "order"); err != nil {
		return "", err
	}
	if err := upsertSubscriptionTopUpTx(tx, &order); err != nil {
		return "", err
	}
	order.Status = common.TopUpStatusSuccess
	order.CompleteTime = common.GetTimestamp()
	order.PaymentMethod = command.PaymentMethod
	order.ProviderPayload = "payment_gateway:" + command.ProviderEventID
	if err := tx.Save(&order).Error; err != nil {
		return "", err
	}
	return fmt.Sprintf("subscription:%d", order.Id), nil
}

func validateGatewayMoney(expected float64, actualMinor int64) error {
	if actualMinor <= 0 {
		return ErrPaymentAmountInvalid
	}
	expectedAmount := decimal.NewFromFloat(expected).Round(2)
	actualAmount := decimal.NewFromInt(actualMinor).Div(decimal.NewFromInt(100))
	if !actualAmount.Equal(expectedAmount) {
		return ErrPaymentAmountMismatch
	}
	return nil
}

func validGatewayPaymentMethod(method string) bool {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "card", "apple_pay", "google_pay", "wechat_pay", "alipay", "wxpay":
		return true
	default:
		return false
	}
}
