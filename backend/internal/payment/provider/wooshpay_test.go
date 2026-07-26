package provider

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestNewWooshPay(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"secretKey":     "sk_test_example",
		"webhookSecret": "whsec_example",
		"apiBase":       wooshPayTestAPIBase,
		"currency":      "cny",
	}
	provider, err := NewWooshPay("instance-1", valid)
	require.NoError(t, err)
	require.Equal(t, "CNY", provider.config["currency"])
	require.Equal(t, payment.TypeWooshPay, provider.ProviderKey())
	require.Equal(t, []payment.PaymentType{payment.TypeWooshPay}, provider.SupportedTypes())

	tests := []struct {
		name   string
		mutate func(map[string]string)
	}{
		{"missing secret key", func(cfg map[string]string) { delete(cfg, "secretKey") }},
		{"missing webhook secret", func(cfg map[string]string) { delete(cfg, "webhookSecret") }},
		{"missing api base", func(cfg map[string]string) { delete(cfg, "apiBase") }},
		{"http api base", func(cfg map[string]string) { cfg["apiBase"] = "http://apitest.wooshpay.com" }},
		{"untrusted api host", func(cfg map[string]string) { cfg["apiBase"] = "https://example.com" }},
		{"api base with credentials", func(cfg map[string]string) { cfg["apiBase"] = "https://user@apitest.wooshpay.com" }},
		{"unsupported currency", func(cfg map[string]string) { cfg["currency"] = "USD" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cloneStringMap(valid)
			tc.mutate(cfg)
			_, err := NewWooshPay("instance-1", cfg)
			require.Error(t, err)
		})
	}
}

func TestWooshPayCreatePayment(t *testing.T) {
	var got wooshPayCheckoutSessionRequest
	var requestHeader http.Header
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/checkout/sessions", r.URL.Path)
		user, pass, ok := r.BasicAuth()
		require.True(t, ok)
		require.Equal(t, "sk_test_example", user)
		require.Empty(t, pass)
		requestHeader = r.Header.Clone()
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":             "cs_test_1",
			"url":            "https://checkouttest.wooshpay.com/pay/cs_test_1",
			"payment_intent": "pi_test_1",
			"currency":       "CNY",
		})
	}))
	defer server.Close()

	provider := newWooshPayTestProvider(server, "sk_test_example")
	response, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID: "sub2_order_1", Amount: "12.34", Subject: "123 Credits",
		ReturnURL: "https://merchant.example/payment/result",
	})
	require.NoError(t, err)
	require.Equal(t, int64(1234), got.LineItems[0].PriceData.UnitAmount)
	require.Equal(t, "CNY", got.LineItems[0].PriceData.Currency)
	require.Equal(t, "123 Credits", got.LineItems[0].PriceData.ProductData.Name)
	require.Equal(t, int64(1), got.LineItems[0].Quantity)
	require.Equal(t, []string{"alipay", "unionpay"}, got.PaymentMethodTypes)
	require.Equal(t, "sub2_order_1", got.ClientReferenceID)
	require.Equal(t, "sub2_order_1", got.Metadata["order_id"])
	require.Equal(t, "https://merchant.example/payment/result", got.SuccessURL)
	require.Equal(t, "https://merchant.example/payment/result", got.CancelURL)
	require.Equal(t, "sub2-wooshpay-sub2_order_1", requestHeader.Get("Idempotency-Key"))
	require.Equal(t, "pi_test_1", response.TradeNo)
	require.Equal(t, "pi_test_1", response.IntentID)
	require.Equal(t, "https://checkouttest.wooshpay.com/pay/cs_test_1", response.PayURL)
	require.Equal(t, "CNY", response.Currency)
}

func TestWooshPayCreatePaymentRejectsInvalidAmount(t *testing.T) {
	t.Parallel()
	provider := &WooshPay{config: map[string]string{"secretKey": "sk_test", "apiBase": wooshPayTestAPIBase}}
	for _, amount := range []string{"", "bad", "0", "-1", "1.001"} {
		_, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: "order-1", Amount: amount})
		require.Error(t, err, amount)
	}
}

func TestWooshPayCreatePaymentResponseErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		want       string
		maxErrSize int
	}{
		{"http error is bounded", http.StatusBadRequest, strings.Repeat("x", 2048), "HTTP 400", 600},
		{"malformed json", http.StatusOK, `{`, "parse response", 0},
		{"missing url", http.StatusOK, `{"payment_intent":"pi_1"}`, "missing checkout URL", 0},
		{"missing intent", http.StatusOK, `{"url":"https://checkout.example/session"}`, "missing payment intent", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			provider := newWooshPayTestProvider(server, "sk_test")
			_, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: "order-1", Amount: "1.00"})
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
			if tc.maxErrSize > 0 {
				require.Less(t, len(err.Error()), tc.maxErrSize)
			}
		})
	}
}

func TestWooshPayQueryOrder(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"succeeded", payment.ProviderStatusPaid},
		{"requires_payment_method", payment.ProviderStatusPending},
		{"processing", payment.ProviderStatusPending},
		{"canceled", payment.ProviderStatusFailed},
		{"failed", payment.ProviderStatusFailed},
	}
	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "/v1/payment_intents/pi_test_1", r.URL.Path)
				user, pass, ok := r.BasicAuth()
				require.True(t, ok)
				require.Equal(t, "sk_test", user)
				require.Empty(t, pass)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "pi_test_1", "status": tc.status, "amount_received": 1234,
					"currency": "CNY", "merchant_order_id": "order-1",
				})
			}))
			defer server.Close()
			provider := newWooshPayTestProvider(server, "sk_test")
			response, err := provider.QueryOrder(context.Background(), "pi_test_1")
			require.NoError(t, err)
			require.Equal(t, "pi_test_1", response.TradeNo)
			require.Equal(t, tc.want, response.Status)
			require.Equal(t, 12.34, response.Amount)
			require.Equal(t, "CNY", response.Metadata["currency"])
			require.Equal(t, tc.status, response.Metadata["status"])
			require.Equal(t, "order-1", response.Metadata["merchant_order_id"])
		})
	}
}

func TestVerifyWooshPayWebhookSignature(t *testing.T) {
	t.Parallel()
	body := `{"type":"payment_intent.succeeded"}`
	now := time.Unix(1_800_000_000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	valid := signWooshPayEvent("whsec_test", ts, body)
	require.NoError(t, verifyWooshPayWebhookSignature(body, "t="+ts+",v1=bad,v1="+valid, "whsec_test", now))

	tests := []struct {
		name      string
		body      string
		header    string
		secret    string
		checkTime time.Time
	}{
		{"altered body", body + " ", "t=" + ts + ",v1=" + valid, "whsec_test", now},
		{"stale timestamp", body, "t=" + ts + ",v1=" + valid, "whsec_test", now.Add(5*time.Minute + time.Second)},
		{"future timestamp", body, "t=" + strconv.FormatInt(now.Add(5*time.Minute+time.Second).Unix(), 10) + ",v1=" + valid, "whsec_test", now},
		{"missing header", body, "", "whsec_test", now},
		{"missing timestamp", body, "v1=" + valid, "whsec_test", now},
		{"duplicate timestamp", body, "t=" + ts + ",t=" + ts + ",v1=" + valid, "whsec_test", now},
		{"malformed timestamp", body, "t=abc,v1=" + valid, "whsec_test", now},
		{"missing signature", body, "t=" + ts, "whsec_test", now},
		{"bad hex", body, "t=" + ts + ",v1=not-hex", "whsec_test", now},
		{"empty secret", body, "t=" + ts + ",v1=" + valid, "", now},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, verifyWooshPayWebhookSignature(tc.body, tc.header, tc.secret, tc.checkTime))
		})
	}
}

func TestVerifyWooshPayWebhookSignatureAcceptsMillisecondTimestamp(t *testing.T) {
	t.Parallel()
	body := `{"type":"payment_intent.succeeded"}`
	now := time.Unix(1_800_000_000, 123_000_000)
	timestamp := strconv.FormatInt(now.UnixMilli(), 10)
	signature := signWooshPayEvent("whsec_test", timestamp, body)

	require.NoError(t, verifyWooshPayWebhookSignature(
		body,
		"t="+timestamp+",v1="+signature,
		"whsec_test",
		now,
	))
}

func TestVerifyWooshPayWebhookSignatureAcceptsDelayedRetry(t *testing.T) {
	t.Parallel()
	body := `{"created":1800000000000,"type":"payment_intent.succeeded"}`
	timestamp := "1800000000159"
	signature := signWooshPayEvent("whsec_test", timestamp, body)
	now := time.UnixMilli(1_800_000_600_000)

	require.NoError(t, verifyWooshPayWebhookSignature(
		body,
		"t="+timestamp+",v1="+signature,
		"whsec_test",
		now,
	))
}

func TestWooshPayNotification(t *testing.T) {
	t.Parallel()
	now := time.Now()
	provider := &WooshPay{config: map[string]string{"webhookSecret": "whsec_test"}}

	tests := []struct {
		name            string
		eventType       string
		intentID        string
		status          string
		currency        string
		amount          int64
		merchantOrderID string
		metadataOrderID string
		wantNil         bool
		wantErr         bool
	}{
		{"both matching identifiers", "payment_intent.succeeded", "pi_1", "succeeded", "CNY", 1234, "order-1", "order-1", false, false},
		{"merchant identifier only", "payment_intent.succeeded", "pi_1", "succeeded", "cny", 1234, "order-1", "", false, false},
		{"metadata identifier only", "payment_intent.succeeded", "pi_1", "succeeded", "CNY", 1234, "", "order-1", false, false},
		{"irrelevant event", "payment_intent.processing", "pi_1", "processing", "CNY", 1234, "order-1", "order-1", true, false},
		{"conflicting identifiers", "payment_intent.succeeded", "pi_1", "succeeded", "CNY", 1234, "order-1", "order-2", false, true},
		{"missing identifiers", "payment_intent.succeeded", "pi_1", "succeeded", "CNY", 1234, "", "", false, true},
		{"missing intent", "payment_intent.succeeded", "", "succeeded", "CNY", 1234, "order-1", "order-1", false, true},
		{"wrong status", "payment_intent.succeeded", "pi_1", "processing", "CNY", 1234, "order-1", "order-1", false, true},
		{"wrong currency", "payment_intent.succeeded", "pi_1", "succeeded", "USD", 1234, "order-1", "order-1", false, true},
		{"zero amount", "payment_intent.succeeded", "pi_1", "succeeded", "CNY", 0, "order-1", "order-1", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := marshalWooshPayEvent(t, tc.eventType, tc.intentID, tc.status, tc.currency, tc.amount, tc.merchantOrderID, tc.metadataOrderID)
			ts := strconv.FormatInt(now.Unix(), 10)
			header := "t=" + ts + ",v1=" + signWooshPayEvent("whsec_test", ts, body)
			notification, err := provider.VerifyNotification(context.Background(), body, map[string]string{"wooshpay-signature": header})
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.wantNil {
				require.Nil(t, notification)
				return
			}
			require.Equal(t, "pi_1", notification.TradeNo)
			require.Equal(t, "order-1", notification.OrderID)
			require.Equal(t, 12.34, notification.Amount)
			require.Equal(t, payment.NotificationStatusSuccess, notification.Status)
			require.Equal(t, "evt_1", notification.Metadata["event_id"])
			require.Equal(t, "CNY", notification.Metadata["currency"])
			require.Equal(t, "succeeded", notification.Metadata["status"])
		})
	}
}

func TestWooshPayNotificationRejectsBadSignatureAndJSON(t *testing.T) {
	provider := &WooshPay{config: map[string]string{"webhookSecret": "whsec_test"}}
	_, err := provider.VerifyNotification(context.Background(), `{}`, map[string]string{"wooshpay-signature": "t=1,v1=bad"})
	require.Error(t, err)

	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	body := `{`
	header := "t=" + ts + ",v1=" + signWooshPayEvent("whsec_test", ts, body)
	_, err = provider.VerifyNotification(context.Background(), body, map[string]string{"Wooshpay-Signature": header})
	require.Error(t, err)
}

func signWooshPayEvent(secret, timestamp, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "." + body))
	return hex.EncodeToString(mac.Sum(nil))
}

func marshalWooshPayEvent(t *testing.T, eventType, intentID, status, currency string, amount int64, merchantOrderID, metadataOrderID string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id":   "evt_1",
		"type": eventType,
		"data": map[string]any{"object": map[string]any{
			"id": intentID, "status": status, "currency": currency,
			"amount_received": amount, "merchant_order_id": merchantOrderID,
			"metadata": map[string]string{"order_id": metadataOrderID},
		}},
	})
	require.NoError(t, err)
	return string(body)
}

func newWooshPayTestProvider(server *httptest.Server, secretKey string) *WooshPay {
	return &WooshPay{
		instanceID: "instance-1",
		config: map[string]string{
			"secretKey": secretKey,
			"apiBase":   server.URL,
			"currency":  "CNY",
		},
		httpClient: server.Client(),
	}
}
