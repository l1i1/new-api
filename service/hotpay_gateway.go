package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	hotPayGatewayURLEnv              = "HOTPAY_GATEWAY_URL"
	hotPayGatewayAPIKeyEnv           = "HOTPAY_GATEWAY_API_KEY"
	hotPayGatewayTimeoutSecondsEnv   = "HOTPAY_GATEWAY_TIMEOUT_SECONDS"
	hotPayGatewayAllowHTTPEnv        = "HOTPAY_GATEWAY_ALLOW_INSECURE_HTTP"
	hotPayGatewayAllowedHostsEnv     = "HOTPAY_GATEWAY_ALLOWED_HOSTS"
	hotPayGatewayDefaultTimeout      = 15 * time.Second
	hotPayGatewayMaxResponseBodySize = 1 << 20
)

var ErrHotPayGatewayNotConfigured = errors.New("hotpay gateway is not configured")

// HotPayGatewayError preserves the gateway's stable error code without
// exposing response bodies or provider credentials to callers.
type HotPayGatewayError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *HotPayGatewayError) Error() string {
	if e == nil {
		return "hotpay gateway request failed"
	}
	if e.Code != "" {
		return fmt.Sprintf("hotpay gateway request failed: %s", e.Code)
	}
	return fmt.Sprintf("hotpay gateway request failed with status %d", e.StatusCode)
}

func (e *HotPayGatewayError) Retryable() bool {
	return e != nil && (e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= http.StatusInternalServerError)
}

type HotPayGatewayConfig struct {
	BaseURL      string
	APIKey       string
	AllowedHosts []string
	Timeout      time.Duration
	AllowHTTP    bool
	HTTPClient   *http.Client
}

type HotPayGatewayClient struct {
	baseURL    *url.URL
	apiKey     string
	httpClient *http.Client
}

type HotPayGatewayCreateOrderRequest struct {
	MerchantOrderID       string         `json:"merchant_order_id"`
	BusinessType          string         `json:"business_type"`
	UserID                string         `json:"user_id"`
	BuyerEmail            string         `json:"buyer_email,omitempty"`
	ProductID             string         `json:"product_id,omitempty"`
	AmountMinor           int64          `json:"amount_minor"`
	Currency              string         `json:"currency"`
	QuotaAmount           int64          `json:"quota_amount,omitempty"`
	Provider              string         `json:"provider"`
	PaymentMethod         string         `json:"payment_method"`
	ProviderAccountID     string         `json:"provider_account_id,omitempty"`
	Environment           string         `json:"environment,omitempty"`
	CompatibilityProtocol string         `json:"compatibility_protocol,omitempty"`
	MerchantNotifyURL     string         `json:"merchant_notify_url,omitempty"`
	ReturnURL             string         `json:"return_url,omitempty"`
	PriceSnapshot         map[string]any `json:"price_snapshot,omitempty"`
	ExpiresAt             string         `json:"expires_at,omitempty"`
	Description           string         `json:"description,omitempty"`
}

type HotPayGatewayOrder struct {
	ID                     string   `json:"id"`
	MerchantOrderID        string   `json:"merchant_order_id"`
	BusinessType           string   `json:"business_type"`
	UserID                 string   `json:"user_id"`
	AmountMinor            int64    `json:"amount_minor"`
	Currency               string   `json:"currency"`
	QuotaAmount            int64    `json:"quota_amount"`
	Provider               string   `json:"provider"`
	ProviderAccountID      string   `json:"provider_account_id"`
	PaymentMethod          string   `json:"payment_method"`
	ProviderPaymentMethods []string `json:"provider_payment_methods"`
	ProviderOrderID        string   `json:"provider_order_id"`
	Environment            string   `json:"environment"`
	Status                 string   `json:"status"`
	ExpiresAt              string   `json:"expires_at"`
	CreatedAt              string   `json:"created_at"`
}

type HotPayGatewayAttempt struct {
	ID                string `json:"id"`
	ProviderSessionID string `json:"provider_session_id"`
	CheckoutURL       string `json:"checkout_url"`
	Status            string `json:"status"`
	ErrorCode         string `json:"error_code"`
}

type HotPayGatewayCreateOrderResponse struct {
	Order   HotPayGatewayOrder   `json:"order"`
	Attempt HotPayGatewayAttempt `json:"attempt"`
}

func NewHotPayGatewayClientFromEnv() (*HotPayGatewayClient, error) {
	return NewHotPayGatewayClient(HotPayGatewayConfig{
		BaseURL:      strings.TrimSpace(os.Getenv(hotPayGatewayURLEnv)),
		APIKey:       strings.TrimSpace(os.Getenv(hotPayGatewayAPIKeyEnv)),
		AllowedHosts: splitHostList(os.Getenv(hotPayGatewayAllowedHostsEnv)),
		Timeout:      hotPayGatewayTimeoutFromEnv(),
		AllowHTTP:    strings.EqualFold(strings.TrimSpace(os.Getenv(hotPayGatewayAllowHTTPEnv)), "true") || strings.TrimSpace(os.Getenv(hotPayGatewayAllowHTTPEnv)) == "1",
	})
}

func IsHotPayGatewayEnabled() bool {
	return strings.TrimSpace(os.Getenv(hotPayGatewayURLEnv)) != ""
}

func NewHotPayGatewayClient(config HotPayGatewayConfig) (*HotPayGatewayClient, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		return nil, ErrHotPayGatewayNotConfigured
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return nil, fmt.Errorf("invalid %s", hotPayGatewayURLEnv)
	}
	if parsed.Scheme != "https" && !(config.AllowHTTP && parsed.Scheme == "http") {
		return nil, fmt.Errorf("%s must use HTTPS unless %s is enabled", hotPayGatewayURLEnv, hotPayGatewayAllowHTTPEnv)
	}
	if len(config.AllowedHosts) == 0 || !hotPayHostAllowed(parsed.Hostname(), config.AllowedHosts) {
		return nil, fmt.Errorf("%s must match a host in %s", hotPayGatewayURLEnv, hotPayGatewayAllowedHostsEnv)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = hotPayGatewayDefaultTimeout
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	clonedClient := *httpClient
	clonedClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errors.New("hotpay gateway redirect rejected")
	}
	return &HotPayGatewayClient{baseURL: parsed, apiKey: strings.TrimSpace(config.APIKey), httpClient: &clonedClient}, nil
}

func splitHostList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' })
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func hotPayHostAllowed(host string, allowedHosts []string) bool {
	for _, allowed := range allowedHosts {
		if strings.EqualFold(strings.TrimSpace(allowed), host) {
			return true
		}
	}
	return false
}

func (c *HotPayGatewayClient) CreateOrder(ctx context.Context, idempotencyKey string, request HotPayGatewayCreateOrderRequest) (HotPayGatewayCreateOrderResponse, error) {
	if c == nil || c.baseURL == nil {
		return HotPayGatewayCreateOrderResponse{}, ErrHotPayGatewayNotConfigured
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return HotPayGatewayCreateOrderResponse{}, errors.New("idempotency key is required")
	}
	request.ProviderAccountID = strings.TrimSpace(request.ProviderAccountID)
	request.Environment = strings.ToLower(strings.TrimSpace(request.Environment))
	if request.ProviderAccountID == "" {
		return HotPayGatewayCreateOrderResponse{}, errors.New("provider account ID is required")
	}
	if request.Environment != "test" && request.Environment != "prod" {
		return HotPayGatewayCreateOrderResponse{}, errors.New("payment environment must be test or prod")
	}
	body, err := json.Marshal(request)
	if err != nil {
		return HotPayGatewayCreateOrderResponse{}, fmt.Errorf("marshal hotpay order: %w", err)
	}
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/payment/orders"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return HotPayGatewayCreateOrderResponse{}, fmt.Errorf("create hotpay request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Idempotency-Key", strings.TrimSpace(idempotencyKey))
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	response, err := c.httpClient.Do(req)
	if err != nil {
		return HotPayGatewayCreateOrderResponse{}, fmt.Errorf("hotpay checkout request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, hotPayGatewayMaxResponseBodySize))
	if err != nil {
		return HotPayGatewayCreateOrderResponse{}, fmt.Errorf("read hotpay response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return HotPayGatewayCreateOrderResponse{}, decodeHotPayGatewayError(response.StatusCode, responseBody)
	}
	var result HotPayGatewayCreateOrderResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return HotPayGatewayCreateOrderResponse{}, fmt.Errorf("decode hotpay response: %w", err)
	}
	if strings.TrimSpace(result.Order.ID) == "" || strings.TrimSpace(result.Order.MerchantOrderID) == "" || result.Order.MerchantOrderID != request.MerchantOrderID || strings.TrimSpace(result.Attempt.CheckoutURL) == "" {
		return HotPayGatewayCreateOrderResponse{}, errors.New("hotpay response is missing order or checkout_url")
	}
	if strings.TrimSpace(request.BusinessType) != "" && (strings.TrimSpace(result.Order.BusinessType) == "" || result.Order.BusinessType != request.BusinessType) {
		return HotPayGatewayCreateOrderResponse{}, errors.New("hotpay response business type does not match request")
	}
	if strings.TrimSpace(request.UserID) != "" && (strings.TrimSpace(result.Order.UserID) == "" || result.Order.UserID != request.UserID) {
		return HotPayGatewayCreateOrderResponse{}, errors.New("hotpay response user does not match request")
	}
	if strings.TrimSpace(request.Currency) != "" && (strings.TrimSpace(result.Order.Currency) == "" || !strings.EqualFold(result.Order.Currency, request.Currency)) {
		return HotPayGatewayCreateOrderResponse{}, errors.New("hotpay response currency does not match request")
	}
	if strings.TrimSpace(request.Provider) != "" && (strings.TrimSpace(result.Order.Provider) == "" || !strings.EqualFold(result.Order.Provider, request.Provider)) {
		return HotPayGatewayCreateOrderResponse{}, errors.New("hotpay response provider does not match request")
	}
	if strings.TrimSpace(result.Order.ProviderAccountID) == "" || result.Order.ProviderAccountID != request.ProviderAccountID {
		return HotPayGatewayCreateOrderResponse{}, errors.New("hotpay response provider account does not match request")
	}
	if !strings.EqualFold(strings.TrimSpace(result.Order.Environment), request.Environment) {
		return HotPayGatewayCreateOrderResponse{}, errors.New("hotpay response environment does not match request")
	}
	if strings.TrimSpace(request.PaymentMethod) != "" && (strings.TrimSpace(result.Order.PaymentMethod) == "" || !strings.EqualFold(result.Order.PaymentMethod, request.PaymentMethod)) {
		return HotPayGatewayCreateOrderResponse{}, errors.New("hotpay response payment method does not match request")
	}
	if len(result.Order.ProviderPaymentMethods) == 0 || !containsFold(result.Order.ProviderPaymentMethods, request.PaymentMethod) {
		return HotPayGatewayCreateOrderResponse{}, errors.New("hotpay response provider payment method whitelist is missing or does not contain request method")
	}
	if request.AmountMinor > 0 && result.Order.AmountMinor != request.AmountMinor {
		return HotPayGatewayCreateOrderResponse{}, errors.New("hotpay response amount does not match request")
	}
	if strings.TrimSpace(result.Order.Status) == "" || (result.Order.Status != "pending" && result.Order.Status != "processing") {
		return HotPayGatewayCreateOrderResponse{}, errors.New("hotpay response order status is not payable")
	}
	if strings.TrimSpace(result.Attempt.Status) == "" {
		return HotPayGatewayCreateOrderResponse{}, errors.New("hotpay response attempt status is missing")
	}
	if strings.TrimSpace(result.Attempt.ID) == "" {
		return HotPayGatewayCreateOrderResponse{}, errors.New("hotpay response attempt ID is missing")
	}
	return result, nil
}

func containsFold(values []string, wanted string) bool {
	wanted = normalizeGatewayPaymentMethod(wanted)
	if wanted == "" {
		return false
	}
	for _, value := range values {
		if normalizeGatewayPaymentMethod(value) == wanted {
			return true
		}
	}
	return false
}

func normalizeGatewayPaymentMethod(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "wechat", "wechat_pay", "wxpay":
		return "wechat_pay"
	case "applepay", "apple_pay":
		return "apple_pay"
	case "googlepay", "google_pay":
		return "google_pay"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func decodeHotPayGatewayError(status int, body []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	return &HotPayGatewayError{StatusCode: status, Code: strings.TrimSpace(envelope.Error.Code), Message: strings.TrimSpace(envelope.Error.Message)}
}

func hotPayGatewayTimeoutFromEnv() time.Duration {
	value := strings.TrimSpace(os.Getenv(hotPayGatewayTimeoutSecondsEnv))
	if value == "" {
		return hotPayGatewayDefaultTimeout
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 || seconds > 120 {
		return hotPayGatewayDefaultTimeout
	}
	return time.Duration(seconds) * time.Second
}
