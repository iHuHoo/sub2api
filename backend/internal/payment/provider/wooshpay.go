package provider

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/shopspring/decimal"
)

const (
	wooshPayTestAPIBase      = "https://apitest.wooshpay.com"
	wooshPayLiveAPIBase      = "https://api.wooshpay.com"
	wooshPayCurrency         = "CNY"
	wooshPayHTTPTimeout      = 15 * time.Second
	wooshPayMaxResponseSize  = 1 << 20
	wooshPayMaxErrorSummary  = 512
	wooshPayWebhookTolerance = 5 * time.Minute
)

type WooshPay struct {
	instanceID string
	config     map[string]string
	httpClient *http.Client
}

type wooshPayCheckoutSessionRequest struct {
	CancelURL          string             `json:"cancel_url"`
	SuccessURL         string             `json:"success_url"`
	Mode               string             `json:"mode"`
	ClientReferenceID  string             `json:"client_reference_id"`
	Metadata           map[string]string  `json:"metadata"`
	PaymentMethodTypes []string           `json:"payment_method_types"`
	LineItems          []wooshPayLineItem `json:"line_items"`
}

type wooshPayLineItem struct {
	PriceData wooshPayPriceData `json:"price_data"`
	Quantity  int64             `json:"quantity"`
}

type wooshPayPriceData struct {
	Currency    string              `json:"currency"`
	UnitAmount  int64               `json:"unit_amount"`
	ProductData wooshPayProductData `json:"product_data"`
}

type wooshPayProductData struct {
	Name string `json:"name"`
}

type wooshPayCheckoutSession struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	PaymentIntent string `json:"payment_intent"`
	Currency      string `json:"currency"`
}

type wooshPayPaymentIntent struct {
	ID              string            `json:"id"`
	Status          string            `json:"status"`
	AmountReceived  int64             `json:"amount_received"`
	Currency        string            `json:"currency"`
	MerchantOrderID string            `json:"merchant_order_id"`
	Metadata        map[string]string `json:"metadata"`
}

type wooshPayEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object wooshPayPaymentIntent `json:"object"`
	} `json:"data"`
}

func NewWooshPay(instanceID string, config map[string]string) (*WooshPay, error) {
	for _, key := range []string{"secretKey", "webhookSecret", "apiBase"} {
		if strings.TrimSpace(config[key]) == "" {
			return nil, fmt.Errorf("wooshpay config missing required key: %s", key)
		}
	}
	cfg := cloneStringMap(config)
	apiBase, err := normalizeWooshPayAPIBase(cfg["apiBase"])
	if err != nil {
		return nil, err
	}
	cfg["apiBase"] = apiBase
	currency := strings.ToUpper(strings.TrimSpace(cfg["currency"]))
	if currency != wooshPayCurrency {
		return nil, fmt.Errorf("wooshpay config currency must be %s", wooshPayCurrency)
	}
	cfg["currency"] = currency
	return &WooshPay{
		instanceID: instanceID,
		config:     cfg,
		httpClient: &http.Client{Timeout: wooshPayHTTPTimeout},
	}, nil
}

func normalizeWooshPayAPIBase(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("wooshpay apiBase must be an HTTPS URL")
	}
	if parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("wooshpay apiBase must not include credentials, port, query, or fragment")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "apitest.wooshpay.com" && host != "api.wooshpay.com" {
		return "", fmt.Errorf("wooshpay apiBase host must be apitest.wooshpay.com or api.wooshpay.com")
	}
	if parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
		return "", fmt.Errorf("wooshpay apiBase must not include a path")
	}
	parsed.Path = ""
	return parsed.String(), nil
}

func (w *WooshPay) Name() string        { return "WooshPay" }
func (w *WooshPay) ProviderKey() string { return payment.TypeWooshPay }
func (w *WooshPay) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeWooshPay}
}

func (w *WooshPay) MerchantIdentityMetadata() map[string]string {
	return map[string]string{"currency": wooshPayCurrency}
}

func (w *WooshPay) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	unitAmount, err := wooshPayMinorUnits(req.Amount)
	if err != nil {
		return nil, fmt.Errorf("wooshpay create payment: %w", err)
	}
	orderID := strings.TrimSpace(req.OrderID)
	if orderID == "" {
		return nil, fmt.Errorf("wooshpay create payment: missing order id")
	}
	successURL := strings.TrimSpace(w.config["successUrl"])
	if successURL == "" {
		successURL = strings.TrimSpace(req.ReturnURL)
	}
	cancelURL := strings.TrimSpace(w.config["cancelUrl"])
	if cancelURL == "" {
		cancelURL = strings.TrimSpace(req.ReturnURL)
	}
	payload := wooshPayCheckoutSessionRequest{
		CancelURL:          cancelURL,
		SuccessURL:         successURL,
		Mode:               "payment",
		ClientReferenceID:  orderID,
		Metadata:           map[string]string{"order_id": orderID},
		PaymentMethodTypes: []string{"alipay", "unionpay"},
		LineItems: []wooshPayLineItem{{
			PriceData: wooshPayPriceData{
				Currency:    wooshPayCurrency,
				UnitAmount:  unitAmount,
				ProductData: wooshPayProductData{Name: strings.TrimSpace(req.Subject)},
			},
			Quantity: 1,
		}},
	}
	var session wooshPayCheckoutSession
	if err := w.doJSON(ctx, http.MethodPost, "/v1/checkout/sessions", "sub2-wooshpay-"+orderID, payload, &session); err != nil {
		return nil, fmt.Errorf("wooshpay create payment: %w", err)
	}
	if strings.TrimSpace(session.URL) == "" {
		return nil, fmt.Errorf("wooshpay create payment: missing checkout URL")
	}
	if strings.TrimSpace(session.PaymentIntent) == "" {
		return nil, fmt.Errorf("wooshpay create payment: missing payment intent")
	}
	return &payment.CreatePaymentResponse{
		TradeNo:  session.PaymentIntent,
		IntentID: session.PaymentIntent,
		PayURL:   session.URL,
		Currency: wooshPayCurrency,
	}, nil
}

func (w *WooshPay) QueryOrder(ctx context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	intentID := strings.TrimSpace(tradeNo)
	if intentID == "" {
		return nil, fmt.Errorf("wooshpay query order: missing payment intent id")
	}
	var intent wooshPayPaymentIntent
	if err := w.doJSON(ctx, http.MethodGet, "/v1/payment_intents/"+url.PathEscape(intentID), "", nil, &intent); err != nil {
		return nil, fmt.Errorf("wooshpay query order: %w", err)
	}
	if strings.TrimSpace(intent.ID) == "" {
		return nil, fmt.Errorf("wooshpay query order: missing payment intent id in response")
	}
	status := strings.ToLower(strings.TrimSpace(intent.Status))
	return &payment.QueryOrderResponse{
		TradeNo: intent.ID,
		Status:  wooshPayProviderStatus(status),
		Amount:  decimal.NewFromInt(intent.AmountReceived).Shift(-2).InexactFloat64(),
		Metadata: map[string]string{
			"currency":          strings.ToUpper(strings.TrimSpace(intent.Currency)),
			"status":            status,
			"merchant_order_id": strings.TrimSpace(intent.MerchantOrderID),
		},
	}, nil
}

func (w *WooshPay) VerifyNotification(_ context.Context, rawBody string, headers map[string]string) (*payment.PaymentNotification, error) {
	signatureHeader := wooshPayHeaderValue(headers, "wooshpay-signature")
	if err := verifyWooshPayWebhookSignature(rawBody, signatureHeader, w.config["webhookSecret"], time.Now()); err != nil {
		return nil, err
	}
	var event wooshPayEvent
	if err := json.Unmarshal([]byte(rawBody), &event); err != nil {
		return nil, fmt.Errorf("wooshpay parse webhook: %w", err)
	}
	if event.Type != "payment_intent.succeeded" {
		return nil, nil
	}
	intent := event.Data.Object
	orderID, err := resolveWooshPayOrderID(intent.MerchantOrderID, intent.Metadata["order_id"])
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(intent.ID) == "" {
		return nil, fmt.Errorf("wooshpay succeeded webhook missing payment intent id")
	}
	if strings.ToLower(strings.TrimSpace(intent.Status)) != "succeeded" {
		return nil, fmt.Errorf("wooshpay succeeded webhook has non-succeeded status: %s", intent.Status)
	}
	if !strings.EqualFold(strings.TrimSpace(intent.Currency), wooshPayCurrency) {
		return nil, fmt.Errorf("wooshpay succeeded webhook currency must be %s", wooshPayCurrency)
	}
	if intent.AmountReceived <= 0 {
		return nil, fmt.Errorf("wooshpay succeeded webhook has invalid amount_received")
	}
	return &payment.PaymentNotification{
		TradeNo: intent.ID,
		OrderID: orderID,
		Amount:  decimal.NewFromInt(intent.AmountReceived).Shift(-2).InexactFloat64(),
		Status:  payment.NotificationStatusSuccess,
		RawData: rawBody,
		Metadata: map[string]string{
			"event_id":          strings.TrimSpace(event.ID),
			"currency":          wooshPayCurrency,
			"status":            "succeeded",
			"merchant_order_id": orderID,
		},
	}, nil
}

func (w *WooshPay) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	return nil, fmt.Errorf("wooshpay refunds are not supported")
}

func (w *WooshPay) doJSON(ctx context.Context, method, path, idempotencyKey string, payload any, out any) error {
	var bodyReader io.Reader
	if payload != nil {
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, w.config["apiBase"]+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(w.config["secretKey"], "")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	client := w.httpClient
	if client == nil {
		client = &http.Client{Timeout: wooshPayHTTPTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, wooshPayMaxResponseSize))
	if err != nil {
		return err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, summarizeWooshPayResponse(body))
	}
	if out == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	return nil
}

func wooshPayMinorUnits(raw string) (int64, error) {
	amount, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil || amount.LessThanOrEqual(decimal.Zero) {
		return 0, fmt.Errorf("invalid amount %q", raw)
	}
	shifted := amount.Shift(2)
	if !shifted.Equal(shifted.Truncate(0)) || !shifted.BigInt().IsInt64() {
		return 0, fmt.Errorf("amount must be positive with at most two decimal places")
	}
	return shifted.IntPart(), nil
}

func wooshPayProviderStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded":
		return payment.ProviderStatusSuccess
	case "canceled", "cancelled", "failed":
		return payment.ProviderStatusFailed
	default:
		return payment.ProviderStatusPending
	}
}

func summarizeWooshPayResponse(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > wooshPayMaxErrorSummary {
		trimmed = trimmed[:wooshPayMaxErrorSummary]
	}
	if len(trimmed) == 0 {
		return http.StatusText(http.StatusBadGateway)
	}
	return string(trimmed)
}

func verifyWooshPayWebhookSignature(rawBody, signatureHeader, secret string, now time.Time) error {
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("wooshpay webhookSecret not configured")
	}
	var timestampRaw string
	var signatures [][]byte
	for _, part := range strings.Split(signatureHeader, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		switch strings.TrimSpace(key) {
		case "t":
			if timestampRaw != "" {
				return fmt.Errorf("wooshpay signature has duplicate timestamp")
			}
			timestampRaw = strings.TrimSpace(value)
		case "v1":
			decoded, err := hex.DecodeString(strings.TrimSpace(value))
			if err == nil && len(decoded) == sha256.Size {
				signatures = append(signatures, decoded)
			}
		}
	}
	if timestampRaw == "" || len(signatures) == 0 {
		return fmt.Errorf("wooshpay signature missing valid t or v1 value")
	}
	timestampUnix, err := strconv.ParseInt(timestampRaw, 10, 64)
	if err != nil {
		return fmt.Errorf("wooshpay signature timestamp is invalid")
	}
	age := now.Sub(time.Unix(timestampUnix, 0))
	if age < -wooshPayWebhookTolerance || age > wooshPayWebhookTolerance {
		return fmt.Errorf("wooshpay signature timestamp is outside tolerance")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestampRaw + "." + rawBody))
	expected := mac.Sum(nil)
	for _, signature := range signatures {
		if hmac.Equal(signature, expected) {
			return nil
		}
	}
	return fmt.Errorf("wooshpay signature mismatch")
}

func resolveWooshPayOrderID(merchantOrderID, metadataOrderID string) (string, error) {
	merchantOrderID = strings.TrimSpace(merchantOrderID)
	metadataOrderID = strings.TrimSpace(metadataOrderID)
	if merchantOrderID == "" && metadataOrderID == "" {
		return "", fmt.Errorf("wooshpay webhook missing merchant order id")
	}
	if merchantOrderID != "" && metadataOrderID != "" && merchantOrderID != metadataOrderID {
		return "", fmt.Errorf("wooshpay webhook has conflicting merchant order ids")
	}
	if merchantOrderID != "" {
		return merchantOrderID, nil
	}
	return metadataOrderID, nil
}

func wooshPayHeaderValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return value
		}
	}
	return ""
}
