# Payment Recharge Multiplier Precision Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve the complete balance recharge multiplier through the admin save path while continuing to round only final credited balances and user-facing displays.

**Architecture:** Keep the existing `float64` API contract and `shopspring/decimal` calculation path. Change only the setting serializer for `BalanceRechargeMultiplier` to the existing exact formatter, then lock persistence and final-credit behavior with service tests.

**Tech Stack:** Go, `shopspring/decimal`, Go `testing`, Sub2API setting repository stub

## Global Constraints

- Persist `0.11363636` without converting it to `0.11`.
- Continue rounding the final credited balance to two decimal places.
- Do not change gateway amount formatting, order fulfillment, WooshPay, or frontend display code.
- Keep production payment disabled until the patched release is deployed and verified.

---

### Task 1: Preserve Recharge Multiplier Precision

**Files:**
- Modify: `backend/internal/service/payment_config_service_test.go`
- Modify: `backend/internal/service/payment_config_service.go:348-356`
- Modify: `backend/internal/service/payment_order_result_test.go`

**Interfaces:**
- Consumes: `PaymentConfigService.UpdatePaymentConfig(context.Context, UpdatePaymentConfigRequest) error`
- Consumes: `calculateCreditedBalance(paymentAmount, multiplier float64) float64`
- Produces: an exact persisted string for `SettingBalanceRechargeMult`

- [ ] **Step 1: Write the failing persistence test**

Add this test beside the existing `TestUpdatePaymentConfig_PersistsVisibleMethodRouting` test:

```go
func TestUpdatePaymentConfig_PreservesBalanceRechargeMultiplierPrecision(t *testing.T) {
	repo := &paymentConfigSettingRepoStub{values: map[string]string{}}
	svc := &PaymentConfigService{settingRepo: repo}
	multiplier := 0.11363636

	err := svc.UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{
		BalanceRechargeMultiplier: &multiplier,
	})
	if err != nil {
		t.Fatalf("UpdatePaymentConfig returned error: %v", err)
	}

	if got := repo.values[SettingBalanceRechargeMult]; got != "0.11363636" {
		t.Fatalf("balance recharge multiplier = %q, want 0.11363636", got)
	}
}
```

- [ ] **Step 2: Run the persistence test and verify RED**

Run:

```bash
cd backend
GOCACHE=/tmp/sub2api-payment-precision-red go test ./internal/service \
  -run TestUpdatePaymentConfig_PreservesBalanceRechargeMultiplierPrecision \
  -count=1
```

Expected: FAIL with `balance recharge multiplier = "0.11", want 0.11363636`.

- [ ] **Step 3: Apply the minimal serializer fix**

In `PaymentConfigService.UpdatePaymentConfig`, replace only the multiplier
formatter:

```go
SettingBalanceRechargeMult: formatPositiveFloatExact(req.BalanceRechargeMultiplier),
```

- [ ] **Step 4: Add the final-credit regression assertion**

Extend `TestCalculateCreditedBalanceStillUsesRechargeMultiplier`:

```go
	got = calculateCreditedBalance(88, 0.11363636)
	if got != 10 {
		t.Fatalf("credited balance = %v, want 10", got)
	}
```

- [ ] **Step 5: Run focused tests and verify GREEN**

Run:

```bash
cd backend
GOCACHE=/tmp/sub2api-payment-precision-green go test ./internal/service \
  -run 'TestUpdatePaymentConfig_PreservesBalanceRechargeMultiplierPrecision|TestCalculateCreditedBalanceStillUsesRechargeMultiplier' \
  -count=1
```

Expected: PASS.

- [ ] **Step 6: Run the complete payment service regression set**

Run:

```bash
cd backend
GOCACHE=/tmp/sub2api-payment-precision-regression go test \
  ./internal/payment/... ./internal/service ./internal/handler ./internal/handler/admin \
  -count=1
```

Expected: PASS with no failures.

- [ ] **Step 7: Commit the implementation**

```bash
git add \
  backend/internal/service/payment_config_service.go \
  backend/internal/service/payment_config_service_test.go \
  backend/internal/service/payment_order_result_test.go \
  docs/superpowers/specs/2026-07-26-payment-recharge-multiplier-precision-design.md \
  docs/superpowers/plans/2026-07-26-payment-recharge-multiplier-precision.md
git commit -m "fix(payment): preserve recharge multiplier precision"
```

