# Recharge Quick Amounts Design

## Goal

Reduce the balance-recharge quick amount choices to `100`, `200`, and `500`,
and display the custom payment amount with the selected payment currency symbol.

## Scope

- Change only the quick amount list passed by `PaymentView` to `AmountInput`.
- Treat quick and custom amounts as payment amounts.
- Pass the selected payment currency symbol to `AmountInput`; CNY displays `¥`
  instead of the component's legacy hard-coded `$`.
- Keep credited-balance conversion and display unchanged.
- Keep payment-provider limits, global recharge limits, subscription prices, and order calculations unchanged.
- Do not add an admin setting for quick amounts.

## Verification

- A focused frontend test proves that the recharge view passes exactly `[100, 200, 500]` to `AmountInput`.
- A focused frontend test proves that a CNY payment method gives `AmountInput`
  the `¥` symbol rather than `$`.
- The existing `PaymentView` test suite passes.
- Frontend type checking passes.
