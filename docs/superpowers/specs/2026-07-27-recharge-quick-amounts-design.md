# Recharge Quick Amounts Design

## Goal

Reduce the balance-recharge quick amount choices to `100`, `200`, and `500`.

## Scope

- Change only the quick amount list passed by `PaymentView` to `AmountInput`.
- Keep custom amount entry unchanged.
- Keep payment-provider limits, global recharge limits, subscription prices, and order calculations unchanged.
- Do not add an admin setting for quick amounts.

## Verification

- A focused frontend test proves that the recharge view passes exactly `[100, 200, 500]` to `AmountInput`.
- The existing `PaymentView` test suite passes.
- Frontend type checking passes.

