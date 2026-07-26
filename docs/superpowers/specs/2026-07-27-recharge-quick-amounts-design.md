# Recharge Quick Amounts Design

## Goal

Reduce the balance-recharge quick amount choices to `100`, `200`, and `500`,
display payment amounts with the selected payment currency symbol, and call
the purchased product `Credits` during checkout.

## Scope

- Change only the quick amount list passed by `PaymentView` to `AmountInput`.
- Treat quick and custom amounts as payment amounts.
- Pass the selected payment currency symbol to `AmountInput`; CNY displays `¥`
  instead of the component's legacy hard-coded `$`.
- On the recharge selection page, display the converted purchase quantity as
  `Credits` instead of USD.
- On the waiting-for-payment page, display the purchased quantity as `Credits`
  instead of USD.
- Keep the completed account balance and every page outside the recharge
  selection and waiting-for-payment flow in USD.
- Keep payment-provider limits, global recharge limits, subscription prices, and order calculations unchanged.
- Do not add an admin setting for quick amounts.

## Verification

- A focused frontend test proves that the recharge view passes exactly `[100, 200, 500]` to `AmountInput`.
- A focused frontend test proves that a CNY payment method gives `AmountInput`
  the `¥` symbol rather than `$`.
- A focused frontend test proves that the recharge selection page labels the
  converted purchase quantity as `Credits`.
- A focused component test proves that the waiting-for-payment page labels the
  purchased quantity as `Credits`.
- The existing `PaymentView` test suite passes.
- The existing `PaymentStatusPanel` test suite passes.
- Frontend type checking passes.
