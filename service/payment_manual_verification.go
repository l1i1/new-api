package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
	pancake "github.com/waffo-com/waffo-pancake-sdk-go"
)

const epayReconciliationQueryURLEnv = "EPAY_RECONCILIATION_QUERY_URL"

type PaymentVerificationState string

const (
	PaymentVerificationNotPaid     PaymentVerificationState = "not_paid"
	PaymentVerificationNotFound    PaymentVerificationState = "not_found"
	PaymentVerificationRefunded    PaymentVerificationState = "refunded"
	PaymentVerificationMismatch    PaymentVerificationState = "mismatch"
	PaymentVerificationAmbiguous   PaymentVerificationState = "ambiguous"
	PaymentVerificationUnavailable PaymentVerificationState = "unavailable"
	PaymentVerificationUnsupported PaymentVerificationState = "unsupported"
)

type PaymentVerificationError struct {
	State PaymentVerificationState
}

func (e *PaymentVerificationError) Error() string {
	return "payment verification failed: " + string(e.State)
}

type VerifiedTopUpPayment struct {
	Provider        string
	TradeNo         string
	ProviderTradeNo string
	PaymentMethod   string
	PaidAmount      string
	Currency        string
}

// VerifyTopUpPayment confirms provider-side paid state before an administrator
// can settle a pending local order. It never changes order or wallet state.
func VerifyTopUpPayment(ctx context.Context, topUp *model.TopUp) (*VerifiedTopUpPayment, error) {
	if topUp == nil || strings.TrimSpace(topUp.TradeNo) == "" {
		return nil, verificationError(PaymentVerificationNotFound)
	}
	switch topUp.PaymentProvider {
	case model.PaymentProviderEpay:
		return verifyEpayTopUpPayment(ctx, topUp)
	case model.PaymentProviderWaffoPancake:
		return verifyWaffoPancakeTopUpPayment(ctx, topUp)
	default:
		return nil, verificationError(PaymentVerificationUnsupported)
	}
}

func verificationError(state PaymentVerificationState) error {
	return &PaymentVerificationError{State: state}
}

func PaymentVerificationStateOf(err error) PaymentVerificationState {
	var verificationErr *PaymentVerificationError
	if errors.As(err, &verificationErr) {
		return verificationErr.State
	}
	return PaymentVerificationUnavailable
}

type epayOrderQueryResponse struct {
	Code        int    `json:"code"`
	Status      int    `json:"status"`
	PID         int    `json:"pid"`
	TradeNo     string `json:"trade_no"`
	OutTradeNo  string `json:"out_trade_no"`
	PaymentType string `json:"type"`
	Money       string `json:"money"`
}

func verifyEpayTopUpPayment(ctx context.Context, topUp *model.TopUp) (*VerifiedTopUpPayment, error) {
	queryURL, err := validatedEpayReconciliationURL()
	if err != nil {
		return nil, verificationError(PaymentVerificationUnavailable)
	}
	merchantID, err := strconv.Atoi(strings.TrimSpace(operation_setting.EpayId))
	if err != nil || merchantID <= 0 || strings.TrimSpace(operation_setting.EpayKey) == "" {
		return nil, verificationError(PaymentVerificationUnavailable)
	}

	values := queryURL.Query()
	values.Set("act", "order")
	values.Set("pid", strconv.Itoa(merchantID))
	values.Set("key", operation_setting.EpayKey)
	values.Set("out_trade_no", topUp.TradeNo)
	queryURL.RawQuery = values.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL.String(), nil)
	if err != nil {
		return nil, verificationError(PaymentVerificationUnavailable)
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, verificationError(PaymentVerificationUnavailable)
	}
	transport := defaultTransport.Clone()
	transport.Proxy = nil
	client := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, verificationError(PaymentVerificationUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, verificationError(PaymentVerificationUnavailable)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		return nil, verificationError(PaymentVerificationUnavailable)
	}
	var providerOrder epayOrderQueryResponse
	if err := common.Unmarshal(body, &providerOrder); err != nil {
		return nil, verificationError(PaymentVerificationUnavailable)
	}
	return validateEpayProviderOrder(topUp, merchantID, &providerOrder)
}

func validatedEpayReconciliationURL() (*url.URL, error) {
	rawURL := strings.TrimSpace(os.Getenv(epayReconciliationQueryURLEnv))
	if rawURL == "" {
		return nil, errors.New("epay reconciliation query URL is not configured")
	}
	queryURL, err := url.Parse(rawURL)
	if err != nil || queryURL.Host == "" || queryURL.User != nil || queryURL.RawQuery != "" || queryURL.Fragment != "" {
		return nil, errors.New("invalid epay reconciliation query URL")
	}
	if queryURL.Scheme != "https" {
		queryIP := net.ParseIP(queryURL.Hostname())
		if queryURL.Scheme != "http" || queryIP == nil || (!queryIP.IsPrivate() && !queryIP.IsLoopback()) {
			return nil, errors.New("epay reconciliation query URL must use HTTPS or private-address HTTP")
		}
	}
	if !strings.EqualFold(strings.TrimSpace(queryURL.Path), "/api.php") {
		return nil, errors.New("epay reconciliation query URL must target /api.php")
	}
	publicURL, err := url.Parse(strings.TrimSpace(operation_setting.PayAddress))
	if err == nil && strings.EqualFold(queryURL.Hostname(), publicURL.Hostname()) {
		return nil, errors.New("epay reconciliation must not use the public payment origin")
	}
	return queryURL, nil
}

func validateEpayProviderOrder(topUp *model.TopUp, merchantID int, providerOrder *epayOrderQueryResponse) (*VerifiedTopUpPayment, error) {
	if providerOrder == nil {
		return nil, verificationError(PaymentVerificationUnavailable)
	}
	if providerOrder.Code == -1 {
		return nil, verificationError(PaymentVerificationNotFound)
	}
	if providerOrder.Code != 1 {
		return nil, verificationError(PaymentVerificationUnavailable)
	}
	switch providerOrder.Status {
	case 0:
		return nil, verificationError(PaymentVerificationNotPaid)
	case 2:
		return nil, verificationError(PaymentVerificationRefunded)
	case 1:
	default:
		return nil, verificationError(PaymentVerificationMismatch)
	}
	paymentMethod := strings.TrimSpace(providerOrder.PaymentType)
	if providerOrder.PID != merchantID ||
		strings.TrimSpace(providerOrder.OutTradeNo) != topUp.TradeNo ||
		paymentMethod == "" ||
		!operation_setting.ContainsPayMethod(paymentMethod) ||
		strings.TrimSpace(providerOrder.TradeNo) == "" {
		return nil, verificationError(PaymentVerificationMismatch)
	}
	if err := validatePaymentAmount(topUp.Money, providerOrder.Money); err != nil {
		return nil, verificationError(PaymentVerificationMismatch)
	}
	return &VerifiedTopUpPayment{
		Provider:        model.PaymentProviderEpay,
		TradeNo:         topUp.TradeNo,
		ProviderTradeNo: strings.TrimSpace(providerOrder.TradeNo),
		PaymentMethod:   paymentMethod,
		PaidAmount:      strings.TrimSpace(providerOrder.Money),
	}, nil
}

type waffoPancakeReconciliationPayment struct {
	ID                      string `json:"id"`
	OrderMerchantExternalID string `json:"orderMerchantExternalId"`
	Status                  string `json:"status"`
	RefundStatus            string `json:"refundStatus"`
	SnapshotAmountDetails   struct {
		Currency string `json:"currency"`
		Subtotal string `json:"subtotal"`
	} `json:"snapshotAmountDetails"`
	OnetimeOrder struct {
		Status   string `json:"status"`
		TestMode bool   `json:"testMode"`
		Store    struct {
			ID string `json:"id"`
		} `json:"store"`
	} `json:"onetimeOrder"`
}

func verifyWaffoPancakeTopUpPayment(ctx context.Context, topUp *model.TopUp) (*VerifiedTopUpPayment, error) {
	client, err := newWaffoPancakeClient()
	if err != nil {
		return nil, verificationError(PaymentVerificationUnavailable)
	}
	type queryShape struct {
		Payments []waffoPancakeReconciliationPayment `json:"payments"`
	}
	response, err := pancake.GraphQLQuery[queryShape](ctx, client, pancake.GraphQLParams{
		Query: `query PaymentReconciliation($ref: String!) {
			payments(limit: 10, filter: { orderMerchantExternalId: { eq: $ref } }) {
				id
				orderMerchantExternalId
				status
				refundStatus
				snapshotAmountDetails { currency subtotal }
				onetimeOrder { status testMode store { id } }
			}
		}`,
		Variables: map[string]any{"ref": topUp.TradeNo},
	})
	if err != nil || response == nil || len(response.Errors) > 0 {
		return nil, verificationError(PaymentVerificationUnavailable)
	}
	return validateWaffoPancakeProviderPayments(topUp, response.Data.Payments)
}

func validateWaffoPancakeProviderPayments(topUp *model.TopUp, payments []waffoPancakeReconciliationPayment) (*VerifiedTopUpPayment, error) {
	succeeded := make([]waffoPancakeReconciliationPayment, 0, 1)
	for _, payment := range payments {
		if strings.EqualFold(strings.TrimSpace(payment.Status), "succeeded") {
			succeeded = append(succeeded, payment)
		}
	}
	if len(succeeded) == 0 {
		return nil, verificationError(PaymentVerificationNotPaid)
	}
	if len(succeeded) != 1 {
		return nil, verificationError(PaymentVerificationAmbiguous)
	}
	payment := succeeded[0]
	if strings.EqualFold(strings.TrimSpace(payment.RefundStatus), "succeeded") {
		return nil, verificationError(PaymentVerificationRefunded)
	}
	if strings.TrimSpace(payment.ID) == "" ||
		strings.TrimSpace(payment.OrderMerchantExternalID) != topUp.TradeNo ||
		!strings.EqualFold(strings.TrimSpace(payment.OnetimeOrder.Status), "completed") ||
		payment.OnetimeOrder.TestMode ||
		strings.TrimSpace(payment.OnetimeOrder.Store.ID) != strings.TrimSpace(setting.WaffoPancakeStoreID) ||
		!strings.EqualFold(strings.TrimSpace(payment.SnapshotAmountDetails.Currency), "USD") {
		return nil, verificationError(PaymentVerificationMismatch)
	}
	if err := validatePaymentAmount(topUp.Money, payment.SnapshotAmountDetails.Subtotal); err != nil {
		return nil, verificationError(PaymentVerificationMismatch)
	}
	return &VerifiedTopUpPayment{
		Provider:        model.PaymentProviderWaffoPancake,
		TradeNo:         topUp.TradeNo,
		ProviderTradeNo: strings.TrimSpace(payment.ID),
		PaymentMethod:   topUp.PaymentMethod,
		PaidAmount:      strings.TrimSpace(payment.SnapshotAmountDetails.Subtotal),
		Currency:        "USD",
	}, nil
}

func validatePaymentAmount(expectedMoney float64, actualMoney string) error {
	actual, err := decimal.NewFromString(strings.TrimSpace(actualMoney))
	if err != nil || actual.LessThanOrEqual(decimal.Zero) {
		return fmt.Errorf("invalid paid amount")
	}
	expected := decimal.NewFromFloat(expectedMoney).Round(2)
	if !actual.Equal(expected) {
		return fmt.Errorf("paid amount mismatch")
	}
	return nil
}
