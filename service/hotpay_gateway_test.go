package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHotPayGatewayClientCreateOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/payment/orders" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "wallet-key" {
			t.Fatalf("idempotency key = %q", got)
		}
		if got := r.Header.Get("X-API-Key"); got != "test-api-key" {
			t.Fatalf("api key = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"order":{"id":"ord_1","merchant_order_id":"trade_1","business_type":"wallet_topup","user_id":"7","amount_minor":1000,"currency":"CNY","provider":"waffo_pancake","provider_account_id":"store_1","payment_method":"wechat_pay","provider_payment_methods":["wechat"],"environment":"test","status":"pending","expires_at":"2026-08-15T00:00:00Z"},"attempt":{"id":"attempt_1","provider_session_id":"sess_1","checkout_url":"https://checkout.example/1","status":"created"}}`))
	}))
	defer server.Close()

	client, err := NewHotPayGatewayClient(HotPayGatewayConfig{BaseURL: server.URL, APIKey: "test-api-key", AllowedHosts: []string{"127.0.0.1"}, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.CreateOrder(context.Background(), "wallet-key", HotPayGatewayCreateOrderRequest{
		MerchantOrderID: "trade_1", BusinessType: "wallet_topup", UserID: "7", AmountMinor: 1000,
		Currency: "CNY", Provider: "waffo_pancake", ProviderAccountID: "store_1", PaymentMethod: "wechat_pay", Environment: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Order.MerchantOrderID != "trade_1" || result.Attempt.CheckoutURL == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestHotPayGatewayClientReturnsTypedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"code":"unsupported_payment_method","message":"unsupported"}}`))
	}))
	defer server.Close()

	client, err := NewHotPayGatewayClient(HotPayGatewayConfig{BaseURL: server.URL, AllowedHosts: []string{"127.0.0.1"}, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateOrder(context.Background(), "key", HotPayGatewayCreateOrderRequest{MerchantOrderID: "trade", ProviderAccountID: "store_1", Environment: "test"})
	gatewayErr, ok := err.(*HotPayGatewayError)
	if !ok || gatewayErr.Code != "unsupported_payment_method" || gatewayErr.Retryable() {
		t.Fatalf("unexpected gateway error: %T %v", err, err)
	}
}

func TestHotPayGatewayClientRequiresCanonicalOrderID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"order":{"merchant_order_id":"trade_1"},"attempt":{"checkout_url":"https://checkout.example/1"}}`))
	}))
	defer server.Close()

	client, err := NewHotPayGatewayClient(HotPayGatewayConfig{BaseURL: server.URL, AllowedHosts: []string{"127.0.0.1"}, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateOrder(context.Background(), "key", HotPayGatewayCreateOrderRequest{MerchantOrderID: "trade_1", ProviderAccountID: "store_1", Environment: "test"})
	if err == nil {
		t.Fatal("expected missing canonical order ID to be rejected")
	}
}

func TestNewHotPayGatewayClientRequiresTLSByDefault(t *testing.T) {
	if _, err := NewHotPayGatewayClient(HotPayGatewayConfig{BaseURL: "http://localhost:8080"}); err == nil {
		t.Fatal("expected insecure URL to be rejected")
	}
}

func TestNewHotPayGatewayClientRequiresHostAllowlist(t *testing.T) {
	if _, err := NewHotPayGatewayClient(HotPayGatewayConfig{BaseURL: "https://pay.example.com"}); err == nil {
		t.Fatal("expected gateway host allowlist to be required")
	}
}

func TestHotPayGatewayClientRejectsMismatchedProviderSnapshot(t *testing.T) {
	tests := []struct {
		name        string
		accountID   string
		environment string
		wantError   string
	}{
		{name: "provider account", accountID: "store_2", environment: "test", wantError: "provider account"},
		{name: "environment", accountID: "store_1", environment: "prod", wantError: "environment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"order":{"id":"ord_1","merchant_order_id":"trade_1","user_id":"7","amount_minor":1000,"currency":"CNY","provider":"waffo_pancake","provider_account_id":"` + tt.accountID + `","payment_method":"wechat_pay","environment":"` + tt.environment + `"},"attempt":{"checkout_url":"https://checkout.example/1"}}`))
			}))
			defer server.Close()

			client, err := NewHotPayGatewayClient(HotPayGatewayConfig{BaseURL: server.URL, AllowedHosts: []string{"127.0.0.1"}, AllowHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CreateOrder(context.Background(), "wallet-key", HotPayGatewayCreateOrderRequest{
				MerchantOrderID: "trade_1", UserID: "7", AmountMinor: 1000, Currency: "CNY", Provider: "waffo_pancake",
				ProviderAccountID: "store_1", PaymentMethod: "wechat_pay", Environment: "test",
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want %q mismatch", err, tt.wantError)
			}
		})
	}
}

func TestHotPayGatewayClientRequiresProviderSnapshot(t *testing.T) {
	client, err := NewHotPayGatewayClient(HotPayGatewayConfig{BaseURL: "https://pay.example.com", AllowedHosts: []string{"pay.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateOrder(context.Background(), "wallet-key", HotPayGatewayCreateOrderRequest{MerchantOrderID: "trade_1"})
	if err == nil || !strings.Contains(err.Error(), "provider account") {
		t.Fatalf("error = %v, want missing provider account error", err)
	}
}
