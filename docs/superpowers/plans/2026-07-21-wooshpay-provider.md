# WooshPay Checkout Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a first-class, upstreamable WooshPay provider for one-time CNY recharge through a hosted Checkout Session offering Alipay and UnionPay, with signed and idempotent webhook fulfillment.

**Architecture:** Add `wooshpay` as one visible payment/provider type. A small Go adapter creates hosted sessions and parses signed `payment_intent.succeeded` events; existing provider-instance selection, order snapshots, amount reconciliation, and transactional fulfillment remain authoritative. The frontend uses the existing generic `pay_url` redirect flow and adds only provider configuration, labels, and method presentation—no WooshPay browser SDK.

**Tech Stack:** Go 1.26, Gin, Ent, `shopspring/decimal`, Vue 3, TypeScript, Vitest, pnpm.

## Global Constraints

- Currency is exactly `CNY`; convert major-unit order amounts to integer fen without float arithmetic.
- Checkout methods are fixed to `alipay` and `unionpay`; do not expose AlipayHK, HKD, cards, subscriptions, refunds, or direct PaymentIntent checkout.
- Authenticate API calls with HTTP Basic using the secret key as username and an empty password.
- Send a deterministic `Idempotency-Key` derived from the immutable Sub2API `out_trade_no` when creating a Checkout Session.
- Test API base is `https://apitest.wooshpay.com`; live API base is `https://api.wooshpay.com`; reject other hosts and non-HTTPS URLs.
- Treat `payment_intent.succeeded` plus a valid `Wooshpay-Signature` as the only fulfillment signal.
- Verify the HMAC-SHA256 over `timestamp + "." + raw_body`, accept any matching `v1`, compare in constant time, and reject timestamps outside five minutes.
- Never return `secretKey` or `webhookSecret` from admin APIs, and never log either value or full webhook bodies.
- Reuse existing order idempotency and fulfillment; no new payment tables or migrations.
- Keep Aox version/update/release behavior out of this upstreamable patch.

---

### Task 1: Register the provider type and configuration contract

**Files:**
- Modify: `backend/internal/payment/types.go`
- Modify: `backend/internal/payment/provider/factory.go`
- Modify: `backend/internal/service/payment_config_providers.go`
- Modify: `backend/internal/service/payment_currency.go`
- Test: `backend/internal/service/payment_config_providers_test.go`
- Test: `backend/internal/service/payment_config_service_test.go`

**Interfaces:**
- Produces: `payment.TypeWooshPay == "wooshpay"`; factory dispatch to `provider.NewWooshPay`; sensitive config keys `secretKey` and `webhookSecret`; protected identity keys `secretKey`, `webhookSecret`, `apiBase`, and `currency`.

- [ ] **Step 1: Add failing registration and masking tests**

```go
func TestWooshPayProviderConfigFields(t *testing.T) {
    require.True(t, validProviderKeys[payment.TypeWooshPay])
    require.True(t, isSensitiveProviderConfigField(payment.TypeWooshPay, "secretKey"))
    require.True(t, isSensitiveProviderConfigField(payment.TypeWooshPay, "webhookSecret"))
    require.False(t, isSensitiveProviderConfigField(payment.TypeWooshPay, "apiBase"))
    require.Equal(t, "CNY", paymentProviderConfigCurrency(payment.TypeWooshPay, map[string]string{"currency": "CNY"}))
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `cd backend && go test ./internal/service -run 'TestWooshPayProviderConfigFields|TestValidProvider' -count=1`

Expected: compile failure because `payment.TypeWooshPay` is undefined.

- [ ] **Step 3: Add the minimal provider type wiring**

```go
const TypeWooshPay PaymentType = "wooshpay"

// GetBasePaymentType switch:
case t == TypeWooshPay:
    return TypeWooshPay

// factory switch:
case payment.TypeWooshPay:
    return NewWooshPay(instanceID, config)

// service maps:
payment.TypeWooshPay: {"secretkey": {}, "webhooksecret": {}},
payment.TypeWooshPay: {"secretkey": {}, "webhooksecret": {}, "apibase": {}, "currency": {}},

// validProviderKeys:
payment.TypeWooshPay: true,

// paymentProviderConfigCurrency:
case payment.TypeStripe, payment.TypeAirwallex, payment.TypeWooshPay:
```

- [ ] **Step 4: Run the focused tests**

Run: `cd backend && go test ./internal/service ./internal/payment -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/payment/types.go backend/internal/payment/provider/factory.go backend/internal/service/payment_config_providers.go backend/internal/service/payment_currency.go backend/internal/service/payment_config_providers_test.go backend/internal/service/payment_config_service_test.go
git commit -m "feat(payment): register wooshpay provider type"
```

### Task 2: Implement hosted Checkout Session creation and PaymentIntent lookup

**Files:**
- Create: `backend/internal/payment/provider/wooshpay.go`
- Create: `backend/internal/payment/provider/wooshpay_test.go`

**Interfaces:**
- Consumes: `payment.CreatePaymentRequest` and the `payment.Provider` interface.
- Produces: `NewWooshPay(instanceID string, config map[string]string) (*WooshPay, error)`; `CreatePayment` returning `TradeNo=payment_intent`, `IntentID=payment_intent`, `PayURL=url`, and `Currency=CNY`; `QueryOrder` mapped from WooshPay PaymentIntent status.

- [ ] **Step 1: Write failing constructor and request-contract tests**

```go
func TestWooshPayCreatePayment(t *testing.T) {
    var got wooshPayCheckoutSessionRequest
    server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        require.Equal(t, http.MethodPost, r.Method)
        require.Equal(t, "/v1/checkout/sessions", r.URL.Path)
        user, pass, ok := r.BasicAuth()
        require.True(t, ok)
        require.Equal(t, "sk_test_example", user)
        require.Empty(t, pass)
        require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
        _ = json.NewEncoder(w).Encode(map[string]any{
            "id": "cs_test_1", "url": "https://checkouttest.wooshpay.com/pay/cs_test_1",
            "payment_intent": "pi_test_1", "currency": "CNY",
        })
    }))
    defer server.Close()

    provider := newWooshPayTestProvider(t, server, "sk_test_example")
    response, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
        OrderID: "sub2_order_1", Amount: "12.34", Subject: "123 Credits",
        ReturnURL: "https://merchant.example/payment/result",
    })
    require.NoError(t, err)
    require.Equal(t, int64(1234), got.LineItems[0].PriceData.UnitAmount)
    require.Equal(t, "CNY", got.LineItems[0].PriceData.Currency)
    require.Equal(t, []string{"alipay", "unionpay"}, got.PaymentMethodTypes)
    require.Equal(t, "sub2_order_1", got.ClientReferenceID)
    require.Equal(t, "sub2_order_1", got.Metadata["order_id"])
    require.Equal(t, "sub2-wooshpay-sub2_order_1", requestHeader.Get("Idempotency-Key"))
    require.Equal(t, "pi_test_1", response.TradeNo)
    require.Equal(t, "https://checkouttest.wooshpay.com/pay/cs_test_1", response.PayURL)
}
```

Also cover missing secrets, invalid API hosts, zero/negative amounts, more than two decimal places, non-2xx with a bounded error summary, malformed JSON, missing URL, and missing payment intent.

- [ ] **Step 2: Run provider tests and verify they fail**

Run: `cd backend && go test ./internal/payment/provider -run WooshPay -count=1`

Expected: compile failure because `NewWooshPay` and request types do not exist.

- [ ] **Step 3: Implement the minimal provider client**

```go
const (
    wooshPayTestAPIBase = "https://apitest.wooshpay.com"
    wooshPayLiveAPIBase = "https://api.wooshpay.com"
    wooshPayCurrency = "CNY"
)

type WooshPay struct {
    instanceID string
    config map[string]string
    httpClient *http.Client
}

type wooshPayCheckoutSessionRequest struct {
    CancelURL string `json:"cancel_url"`
    SuccessURL string `json:"success_url"`
    Mode string `json:"mode"`
    ClientReferenceID string `json:"client_reference_id"`
    Metadata map[string]string `json:"metadata"`
    PaymentMethodTypes []string `json:"payment_method_types"`
    LineItems []wooshPayLineItem `json:"line_items"`
}

func (w *WooshPay) Name() string { return "WooshPay" }
func (w *WooshPay) ProviderKey() string { return payment.TypeWooshPay }
func (w *WooshPay) SupportedTypes() []payment.PaymentType { return []payment.PaymentType{payment.TypeWooshPay} }
func (w *WooshPay) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
    return nil, fmt.Errorf("wooshpay refunds are not supported")
}
```

Use `decimal.NewFromString(req.Amount).Shift(2)` and require the shifted value to be a positive integer before converting to `int64`. Resolve success/cancel URLs from optional config overrides, otherwise use `req.ReturnURL`. Send JSON with Basic auth, a deterministic `Idempotency-Key` based only on `req.OrderID`, and cap response/error reads at 1 MiB/512 bytes.

- [ ] **Step 4: Add and pass PaymentIntent query tests**

Test `GET /v1/payment_intents/{id}` and map `succeeded` to `payment.ProviderStatusSuccess`, pending states to `payment.ProviderStatusPending`, and terminal failures to `payment.ProviderStatusFailed`; return major-unit amount (`amount / 100`) and metadata containing `currency`, `status`, and `merchant_order_id`.

Run: `cd backend && go test ./internal/payment/provider -run WooshPay -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/payment/provider/wooshpay.go backend/internal/payment/provider/wooshpay_test.go
git commit -m "feat(payment): create wooshpay checkout sessions"
```

### Task 3: Verify and parse WooshPay webhooks

**Files:**
- Modify: `backend/internal/payment/provider/wooshpay.go`
- Modify: `backend/internal/payment/provider/wooshpay_test.go`

**Interfaces:**
- Produces: `verifyWooshPayWebhookSignature(rawBody, signatureHeader, secret string, now time.Time) error`; `VerifyNotification` returning `OrderID` from a consistent `merchant_order_id` / `metadata.order_id`, `TradeNo=payment_intent.id`, major-unit amount, and metadata `{event_id,currency,status,merchant_order_id}`.

- [ ] **Step 1: Write failing signature tests**

```go
func signWooshPayEvent(secret, timestamp, body string) string {
    mac := hmac.New(sha256.New, []byte(secret))
    _, _ = mac.Write([]byte(timestamp + "." + body))
    return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWooshPayWebhookSignature(t *testing.T) {
    body := `{"type":"payment_intent.succeeded"}`
    now := time.Unix(1_800_000_000, 0)
    ts := strconv.FormatInt(now.Unix(), 10)
    valid := signWooshPayEvent("whsec_test", ts, body)
    require.NoError(t, verifyWooshPayWebhookSignature(body, "t="+ts+",v1=bad,v1="+valid, "whsec_test", now))
    require.Error(t, verifyWooshPayWebhookSignature(body+" ", "t="+ts+",v1="+valid, "whsec_test", now))
    require.Error(t, verifyWooshPayWebhookSignature(body, "t="+ts+",v1="+valid, "whsec_test", now.Add(5*time.Minute+time.Second)))
}
```

Add table cases for missing/malformed headers, duplicate `t`, bad hex, empty secret, and future timestamps.

- [ ] **Step 2: Run the signature tests and verify failure**

Run: `cd backend && go test ./internal/payment/provider -run 'WooshPayWebhook|WooshPayNotification' -count=1`

Expected: compile failure because webhook helpers do not exist.

- [ ] **Step 3: Implement strict signature verification and event parsing**

```go
type wooshPayEvent struct {
    ID string `json:"id"`
    Type string `json:"type"`
    Data struct { Object wooshPayPaymentIntent `json:"object"` } `json:"data"`
}

func (w *WooshPay) VerifyNotification(_ context.Context, rawBody string, headers map[string]string) (*payment.PaymentNotification, error) {
    if err := verifyWooshPayWebhookSignature(rawBody, headers["wooshpay-signature"], w.config["webhookSecret"], time.Now()); err != nil {
        return nil, err
    }
    var event wooshPayEvent
    if err := json.Unmarshal([]byte(rawBody), &event); err != nil { return nil, fmt.Errorf("wooshpay parse webhook: %w", err) }
    if event.Type != "payment_intent.succeeded" { return nil, nil }
    intent := event.Data.Object
    orderID, err := resolveWooshPayOrderID(intent.MerchantOrderID, intent.Metadata["order_id"])
    if err != nil || intent.Status != "succeeded" || intent.ID == "" { return nil, fmt.Errorf("wooshpay invalid succeeded event") }
    if !strings.EqualFold(intent.Currency, wooshPayCurrency) || intent.AmountReceived <= 0 { return nil, fmt.Errorf("wooshpay invalid payment amount or currency") }
    return &payment.PaymentNotification{
        TradeNo: intent.ID, OrderID: orderID,
        Amount: decimal.NewFromInt(intent.AmountReceived).Shift(-2).InexactFloat64(),
        Status: payment.NotificationStatusSuccess, RawData: rawBody,
        Metadata: map[string]string{"event_id": event.ID, "currency": intent.Currency, "status": intent.Status, "merchant_order_id": intent.MerchantOrderID},
    }, nil
}
```

- [ ] **Step 4: Test valid, irrelevant, malformed, and mismatched events**

Run: `cd backend && go test ./internal/payment/provider -run WooshPay -count=1`

Expected: PASS for valid signed success, metadata-only and merchant-order-only identifiers, matching dual identifiers, multiple-signature rotation, ignored non-success events, invalid signature, stale timestamp, altered raw body, missing or conflicting order IDs, missing intent ID, non-succeeded status, wrong currency, and non-positive amount.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/payment/provider/wooshpay.go backend/internal/payment/provider/wooshpay_test.go
git commit -m "feat(payment): verify wooshpay webhooks"
```

### Task 4: Route callbacks and enforce order/provider reconciliation

**Files:**
- Modify: `backend/internal/handler/payment_webhook_handler.go`
- Modify: `backend/internal/server/routes/payment.go`
- Modify: `backend/internal/service/payment_order.go`
- Modify: `backend/internal/service/payment_order_provider_snapshot.go`
- Test: `backend/internal/handler/payment_webhook_handler_test.go`
- Test: `backend/internal/service/payment_order_provider_snapshot_test.go`
- Test: `backend/internal/service/payment_fulfillment_test.go`
- Test: `backend/internal/service/payment_fulfillment_order_not_found_test.go`

**Interfaces:**
- Produces: `POST /api/v1/payment/webhook/wooshpay`; raw-body order extraction from `data.object.merchant_order_id`; snapshot currency `CNY`; empty HTTP 200 acknowledgement.

- [ ] **Step 1: Write failing route/extraction/snapshot tests**

```go
func TestExtractOutTradeNoWooshPay(t *testing.T) {
    body := `{"data":{"object":{"merchant_order_id":"sub2_order_1","metadata":{"order_id":"sub2_order_1"}}}}`
    require.Equal(t, "sub2_order_1", extractOutTradeNo(body, payment.TypeWooshPay))
}

func TestWooshPaySnapshotCurrency(t *testing.T) {
    got := buildPaymentOrderProviderSnapshot(&payment.InstanceSelection{
        ProviderKey: payment.TypeWooshPay,
        Config: map[string]string{"currency": "CNY"},
    }, CreateOrderRequest{})
    require.Equal(t, "CNY", got["currency"])
}
```

- [ ] **Step 2: Run focused tests and verify failure**

Run: `cd backend && go test ./internal/handler ./internal/service -run WooshPay -count=1`

Expected: failures for missing handler route, extraction, and snapshot validation.

- [ ] **Step 3: Add handler and snapshot wiring**

```go
func (h *PaymentWebhookHandler) WooshPayWebhook(c *gin.Context) {
    h.handleNotify(c, payment.TypeWooshPay)
}

// extractOutTradeNo JSON providers:
case payment.TypeAirwallex, payment.TypeWooshPay:
    // unmarshal data.object.merchant_order_id and metadata.order_id;
    // accept either, but return empty on conflict so no instance is trusted pre-verification

// success response:
case payment.TypeStripe, payment.TypeAirwallex, payment.TypeWooshPay:
    c.String(http.StatusOK, "")

// route:
webhook.POST("/wooshpay", webhookHandler.WooshPayWebhook)
```

Add `TypeWooshPay` to snapshot currency creation and validate notification metadata currency, `status == succeeded`, and `merchant_order_id` consistency. Do not add new persistence: `confirmPayment` already validates provider key, amount tolerance, and uses conditional status transitions/fulfillment leases for duplicate delivery.

- [ ] **Step 4: Add idempotency and rejection integration tests**

Test two calls with the same signed event and two distinct event IDs carrying the same PaymentIntent; assert the balance is credited once. Test wrong amount, wrong currency, provider-key mismatch, unknown order, and conflicting merchant order; assert no credit and the handler response follows existing retry policy.

Run: `cd backend && go test ./internal/handler ./internal/service -run 'WooshPay|Duplicate.*Payment' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/handler/payment_webhook_handler.go backend/internal/server/routes/payment.go backend/internal/service/payment_order.go backend/internal/service/payment_order_provider_snapshot.go backend/internal/handler/payment_webhook_handler_test.go backend/internal/service/payment_order_provider_snapshot_test.go backend/internal/service/payment_fulfillment_test.go backend/internal/service/payment_fulfillment_order_not_found_test.go
git commit -m "feat(payment): fulfill signed wooshpay callbacks"
```

### Task 5: Add admin configuration and user-visible checkout entry

**Files:**
- Modify: `frontend/src/types/payment.ts`
- Modify: `frontend/src/components/payment/providerConfig.ts`
- Modify: `frontend/src/components/payment/paymentFlow.ts`
- Modify: `frontend/src/views/admin/SettingsView.vue`
- Modify: `frontend/src/views/user/PaymentView.vue`
- Modify: `frontend/src/components/admin/payment/AdminOrderTable.vue`
- Modify: `frontend/src/i18n/locales/en/misc.ts`
- Modify: `frontend/src/i18n/locales/zh/misc.ts`
- Modify: `frontend/src/i18n/locales/en/admin/settings.ts`
- Modify: `frontend/src/i18n/locales/zh/admin/settings.ts`
- Test: `frontend/src/components/payment/__tests__/providerConfig.spec.ts`
- Test: `frontend/src/components/payment/__tests__/paymentFlow.spec.ts`
- Test: `frontend/src/views/admin/__tests__/SettingsView.spec.ts`
- Test: `frontend/src/views/user/__tests__/PaymentView.spec.ts`

**Interfaces:**
- Produces: one visible method `wooshpay`; provider fields `secretKey`, `webhookSecret`, `apiBase`, optional `successUrl`, optional `cancelUrl`, and fixed read-only currency `CNY`; generic redirect launch using `pay_url`.

- [ ] **Step 1: Write failing provider configuration tests**

```ts
it('defines wooshpay as one hosted checkout method', () => {
  expect(PROVIDER_SUPPORTED_TYPES.wooshpay).toEqual(['wooshpay'])
  expect(PROVIDER_CALLBACK_PATHS.wooshpay.notifyUrl).toBe('/api/v1/payment/webhook/wooshpay')
  expect(PROVIDER_CONFIG_FIELDS.wooshpay.map(field => field.key)).toEqual([
    'secretKey', 'webhookSecret', 'apiBase', 'successUrl', 'cancelUrl', 'currency',
  ])
  expect(PROVIDER_CONFIG_FIELDS.wooshpay.filter(field => field.sensitive).map(field => field.key))
    .toEqual(['secretKey', 'webhookSecret'])
})
```

Add payment-flow tests proving `normalizeVisibleMethod('wooshpay') === 'wooshpay'` and a `pay_url` result becomes `redirect_waiting` without a client secret or SDK route.

- [ ] **Step 2: Run frontend tests and verify failure**

Run: `pnpm --dir frontend exec vitest run src/components/payment/__tests__/providerConfig.spec.ts src/components/payment/__tests__/paymentFlow.spec.ts src/views/admin/__tests__/SettingsView.spec.ts src/views/user/__tests__/PaymentView.spec.ts`

Expected: FAIL because WooshPay types/config/labels are absent.

- [ ] **Step 3: Add the minimal frontend contract**

```ts
export type PaymentType = 'alipay' | 'wxpay' | 'alipay_direct' | 'wxpay_direct' | 'stripe' | 'easypay' | 'airwallex' | 'wooshpay'

export const PROVIDER_SUPPORTED_TYPES = {
  // existing entries
  wooshpay: ['wooshpay'],
}

export const PROVIDER_CALLBACK_PATHS = {
  // existing entries
  wooshpay: { notifyUrl: '/api/v1/payment/webhook/wooshpay' },
}

// paymentFlow aliases/type:
wooshpay: 'wooshpay',
export type VisiblePaymentMethod = 'alipay' | 'wxpay' | 'stripe' | 'airwallex' | 'wooshpay'
```

Add WooshPay to `METHOD_ORDER`, enabled payment types, provider-key options, order filter labels, and English/Chinese labels/hints. Keep method presentation as one button labeled “WooshPay (Alipay / UnionPay)” / “WooshPay（支付宝 / 银联）”. Reuse the existing generic redirect branch; do not add a route or SDK component.

- [ ] **Step 4: Pass frontend tests, lint, and typecheck**

Run: `pnpm --dir frontend exec vitest run src/components/payment/__tests__/providerConfig.spec.ts src/components/payment/__tests__/paymentFlow.spec.ts src/views/admin/__tests__/SettingsView.spec.ts src/views/user/__tests__/PaymentView.spec.ts`

Run: `pnpm --dir frontend run lint:check`

Run: `pnpm --dir frontend run typecheck`

Expected: all commands exit 0.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/types/payment.ts frontend/src/components/payment/providerConfig.ts frontend/src/components/payment/paymentFlow.ts frontend/src/views/admin/SettingsView.vue frontend/src/views/user/PaymentView.vue frontend/src/components/admin/payment/AdminOrderTable.vue frontend/src/i18n/locales/en/misc.ts frontend/src/i18n/locales/zh/misc.ts frontend/src/i18n/locales/en/admin/settings.ts frontend/src/i18n/locales/zh/admin/settings.ts frontend/src/components/payment/__tests__/providerConfig.spec.ts frontend/src/components/payment/__tests__/paymentFlow.spec.ts frontend/src/views/admin/__tests__/SettingsView.spec.ts frontend/src/views/user/__tests__/PaymentView.spec.ts
git commit -m "feat(payment): expose wooshpay hosted checkout"
```

### Task 6: End-to-end regression verification and operator documentation

**Files:**
- Create: `docs/payment/wooshpay.md`
- Modify: `README.md` only if the repository already links individual provider guides; otherwise leave it untouched.

**Interfaces:**
- Produces: test/live configuration instructions, webhook URL/event, secret-handling warning, CNY-only behavior, and a test checklist.

- [ ] **Step 1: Write the provider guide**

Document these exact values:

```text
Provider key: wooshpay
Test API base: https://apitest.wooshpay.com
Live API base: https://api.wooshpay.com
Webhook: https://<host>/api/v1/payment/webhook/wooshpay
Enabled event: payment_intent.succeeded
Checkout methods: alipay, unionpay
Currency: CNY
```

State that redirect success is not payment proof, secrets are server-only, the smallest safe test amount should be used, and refunds/subscriptions are unsupported.

- [ ] **Step 2: Run backend verification**

Run: `cd backend && gofmt -w internal/payment/types.go internal/payment/provider/factory.go internal/payment/provider/wooshpay.go internal/payment/provider/wooshpay_test.go internal/handler/payment_webhook_handler.go internal/handler/payment_webhook_handler_test.go internal/server/routes/payment.go internal/service/payment_config_providers.go internal/service/payment_config_providers_test.go internal/service/payment_config_service_test.go internal/service/payment_currency.go internal/service/payment_order.go internal/service/payment_order_provider_snapshot.go internal/service/payment_order_provider_snapshot_test.go internal/service/payment_fulfillment_test.go internal/service/payment_fulfillment_order_not_found_test.go`

Run: `cd backend && go test ./internal/payment/... ./internal/handler/... ./internal/service/... ./internal/server/routes/... -count=1`

Expected: PASS with zero failures.

- [ ] **Step 3: Run frontend and repository verification**

Run: `pnpm --dir frontend run lint:check && pnpm --dir frontend run typecheck && pnpm --dir frontend run test:run`

Run: `make build-backend build-frontend`

Expected: all commands exit 0.

- [ ] **Step 4: Inspect the final diff for scope and secrets**

Run: `git diff --check && git diff --stat origin/main...HEAD && rg -n 'sk_(test|live)_[A-Za-z0-9]|whsec_[A-Za-z0-9]' backend frontend docs/payment/wooshpay.md`

Expected: `git diff --check` exits 0; only WooshPay/provider integration files are changed; secret scan returns no real credential values (test placeholders such as `sk_test_example` are acceptable only in tests).

- [ ] **Step 5: Commit documentation**

```bash
git add docs/payment/wooshpay.md README.md
git commit -m "docs(payment): document wooshpay setup"
```

## Plan Review Notes

- Every v1 acceptance criterion in issue #1 maps to Tasks 2–5.
- Existing conditional order transitions and fulfillment leases provide duplicate-event and concurrent-delivery idempotency; the plan tests this instead of introducing a second idempotency store.
- `client_reference_id` and Checkout metadata carry the existing `out_trade_no`; webhook parsing accepts the provider's `merchant_order_id` and/or propagated `metadata.order_id`, rejects conflicts, and fulfillment validates the resolved identifier.
- The only browser contract is `pay_url`, so no CSP changes, external scripts, new route, or JS dependency are needed.
- No database migration, refund support, subscription behavior, Aox update policy, or release workflow is included.
