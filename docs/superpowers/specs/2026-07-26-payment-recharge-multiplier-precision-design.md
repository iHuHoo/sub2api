# Payment Recharge Multiplier Precision Design

## Problem

AoxToken sells one Credit for CNY 8.80, so the Sub2API balance recharge
multiplier must be:

```text
1 / 8.80 = 0.11363636
```

The admin API currently serializes this multiplier with two decimal places.
After saving and reloading, `0.11363636` becomes `0.11`. A CNY 88 recharge
would therefore credit USD 9.68 instead of 10.00 Credits.

## Options Considered

1. Change the commercial rate to one representable with two decimals.
   This would make the application fit the bug and contradict the approved
   AoxToken pricing.
2. Edit the production setting directly in the database.
   The parser already accepts full precision, but bypassing the admin workflow
   is fragile and makes later saves truncate the value again.
3. Preserve full precision when the admin API serializes the multiplier.
   This fixes the source of the truncation and uses an exact-float formatter
   that already exists for the adjacent subscription exchange-rate setting.

Option 3 is selected because it is the smallest durable change.

## Design

Change only `PaymentConfigService.UpdatePaymentConfig` so
`BalanceRechargeMultiplier` uses `formatPositiveFloatExact` instead of
`formatPositiveFloat`.

Keep precision concerns separated by layer:

- persist the multiplier without an early two-decimal conversion;
- calculate credited balance with `shopspring/decimal`;
- round the final credited balance to two decimal places;
- format CNY gateway amounts according to the currency minor unit;
- show user-facing balances, credited amounts, and rate previews with two
  decimal places.

The administrator multiplier input remains an operator configuration control
and exposes the stored value. User-facing pages and previews continue to show
two decimal places. Formatting a displayed value must never mutate the stored
configuration value or the value submitted when unrelated settings are saved.

Do not change:

- payment or credit calculation;
- order creation and fulfillment;
- historical settings or balances;
- other amount fields;
- the WooshPay provider;
- subscription plan conversion.

The admin input already accepts the required value and sends it as a number.
The backend parser and credit calculation already accept the resulting
precision. Existing UI formatting already uses two decimal places for the
public rate preview and credited balance.

## Verification

Add a service-level regression test that:

1. saves `0.11363636` through `UpdatePaymentConfig`;
2. reads the persisted setting;
3. asserts that it remains `0.11363636`.

The test must fail against the current implementation with an actual value of
`0.11`, then pass after the one-line production change.

Extend the credited-balance regression test to assert that CNY 88 multiplied
by `0.11363636` produces a final credited balance of `10.00`.

After release, production verification requires:

- saving `0.11363636` in the admin UI;
- confirming through the persisted admin setting that the stored value remains
  `0.11363636`;
- confirming the UI rate preview remains two decimal places;
- confirming CNY 88 previews and credits as 10.00;
- keeping payment disabled until those checks pass.
