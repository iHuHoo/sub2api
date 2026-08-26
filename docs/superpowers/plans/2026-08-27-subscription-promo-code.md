# Subscription Promo Code Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add globally single-use percentage promo codes to every subscription purchase and renewal, with reservation, expiry release, immutable pricing snapshots, and discounted affiliate rebates.

**Architecture:** Extend the existing promo-code tables and `PromoService`, inject that service into the existing `PaymentService`, and attach reservation transitions to the current order transaction, payment-success transition, and expiry sweep. Extend the existing payment/admin APIs and Vue views; do not add a second promo subsystem, dependency, or background worker.

**Tech Stack:** Go 1.27, Ent/PostgreSQL, shopspring/decimal, Gin, Vue 3, TypeScript, Pinia, Vitest.

**Spec:** `docs/superpowers/specs/2026-08-26-subscription-promo-code-design.md`

## Global Constraints

- Subscription promo codes are percentage-only, apply to every plan and renewal, and have exactly one successful global use.
- One order accepts at most one promo code; balance orders reject it; zero/minor-unit-invalid payable amounts are rejected.
- Pending orders reserve the code; cancelled/expired unpaid orders retain it for the existing five-minute recovery window, then release it.
- Successful payment consumes the code permanently; refunds never restore it.
- `payment_orders.amount` is the discounted subscription value and is the affiliate rebate base; `pay_amount` is the converted/fee-inclusive gateway amount.
- Existing registration promo codes and non-promo payment behavior remain backward compatible.
- Reuse the existing order expiry service and payment audit log; add no dependency and no new worker.
- Every production behavior is implemented test-first and observed failing before implementation.

---

### Task 1: Persist promo purpose, reservation state, and order snapshots

**Files:**
- Create: `backend/migrations/231_subscription_promo_codes.sql`
- Create: `backend/migrations/subscription_promo_codes_migration_test.go`
- Modify: `backend/ent/schema/promo_code.go`
- Modify: `backend/ent/schema/promo_code_usage.go`
- Modify: `backend/ent/schema/payment_order.go`
- Regenerate: `backend/ent/**`

**Interfaces:**
- Produces promo fields `purpose string` and `discount_rate *float64`.
- Produces usage fields `payment_order_id *int64`, `usage_type string`, `status string`, `discount_amount float64`, and reservation/consumption/release timestamps.
- Produces payment-order snapshots `promo_code_id *int64`, `promo_code *string`, `original_amount *float64`, `discount_rate *float64`, and `discount_amount float64`.

- [ ] **Step 1: Write the failing migration contract test**

```go
func TestSubscriptionPromoCodeMigrationPreservesRegistrationCodes(t *testing.T) {
	content, err := FS.ReadFile("231_subscription_promo_codes.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "purpose VARCHAR(32) NOT NULL DEFAULT 'registration_bonus'")
	require.Contains(t, sql, "discount_rate DECIMAL(10,4)")
	require.Contains(t, sql, "payment_order_id BIGINT")
	require.Contains(t, sql, "status VARCHAR(20) NOT NULL DEFAULT 'consumed'")
	require.Contains(t, sql, "WHERE usage_type = 'subscription_discount' AND status = 'reserved'")
	require.Contains(t, sql, "WHERE usage_type = 'subscription_discount' AND status = 'consumed'")
	require.Contains(t, sql, "original_amount DECIMAL(20,2)")
}
```

- [ ] **Step 2: Run it and verify RED**

Run: `go test ./migrations -run TestSubscriptionPromoCodeMigrationPreservesRegistrationCodes -count=1`

Expected: FAIL because `231_subscription_promo_codes.sql` does not exist.

- [ ] **Step 3: Add the idempotent SQL migration**

Add columns with `IF NOT EXISTS`, backfill existing rows as registration/consumed, remove the unconditional `(promo_code_id,user_id)` uniqueness rule, and add these partial indexes:

```sql
CREATE UNIQUE INDEX IF NOT EXISTS uq_promo_registration_user
ON promo_code_usages (promo_code_id, user_id)
WHERE usage_type = 'registration_bonus';

CREATE UNIQUE INDEX IF NOT EXISTS uq_promo_subscription_reserved
ON promo_code_usages (promo_code_id)
WHERE usage_type = 'subscription_discount' AND status = 'reserved';

CREATE UNIQUE INDEX IF NOT EXISTS uq_promo_subscription_consumed
ON promo_code_usages (promo_code_id)
WHERE usage_type = 'subscription_discount' AND status = 'consumed';

CREATE UNIQUE INDEX IF NOT EXISTS uq_promo_usage_payment_order
ON promo_code_usages (payment_order_id)
WHERE payment_order_id IS NOT NULL;
```

Add check constraints for valid promo purpose, usage type/status, and subscription `discount_rate > 0 AND discount_rate < 1`. Make payment-order promo references nullable with `ON DELETE SET NULL`; preserve immutable code/rate/amount snapshots when the promo row is deleted.

- [ ] **Step 4: Update Ent schemas and regenerate**

Use decimal-backed float fields consistently with existing money fields and `entsql.IndexWhere` for partial indexes. Run:

```bash
go generate ./ent
```

- [ ] **Step 5: Verify GREEN and schema generation**

Run:

```bash
go test ./migrations ./ent/schema ./ent/migrate -count=1
git diff --check
```

- [ ] **Step 6: Commit**

```bash
git add backend/migrations backend/ent
git commit -m "feat(payment): add subscription promo persistence"
```

---

### Task 2: Extend PromoService without breaking registration bonuses

**Files:**
- Modify: `backend/internal/domain/constants.go`
- Modify: `backend/internal/service/domain_constants.go`
- Modify: `backend/internal/service/promo_code.go`
- Modify: `backend/internal/service/promo_service.go`
- Modify: `backend/internal/service/promo_code_repository.go`
- Modify: `backend/internal/repository/promo_code_repo.go`
- Create: `backend/internal/service/promo_service_subscription_test.go`
- Modify: `backend/internal/handler/admin/promo_handler.go`
- Modify: `backend/internal/handler/dto/types.go`
- Modify: `backend/internal/handler/dto/mappers.go`

**Interfaces:**
- Produces constants `PromoCodePurposeRegistrationBonus`, `PromoCodePurposeSubscriptionDiscount`, `PromoUsageStatusReserved`, `PromoUsageStatusConsumed`, and `PromoUsageStatusReleased`.
- Produces `SubscriptionPromoPreview` with normalized code, rate, original, discount, and discounted amounts.
- Produces `ValidateSubscriptionPromo(ctx, code string, originalAmount float64) (*SubscriptionPromoPreview, error)`.
- Preserves `ValidatePromoCode` and `ApplyPromoCode` as registration-only behavior.
- Test utility `newPromoServiceTestHarness(t, promo)` creates the repository's existing `modernc.org/sqlite` + Ent test client, saves the supplied promo row, and returns a real `PromoService`; `ptr(0.8)` is a local generic pointer helper.

- [ ] **Step 1: Write failing service tests for purpose and percentage validation**

```go
func TestValidateSubscriptionPromoCalculatesPayablePercentage(t *testing.T) {
	svc := newPromoServiceTestHarness(t, PromoCode{Code: "SAVE20", Purpose: PromoCodePurposeSubscriptionDiscount, DiscountRate: ptr(0.8), MaxUses: 1, Status: PromoCodeStatusActive})
	got, err := svc.ValidateSubscriptionPromo(context.Background(), " save20 ", 149)
	require.NoError(t, err)
	require.Equal(t, "SAVE20", got.Code)
	require.InDelta(t, 29.80, got.DiscountAmount, 0.000001)
	require.InDelta(t, 119.20, got.DiscountedAmount, 0.000001)
}

func TestRegistrationValidatorRejectsSubscriptionPromo(t *testing.T) {
	svc := newPromoServiceTestHarness(t, PromoCode{Code: "SAVE20", Purpose: PromoCodePurposeSubscriptionDiscount, DiscountRate: ptr(0.8), MaxUses: 1, Status: PromoCodeStatusActive})
	_, err := svc.ValidatePromoCode(context.Background(), "SAVE20")
	require.ErrorIs(t, err, ErrPromoCodeWrongPurpose)
}
```

Add table rows with literal expected reasons: rates `0` and `1` return `PROMO_CODE_INVALID_DISCOUNT`; disabled returns `PROMO_CODE_DISABLED`; past expiry returns `PROMO_CODE_EXPIRED`; reserved returns `PROMO_CODE_RESERVED`; consumed returns `PROMO_CODE_CONSUMED`; released produces the same `119.20` preview as an unused code.

- [ ] **Step 2: Run and verify RED**

Run: `go test ./internal/service -run 'Test(ValidateSubscriptionPromo|RegistrationValidator)' -count=1`

Expected: FAIL because purpose/rate fields and subscription validation do not exist.

- [ ] **Step 3: Implement the minimum domain and repository mapping**

Normalize codes once with `strings.ToUpper(strings.TrimSpace(code))`. Keep registration usage creation setting `usage_type=registration_bonus`, `status=consumed`, and `consumed_at/used_at` so old API behavior remains unchanged. Validate create/update inputs so subscription codes force `bonus_amount=0` and `max_uses=1`, while registration codes require no discount rate.

- [ ] **Step 4: Add admin request/response fields and validation tests**

Extend create/update DTOs with `purpose` and nullable `discount_rate`; extend promo/usage response DTOs with purpose, rate, usage state, order ID, discount amount, and timestamps. A subscription code with `discount_rate=0.8` succeeds; `1`, `0`, or configurable `max_uses != 1` is rejected.

- [ ] **Step 5: Verify GREEN and registration regressions**

Run:

```bash
go test ./internal/service ./internal/repository ./internal/handler/admin -run Promo -count=1
git diff --check
```

- [ ] **Step 6: Commit**

```bash
git add backend/internal/domain backend/internal/service backend/internal/repository backend/internal/handler
git commit -m "feat(payment): support subscription promo definitions"
```

---

### Task 3: Preview, price, and reserve a code with the subscription order

**Files:**
- Create: `backend/internal/service/payment_promo.go`
- Create: `backend/internal/service/payment_promo_test.go`
- Modify: `backend/internal/service/payment_service.go`
- Modify: `backend/internal/service/payment_order.go`
- Modify: `backend/internal/service/wire.go`
- Regenerate: `backend/cmd/server/wire_gen.go`
- Modify: `backend/internal/handler/payment_handler.go`
- Modify: `backend/internal/server/routes/payment.go`
- Modify: `backend/internal/handler/payment_handler_resume_test.go`

**Interfaces:**
- `CreateOrderRequest` gains `PromoCode string`.
- `CreateOrderResponse` gains optional `OriginalAmount`, `DiscountRate`, and `DiscountAmount` JSON fields.
- Produces `PreviewSubscriptionPromo(ctx, userID, planID, code) (*SubscriptionPromoPreview, error)`.
- Produces authenticated `POST /api/v1/payment/promo/preview` with `{code, plan_id}`.
- `PaymentService` receives the existing `*PromoService`; no new repository abstraction is added.
- Test utility `newPaymentPromoHarness(t, planPrice, discountRate)` uses the existing SQLite Ent/payment-provider fakes, creates a real user/group/plan/promo, and returns accessors for the persisted order and usage.

- [ ] **Step 1: Write failing pricing and reservation tests**

```go
func TestCreateSubscriptionOrderReservesPromoAndSnapshotsDiscount(t *testing.T) {
	h := newPaymentPromoHarness(t, 149, 0.8)
	order, err := h.service.CreateOrder(context.Background(), CreateOrderRequest{
		UserID: 7, OrderType: payment.OrderTypeSubscription, PlanID: 3,
		PaymentType: payment.TypeWooshPay, PromoCode: "SAVE20",
	})
	require.NoError(t, err)
	persisted := h.order(t, order.OrderID)
	require.InDelta(t, 149, *persisted.OriginalAmount, 0.000001)
	require.InDelta(t, 119.20, persisted.Amount, 0.000001)
	require.InDelta(t, 29.80, persisted.DiscountAmount, 0.000001)
	require.Equal(t, "SAVE20", *persisted.PromoCode)
	require.Equal(t, PromoUsageStatusReserved, h.usage(t).Status)
}
```

Add named cases with literal outcomes: `TestBalanceOrderRejectsSubscriptionPromo` expects `PROMO_CODE_SUBSCRIPTION_ONLY`; `TestSubscriptionPromoRejectsZeroMinorUnit` expects `PROMO_CODE_NOT_PAYABLE`; `TestConcurrentSubscriptionPromoReservationHasOneWinner` expects one `reserved` row; and `TestSubscriptionPromoConvertsAndFeesDiscountedAmount` expects conversion/fee input `119.20`, never `149`.

- [ ] **Step 2: Run and verify RED**

Run: `go test ./internal/service -run 'Test(CreateSubscriptionOrder|PreviewSubscriptionPromo)' -count=1`

Expected: FAIL because promo pricing/reservation is absent.

- [ ] **Step 3: Implement preview and authoritative pricing**

Use `shopspring/decimal` to calculate:

```go
discounted := decimal.NewFromFloat(original).Mul(decimal.NewFromFloat(rate)).Round(2)
discount := decimal.NewFromFloat(original).Sub(discounted).Round(2)
```

Preview reads only. Create-order validation repeats under a promo row `FOR UPDATE` lock. Store discounted USD subscription value in `amount`, calculate gateway conversion and fee from it, and reject any final amount that cannot produce a positive provider minor unit.

- [ ] **Step 4: Commit order and reservation atomically**

Inside `createOrderInTx`, write all payment-order snapshots and one `reserved` usage row before committing. Partial unique indexes are the final concurrency guard; map their conflicts to `PROMO_CODE_RESERVED` without revealing the other order/user.

- [ ] **Step 5: Add route/handler contract and tests**

The handler passes only `code`, authenticated user ID, and plan ID. The response uses backend amounts. Add `promo_code` to order creation and reject it when `order_type != subscription`.

- [ ] **Step 6: Verify GREEN**

Run:

```bash
go generate ./cmd/server
go test ./internal/service ./internal/handler ./internal/server/routes -run 'Promo|CreateOrder' -count=1
git diff --check
```

- [ ] **Step 7: Commit**

```bash
git add backend/internal backend/cmd/server/wire_gen.go
git commit -m "feat(payment): reserve promo codes on subscription orders"
```

---

### Task 4: Consume, release, recover, refund, and rebate safely

**Files:**
- Modify: `backend/internal/service/payment_promo.go`
- Modify: `backend/internal/service/payment_fulfillment.go`
- Modify: `backend/internal/service/payment_order_lifecycle.go`
- Modify: `backend/internal/service/payment_order_expiry_service.go`
- Create: `backend/internal/service/payment_promo_lifecycle_test.go`
- Modify: `backend/internal/service/payment_fulfillment_test.go`
- Modify: `backend/internal/service/payment_order_lifecycle_test.go`
- Modify: `backend/internal/service/payment_refund_test.go`

**Interfaces:**
- Produces idempotent `consumeSubscriptionPromoForPaidOrder(ctx, tx, orderID) error`.
- Produces idempotent `releaseSubscriptionPromoForOrder(ctx, orderID, now) error`.
- Produces `ReleaseFinalSubscriptionPromoReservations(ctx, now) (int, error)` invoked by the existing expiry service.
- `reservedPromoOrderHarness` and `releasedPromoOrderHarness` are local real-Ent test fixtures that create the same user/group/plan/order/promo rows and differ only in the persisted usage status; their accessors query real rows and payment audit records.

- [ ] **Step 1: Write failing lifecycle tests**

```go
func TestPaidDiscountedOrderConsumesCodeExactlyOnce(t *testing.T) {
	h := reservedPromoOrderHarness(t)
	require.NoError(t, h.notifyPaid())
	require.NoError(t, h.notifyPaid())
	require.Equal(t, PromoUsageStatusConsumed, h.usage(t).Status)
	require.Equal(t, 1, h.promo(t).UsedCount)
	require.Equal(t, 1, h.subscriptionAssignments(t))
}

func TestReleasedPromoRejectsLatePayment(t *testing.T) {
	h := releasedPromoOrderHarness(t)
	require.NoError(t, h.notifyPaid())
	require.Equal(t, OrderStatusExpired, h.order(t).Status)
	require.Equal(t, 0, h.subscriptionAssignments(t))
	require.True(t, h.hasAudit(t, "PAYMENT_AFTER_PROMO_RELEASE"))
}
```

Add explicit cases: at `cancelled_at+4m59s` release count is `0`; at `+5m` it is `1`; payment at `+4m59s` consumes; two release sweeps still produce one `released` row; a pre-provider failure releases immediately; and a full refund leaves `status=consumed` and `used_count=1`.

- [ ] **Step 2: Run and verify RED**

Run: `go test ./internal/service -run 'Test(PaidDiscountedOrder|ReleasedPromo|SubscriptionPromoRelease|PromoRefund)' -count=1`

Expected: FAIL because transitions are absent.

- [ ] **Step 3: Make paid transition and consumption atomic**

For discounted orders, `toPaid` opens one transaction, locks the linked reservation, rejects `released`, accepts existing `consumed` idempotently, changes `reserved -> consumed`, increments `promo_codes.used_count` once, and changes the order to `PAID`. Continue using the existing fulfillment lease and subscription-assignment audit.

- [ ] **Step 4: Extend the existing expiry sweep**

After `ExpireTimedOutOrders`, call `ReleaseFinalSubscriptionPromoReservations` in `PaymentOrderExpiryService.runOnce`. Release only `reserved` rows whose cancelled/expired order transition is at least five minutes old. A payment-creation failure known to occur before an upstream order exists may release immediately; an ambiguous provider timeout stays protected until the normal final-release sweep.

- [ ] **Step 5: Prove refund and affiliate semantics**

Keep refund proportional calculations unchanged because `order.amount` is discounted. Add a literal regression asserting a `149 × 0.8 = 119.20` subscription order at a 10% rebate accrues `11.92`, excluding gateway fee/conversion. Assert refund leaves usage `consumed`.

- [ ] **Step 6: Verify GREEN**

Run:

```bash
go test ./internal/service -run 'Payment|Promo|Affiliate|Refund' -count=1
git diff --check
```

- [ ] **Step 7: Commit**

```bash
git add backend/internal/service
git commit -m "feat(payment): finalize subscription promo lifecycle"
```

---

### Task 5: Preserve the code through WeChat OAuth recovery

**Files:**
- Modify: `backend/internal/service/payment_resume_service.go`
- Modify: `backend/internal/service/payment_resume_service_test.go`
- Modify: `backend/internal/handler/auth_wechat_oauth.go`
- Modify: `backend/internal/handler/auth_wechat_oauth_test.go`
- Modify: `backend/internal/handler/payment_handler.go`
- Modify: `backend/internal/handler/payment_handler_resume_test.go`
- Modify: `frontend/src/views/user/paymentWechatResume.ts`
- Modify: `frontend/src/views/user/__tests__/paymentWechatResume.spec.ts`

**Interfaces:**
- `WeChatPaymentResumeClaims` gains `PromoCode string \`json:"pc,omitempty"\``.
- `ParsedWechatResumeRoute` gains optional `promoCode` for legacy query recovery; opaque token remains authoritative.

- [ ] **Step 1: Write failing signed-context tests**

```go
func TestWeChatPaymentResumeTokenPreservesPromoCode(t *testing.T) {
	svc := NewPaymentResumeService([]byte("01234567890123456789012345678901"))
	token, err := svc.CreateWeChatPaymentResumeToken(WeChatPaymentResumeClaims{
		OpenID: "openid-1", PaymentType: payment.TypeWxpay,
		OrderType: payment.OrderTypeSubscription, PlanID: 7, PromoCode: "SAVE20",
	})
	require.NoError(t, err)
	claims, err := svc.ParseWeChatPaymentResumeToken(token)
	require.NoError(t, err)
	require.Equal(t, "SAVE20", claims.PromoCode)
}

func TestApplyWeChatPaymentResumeClaimsRejectsPromoMismatch(t *testing.T) {
	req := CreateOrderRequest{PaymentType: payment.TypeWxpay, PromoCode: "SAVE10"}
	err := applyWeChatPaymentResumeClaims(&req, &service.WeChatPaymentResumeClaims{
		OpenID: "openid-1", PaymentType: payment.TypeWxpay, PromoCode: "SAVE20",
	})
	require.Equal(t, "INVALID_WECHAT_PAYMENT_RESUME_TOKEN", infraerrors.Reason(err))
}
```

```ts
it('removes the legacy promo code after WeChat recovery', () => {
  expect(stripWechatResumeQuery({ promo_code: 'SAVE20', foo: 'bar' })).toEqual({ foo: 'bar' })
})
```

- [ ] **Step 2: Run and verify RED**

Run:

```bash
go test ./internal/service ./internal/handler -run WeChatPaymentResume -count=1
corepack pnpm@9.15.9 exec vitest run src/views/user/__tests__/paymentWechatResume.spec.ts
```

- [ ] **Step 3: Implement the minimal signed propagation**

Add the normalized promo code to OAuth start query context, server cookie context, signed resume claims, and `applyWeChatPaymentResumeClaims`. The resumed create-order call still revalidates and reserves against current database state.

- [ ] **Step 4: Verify GREEN and commit**

Run the commands from Step 2, then:

```bash
git add backend/internal frontend/src/views/user
git commit -m "feat(payment): preserve promo code through WeChat resume"
```

---

### Task 6: Add the subscription checkout preview and immutable payload

**Files:**
- Modify: `frontend/src/types/payment.ts`
- Modify: `frontend/src/api/payment.ts`
- Modify: `frontend/src/stores/payment.ts`
- Modify: `frontend/src/views/user/PaymentView.vue`
- Modify: `frontend/src/views/user/__tests__/PaymentView.spec.ts`
- Modify: `frontend/src/i18n/locales/en/common.ts`
- Modify: `frontend/src/i18n/locales/zh/common.ts`

**Interfaces:**
- Produces `SubscriptionPromoPreview` matching the backend preview JSON.
- Produces `paymentAPI.previewSubscriptionPromo({code, plan_id})`.
- `CreateOrderRequest` gains optional `promo_code`; it never contains client-computed amount/rate fields.

- [ ] **Step 1: Write failing payment-page tests**

```ts
it('applies the server promo preview and submits only its normalized code', async () => {
  const wrapper = await mountSubscriptionConfirm()
  previewSubscriptionPromo.mockResolvedValue({ data: {
    code: 'SAVE20', discount_rate: 0.8, original_amount: 149,
    discount_amount: 29.8, discounted_amount: 119.2,
  } })
  await wrapper.get('[data-testid="subscription-promo-input"]').setValue(' save20 ')
  await wrapper.get('[data-testid="subscription-promo-apply"]').trigger('click')
  await wrapper.get('[data-testid="subscription-submit"]').trigger('click')
  expect(createOrder).toHaveBeenCalledWith(expect.objectContaining({ promo_code: 'SAVE20' }))
  expect(createOrder.mock.calls[0][0]).not.toHaveProperty('discount_rate')
  expect(createOrder.mock.calls[0][0]).not.toHaveProperty('discount_amount')
})
```

Also assert plan change clears preview, fee/method availability use discounted amount, invalid/reserved/consumed errors render, and balance checkout has no promo payload.

- [ ] **Step 2: Run and verify RED**

Run: `corepack pnpm@9.15.9 exec vitest run src/views/user/__tests__/PaymentView.spec.ts`

Expected: FAIL because preview controls/API are absent.

- [ ] **Step 3: Implement the smallest checkout UI**

Add one input and Apply button only inside selected subscription confirmation. Display server-returned original amount, payable percentage, discount, and discounted amount. Reuse existing currency/fee helpers with `discounted_amount`; clear state on plan change/cancel. Submit only the normalized code.

- [ ] **Step 4: Verify GREEN and commit**

Run:

```bash
corepack pnpm@9.15.9 exec vitest run src/views/user/__tests__/PaymentView.spec.ts src/views/user/__tests__/paymentWechatResume.spec.ts
corepack pnpm@9.15.9 run typecheck
git diff --check
git add frontend/src
git commit -m "feat(payment): apply promo codes at subscription checkout"
```

---

### Task 7: Extend the existing admin promo-code screen

**Files:**
- Modify: `frontend/src/types/index.ts`
- Modify: `frontend/src/views/admin/PromoCodesView.vue`
- Create: `frontend/src/views/admin/__tests__/PromoCodesView.spec.ts`
- Modify: `frontend/src/i18n/locales/en/admin/resources.ts`
- Modify: `frontend/src/i18n/locales/zh/admin/resources.ts`

**Interfaces:**
- Admin create/update payloads carry `purpose` plus either `bonus_amount` or `discount_rate`.
- Subscription promo rows expose one-use reservation/consumption state and linked order/user timestamps through the existing usages dialog.

- [ ] **Step 1: Write failing admin-view tests**

```ts
it('creates a single-use subscription discount from payable percent', async () => {
  const wrapper = mountPromoCodesView()
  await wrapper.get('[data-testid="promo-purpose"]').setValue('subscription_discount')
  await wrapper.get('[data-testid="promo-pay-percent"]').setValue('80')
  await wrapper.get('[data-testid="promo-create-submit"]').trigger('submit')
  expect(createPromo).toHaveBeenCalledWith(expect.objectContaining({
    purpose: 'subscription_discount', discount_rate: 0.8,
    bonus_amount: 0, max_uses: 1,
  }))
})

it.each(['reserved', 'consumed', 'released'])('renders %s subscription usage', async (status) => {
  getUsages.mockResolvedValue(usageResponse({ status, payment_order_id: 42, discount_amount: 29.8 }))
  const wrapper = mountPromoCodesView()
  await openUsageDialog(wrapper)
  expect(wrapper.text()).toContain(status)
  expect(wrapper.text()).toContain('42')
})
```

`mountPromoCodesView`, `usageResponse`, and `openUsageDialog` live in the new spec file, mount the real view, and mock only HTTP calls through `adminAPI.promo`; the assertions target rendered controls and the request boundary rather than mock elements.

- [ ] **Step 2: Run and verify RED**

Run: `corepack pnpm@9.15.9 exec vitest run src/views/admin/__tests__/PromoCodesView.spec.ts`

- [ ] **Step 3: Implement conditional fields in the existing view**

Do not add a route or second screen. Add purpose/discount columns and conditionally render the existing dialog fields. Keep editing existing registration codes unchanged.

- [ ] **Step 4: Verify GREEN and commit**

Run:

```bash
corepack pnpm@9.15.9 exec vitest run src/views/admin/__tests__/PromoCodesView.spec.ts
corepack pnpm@9.15.9 run lint:check
corepack pnpm@9.15.9 run typecheck
git diff --check
git add frontend/src
git commit -m "feat(admin): manage subscription promo codes"
```

---

### Task 8: Full regression and release-readiness verification

**Files:**
- Modify only files required to fix failures attributable to Tasks 1-7.

**Interfaces:**
- Produces a clean feature branch ready for review; no deployment or release is performed in this task.

- [ ] **Step 1: Run backend unit and integration suites**

```bash
cd backend
make test-unit
make test-integration
```

Expected: all packages PASS.

- [ ] **Step 2: Run frontend checks**

```bash
cd frontend
corepack pnpm@9.15.9 run lint:check
corepack pnpm@9.15.9 run typecheck
corepack pnpm@9.15.9 run test:run
```

Expected: all test files PASS.

- [ ] **Step 3: Verify generation, migration count, and diff hygiene**

```bash
cd backend
go generate ./ent
go generate ./cmd/server
cd ..
git diff --exit-code
git diff --check
git status --short
```

Expected: generation is reproducible, `git diff --check` is silent, and only intentional committed changes exist.

- [ ] **Step 4: Review security and money invariants**

Confirm from tests and diff that frontend values never set the charged amount/rate, one code has one reserved/consumed row under concurrency, released callbacks do not fulfill, refunds do not restore codes, and affiliate rebate uses discounted `payment_orders.amount`.

- [ ] **Step 5: Commit any verification-only fixes**

```bash
git add -A
git commit -m "test(payment): cover subscription promo lifecycle"
```
