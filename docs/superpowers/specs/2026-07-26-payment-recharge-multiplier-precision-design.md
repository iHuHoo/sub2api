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

Do not change:

- payment or credit calculation;
- order creation and fulfillment;
- historical settings or balances;
- other amount fields;
- the WooshPay provider;
- subscription plan conversion.

The admin input already accepts the required value and sends it as a number.
The backend parser and credit calculation already accept the resulting
precision.

## Verification

Add a service-level regression test that:

1. saves `0.11363636` through `UpdatePaymentConfig`;
2. reads the persisted setting;
3. asserts that it remains `0.11363636`.

The test must fail against the current implementation with an actual value of
`0.11`, then pass after the one-line production change.

After release, production verification requires:

- saving `0.11363636` in the admin UI;
- reloading and observing the same value;
- confirming CNY 88 previews and credits as 10.00;
- keeping payment disabled until those checks pass.
