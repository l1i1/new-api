package controller

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
)

const (
	defaultPaymentGatewaySettlementMaxAge = 5 * time.Minute
	maximumPaymentGatewaySettlementMaxAge = 24 * time.Hour
)

// PaymentGatewaySettlement receives the gateway's signed, idempotent ledger
// command. It returns 2xx only after the local transaction has committed.
func PaymentGatewaySettlement(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var command model.PaymentGatewaySettlementCommand
	if err := c.ShouldBindJSON(&command); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_settlement", "message": "Settlement command is invalid"}})
		return
	}
	command.Signature = strings.TrimSpace(command.Signature)
	headerSignature := strings.TrimSpace(c.GetHeader("X-Gateway-Signature"))
	if command.Signature == "" {
		command.Signature = headerSignature
	}
	if headerSignature != "" && command.Signature != headerSignature {
		writeSettlementError(c, http.StatusUnauthorized, "signature_invalid", "Settlement signature is invalid")
		return
	}
	if command.CommandID == "" {
		writeSettlementError(c, http.StatusBadRequest, "invalid_settlement", "Settlement command is invalid")
		return
	}
	if headerCommandID := strings.TrimSpace(c.GetHeader("X-Gateway-Command-ID")); headerCommandID != "" && headerCommandID != command.CommandID {
		writeSettlementError(c, http.StatusUnauthorized, "command_mismatch", "Settlement command is invalid")
		return
	}
	if idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key")); idempotencyKey != "" && idempotencyKey != command.CommandID {
		writeSettlementError(c, http.StatusUnauthorized, "command_mismatch", "Settlement command is invalid")
		return
	}
	// HotPay signs each order with the secret of the upstream application that
	// owns it, so every configured settlement secret is a valid candidate. A
	// deployment that serves several applications lists them all in
	// HOTPAY_SETTLEMENT_SECRETS; the single-secret setting/environment pair
	// remains the primary entry.
	if !model.VerifyPaymentGatewaySettlementSignatureWithSecrets(command, hotPaySettlementSecrets(), command.Signature) {
		writeSettlementError(c, http.StatusUnauthorized, "signature_invalid", "Settlement signature is invalid")
		return
	}
	if !paymentGatewaySettlementTimestampValid(command.IssuedAt, time.Now().UTC()) {
		writeSettlementError(c, http.StatusBadRequest, "settlement_expired", "Settlement command is outside the replay window")
		return
	}
	result, err := model.ApplyPaymentGatewaySettlement(command)
	if err != nil {
		status, code := http.StatusServiceUnavailable, "settlement_retryable"
		switch {
		case errors.Is(err, model.ErrPaymentGatewaySettlementInvalid),
			errors.Is(err, model.ErrPaymentGatewaySettlementConflict),
			errors.Is(err, model.ErrPaymentGatewaySettlementMismatch),
			errors.Is(err, model.ErrPaymentMethodMismatch),
			errors.Is(err, model.ErrPaymentAmountInvalid),
			errors.Is(err, model.ErrPaymentAmountMismatch),
			errors.Is(err, model.ErrTopUpNotFound),
			errors.Is(err, model.ErrSubscriptionOrderNotFound):
			status, code = http.StatusConflict, "payment_mismatch"
		case errors.Is(err, model.ErrTopUpStatusInvalid),
			errors.Is(err, model.ErrSubscriptionOrderStatusInvalid):
			status, code = http.StatusConflict, "invalid_order_status"
		}
		writeSettlementError(c, status, code, "Settlement was not committed")
		return
	}
	committedAt := time.Unix(result.Settlement.CommittedAt, 0).UTC()
	acknowledgedAt := time.Now().UTC()
	c.JSON(http.StatusOK, gin.H{
		"acknowledged":       true,
		"committed":          true,
		"status":             model.PaymentGatewaySettlementCommitted,
		"command_id":         command.CommandID,
		"credited_reference": result.CreditedReference,
		"acknowledged_at":    acknowledgedAt,
		"committed_at":       committedAt,
		"duplicate":          result.Duplicate,
		"request_id":         settlementRequestID(c),
	})
}

// hotPaySettlementSecrets collects every settlement secret this deployment
// accepts. The option value wins over the environment, and
// HOTPAY_SETTLEMENT_SECRETS may list additional secrets (comma-separated) when
// several upstream applications settle into the same New API instance.
func hotPaySettlementSecrets() []string {
	secrets := make([]string, 0, 2)
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		secrets = append(secrets, value)
	}
	add(setting.HotPaySettlementSecret)
	add(os.Getenv("HOTPAY_SETTLEMENT_SECRET"))
	for _, value := range strings.FieldsFunc(os.Getenv("HOTPAY_SETTLEMENT_SECRETS"), func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		add(value)
	}
	return secrets
}

func paymentGatewaySettlementTimestampValid(issuedAt, now time.Time) bool {
	if issuedAt.IsZero() {
		return false
	}
	maxAge := defaultPaymentGatewaySettlementMaxAge
	if setting.HotPaySettlementMaxAgeSec > 0 {
		maxAge = time.Duration(setting.HotPaySettlementMaxAgeSec) * time.Second
		if maxAge > maximumPaymentGatewaySettlementMaxAge {
			maxAge = maximumPaymentGatewaySettlementMaxAge
		}
	} else if raw := strings.TrimSpace(os.Getenv("HOTPAY_SETTLEMENT_MAX_AGE_SECONDS")); raw != "" {
		if seconds, err := time.ParseDuration(raw + "s"); err == nil && seconds > 0 && seconds <= maximumPaymentGatewaySettlementMaxAge {
			maxAge = seconds
		}
	}
	issuedAt = issuedAt.UTC()
	now = now.UTC()
	return !issuedAt.After(now.Add(30*time.Second)) && now.Sub(issuedAt) <= maxAge
}

func writeSettlementError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message, "request_id": settlementRequestID(c)}})
}

func settlementRequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.GetHeader("X-Request-ID"))
}
